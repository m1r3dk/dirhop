package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOneShotCommandsReusePersistentIndex(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/pub/":
			fmt.Fprint(w, `<title>Index of /pub/</title><a href="docs/">docs/</a><a href="root.zip">root.zip</a>`)
		case "/pub/docs/":
			fmt.Fprint(w, `<title>Index</title><a href="manual.pdf">manual.pdf</a>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	databasePath := filepath.Join(tmp, "index.db")
	if err := os.WriteFile(configPath, []byte("database = \""+databasePath+"\"\nhistory = \""+filepath.Join(tmp, "history")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		root, cleanup, err := newRoot(&stdout, &stderr, configPath)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("dirclone %v: %v; stderr=%s", args, err, stderr.String())
		}
		return stdout.String()
	}

	run("--name", "fixture", "scan", server.URL+"/pub/")
	requestCount := requests.Load()
	listing := run("-s", "fixture", "ls", "/")
	if !strings.Contains(listing, "docs/") || !strings.Contains(listing, "root.zip") {
		t.Fatalf("ls output: %q", listing)
	}
	if requests.Load() != requestCount {
		t.Fatal("local ls unexpectedly accessed network")
	}
	found := run("-s", "fixture", "find", "*.zip", "--json")
	if !strings.Contains(found, "root.zip") {
		t.Fatalf("find output: %q", found)
	}
	run("-s", "fixture", "cd", "/docs", "--quiet")
	if got := strings.TrimSpace(run("-s", "fixture", "pwd")); got != "/docs" {
		t.Fatalf("pwd=%q", got)
	}
}
