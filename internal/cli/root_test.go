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

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
		case "/pub/root.zip":
			fmt.Fprint(w, "ZIP")
		case "/pub/docs/manual.pdf":
			fmt.Fprint(w, "MANUAL")
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
			t.Fatalf("dirhop %v: %v; stderr=%s", args, err, stderr.String())
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
	treeJSON := run("-s", "fixture", "tree", "--json")
	if !strings.Contains(treeJSON, `"Name": "root.zip"`) || strings.Contains(treeJSON, "directories, ") {
		t.Fatalf("tree --json output changed from entry JSON: %q", treeJSON)
	}
	if requests.Load() != requestCount {
		t.Fatal("local tree --json unexpectedly accessed network")
	}
	found := run("-s", "fixture", "find", "*.zip", "--json")
	if !strings.Contains(found, "root.zip") {
		t.Fatalf("find output: %q", found)
	}
	run("-s", "fixture", "cd", "/docs", "--quiet")
	if got := strings.TrimSpace(run("-s", "fixture", "pwd")); got != "/docs" {
		t.Fatalf("pwd=%q", got)
	}
	if got := run("-s", "fixture", "cat", "/root.zip", "manual.pdf"); got != "ZIPMANUAL" {
		t.Fatalf("cat output=%q want ZIPMANUAL", got)
	}
}

func TestAllSitesFindSearchAndDownload(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/a/":
			fmt.Fprint(w, `<title>Index of /a/</title><a href="root.zip">root.zip</a><a href="manual.txt">manual.txt</a>`)
		case "/a/root.zip":
			fmt.Fprint(w, "ALPHAZIP")
		case "/a/manual.txt":
			fmt.Fprint(w, "MANUAL")
		case "/b/":
			fmt.Fprint(w, `<title>Index of /b/</title><a href="nested/">nested/</a>`)
		case "/b/nested/":
			fmt.Fprint(w, `<title>Index of /b/nested/</title><a href="secrets.zip">secrets.zip</a><a href="notes.txt">notes.txt</a>`)
		case "/b/nested/secrets.zip":
			fmt.Fprint(w, "BETAZIP")
		case "/b/nested/notes.txt":
			fmt.Fprint(w, "NOTES")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "index.db")+"\"\nhistory = \""+filepath.Join(tmp, "history")+"\"\n"), 0o600); err != nil {
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
			t.Fatalf("dirhop %v: %v; stderr=%s", args, err, stderr.String())
		}
		return stdout.String()
	}

	run("--name", "alpha", "scan", server.URL+"/a/")
	run("--name", "beta", "scan", server.URL+"/b/")
	indexedRequests := requests.Load()

	found := run("find", "--all-sites", "--ext", "zip")
	if !strings.Contains(found, "alpha:root.zip") || !strings.Contains(found, "beta:nested/secrets.zip") || strings.Contains(found, "manual.txt") {
		t.Fatalf("all-sites find output: %q", found)
	}
	stats := run("stats")
	if !strings.Contains(stats, "Sessions:    2") || !strings.Contains(stats, "Files:       4") || !strings.Contains(stats, ".zip") || !strings.Contains(stats, ".txt") {
		t.Fatalf("stats output: %q", stats)
	}
	statsJSON := run("stats", "--json")
	if !strings.Contains(statsJSON, `"Files": 4`) || !strings.Contains(statsJSON, `"Extension": ".zip"`) {
		t.Fatalf("stats json output: %q", statsJSON)
	}
	searched := run("search", "--all-sites", "secrets")
	if !strings.Contains(searched, "beta:nested/secrets.zip") || strings.Contains(searched, "alpha:") {
		t.Fatalf("all-sites search output: %q", searched)
	}
	if requests.Load() != indexedRequests {
		t.Fatal("all-sites find/search unexpectedly accessed network")
	}

	downloadDir := filepath.Join(tmp, "downloads")
	run("download", "--all-sites", "--ext", "zip", "--output", downloadDir)
	if got, err := os.ReadFile(filepath.Join(downloadDir, "alpha", "root.zip")); err != nil || string(got) != "ALPHAZIP" {
		t.Fatalf("alpha zip = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(downloadDir, "beta", "nested", "secrets.zip")); err != nil || string(got) != "BETAZIP" {
		t.Fatalf("beta zip = %q, %v", got, err)
	}
}

func TestUnknownSessionIsClearExitCode3(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-s", "nosuch", "ls"}, {"session", "use", "nosuch"}, {"session", "rename", "nosuch", "x"}, {"session", "delete", "nosuch", "--yes"}} {
		var out bytes.Buffer
		root, cleanup, err := newRoot(&out, &out, configPath)
		if err != nil {
			t.Fatal(err)
		}
		root.SetArgs(args)
		err = root.Execute()
		cleanup()
		if ExitCode(err) != 3 || !strings.Contains(err.Error(), `unknown session "nosuch"`) {
			t.Fatalf("%v: exit=%d err=%v", args, ExitCode(err), err)
		}
	}
}

// Every flag must be reachable by a POSIX long name, and no command may
// register the same shorthand twice (including inherited persistent flags).
func TestEveryFlagHasLongNameAndUniqueShorthand(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root, cleanup, err := newRoot(&out, &out, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		seen := map[string]string{}
		cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
			if f.Shorthand != "" {
				seen[f.Shorthand] = f.Name
			}
		})
		cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "" {
				t.Errorf("%s: flag with shorthand -%s has no long name", cmd.CommandPath(), f.Shorthand)
			}
			if f.Shorthand == "" {
				return
			}
			if other, dup := seen[f.Shorthand]; dup && other != f.Name {
				t.Errorf("%s: shorthand -%s maps to both --%s and --%s", cmd.CommandPath(), f.Shorthand, other, f.Name)
			}
			seen[f.Shorthand] = f.Name
		})
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// Every command displayed in help must explain what it does. This walks nested
// commands too, so newly added command groups cannot silently regress.
func TestEveryCommandHasDescription(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root, cleanup, err := newRoot(&out, &out, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if strings.TrimSpace(cmd.Short) == "" {
			t.Errorf("%s has no help description", cmd.CommandPath())
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// Short flags documented to users must keep working alongside their long form.
func TestShortAndLongFlagsAreEquivalent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pub/" {
			fmt.Fprint(w, `<title>Index of /pub/</title><a href="a.zip">a.zip</a>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "index.db")+"\"\nhistory = \""+filepath.Join(tmp, "history")+"\"\n"), 0o600); err != nil {
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
			t.Fatalf("dirhop %v: %v; stderr=%s", args, err, stderr.String())
		}
		return stdout.String()
	}

	run("--name", "pairs", "scan", server.URL+"/pub/")
	for _, pair := range [][2][]string{
		{{"-s", "pairs", "find", "--ext", "zip"}, {"--session", "pairs", "find", "-e", "zip"}},
		{{"-s", "pairs", "urls", "--files-only"}, {"-s", "pairs", "urls", "-f"}},
		{{"-s", "pairs", "ls", "--json"}, {"-s", "pairs", "ls", "-j"}},
		{{"-s", "pairs", "tree", "--depth", "1"}, {"-s", "pairs", "tree", "-L", "1"}},
		{{"-s", "pairs", "errors", "--limit", "5"}, {"-s", "pairs", "errors", "-l", "5"}},
	} {
		if long, short := run(pair[0]...), run(pair[1]...); long != short {
			t.Errorf("%v=%q differs from %v=%q", pair[0], long, pair[1], short)
		}
	}
}

// Version queries must be recognized before the app (and its database) open, so
// `dirhop version`/`--version` work even when the local index is missing or
// stale. `version --json` sets the JSON flag; other commands are not matched.
func TestVersionQueryRouting(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		wantJSON bool
		wantOK   bool
	}{
		{[]string{"version"}, false, true},
		{[]string{"--version"}, false, true},
		{[]string{"-V"}, false, true},
		{[]string{"version", "--json"}, true, true},
		{[]string{"version", "-j"}, true, true},
		{[]string{"ls"}, false, false},
		{[]string{"-s", "x", "version"}, false, false},
		{[]string{"version", "--bogus"}, false, false},
		{nil, false, false},
	} {
		gotJSON, gotOK := versionQuery(tc.args)
		if gotJSON != tc.wantJSON || gotOK != tc.wantOK {
			t.Errorf("versionQuery(%v) = (json=%v, ok=%v), want (json=%v, ok=%v)",
				tc.args, gotJSON, gotOK, tc.wantJSON, tc.wantOK)
		}
	}
}

func TestConfigArgumentAcceptsPOSIXSpellings(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--config", "a.toml", "ls"}, "a.toml"},
		{[]string{"--config=a.toml", "ls"}, "a.toml"},
		{[]string{"-c", "a.toml", "ls"}, "a.toml"},
		{[]string{"-c=a.toml", "ls"}, "a.toml"},
		{[]string{"-ca.toml", "ls"}, "a.toml"},
		{[]string{"ls", "-l"}, ""},
		{[]string{"--color"}, ""},
	} {
		if got := configArgument(tc.args); got != tc.want {
			t.Errorf("configArgument(%v)=%q want %q", tc.args, got, tc.want)
		}
	}
}

// Flag misuse is user error and must exit 2, not the generic 1.
func TestFlagMisuseExitsWithInvalidArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pub/" {
			fmt.Fprint(w, `<title>Index of /pub/</title><a href="a.zip">a.zip</a>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "index.db")+"\"\nhistory = \""+filepath.Join(tmp, "history")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec := func(args ...string) error {
		var out bytes.Buffer
		root, cleanup, err := newRoot(&out, &out, configPath)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		root.SetArgs(args)
		return root.Execute()
	}
	if err := exec("--name", "ex", "scan", server.URL+"/pub/"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-s", "ex", "ls", "-Z"},
		{"-s", "ex", "ls", "--nope"},
		{"-s", "ex", "tree", "-d", "-f"},
		{"-s", "ex", "tree", "--dirs-only", "--files-only"},
		{"-s", "ex", "urls", "-f", "-d"},
		{"-s", "ex", "download", "--all", "--overwrite", "--skip-existing"},
		{"-s", "ex", "cat", "--json", "a.zip"},
		{"-s", "ex", "cat", "/"},
	} {
		err := exec(args...)
		if ExitCode(err) != 2 {
			t.Errorf("%v: exit=%d err=%v, want exit 2", args, ExitCode(err), err)
		}
	}
}

// `dirhop shell` is advertised in the root help; it must exist and run.
func TestShellSubcommandRunsOnSelectedSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pub/" {
			fmt.Fprint(w, `<title>Index of /pub/</title><a href="a.zip">a.zip</a>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "index.db")+"\"\nhistory = \""+filepath.Join(tmp, "history")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root, cleanup, err := newRoot(&out, &out, configPath)
	if err != nil {
		t.Fatal(err)
	}
	root.SetArgs([]string{"--name", "sh", "scan", server.URL + "/pub/"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	cleanup()

	// The root help advertises "dirhop shell"; make sure it is a real command.
	var help bytes.Buffer
	root, cleanup, err = newRoot(&help, &help, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var found bool
	for _, c := range root.Commands() {
		if c.Name() == "shell" {
			found = true
		}
	}
	if !found {
		t.Fatal("root help documents `dirhop shell` but no such command is registered")
	}
	if !strings.Contains(root.Example, "dirhop shell") {
		t.Fatal("expected the shell example to stay documented")
	}
}
