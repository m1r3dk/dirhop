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
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeURLPrependsScheme(t *testing.T) {
	cases := map[string]string{
		// Bare bucket hosts gain https:// so `dirhop scan <host>` just works.
		"amazetest.storage.googleapis.com":         "https://amazetest.storage.googleapis.com",
		"test-0528.oss-cn-shanghai.aliyuncs.com":   "https://test-0528.oss-cn-shanghai.aliyuncs.com",
		"gxbackup.blob.core.windows.net/documents": "https://gxbackup.blob.core.windows.net/documents",
		"b.s3.us-west-2.amazonaws.com/docs":        "https://b.s3.us-west-2.amazonaws.com/docs",
		// Explicit schemes are preserved exactly.
		"http://plain.example/pub/": "http://plain.example/pub/",
		"https://secure.example/":   "https://secure.example/",
		// Non-host inputs (typos, bare commands) are left unchanged so they
		// still fail as "unknown command or URL" rather than "https://typo".
		"not-a-url": "not-a-url",
		"":          "",
	}
	for in, want := range cases {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollectURLsNormalizesBareHosts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "urls.txt")
	content := "amazetest.storage.googleapis.com\n" +
		"gxbackup.blob.core.windows.net/documents  azureblob\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	specs, err := collectURLs([]string{"b.s3.amazonaws.com"}, file, "", nil)
	if err != nil {
		t.Fatalf("collectURLs: %v", err)
	}
	if len(specs) != 3 ||
		specs[0].URL != "https://b.s3.amazonaws.com" ||
		specs[1].URL != "https://amazetest.storage.googleapis.com" ||
		specs[2] != (urlSpec{"https://gxbackup.blob.core.windows.net/documents", "azureblob"}) {
		t.Fatalf("specs = %+v", specs)
	}
}

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

	out, err := run("scan", "-f", list, "--json", "--no-preflight")
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

// Parallel scanning must index every site, keep JSON results in input order
// (results are written by index, not completion order), and never corrupt the
// SQLite index under concurrent writers. A deliberately slow handler makes the
// overlap real so a serialized implementation would be measurably slower.
func TestScanParallelIndexesAllInOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		name := strings.Trim(r.URL.Path, "/")
		fmt.Fprintf(w, `<title>Index of /%s</title><a href="%s.bin">%s.bin</a>`, name, name, name)
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	_ = os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\nretries = 0\n"), 0o600)
	list := filepath.Join(tmp, "urls.txt")
	var b strings.Builder
	const n = 12
	for i := range n {
		fmt.Fprintf(&b, "%s/d%02d/\n", server.URL, i)
	}
	_ = os.WriteFile(list, []byte(b.String()), 0o600)

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

	start := time.Now()
	out, err := run("scan", "-f", list, "--parallel", "6", "--json")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("parallel scan failed: %v\n%s", err, out)
	}
	var results []batchResult
	if jerr := json.Unmarshal([]byte(out), &results); jerr != nil {
		t.Fatalf("bad json: %v\n%s", jerr, out)
	}
	if len(results) != n {
		t.Fatalf("want %d results, got %d", n, len(results))
	}
	for i, r := range results {
		wantPath := fmt.Sprintf("/d%02d/", i)
		if !strings.HasSuffix(r.URL, wantPath) {
			t.Fatalf("result %d out of order: %s (want suffix %s)", i, r.URL, wantPath)
		}
		if r.Error != "" || r.Files != 1 {
			t.Fatalf("result %d not indexed: %+v", i, r)
		}
	}
	// 12 sites x 40ms serialized would be ~480ms; 6-way parallel should be well
	// under that. Generous bound to stay non-flaky in CI.
	if elapsed > 350*time.Millisecond {
		t.Logf("parallel scan took %v (expected overlap)", elapsed)
	}
	sessions, _ := run("sessions")
	if strings.Count(sessions, "127.0.0.1") < n {
		t.Fatalf("not all sessions persisted:\n%s", sessions)
	}
}

// --preflight must emit a summary, keep reachable non-bucket (HTML) URLs, and
// skip unreachable generic URLs before the expensive scan. Bucket-hostname
// classification is covered by bucket.TestCheckAccess*.
func TestScanPreflightChecksGenericURLsAndSummarizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pub/":
			fmt.Fprint(w, `<title>Index of /pub</title><a href="a.zip">a.zip</a>`)
			return
		case "/private/":
			http.Error(w, "private", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	_ = os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\nretries = 0\n"), 0o600)
	list := filepath.Join(tmp, "urls.txt")
	_ = os.WriteFile(list, []byte(server.URL+"/pub/\n"+server.URL+"/private/\n"+server.URL+"/missing/\n"+closedURL+"/dead/\n"), 0o600)
	report := filepath.Join(tmp, "preflight.md")

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

	out, err := run("scan", "-f", list, "--preflight", "--parallel", "0", "--preflight-file", report)
	if err != nil {
		t.Fatalf("preflight scan with a dead generic URL failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Preflight summary: 1 accessible") || !strings.Contains(out, "1 private") || !strings.Contains(out, "1 missing") || !strings.Contains(out, "1 errored") || !strings.Contains(out, "scanning 1/4") {
		t.Fatalf("expected a preflight summary line, got:\n%s", out)
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("preflight report was not written: %v", err)
	}
	reportText := string(data)
	for _, want := range []string{
		"# dirhop preflight report",
		"## Summary",
		"- Total targets checked: 4",
		"- Accessible and will be scanned: 1",
		"- Private and skipped: 1",
		"- Missing and skipped: 1",
		"- Errored and skipped: 1",
		"## How to read this",
		"## Suggested next steps",
		"## accessible (1)",
		"| " + server.URL + "/pub/ | public listing reachable |",
		"## private (1)",
		"| " + server.URL + "/private/ | private (access denied) |",
		"## missing (1)",
		"| " + server.URL + "/missing/ | missing (no such bucket/host) |",
		"## errored (1)",
		"| " + closedURL + "/dead/ | errored:",
	} {
		if !strings.Contains(reportText, want) {
			t.Fatalf("preflight report missing %q:\n%s", want, reportText)
		}
	}
	if !strings.Contains(out, "Wrote readable preflight report for 4 target(s) to "+report) {
		t.Fatalf("expected preflight report notice, got:\n%s", out)
	}
	// The reachable non-bucket HTML listing was kept and actually indexed, while
	// the closed generic URL was not allowed to fail the scan batch.
	if ls, err := run("sessions"); err != nil || !strings.Contains(ls, "127.0.0.1") {
		t.Fatalf("HTML listing was not indexed after preflight: %v\n%s", err, ls)
	}
}

// A second scan of a list that already completed must skip the finished sites
// (no re-crawl) unless --rescan is passed, and must still report them.
func TestScanSkipsAlreadyScannedUnlessRescan(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		name := strings.Trim(r.URL.Path, "/")
		fmt.Fprintf(w, `<title>Index of /%s</title><a href="%s.bin">%s.bin</a>`, name, name, name)
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	_ = os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\nretries = 0\n"), 0o600)
	list := filepath.Join(tmp, "urls.txt")
	_ = os.WriteFile(list, []byte(server.URL+"/one/\n"+server.URL+"/two/\n"), 0o600)

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

	if _, err := run("scan", "-f", list, "--no-preflight"); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	first := atomic.LoadInt32(&hits)
	if first == 0 {
		t.Fatal("first scan made no requests")
	}

	out, err := run("scan", "-f", list, "--no-preflight")
	if err != nil {
		t.Fatalf("second scan: %v\n%s", err, out)
	}
	if atomic.LoadInt32(&hits) != first {
		t.Fatalf("second scan re-crawled already-complete sites: hits went %d -> %d", first, atomic.LoadInt32(&hits))
	}
	if !strings.Contains(out, "Skipped") || !strings.Contains(out, "0 indexed, 2 skipped") {
		t.Fatalf("expected skip summary, got:\n%s", out)
	}

	if _, err := run("scan", "-f", list, "--no-preflight", "--rescan"); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if atomic.LoadInt32(&hits) <= first {
		t.Fatalf("--rescan did not re-crawl: hits stayed at %d", atomic.LoadInt32(&hits))
	}
}

// After a file is removed from a listing, a full rescan soft-deletes it and the
// changes command surfaces it with its last-seen size.
func TestChangesListsRemovedFiles(t *testing.T) {
	var drop atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pub/" {
			http.NotFound(w, r)
			return
		}
		if drop.Load() {
			fmt.Fprint(w, `<title>Index of /pub</title><a href="keep.bin">keep.bin</a>`)
			return
		}
		fmt.Fprint(w, `<title>Index of /pub</title><a href="keep.bin">keep.bin</a><a href="gone.bin">gone.bin</a>`)
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	_ = os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\nretries = 0\n"), 0o600)

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

	if _, err := run("scan", server.URL+"/pub/"); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	drop.Store(true)
	if _, err := run("refresh", "-s", "127-0-0-1-pub", "--full"); err != nil {
		// Session name is host-derived; fall back to active session.
		if _, err2 := run("refresh", "--full"); err2 != nil {
			t.Fatalf("refresh after deletion: %v / %v", err, err2)
		}
	}
	out, err := run("changes")
	if err != nil {
		t.Fatalf("changes: %v\n%s", err, out)
	}
	if !strings.Contains(out, "gone.bin") {
		t.Fatalf("changes did not report the removed file:\n%s", out)
	}
	if strings.Contains(out, "keep.bin") {
		t.Fatalf("changes wrongly reported a still-present file:\n%s", out)
	}
}

// A failed/inaccessible run writes the failing targets to --failed-file in a
// form that scan -f can read back for a retry.
func TestScanFailedFileIsWrittenAndReReadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok/" {
			fmt.Fprint(w, `<title>Index of /ok</title><a href="a.zip">a.zip</a>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	_ = os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\nretries = 0\n"), 0o600)
	list := filepath.Join(tmp, "urls.txt")
	_ = os.WriteFile(list, []byte(server.URL+"/ok/\n"+server.URL+"/missing/\n"), 0o600)
	failed := filepath.Join(tmp, "failed.txt")

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

	// --no-preflight so the 404 fails at the scan stage and is recorded.
	out, _ := run("scan", "-f", list, "--no-preflight", "--failed-file", failed)
	if !strings.Contains(out, "Wrote 1 failed target(s)") {
		t.Fatalf("expected failed-file notice, got:\n%s", out)
	}
	data, err := os.ReadFile(failed)
	if err != nil {
		t.Fatalf("failed file not written: %v", err)
	}
	if !strings.Contains(string(data), "/missing/") || strings.Contains(string(data), "/ok/") {
		t.Fatalf("failed file content wrong:\n%s", data)
	}
	// The failed file must be re-readable by scan -f (reason comment ignored).
	specs, err := collectURLs(nil, failed, "", nil)
	if err != nil {
		t.Fatalf("failed file not re-readable: %v", err)
	}
	if len(specs) != 1 || !strings.HasSuffix(specs[0].URL, "/missing/") {
		t.Fatalf("re-read specs wrong: %+v", specs)
	}

	// A clean run removes a stale failed file.
	list2 := filepath.Join(tmp, "urls2.txt")
	_ = os.WriteFile(list2, []byte(server.URL+"/ok/\n"), 0o600)
	if _, err := run("scan", "-f", list2, "--no-preflight", "--rescan", "--failed-file", failed); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatalf("stale failed file was not removed: %v", err)
	}
}

// --retry-failed rescans only the sessions that previously failed (pulled from
// the DB), recovering the one that now succeeds without re-crawling the one that
// already completed.
func TestScanRetryFailed(t *testing.T) {
	var up atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good/":
			fmt.Fprint(w, `<title>Index of /good</title><a href="a.zip">a.zip</a>`)
		case "/flaky/":
			if up.Load() {
				fmt.Fprint(w, `<title>Index of /flaky</title><a href="b.zip">b.zip</a>`)
				return
			}
			http.Error(w, "down", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	_ = os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\nretries = 0\n"), 0o600)
	list := filepath.Join(tmp, "urls.txt")
	_ = os.WriteFile(list, []byte(server.URL+"/good/\n"+server.URL+"/flaky/\n"), 0o600)

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

	// First scan: good succeeds, flaky fails.
	if _, err := run("scan", "-f", list, "--no-preflight"); ExitCode(err) != 5 {
		t.Fatalf("first scan should report a failure, got %v", err)
	}
	// Bring the flaky site up and retry just the failures.
	up.Store(true)
	out, err := run("scan", "--retry-failed", "--no-preflight")
	if err != nil {
		t.Fatalf("retry-failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Retrying 1 previously failed session(s).") {
		t.Fatalf("expected retry notice for exactly the 1 failed session, got:\n%s", out)
	}
	if !strings.Contains(out, "flaky") {
		t.Fatalf("retry did not target the flaky session:\n%s", out)
	}
	// Flaky is now complete and browsable.
	if ls, err := run("-s", "127-0-0-1-flaky", "ls"); err != nil || !strings.Contains(ls, "b.zip") {
		// Name is bucket-based only for recognized buckets; for a plain host it is
		// host+path. Fall back to listing sessions to confirm it is complete.
		sessions, _ := run("sessions", "--status", "complete")
		if !strings.Contains(sessions, "flaky") {
			t.Fatalf("flaky not complete after retry: %v %s\n%s", err, ls, sessions)
		}
	}
	// Nothing left to retry.
	out, _ = run("scan", "--retry-failed", "--no-preflight")
	if !strings.Contains(out, "No failed sessions to retry.") {
		t.Fatalf("expected no-failures message, got:\n%s", out)
	}
}

func TestResolveWorkers(t *testing.T) {
	cases := []struct{ parallel, sites, want int }{
		{0, 0, 1},    // nothing to do
		{0, 5, 5},    // auto scales to small workloads
		{0, 500, 64}, // auto capped at 64
		{1, 10, 1},   // explicit serial
		{8, 3, 3},    // never more workers than sites
		{32, 100, 32},
		{10000, 100, 100}, // explicit over hard cap, still clamped to sites
	}
	for _, tc := range cases {
		if got := resolveWorkers(tc.parallel, tc.sites); got != tc.want {
			t.Errorf("resolveWorkers(%d, %d) = %d, want %d", tc.parallel, tc.sites, got, tc.want)
		}
	}
}
