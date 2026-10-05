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
	found := run("-s", "fixture", "find", "*.zip", "--json")
	if !strings.Contains(found, "root.zip") {
		t.Fatalf("find output: %q", found)
	}
	run("-s", "fixture", "cd", "/docs", "--quiet")
	if got := strings.TrimSpace(run("-s", "fixture", "pwd")); got != "/docs" {
		t.Fatalf("pwd=%q", got)
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
