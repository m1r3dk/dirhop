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

	"github.com/m1r3dk/dirclone/internal/app"
	"github.com/m1r3dk/dirclone/internal/config"
	"github.com/m1r3dk/dirclone/internal/shell"
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
