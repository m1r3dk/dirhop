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
	target, ok := bucket.Detect(rawURL)
	if !ok {
		access, err := a.checkGenericAccess(ctx, rawURL)
		return Preflight{URL: rawURL, Access: access, Bucket: false, Err: err}
	}
	access, err := bucket.CheckAccess(ctx, a.HTTP, target, a.Config.UserAgent)
	return Preflight{URL: rawURL, Access: access, Bucket: true, Err: err}
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
	resp, err := a.HTTP.Do(req)
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
func (a *App) crawlBucket(ctx context.Context, site *model.Site, target bucket.Target, run *model.CrawlRun) error {
	root, err := a.DB.EntryByPath(ctx, site.ID, "/", false)
	if err != nil {
		return err
	}
	if err := a.DB.SetSiteParser(ctx, site.ID, string(target.Provider)); err != nil {
		return err
	}
	// ponytail: directory-ID map grows with directory count, not object count.
	dirs := map[string]int64{"/": root.ID}
	seen := run.StartedAt
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

	// Walk serializes callbacks, so ensureDir/dirs need no extra locking.
	workers := a.bucketWorkers(site)
	_, err = bucket.Walk(ctx, a.HTTP, target, a.Config.UserAgent, workers, func(objects []bucket.Object, prefixes []string) error {
		for _, p := range prefixes {
			if err := ensureDir(path.Clean("/" + strings.TrimPrefix(p, target.Prefix))); err != nil {
				return err
			}
		}
		batch := make([]model.Entry, 0, len(objects))
		for _, o := range objects {
			rel := strings.TrimPrefix(o.Key, target.Prefix)
			virtual := path.Clean("/" + rel)
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
		_, err := a.DB.UpsertEntriesNoRecount(ctx, batch)
		a.report(run)
		return err
	})
	if errors.Is(err, bucket.ErrAccessDenied) {
		return fmt.Errorf("%w: %v", ErrUnsupportedListing, err)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	return err
}
