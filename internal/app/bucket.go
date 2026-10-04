package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/m1r3dk/dirclone/internal/bucket"
	"github.com/m1r3dk/dirclone/internal/model"
)

// crawlBucket indexes an S3/GCS bucket via paginated ListObjectsV2. Keys map to
// files; every key prefix (and every zero-byte "dir/" placeholder) maps to a
// directory, so buckets browse like any other listing.
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
