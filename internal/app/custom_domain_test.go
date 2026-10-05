package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/m1r3dk/dirhop/internal/config"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg, _ := config.Default()
	tmp := t.TempDir()
	cfg.Paths.Database, cfg.Paths.History = filepath.Join(tmp, "db"), filepath.Join(tmp, "h")
	a, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// A CDN hostname CNAMEd to object storage serves ListObjectsV2 XML that no
// hostname rule matches. Indexing it must still work (regression: it failed
// with "not a recognizable directory listing or public bucket").
func TestCrawlIndexesS3CompatibleCustomDomain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
		if r.URL.Query().Get("list-type") != "2" {
			// The HTML crawler's plain GET of the root: still XML, as a real
			// bucket endpoint would answer.
			fmt.Fprint(w, `<?xml version='1.0'?><ListBucketResult><Name>cdn</Name><KeyCount>2</KeyCount><IsTruncated>false</IsTruncated></ListBucketResult>`)
			return
		}
		fmt.Fprint(w, `<?xml version='1.0'?><ListBucketResult><Name>cdn</Name><KeyCount>2</KeyCount><IsTruncated>false</IsTruncated>`+
			`<Contents><Key>theme.css</Key><Size>5198</Size><ETag>"abc"</ETag></Contents>`+
			`<Contents><Key>lib/app.js</Key><Size>120</Size></Contents>`+
			`</ListBucketResult>`)
	}))
	defer server.Close()

	a := newTestApp(t)
	ctx := context.Background()
	site, _, err := a.OpenURL(ctx, server.URL+"/", "cdn", true)
	if err != nil {
		t.Fatalf("indexing a custom-domain bucket failed: %v", err)
	}
	if site.ParserType != "s3" {
		t.Errorf("parser=%q want s3", site.ParserType)
	}
	if site.FileCount != 2 {
		t.Errorf("files=%d want 2", site.FileCount)
	}
	entries, err := a.FS(site).List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "theme.css") || !strings.Contains(got, "lib") {
		t.Errorf("root listing = %q, want theme.css and lib/", got)
	}
}

// The probe must not add a request to ordinary HTML crawls; it may only run
// once the root has already proved unparseable.
func TestCrawlDoesNotProbeParseableListings(t *testing.T) {
	var listRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			listRequests.Add(1)
		}
		fmt.Fprint(w, `<title>Index of /</title><a href="a.txt">a.txt</a>`)
	}))
	defer server.Close()

	a := newTestApp(t)
	if _, _, err := a.OpenURL(context.Background(), server.URL+"/", "html", true); err != nil {
		t.Fatal(err)
	}
	if n := listRequests.Load(); n != 0 {
		t.Errorf("bucket probe ran %d times on a parseable HTML listing, want 0", n)
	}
}

// A site that is neither parseable HTML nor a bucket must keep its clear
// "unsupported listing" error and exit code.
func TestCrawlStillReportsUnsupportedListings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer server.Close()

	a := newTestApp(t)
	_, _, err := a.OpenURL(context.Background(), server.URL+"/", "json", true)
	if err == nil || !strings.Contains(err.Error(), "not a recognizable directory listing or public bucket") {
		t.Fatalf("err=%v, want unsupported-listing error", err)
	}
}
