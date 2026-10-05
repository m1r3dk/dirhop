package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadURLFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "urls.txt")
	content := "# my sites\n\nhttps://a.example/pub/   alpha\nhttps://b.example/  # trailing comment\n  https://A.EXAMPLE/pub   \n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	specs, err := collectURLs(nil, file, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Third line is the same canonical URL as the first, so it is dropped.
	if len(specs) != 2 || specs[0] != (urlSpec{"https://a.example/pub/", "alpha"}) || specs[1].URL != "https://b.example/" {
		t.Fatalf("specs = %+v", specs)
	}

	specs, err = collectURLs([]string{"https://c.example/"}, "-", "", strings.NewReader("https://d.example/\n"))
	if err != nil || len(specs) != 2 || specs[1].URL != "https://d.example/" {
		t.Fatalf("stdin specs = %+v err=%v", specs, err)
	}

	for name, tc := range map[string]struct {
		args          []string
		body, nameArg string
	}{
		"bad scheme":        {body: "ftp://x.example/\n"},
		"too many fields":   {body: "https://x.example/ a b\n"},
		"empty":             {body: "# nothing\n"},
		"name with many":    {body: "https://x.example/\nhttps://y.example/\n", nameArg: "n"},
		"bad positional":    {args: []string{"not-a-url"}},
		"missing file path": {},
	} {
		path := filepath.Join(t.TempDir(), "f")
		if name != "missing file path" {
			_ = os.WriteFile(path, []byte(tc.body), 0o600)
		}
		if _, err := collectURLs(tc.args, path, tc.nameArg, nil); ExitCode(err) != 2 {
			t.Errorf("%s: want invalid-arguments exit 2, got %v", name, err)
		}
	}
}

func TestScanFromFileIndexesAllAndContinuesPastFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/one/":
			fmt.Fprint(w, `<title>Index of /one</title><a href="a.zip">a.zip</a>`)
		case "/two/":
			fmt.Fprint(w, `<title>Index of /two</title><a href="b.iso">b.iso</a><a href="c.iso">c.iso</a>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	_ = os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\nretries = 0\n"), 0o600)
	list := filepath.Join(tmp, "urls.txt")
	_ = os.WriteFile(list, []byte(server.URL+"/one/ first\n"+server.URL+"/missing/\n"+server.URL+"/two/\n"), 0o600)

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		root, cleanup, err := newRoot(&out, &out, configPath)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		root.SetArgs(args)
		err = root.Execute()
		return out.String(), err
	}

	out, err := run("scan", "-f", list, "--json")
	if ExitCode(err) != 5 {
		t.Fatalf("want network-failure exit 5 for the 404 site, got %v\n%s", err, out)
	}
	var results []batchResult
	if jerr := json.Unmarshal([]byte(out), &results); jerr != nil {
		t.Fatalf("bad json: %v\n%s", jerr, out)
	}
	if len(results) != 3 || results[0].Session != "first" || results[0].Files != 1 || results[1].Error == "" || results[2].Files != 2 {
		t.Fatalf("results = %+v", results)
	}
	sessions, _ := run("sessions")
	if !strings.Contains(sessions, "first") || strings.Count(sessions, "127.0.0.1") < 2 {
		t.Fatalf("sessions missing batch sites:\n%s", sessions)
	}
	if ls, err := run("-s", "first", "ls", "/"); err != nil || !strings.Contains(ls, "a.zip") {
		t.Fatalf("first session not browsable: %v %s", err, ls)
	}
}
