package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/m1r3dk/dirhop/internal/config"
)

func TestEnrichMetadataUsesHeadOnly(t *testing.T) {
	var fileGets, heads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/m/" {
			fmt.Fprint(w, `<title>Index of /m</title><a href="a.bin">a.bin</a>`)
			return
		}
		if r.Method == http.MethodHead {
			heads.Add(1)
		} else {
			fileGets.Add(1)
		}
		w.Header().Set("Content-Type", "application/x-test")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.Header().Set("Content-Length", "1234")
	}))
	defer server.Close()

	cfg, _ := config.Default()
	tmp := t.TempDir()
	cfg.Paths.Database, cfg.Paths.History = filepath.Join(tmp, "db"), filepath.Join(tmp, "h")
	a, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	site, _, err := a.OpenURL(ctx, server.URL+"/m/", "m", true)
	if err != nil {
		t.Fatal(err)
	}
	n, err := a.EnrichMetadata(ctx, site, 2)
	if err != nil || n != 1 || heads.Load() != 1 || fileGets.Load() != 0 {
		t.Fatalf("n=%d err=%v heads=%d gets=%d", n, err, heads.Load(), fileGets.Load())
	}
	e, err := a.FS(site).Stat(ctx, "/a.bin")
	if err != nil || e.Size == nil || *e.Size != 1234 || e.ContentType != "application/x-test" || e.ETag != `"v1"` || e.ModifiedAt == nil || e.ModifiedAt.Year() != 2015 {
		t.Fatalf("entry=%+v err=%v", e, err)
	}
}
