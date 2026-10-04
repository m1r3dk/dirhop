package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"sync"
	"testing"

	"github.com/1jehuang/dirclone/internal/parser"
)

type memoryRepository struct {
	mu       sync.Mutex
	outcomes []DirectoryOutcome
}

func (repository *memoryRepository) RecordDirectory(_ context.Context, outcome DirectoryOutcome) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.outcomes = append(repository.outcomes, outcome)
	return nil
}

func (repository *memoryRepository) snapshot() []DirectoryOutcome {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]DirectoryOutcome(nil), repository.outcomes...)
}

func TestCrawlerRecursesDirectoriesWithoutGettingFiles(t *testing.T) {
	var mu sync.Mutex
	requested := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requested = append(requested, request.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/html")
		switch request.URL.Path {
		case "/base/":
			fmt.Fprint(w, `<html><body><a href="dir/">dir/</a><a href="file.txt">file.txt</a><a href="/outside/">outside/</a></body></html>`)
		case "/base/dir/":
			fmt.Fprint(w, `<html><body><a href="nested/">nested/</a><a href="asset.bin">asset.bin</a></body></html>`)
		case "/base/dir/nested/":
			fmt.Fprint(w, `<html><body>empty</body></html>`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	baseURL, _ := url.Parse(server.URL + "/base/")
	repository := &memoryRepository{}
	crawler, err := New(server.Client(), repository, Config{BaseURL: baseURL, Workers: 2, MaxDepth: 3, MaxDirectories: 10})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := crawler.Crawl(context.Background()); err != nil {
		t.Fatalf("Crawl() error = %v", err)
	}

	mu.Lock()
	sort.Strings(requested)
	gotRequests := append([]string(nil), requested...)
	mu.Unlock()
	wantRequests := []string{"/base/", "/base/dir/", "/base/dir/nested/"}
	if fmt.Sprint(gotRequests) != fmt.Sprint(wantRequests) {
		t.Fatalf("requested = %v, want %v", gotRequests, wantRequests)
	}

	outcomes := repository.snapshot()
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3: %#v", len(outcomes), outcomes)
	}
	for _, outcome := range outcomes {
		if outcome.Error != "" || outcome.StatusCode != http.StatusOK {
			t.Fatalf("unexpected outcome: %#v", outcome)
		}
	}
}

func TestCrawlerRecordsDirectoryFailureAndContinues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/base/":
			fmt.Fprint(w, `<a href="bad/">bad/</a><a href="good/">good/</a>`)
		case "/base/bad/":
			http.Error(w, "broken", http.StatusInternalServerError)
		case "/base/good/":
			fmt.Fprint(w, `<a href="file.txt">file.txt</a>`)
		}
	}))
	defer server.Close()

	baseURL, _ := url.Parse(server.URL + "/base/")
	repository := &memoryRepository{}
	crawler, err := New(server.Client(), repository, Config{BaseURL: baseURL, Workers: 2, MaxDepth: 2, MaxDirectories: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := crawler.Crawl(context.Background()); err != nil {
		t.Fatalf("Crawl() error = %v", err)
	}

	outcomes := repository.snapshot()
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(outcomes))
	}
	var sawBad, sawGood bool
	for _, outcome := range outcomes {
		switch outcome.StatusCode {
		case http.StatusInternalServerError:
			sawBad = outcome.Error != ""
		case http.StatusOK:
			if parsed, _ := url.Parse(outcome.URL); parsed.Path == "/base/good/" {
				sawGood = true
			}
		}
	}
	if !sawBad || !sawGood {
		t.Fatalf("outcomes did not record failure and success: %#v", outcomes)
	}
}

func TestCrawlerHonorsDepthAndDirectoryBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		fmt.Fprintf(w, `<a href="a/">a/</a><a href="b/">b/</a><a href="c/">c/</a>`)
	}))
	defer server.Close()

	baseURL, _ := url.Parse(server.URL + "/base/")
	repository := &memoryRepository{}
	crawler, err := New(server.Client(), repository, Config{BaseURL: baseURL, Workers: 3, MaxDepth: 5, MaxDirectories: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := crawler.Crawl(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(repository.snapshot()); got != 2 {
		t.Fatalf("recorded directories = %d, want bound 2", got)
	}
}

var _ Repository = (*memoryRepository)(nil)
var _ = parser.Entry{}
