package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m1r3dk/dirhop/internal/bucket"
	"github.com/m1r3dk/dirhop/internal/config"
	"github.com/m1r3dk/dirhop/internal/downloader"
	"github.com/m1r3dk/dirhop/internal/model"
)

func TestPersistentIndexRefreshAndExplicitDownload(t *testing.T) {
	var directoryRequests, fileRequests, conditionalRequests atomic.Int64
	var changed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pub/":
			directoryRequests.Add(1)
			if r.Header.Get("If-None-Match") == `"root-v1"` {
				conditionalRequests.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"root-v1"`)
			fmt.Fprint(w, `<html><title>Index of /pub/</title><a href="releases/">releases/</a></html>`)
		case "/pub/releases/":
			directoryRequests.Add(1)
			etag := `"release-v1"`
			if changed.Load() {
				etag = `"release-v2"`
			}
			if r.Header.Get("If-None-Match") == etag {
				conditionalRequests.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			if changed.Load() {
				fmt.Fprint(w, `<html><title>Index</title><a href="new.zip">new.zip</a></html>`)
			} else {
				fmt.Fprint(w, `<html><title>Index</title><a href="app.zip">app.zip</a></html>`)
			}
		case "/pub/releases/app.zip":
			fileRequests.Add(1)
			fmt.Fprint(w, "archive")
		case "/pub/releases/new.zip":
			fileRequests.Add(1)
			fmt.Fprint(w, "new")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	cfg.Paths.DataDir, cfg.Paths.ConfigDir, cfg.Paths.CacheDir = tmp, tmp, tmp
	cfg.Paths.Database = filepath.Join(tmp, "dirhop.db")
	cfg.Paths.History = filepath.Join(tmp, "history")
	cfg.HTTPTimeout = 5 * time.Second

	a, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	site, created, err := a.OpenURL(ctx, server.URL+"/pub/", "fixture", true)
	if err != nil || !created {
		t.Fatalf("open: created=%v err=%v", created, err)
	}
	if fileRequests.Load() != 0 {
		t.Fatalf("crawl downloaded file content")
	}
	entries, err := a.FS(site).Find(ctx, "/", findZip())
	if err != nil || len(entries) != 1 || entries[0].Name != "app.zip" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}

	before := directoryRequests.Load()
	reopened, created, err := a.OpenURL(ctx, server.URL+"/pub/", "", true)
	if err != nil || created || reopened.ID != site.ID {
		t.Fatalf("reopen: created=%v err=%v", created, err)
	}
	if directoryRequests.Load() != before {
		t.Fatalf("reopen recrawled the site")
	}
	if err := a.Crawl(ctx, site, false); err != nil {
		t.Fatal(err)
	}
	if conditionalRequests.Load() != 2 {
		t.Fatalf("conditional requests=%d, want 2", conditionalRequests.Load())
	}
	entries, err = a.FS(site).Find(ctx, "/", findZip())
	if err != nil || len(entries) != 1 || entries[0].Name != "app.zip" {
		t.Fatalf("304 refresh entries=%+v err=%v", entries, err)
	}

	destination := filepath.Join(tmp, "downloads")
	result, err := a.Download(ctx, site, []string{"/releases/app.zip"}, destination, downloader.Options{Concurrency: 2})
	if err != nil || result.Completed != 1 {
		t.Fatalf("download=%+v err=%v", result, err)
	}
	body, err := os.ReadFile(filepath.Join(destination, "app.zip"))
	if err != nil || string(body) != "archive" {
		t.Fatalf("download body=%q err=%v", body, err)
	}

	changed.Store(true)
	if err := a.Crawl(ctx, site, false); err != nil {
		t.Fatal(err)
	}
	entries, err = a.FS(site).Find(ctx, "/", findZip())
	if err != nil || len(entries) != 1 || entries[0].Name != "new.zip" {
		t.Fatalf("refreshed entries=%+v err=%v", entries, err)
	}
}

func findZip() model.FindOptions {
	return model.FindOptions{Extensions: []string{"zip"}, Type: model.EntryTypeFile}
}

// Preflight must probe each host exactly once (no retries) so a huge list is not
// multiplied into a flood, and must honor its short timeout rather than the main
// crawl timeout.
func TestPreflightNoRetriesAndShortTimeout(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// 503 is a status the main client would retry; preflight must not.
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	cfg.Paths.DataDir, cfg.Paths.ConfigDir, cfg.Paths.CacheDir = tmp, tmp, tmp
	cfg.Paths.Database = filepath.Join(tmp, "dirhop.db")
	cfg.Paths.History = filepath.Join(tmp, "history")
	cfg.HTTPTimeout = 30 * time.Second
	cfg.Retries = 3
	cfg.PreflightTimeout = 2 * time.Second

	a, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// A plain host (non-bucket) 503 is one HEAD. Use an S3-style host so the
	// bucket CheckAccess path is exercised with a single GET.
	v := a.PreflightAccess(context.Background(), server.URL+"/")
	if v.Access == bucket.AccessPublic {
		t.Fatalf("503 should not be public: %+v", v)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("preflight sent %d requests, want exactly 1 (no retries)", n)
	}
}
