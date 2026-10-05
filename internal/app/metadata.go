package app

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/m1r3dk/dirhop/internal/database"
	"github.com/m1r3dk/dirhop/internal/model"
)

// EnrichMetadata issues bounded concurrent HEAD requests for indexed files and
// stores Content-Length, Content-Type, ETag, and Last-Modified. File bodies are
// never requested. Returns the number of files updated.
func (a *App) EnrichMetadata(ctx context.Context, site *model.Site, workers int) (int, error) {
	if workers <= 0 {
		workers = a.Config.CrawlConcurrency
	}
	jobs := make(chan model.Entry)
	results := make(chan model.Entry)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				if updated, ok := a.head(ctx, e); ok {
					select {
					case results <- updated:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	var scanErr error
	go func() {
		defer close(jobs)
		scanErr = a.DB.ScanEntries(ctx, site.ID, "/", database.EntryFilter{Type: model.EntryTypeFile}, func(e model.Entry) error {
			select {
			case jobs <- e:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	// Single writer: batch updates into short transactions.
	updated := 0
	batch := make([]model.Entry, 0, 200)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := a.DB.UpsertEntriesNoRecount(context.WithoutCancel(ctx), batch)
		batch = batch[:0]
		return err
	}
	for e := range results {
		batch = append(batch, e)
		updated++
		if len(batch) == cap(batch) {
			if err := flush(); err != nil {
				return updated, err
			}
		}
	}
	if err := flush(); err != nil {
		return updated, err
	}
	if err := a.DB.Recount(context.WithoutCancel(ctx), site.ID); err != nil {
		return updated, err
	}
	if scanErr != nil {
		return updated, scanErr
	}
	return updated, ctx.Err()
}

func (a *App) head(ctx context.Context, e model.Entry) (model.Entry, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, e.URL, nil)
	if err != nil {
		return e, false
	}
	req.Header.Set("User-Agent", a.Config.UserAgent)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return e, false
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return e, false
	}
	if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil && n >= 0 {
		e.Size = &n
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		e.ContentType = ct
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		e.ETag = etag
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		e.LastModified = lm
		if t, err := http.ParseTime(lm); err == nil {
			e.ModifiedAt = &t
		}
	}
	e.UpdatedAt = time.Now().UTC()
	return e, true
}
