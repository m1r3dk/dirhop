package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sourceFunc func(context.Context, []string) ([]Entry, error)

func (f sourceFunc) Expand(ctx context.Context, paths []string) ([]Entry, error) {
	return f(ctx, paths)
}

type eventSink struct {
	mu      sync.Mutex
	events  []Event
	active  atomic.Int32
	overlap atomic.Bool
}

func (s *eventSink) Record(_ context.Context, event Event) error {
	if s.active.Add(1) != 1 {
		s.overlap.Store(true)
	}
	defer s.active.Add(-1)
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *eventSink) statuses(path string) []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	var statuses []Status
	for _, event := range s.events {
		if event.Entry.Path == path {
			statuses = append(statuses, event.Status)
		}
	}
	return statuses
}

func TestDownloadExpandsPlanPreservesHierarchyAndBoundsConcurrency(t *testing.T) {
	t.Parallel()
	var active atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer server.Close()

	entries := []Entry{
		{Path: "root/a.txt", URL: server.URL + "/alpha", Size: 5},
		{Path: "root/deep/b.txt", URL: server.URL + "/bravo", Size: 5},
		{Path: "root/deep/c.txt", URL: server.URL + "/cello", Size: 5},
		{Path: "root/d.txt", URL: server.URL + "/delta", Size: 5},
	}
	var gotPaths []string
	source := sourceFunc(func(_ context.Context, paths []string) ([]Entry, error) {
		gotPaths = append([]string(nil), paths...)
		return entries, nil
	})
	sink := &eventSink{}
	d := Downloader{Source: source, State: sink, Client: server.Client()}
	destination := t.TempDir()

	result, err := d.Download(context.Background(), []string{"root"}, destination, Options{Concurrency: 2})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "root" {
		t.Fatalf("source paths = %v", gotPaths)
	}
	if result.Planned != 4 || result.Completed != 4 || result.Failed != 0 || result.Bytes != 20 {
		t.Fatalf("result = %+v", result)
	}
	if got := maximum.Load(); got < 2 || got > 2 {
		t.Fatalf("maximum concurrency = %d, want 2", got)
	}
	if sink.overlap.Load() {
		t.Fatal("state callbacks overlapped")
	}
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(destination, filepath.FromSlash(entry.Path)))
		if readErr != nil {
			t.Fatalf("read %s: %v", entry.Path, readErr)
		}
		if string(data) != strings.TrimPrefix(mustPathFromURL(t, entry.URL), "/") {
			t.Fatalf("content for %s = %q", entry.Path, data)
		}
		if got := sink.statuses(entry.Path); !equalStatuses(got, []Status{StatusPlanned, StatusRunning, StatusCompleted}) {
			t.Fatalf("statuses for %s = %v", entry.Path, got)
		}
		if _, statErr := os.Stat(filepath.Join(destination, filepath.FromSlash(entry.Path)) + ".part"); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("partial still exists for %s", entry.Path)
		}
	}
}

func TestResumeRequiresValid206AndRestartsSafely(t *testing.T) {
	t.Parallel()
	content := []byte("0123456789")
	t.Run("valid range appends", func(t *testing.T) {
		var rangeHeader string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rangeHeader = r.Header.Get("Range")
			w.Header().Set("Content-Range", "bytes 4-9/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[4:])
		}))
		defer server.Close()
		destination := t.TempDir()
		partial := filepath.Join(destination, "file.bin.part")
		if err := os.WriteFile(partial, content[:4], 0o644); err != nil {
			t.Fatal(err)
		}
		d := Downloader{Client: server.Client()}
		_, err := d.DownloadPlan(context.Background(), []Entry{{Path: "file.bin", URL: server.URL, Size: 10}}, destination, Options{})
		if err != nil {
			t.Fatalf("DownloadPlan() error = %v", err)
		}
		if rangeHeader != "bytes=4-" {
			t.Fatalf("Range = %q", rangeHeader)
		}
		assertFileContent(t, filepath.Join(destination, "file.bin"), content)
	})

	t.Run("invalid range is discarded and full body replaces partial", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request := requests.Add(1)
			if request == 1 {
				w.Header().Set("Content-Range", "bytes 0-9/10")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(content)
				return
			}
			if r.Header.Get("Range") != "" {
				t.Errorf("fallback request unexpectedly had Range %q", r.Header.Get("Range"))
			}
			_, _ = w.Write(content)
		}))
		defer server.Close()
		destination := t.TempDir()
		if err := os.WriteFile(filepath.Join(destination, "file.bin.part"), []byte("bad!"), 0o644); err != nil {
			t.Fatal(err)
		}
		d := Downloader{Client: server.Client()}
		_, err := d.DownloadPlan(context.Background(), []Entry{{Path: "file.bin", URL: server.URL, Size: 10}}, destination, Options{})
		if err != nil {
			t.Fatalf("DownloadPlan() error = %v", err)
		}
		if requests.Load() != 2 {
			t.Fatalf("requests = %d, want 2", requests.Load())
		}
		assertFileContent(t, filepath.Join(destination, "file.bin"), content)
	})

	t.Run("ignored range restarts from zero", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(content)
		}))
		defer server.Close()
		destination := t.TempDir()
		if err := os.WriteFile(filepath.Join(destination, "file.bin.part"), content[:3], 0o644); err != nil {
			t.Fatal(err)
		}
		d := Downloader{Client: server.Client()}
		_, err := d.DownloadPlan(context.Background(), []Entry{{Path: "file.bin", URL: server.URL, Size: 10}}, destination, Options{})
		if err != nil {
			t.Fatalf("DownloadPlan() error = %v", err)
		}
		assertFileContent(t, filepath.Join(destination, "file.bin"), content)
	})
}

func TestExistingPolicies(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "new")
	}))
	defer server.Close()
	entry := Entry{Path: "file.txt", URL: server.URL, Size: 3}

	t.Run("skip", func(t *testing.T) {
		destination := t.TempDir()
		path := filepath.Join(destination, entry.Path)
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := requests.Load()
		result, err := (&Downloader{Client: server.Client()}).DownloadPlan(context.Background(), []Entry{entry}, destination, Options{Policy: ExistingSkip})
		if err != nil || result.Skipped != 1 {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		if requests.Load() != before {
			t.Fatal("skip made an HTTP request")
		}
		assertFileContent(t, path, []byte("old"))
	})

	t.Run("error", func(t *testing.T) {
		destination := t.TempDir()
		path := filepath.Join(destination, entry.Path)
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := (&Downloader{Client: server.Client()}).DownloadPlan(context.Background(), []Entry{entry}, destination, Options{Policy: ExistingError})
		if err == nil || result.Failed != 1 {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		assertFileContent(t, path, []byte("old"))
	})

	t.Run("overwrite", func(t *testing.T) {
		destination := t.TempDir()
		path := filepath.Join(destination, entry.Path)
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := (&Downloader{Client: server.Client()}).DownloadPlan(context.Background(), []Entry{entry}, destination, Options{Policy: ExistingOverwrite})
		if err != nil || result.Completed != 1 {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		assertFileContent(t, path, []byte("new"))
	})
}

func TestRetriesSizeVerificationAndIndependentFailures(t *testing.T) {
	t.Parallel()
	var flakyRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/flaky", func(w http.ResponseWriter, _ *http.Request) {
		if flakyRequests.Add(1) == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "good")
	})
	mux.HandleFunc("/short", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "bad")
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	destination := t.TempDir()
	entries := []Entry{
		{Path: "ok.txt", URL: server.URL + "/flaky", Size: 4},
		{Path: "bad.txt", URL: server.URL + "/short", Size: 4},
	}
	result, err := (&Downloader{Client: server.Client()}).DownloadPlan(context.Background(), entries, destination, Options{Concurrency: 2, Retries: 1, Backoff: time.Millisecond})
	if err == nil {
		t.Fatal("DownloadPlan() error = nil")
	}
	var planErr *PlanError
	if !errors.As(err, &planErr) || planErr.Failures["bad.txt"] == nil {
		t.Fatalf("error = %#v", err)
	}
	if result.Completed != 1 || result.Failed != 1 || flakyRequests.Load() != 2 {
		t.Fatalf("result = %+v, flaky requests = %d", result, flakyRequests.Load())
	}
	assertFileContent(t, filepath.Join(destination, "ok.txt"), []byte("good"))
	if _, statErr := os.Stat(filepath.Join(destination, "bad.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bad final file exists, stat error = %v", statErr)
	}
	assertFileContent(t, filepath.Join(destination, "bad.txt.part"), []byte("bad"))
}

func TestCancellationStopsAndLeavesPartial(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			once.Do(func() { close(started) })
			if _, err := w.Write(bytes.Repeat([]byte{'x'}, 1024)); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	destination := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := (&Downloader{Client: server.Client()}).DownloadPlan(ctx, []Entry{{Path: "large.bin", URL: server.URL, Size: 100 * 1024}}, destination, Options{})
		done <- err
	}()
	<-started
	time.Sleep(15 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("download did not stop after cancellation")
	}
	info, err := os.Stat(filepath.Join(destination, "large.bin.part"))
	if err != nil {
		t.Fatalf("partial missing: %v", err)
	}
	if info.Size() <= 0 || info.Size() >= 100*1024 {
		t.Fatalf("partial size = %d", info.Size())
	}
	if _, err := os.Stat(filepath.Join(destination, "large.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final file exists, stat error = %v", err)
	}
}

func TestSegmentedDownloadAndSafeFallback(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("abcdefghij"), 100)

	t.Run("validated ranges merge in order", func(t *testing.T) {
		var ranged atomic.Int32
		server := httptest.NewServer(rangeHandler(t, content, &ranged))
		defer server.Close()
		destination := t.TempDir()
		result, err := (&Downloader{Client: server.Client()}).DownloadPlan(context.Background(), []Entry{{Path: "file.bin", URL: server.URL, Size: int64(len(content))}}, destination, Options{Concurrency: 2, Segments: 4, SegmentThreshold: 1})
		if err != nil || result.Completed != 1 {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		if ranged.Load() < 5 {
			t.Fatalf("range requests = %d, want probe plus four pieces", ranged.Load())
		}
		assertFileContent(t, filepath.Join(destination, "file.bin"), content)
		matches, err := filepath.Glob(filepath.Join(destination, "file.bin.part.seg-*"))
		if err != nil || len(matches) != 0 {
			t.Fatalf("segment files remain: %v, err = %v", matches, err)
		}
	})

	t.Run("ignored probe falls back to one stream", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			_, _ = w.Write(content)
		}))
		defer server.Close()
		destination := t.TempDir()
		result, err := (&Downloader{Client: server.Client()}).DownloadPlan(context.Background(), []Entry{{Path: "file.bin", URL: server.URL, Size: int64(len(content))}}, destination, Options{Concurrency: 2, Segments: 4, SegmentThreshold: 1})
		if err != nil || result.Completed != 1 {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		if requests.Load() != 2 {
			t.Fatalf("requests = %d, want probe and fallback", requests.Load())
		}
		assertFileContent(t, filepath.Join(destination, "file.bin"), content)
	})
}

func TestPathValidationAndDuplicateOutputs(t *testing.T) {
	t.Parallel()
	d := Downloader{}
	for _, path := range []string{"", "../escape", "/absolute"} {
		if _, err := d.DownloadPlan(context.Background(), []Entry{{Path: path, URL: "https://example.test/file", Size: -1}}, t.TempDir(), Options{}); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
	entries := []Entry{
		{Path: "a/../same", URL: "https://example.test/1", Size: -1},
		{Path: "same", URL: "https://example.test/2", Size: -1},
	}
	if _, err := d.DownloadPlan(context.Background(), entries, t.TempDir(), Options{}); err == nil {
		t.Fatal("duplicate normalized outputs accepted")
	}
	if _, err := d.DownloadPlan(context.Background(), []Entry{{Path: "file", URL: "file:///tmp/source", Size: -1}}, t.TempDir(), Options{}); err == nil {
		t.Fatal("non-HTTP URL accepted")
	}
}

func TestRateLimiterHonorsContextAndApproximateRate(t *testing.T) {
	t.Parallel()
	limiter := newRateLimiter(1000)
	start := time.Now()
	if err := limiter.wait(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("rate limiter elapsed = %v", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.wait(ctx, 1000); !errors.Is(err, context.Canceled) {
		t.Fatalf("rate limiter error = %v", err)
	}
}

func rangeHandler(t *testing.T, content []byte, count *atomic.Int32) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Range")
		if !strings.HasPrefix(header, "bytes=") {
			t.Errorf("missing Range header")
			http.Error(w, "range required", http.StatusBadRequest)
			return
		}
		bounds := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
		if len(bounds) != 2 {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		start, err := strconv.ParseInt(bounds[0], 10, 64)
		if err != nil {
			http.Error(w, "bad start", http.StatusBadRequest)
			return
		}
		end := int64(len(content) - 1)
		if bounds[1] != "" {
			end, err = strconv.ParseInt(bounds[1], 10, 64)
			if err != nil {
				http.Error(w, "bad end", http.StatusBadRequest)
				return
			}
		}
		if start < 0 || end < start || end >= int64(len(content)) {
			http.Error(w, "unsatisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		count.Add(1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	})
}

func mustPathFromURL(t *testing.T, rawURL string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request.URL.Path
}

func equalStatuses(a, b []Status) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content at %q = %q, want %q", path, got, want)
	}
}
