package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/m1r3dk/dirhop/internal/filesystem"
)

func newCatFixture(t *testing.T) (*App, string, *atomic.Int64) {
	t.Helper()
	var fileGets atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pub/":
			fmt.Fprint(w, `<title>Index of /pub/</title><a href="a.txt">a.txt</a><a href="b.bin">b.bin</a><a href="missing.txt">missing.txt</a><a href="dir/">dir/</a>`)
		case "/pub/dir/":
			fmt.Fprint(w, `<title>Index of /pub/dir/</title>`)
		case "/pub/a.txt":
			fileGets.Add(1)
			fmt.Fprint(w, "alpha\n")
		case "/pub/b.bin":
			fileGets.Add(1)
			_, _ = w.Write([]byte{0, 'B', 0xff})
		case "/pub/missing.txt":
			fileGets.Add(1)
			http.Error(w, "gone", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	a := newTestApp(t)
	site, _, err := a.OpenURL(context.Background(), server.URL+"/pub/", "cat", true)
	if err != nil {
		t.Fatal(err)
	}
	return a, site.Name, &fileGets
}

func TestCatStreamsExactBytesInArgumentOrder(t *testing.T) {
	a, siteName, gets := newCatFixture(t)
	site, err := a.Site(context.Background(), siteName)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := a.Cat(context.Background(), site, []string{"b.bin", "a.txt", "b.bin"}, &out); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0, 'B', 0xff}, []byte("alpha\n")...)
	want = append(want, 0, 'B', 0xff)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("cat bytes=%v want=%v", out.Bytes(), want)
	}
	if got := gets.Load(); got != 3 {
		t.Fatalf("file GETs=%d want 3", got)
	}
}

func TestCatRejectsDirectoryWithoutNetworkRequest(t *testing.T) {
	a, siteName, gets := newCatFixture(t)
	site, _ := a.Site(context.Background(), siteName)
	var out bytes.Buffer
	err := a.Cat(context.Background(), site, []string{"dir"}, &out)
	if !errors.Is(err, filesystem.ErrNotFile) || !strings.Contains(err.Error(), "/dir") {
		t.Fatalf("err=%v, want ErrNotFile for /dir", err)
	}
	if gets.Load() != 0 || out.Len() != 0 {
		t.Fatalf("directory cat performed GET or wrote output: gets=%d bytes=%d", gets.Load(), out.Len())
	}
}

func TestCatMapsMissingHTTPObjectToNetworkError(t *testing.T) {
	a, siteName, _ := newCatFixture(t)
	site, _ := a.Site(context.Background(), siteName)
	var out bytes.Buffer
	err := a.Cat(context.Background(), site, []string{"missing.txt"}, &out)
	if !errors.Is(err, ErrNetwork) || !strings.Contains(err.Error(), "HTTP 404 Not Found") {
		t.Fatalf("err=%v, want network HTTP 404", err)
	}
	if out.Len() != 0 {
		t.Fatalf("HTTP error body leaked to stdout: %q", out.String())
	}
}

func TestCatDoesNotAddNewline(t *testing.T) {
	a, siteName, _ := newCatFixture(t)
	site, _ := a.Site(context.Background(), siteName)
	var out bytes.Buffer
	if err := a.Cat(context.Background(), site, []string{"b.bin"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.Bytes(); !bytes.Equal(got, []byte{0, 'B', 0xff}) {
		t.Fatalf("cat changed file bytes: %v", got)
	}
}
