package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m1r3dk/dirhop/internal/app"
	"github.com/m1r3dk/dirhop/internal/config"
	"github.com/m1r3dk/dirhop/internal/shell"
)

// The shell must run the same command implementations (and flags) as the CLI,
// keep a separate working directory per session, and restore it on switch.
func TestShellDelegatesToSharedCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a/":
			fmt.Fprint(w, `<title>Index of /a</title><a href="big.iso">big.iso</a><a href="sub%20dir/">sub dir/</a>`)
		case "/a/sub dir/":
			fmt.Fprint(w, `<title>Index</title><a href="x.zip">x.zip</a>`)
		case "/b/":
			fmt.Fprint(w, `<title>Index of /b</title><a href="docs/">docs/</a>`)
		case "/b/docs/":
			fmt.Fprint(w, `<title>Index</title><a href="r.pdf">r.pdf</a>`)
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
	cfg.Paths.Database, cfg.Paths.History = filepath.Join(tmp, "db"), filepath.Join(tmp, "h")
	a, err := app.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	if _, _, err := a.OpenURL(ctx, server.URL+"/b/", "beta", false); err != nil {
		t.Fatal(err)
	}
	site, _, err := a.OpenURL(ctx, server.URL+"/a/", "alpha", true)
	if err != nil {
		t.Fatal(err)
	}

	script := strings.Join([]string{
		`cd "sub dir"`, `find --ext zip`, `use beta`, `cd docs`, `use alpha`, `pwd`, `nope`, `exit`,
	}, "\n")
	var out bytes.Buffer
	if err := shell.Run(ctx, a, site, strings.NewReader(script), &out, shellExec(a)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"sub dir/x.zip", "Session changed: beta", "beta:/docs >", "alpha:/sub dir > /sub dir", `unknown command "nope"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in shell output:\n%s", want, got)
		}
	}
}

// The shell `sessions` listing is numbered, and `use <n>` switches by that row
// number so users do not have to type long generated session names. Rows are in
// the same name order the listing prints.
func TestShellUseByNumber(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a/":
			fmt.Fprint(w, `<title>Index of /a</title><a href="x.zip">x.zip</a>`)
		case "/b/":
			fmt.Fprint(w, `<title>Index of /b</title><a href="y.zip">y.zip</a>`)
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
	cfg.Paths.Database, cfg.Paths.History = filepath.Join(tmp, "db"), filepath.Join(tmp, "h")
	a, err := app.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	if _, _, err := a.OpenURL(ctx, server.URL+"/b/", "beta", false); err != nil {
		t.Fatal(err)
	}
	site, _, err := a.OpenURL(ctx, server.URL+"/a/", "alpha", true)
	if err != nil {
		t.Fatal(err)
	}

	// Rows (name order): 1=alpha, 2=beta. Switch to 2, then back to 1, then try
	// an out-of-range number.
	script := strings.Join([]string{"use 2", "pwd", "use 1", "use 9", "exit"}, "\n")
	var out bytes.Buffer
	if err := shell.Run(ctx, a, site, strings.NewReader(script), &out, shellExec(a)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Session changed: beta", "beta:/ >", "Session changed: alpha", "no session #9"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in shell output:\n%s", want, got)
		}
	}
	// The numbered listing must show a # column so users know the row numbers.
	var list bytes.Buffer
	if err := shell.Run(ctx, a, site, strings.NewReader("sessions\nexit\n"), &list, shellExec(a)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.String(), "#") {
		t.Fatalf("sessions listing is not numbered:\n%s", list.String())
	}
}

// The shell `sessions` listing must report an index size per session, matching
// the stored total each session computed when crawled.
func TestShellSessionsReportSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a/":
			fmt.Fprint(w, `<title>Index of /a</title><a href="big.iso">big.iso</a> 2.0K`)
		case "/a/big.iso":
			fmt.Fprint(w, "0123456789")
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
	cfg.Paths.Database, cfg.Paths.History = filepath.Join(tmp, "db"), filepath.Join(tmp, "h")
	a, err := app.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	site, _, err := a.OpenURL(ctx, server.URL+"/a/", "alpha", true)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := shell.Run(ctx, a, site, strings.NewReader("sessions\nexit\n"), &out, shellExec(a)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// Header column plus the session's own non-empty size (parsed "2.0K" = 2048).
	if !strings.Contains(got, "SIZE") {
		t.Fatalf("sessions listing missing SIZE header:\n%s", got)
	}
	if !strings.Contains(got, "2.0 KiB") {
		t.Fatalf("sessions listing missing expected size for alpha:\n%s", got)
	}
}
