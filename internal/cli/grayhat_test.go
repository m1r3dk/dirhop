package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m1r3dk/dirhop/internal/bucket"
	"github.com/m1r3dk/dirhop/internal/grayhat"
)

// The whole point of --scan is that bucket strings returned by GrayHatWarfare
// can be crawled by dirhop. GHW returns host-only strings (and Azure containers
// as host/container with no trailing slash); these must flow through BucketURL
// into bucket.Detect and resolve to the right provider. This pins the exact
// formats from the GHW v2 docs so a detection regression is caught offline.
func TestGHWBucketFormatsAreDetectable(t *testing.T) {
	cases := []struct {
		raw      string
		provider bucket.Provider
		bucket   string
		prefix   string
	}{
		{"my-logs.s3-eu-west-1.amazonaws.com", bucket.S3, "my-logs", ""},
		{"1000bebes.s3.amazonaws.com", bucket.S3, "1000bebes", ""},
		{"coolstuff.blob.core.windows.net/coupons", bucket.Azure, "coupons", ""},
		{"assets.nyc3.digitaloceanspaces.com", bucket.S3, "assets", ""},
	}
	for _, tc := range cases {
		url := grayhat.BucketURL(tc.raw)
		got, ok := bucket.Detect(url)
		if !ok || got.Provider != tc.provider || got.Bucket != tc.bucket || got.Prefix != tc.prefix {
			t.Errorf("Detect(BucketURL(%q)=%q) = %+v ok=%v, want provider=%s bucket=%s prefix=%q",
				tc.raw, url, got, ok, tc.provider, tc.bucket, tc.prefix)
		}
	}
	// A bare Azure account with no container is not addressable and must not be
	// mistaken for a crawlable bucket.
	if _, ok := bucket.Detect(grayhat.BucketURL("acct.blob.core.windows.net")); ok {
		t.Error("bare Azure account host should not be detectable as a bucket")
	}
}

// ghw searches GrayHatWarfare and prints matches. The command must send the
// configured API key, render results, and support --urls for piping.
func TestGHWSearchBucketsListsAndPrintsURLs(t *testing.T) {
	var sawAuth string
	ghw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"meta":{"results":2},"buckets":[
			{"id":1,"bucket":"acme.s3.amazonaws.com","fileCount":42,"type":"aws"},
			{"id":2,"bucket":"acct.blob.core.windows.net/pub","fileCount":7,"type":"azure"}
		]}`)
	}))
	defer ghw.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	body := "database = \"" + filepath.Join(tmp, "db") + "\"\n" +
		"grayhatwarfare_api_key = \"test-key\"\n" +
		"grayhatwarfare_base_url = \"" + ghw.URL + "\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		var out, errb bytes.Buffer
		root, cleanup, err := newRoot(&out, &errb, configPath)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("dirhop %v: %v; stderr=%s", args, err, errb.String())
		}
		return out.String()
	}

	listing := run("ghw", "acme")
	if !strings.Contains(listing, "https://acme.s3.amazonaws.com") || !strings.Contains(listing, "aws") {
		t.Fatalf("listing missing bucket URL/type:\n%s", listing)
	}
	if !strings.Contains(listing, "Showing 2 of 2 matches") {
		t.Fatalf("listing missing footer:\n%s", listing)
	}
	if sawAuth != "Bearer test-key" {
		t.Errorf("auth header=%q", sawAuth)
	}

	urls := run("ghw", "acme", "--urls")
	want := "https://acme.s3.amazonaws.com\nhttps://acct.blob.core.windows.net/pub\n"
	if urls != want {
		t.Fatalf("--urls output=%q want %q", urls, want)
	}
}

// Without a key, ghw must fail with a clear, user-fixable error (exit 2), never
// a silent empty result or a generic crash.
func TestGHWRequiresAPIKey(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Guard against a leaked key in the environment.
	t.Setenv("GRAYHATWARFARE_API_KEY", "")
	var out bytes.Buffer
	root, cleanup, err := newRoot(&out, &out, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	root.SetArgs([]string{"ghw", "anything"})
	err = root.Execute()
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("exit=%d err=%v, want exit 2 and a no-API-key message", ExitCode(err), err)
	}
}

// ghw --scan must feed matched bucket URLs into dirhop's own indexer, producing
// a browsable session. This exercises the full discovery-to-index path with a
// fake GrayHatWarfare API pointing at a fake bucket.
func TestGHWScanIndexesMatchedBuckets(t *testing.T) {
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><ListBucketResult><Name>found</Name><KeyCount>1</KeyCount><IsTruncated>false</IsTruncated>`+
			`<Contents><Key>secret/report.pdf</Key><Size>2048</Size></Contents></ListBucketResult>`)
	}))
	defer bucket.Close()

	// The fake bucket is http://127.0.0.1:port. BucketURL only prefixes https://
	// when a scheme is absent, so the GHW response includes the scheme to keep
	// the scan on http for the test server.
	ghw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"meta":{"results":1},"buckets":[{"id":1,"bucket":"%s/","fileCount":1,"type":"aws"}]}`, bucket.URL)
	}))
	defer ghw.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	body := "database = \"" + filepath.Join(tmp, "db") + "\"\n" +
		"history = \"" + filepath.Join(tmp, "history") + "\"\n" +
		"grayhatwarfare_api_key = \"k\"\n" +
		"grayhatwarfare_base_url = \"" + ghw.URL + "\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	root, cleanup, err := newRoot(&out, &errb, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	root.SetArgs([]string{"ghw", "report", "--scan"})
	if err := root.Execute(); err != nil {
		t.Fatalf("ghw --scan: %v; stderr=%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "Indexed") || !strings.Contains(out.String(), "1 files") {
		t.Fatalf("scan did not index the matched bucket:\n%s", out.String())
	}
}
