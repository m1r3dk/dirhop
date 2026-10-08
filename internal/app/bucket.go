package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/m1r3dk/dirhop/internal/bucket"
	"github.com/m1r3dk/dirhop/internal/httpclient"
	"github.com/m1r3dk/dirhop/internal/model"
)

// Preflight is the accessibility verdict for one target, plus how it was
// classified (native bucket hostname vs. an unrecognized host).
type Preflight struct {
	URL    string
	Access bucket.Access
	Bucket bool // true if the URL is a recognized bucket hostname
	Err    error
}

// Accessible reports whether a full scan is worth attempting.
func (p Preflight) Accessible() bool {
	return p.Access == bucket.AccessPublic
}

// PreflightAccess performs one cheap accessibility check for a raw URL. For a
// recognized bucket hostname it issues a single max-1-key listing request. For
// generic HTTP directory listings it checks reachability with HEAD, falling back
// to a tiny GET when a server does not support HEAD.
func (a *App) PreflightAccess(ctx context.Context, rawURL string) Preflight {
	// Bound every single probe so one stalled host cannot pin a worker. The
	// dedicated preflight client already has a short timeout and no retries; this
	// per-request deadline is a hard ceiling on top of that.
	ctx, cancel := context.WithTimeout(ctx, a.Config.PreflightTimeout)
	defer cancel()
	target, ok := bucket.Detect(rawURL)
	if !ok {
		access, err := a.checkGenericAccess(ctx, rawURL)
		return Preflight{URL: rawURL, Access: access, Bucket: false, Err: err}
	}
	access, err := bucket.CheckAccess(ctx, a.preflightClient(), target, a.Config.UserAgent)
	return Preflight{URL: rawURL, Access: access, Bucket: true, Err: err}
}

// preflightClient returns the short-timeout, no-retry client for accessibility
// checks, falling back to the main client if one was not constructed.
func (a *App) preflightClient() *httpclient.Client {
	if a.Preflight != nil {
		return a.Preflight
	}
	return a.HTTP
}

func (a *App) checkGenericAccess(ctx context.Context, rawURL string) (bucket.Access, error) {
	access, err, retryWithGet := a.doGenericPreflight(ctx, http.MethodHead, rawURL)
	if retryWithGet {
		return a.doGenericPreflightGet(ctx, rawURL)
	}
	return access, err
}

func (a *App) doGenericPreflightGet(ctx context.Context, rawURL string) (bucket.Access, error) {
	access, err, _ := a.doGenericPreflight(ctx, http.MethodGet, rawURL)
	return access, err
}

func (a *App) doGenericPreflight(ctx context.Context, method, rawURL string) (bucket.Access, error, bool) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return bucket.AccessError, err, false
	}
	if a.Config.UserAgent != "" {
		req.Header.Set("User-Agent", a.Config.UserAgent)
	}
	if method == http.MethodGet {
		req.Header.Set("Range", "bytes=0-0")
	}
	resp, err := a.preflightClient().Do(req)
	if err != nil {
		if isNoSuchHost(err) {
			return bucket.AccessMissing, nil, false
		}
		return bucket.AccessError, err, false
	}
	defer resp.Body.Close()
	if method == http.MethodGet {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	}
	if method == http.MethodHead && (resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented) {
		return bucket.AccessError, nil, true
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 400:
		return bucket.AccessPublic, nil, false
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return bucket.AccessDenied, nil, false
	case resp.StatusCode == http.StatusNotFound:
		return bucket.AccessMissing, nil, false
	default:
		return bucket.AccessError, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode), false
	}
}

func isNoSuchHost(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns) && dns.IsNotFound
}

// crawlBucket indexes an object-storage bucket (S3, GCS, DigitalOcean Spaces,
// or Azure Blob) via its paginated listing API. Keys map to files; every key
// prefix (and every zero-byte "dir/" placeholder) maps to a directory, so
// buckets browse like any other listing.
//
// The scan is crash-safe: objects arrive in lexicographic key order and the
// greatest committed key is persisted as a checkpoint after every page, so an
// interrupted scan (Ctrl-C, crash, network loss) keeps everything already
// indexed and the next run resumes strictly after the last checkpointed key
// instead of re-listing the bucket from the start. The checkpoint also pins the
// original scan start time so post-scan reconciliation never reaps entries that
// an earlier segment of the same logical scan indexed.
func (a *App) crawlBucket(ctx context.Context, site *model.Site, target bucket.Target, run *model.CrawlRun) error {
	root, err := a.DB.EntryByPath(ctx, site.ID, "/", false)
	if err != nil {
		return err
	}
	if err := a.DB.SetSiteParser(ctx, site.ID, string(target.Provider)); err != nil {
		return err
	}

	// Resume if a prior scan of this site was interrupted: continue after the
	// last checkpointed key and keep the original scan start so reconciliation
	// does not treat earlier-indexed entries as stale.
	startAfter, startedAt, resuming, err := a.DB.ScanCheckpoint(ctx, site.ID)
	if err != nil {
		return err
	}
	if resuming && !startedAt.IsZero() {
		run.StartedAt = startedAt
	}
	seen := run.StartedAt

	// The directory-ID map grows with directory count, not object count.
	dirs := map[string]int64{"/": root.ID}
	root.LastSeenAt = seen
	if _, err := a.DB.UpsertEntriesNoRecount(ctx, []model.Entry{*root}); err != nil {
		return err
	}

	ensureDir := func(dir string) error {
		var missing []string
		for d := dir; d != "/"; d = path.Dir(d) {
			if _, ok := dirs[d]; ok {
				break
			}
			missing = append(missing, d)
		}
		for i := len(missing) - 1; i >= 0; i-- {
			d := missing[i]
			parent := dirs[path.Dir(d)]
			key := target.Prefix + strings.TrimPrefix(d, "/") + "/"
			saved, err := a.DB.UpsertEntriesNoRecount(ctx, []model.Entry{{
				SiteID: site.ID, ParentID: &parent, Name: path.Base(d), NormalizedPath: d,
				URL: target.ObjectURL(key), Type: model.EntryTypeDirectory, LastSeenAt: seen,
			}})
			if err != nil {
				return err
			}
			dirs[d] = saved[0].ID
			run.Directories++
			a.report(run)
		}
		return nil
	}

	// Strictly sequential listing in key order gives one monotonic cursor to
	// checkpoint. A page is fully downloaded before its callback runs, so it is
	// committed with a non-cancellable context: an interruption (Ctrl-C, ctx
	// deadline) then keeps the work already fetched instead of discarding it,
	// and the saved cursor lets the next run resume right after it.
	_, err = bucket.ListResumable(ctx, a.HTTP, target, a.Config.UserAgent, startAfter, func(objects []bucket.Object, cursor string) error {
		commitCtx := context.WithoutCancel(ctx)
		batch := make([]model.Entry, 0, len(objects))
		for _, o := range objects {
			rel := strings.TrimPrefix(o.Key, target.Prefix)
			virtual := path.Clean("/" + rel)
			run.CurrentPath = virtual
			if virtual == "/" {
				continue
			}
			if strings.HasSuffix(o.Key, "/") {
				if err := ensureDir(virtual); err != nil {
					return err
				}
				continue
			}
			parentPath := path.Dir(virtual)
			if err := ensureDir(parentPath); err != nil {
				return err
			}
			parent := dirs[parentPath]
			size := o.Size
			entry := model.Entry{
				SiteID: site.ID, ParentID: &parent, Name: path.Base(virtual), NormalizedPath: virtual,
				URL: target.ObjectURL(o.Key), Type: model.EntryTypeFile, Size: &size,
				ETag: o.ETag, LastSeenAt: seen,
			}
			if !o.LastModified.IsZero() {
				modified := o.LastModified
				entry.ModifiedAt = &modified
				entry.LastModified = modified.UTC().Format(time.RFC1123)
			}
			batch = append(batch, entry)
			run.Files++
			run.Bytes += size
		}
		if _, err := a.DB.UpsertEntriesNoRecount(commitCtx, batch); err != nil {
			return err
		}
		// Persist the resume point only after the page's entries are durably
		// committed, so the cursor never advances past unindexed keys.
		if cursor != "" {
			if err := a.DB.SaveScanCheckpoint(commitCtx, site.ID, cursor, run.StartedAt); err != nil {
				return err
			}
		}
		a.report(run)
		// Surface interruption after the page is safely persisted, so the scan
		// stops promptly but loses no already-fetched work.
		return ctx.Err()
	})
	if errors.Is(err, bucket.ErrAccessDenied) {
		return fmt.Errorf("%w: %v", ErrUnsupportedListing, err)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	if err == nil {
		// Scan ran to completion: drop the checkpoint so the next scan starts
		// fresh instead of resuming from the final key.
		if clearErr := a.DB.ClearScanCheckpoint(context.WithoutCancel(ctx), site.ID); clearErr != nil {
			return clearErr
		}
	}
	return err
}
