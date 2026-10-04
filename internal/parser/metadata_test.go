package parser

import (
	"net/url"
	"strings"
	"testing"
)

func TestListingMetadata(t *testing.T) {
	page, _ := url.Parse("https://example.test/pub/")
	tests := []struct {
		name, parser, html, entry string
		size                      int64 // -1 means nil
		year                      int   // 0 means nil
	}{
		{"apache table", "apache", `<title>Index of /pub</title><table><tr><td><a href="app.zip">app.zip</a></td><td align="right">2024-06-28 10:46  </td><td align="right"> 38M</td></tr></table><address>Apache</address>`, "app.zip", 38 << 20, 2024},
		{"apache dir has no size", "apache", `<title>Index of /pub</title><table><tr><td><a href="bin/">bin/</a></td><td>2023-03-11 10:19</td><td>  - </td></tr></table>Apache`, "bin", -1, 2023},
		{"nginx pre", "nginx", "<title>Index of /pub/</title><pre><a href=\"a.iso\">a.iso</a>   20-Sep-2026 14:32   850000\n<a href=\"b/\">b/</a>  21-Sep-2025 10:00  -\n</pre>nginx", "a.iso", 850000, 2026},
		{"iis pre before link", "iis", `<pre><A HREF="/">[To Parent Directory]</A><br><br> 2/28/2025 11:26 AM         4120 <A HREF="/pub/mail.html">mail.html</A><br> 10/1/2026  2:10 PM        &lt;dir&gt; <A HREF="/pub/fonts/">fonts</A><br></pre>`, "mail.html", 4120, 2025},
		{"iis dir", "iis", `<pre><A HREF="/">[To Parent Directory]</A><br><br> 10/1/2026  2:10 PM        &lt;dir&gt; <A HREF="/pub/fonts/">fonts</A><br></pre>`, "fonts", -1, 2026},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, entries, err := Parse(strings.NewReader(tt.html), nil, page)
			if err != nil {
				t.Fatal(err)
			}
			if name != tt.parser {
				t.Fatalf("parser=%s want %s", name, tt.parser)
			}
			for _, e := range entries {
				if e.Name != tt.entry {
					continue
				}
				if tt.size < 0 && e.Size != nil || tt.size >= 0 && (e.Size == nil || *e.Size != tt.size) {
					t.Fatalf("size=%v want %d", e.Size, tt.size)
				}
				if tt.year == 0 && e.Modified != nil || tt.year != 0 && (e.Modified == nil || e.Modified.Year() != tt.year) {
					t.Fatalf("modified=%v want year %d", e.Modified, tt.year)
				}
				return
			}
			t.Fatalf("entry %q not found in %+v", tt.entry, entries)
		})
	}
}
