package parser

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDetectAndParseListingFormats(t *testing.T) {
	pageURL := mustURL(t, "https://example.test/base/")
	tests := []struct {
		name       string
		html       string
		wantParser string
		wantName   string
		wantDir    bool
	}{
		{
			name:       "apache",
			html:       `<html><head><title>Index of /base/</title></head><body><table><tr><th>Name</th></tr><tr><td><a href="file.txt">file.txt</a></td><td>2026-01-02 03:04</td><td>12K</td></tr></table><address>Apache Server</address></body></html>`,
			wantParser: "apache", wantName: "file.txt",
		},
		{
			name: "nginx",
			html: `<html><head><title>Index of /base/</title></head><body><h1>Index of /base/</h1><hr><pre><a href="../">../</a>
<a href="child/">child/</a>  02-Jan-2026 03:04  -</pre><hr><center>nginx</center></body></html>`,
			wantParser: "nginx", wantName: "child", wantDir: true,
		},
		{
			name:       "python",
			html:       `<html><head><title>Directory listing for /base/</title></head><body><h1>Directory listing for /base/</h1><ul><li><a href="data.csv">data.csv</a></li></ul></body></html>`,
			wantParser: "python", wantName: "data.csv",
		},
		{
			name:       "generic",
			html:       `<html><body><a href="notes.txt">notes.txt</a></body></html>`,
			wantParser: "generic", wantName: "notes.txt",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parserName, entries, err := Parse(strings.NewReader(test.html), http.Header{"Content-Type": {"text/html"}}, pageURL)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if parserName != test.wantParser {
				t.Fatalf("parser = %q, want %q", parserName, test.wantParser)
			}
			if len(entries) != 1 {
				t.Fatalf("entries = %#v, want one", entries)
			}
			if entries[0].Name != test.wantName || entries[0].IsDir != test.wantDir {
				t.Fatalf("entry = %#v, want name=%q dir=%v", entries[0], test.wantName, test.wantDir)
			}
		})
	}
}

func TestResolveURLConfinesLinksToHostAndBasePath(t *testing.T) {
	base := mustURL(t, "https://Example.test:443/root/path/")
	page := mustURL(t, "https://example.test/root/path/child/")

	resolved, err := ResolveURL(base, page, "next%20dir/", true)
	if err != nil {
		t.Fatalf("ResolveURL() error = %v", err)
	}
	if got, want := resolved.String(), "https://example.test/root/path/child/next%20dir/"; got != want {
		t.Fatalf("resolved = %q, want %q", got, want)
	}

	unsafe := []string{
		"../../outside/",
		"https://evil.test/root/path/",
		"javascript:alert(1)",
		"?C=N;O=D",
		"../",
	}
	for _, href := range unsafe {
		if _, err := ResolveURL(base, page, href, true); err == nil {
			t.Errorf("ResolveURL(%q) succeeded, want rejection", href)
		}
	}
}

func TestGenericParserRejectsEmptyFragmentAndParentLinks(t *testing.T) {
	html := `<html><body>
<a href="../">Parent Directory</a>
<a href="#section">section</a>
<a href="">empty</a>
<a href="good/">good/</a>
</body></html>`
	_, entries, err := Parse(strings.NewReader(html), nil, mustURL(t, "https://example.test/base/"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "good" || !entries[0].IsDir {
		t.Fatalf("entries = %#v, want only good directory", entries)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
