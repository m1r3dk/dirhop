package bucket

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		raw, provider, bucket, prefix, endpoint string
	}{
		{"https://example-bucket.s3.amazonaws.com/", "s3", "example-bucket", "", "https://example-bucket.s3.amazonaws.com/"},
		{"https://b.s3.us-west-2.amazonaws.com/docs", "s3", "b", "docs/", "https://b.s3.us-west-2.amazonaws.com/"},
		{"https://b.s3-eu-west-1.amazonaws.com/", "s3", "b", "", "https://b.s3-eu-west-1.amazonaws.com/"},
		{"https://s3.amazonaws.com/b/x/y/", "s3", "b", "x/y/", "https://s3.amazonaws.com/b/"},
		{"https://storage.googleapis.com/example_bucket/", "gcs", "example_bucket", "", "https://storage.googleapis.com/example_bucket/"},
		{"https://storage.googleapis.com/b/p", "gcs", "b", "p/", "https://storage.googleapis.com/b/"},
		{"https://b.storage.googleapis.com/", "gcs", "b", "", "https://b.storage.googleapis.com/"},
	}
	for _, tt := range tests {
		got, ok := Detect(tt.raw)
		if !ok || string(got.Provider) != tt.provider || got.Bucket != tt.bucket || got.Prefix != tt.prefix || got.Endpoint.String() != tt.endpoint {
			t.Errorf("Detect(%q) = %+v %v", tt.raw, got, ok)
		}
	}
	for _, raw := range []string{"https://example.com/files/", "https://storage.googleapis.com/", "https://ec2.amazonaws.com/"} {
		if _, ok := Detect(raw); ok {
			t.Errorf("Detect(%q) unexpectedly matched", raw)
		}
	}
}

func TestObjectURLEscapesKeys(t *testing.T) {
	target, _ := Detect("https://b.s3.amazonaws.com/")
	if got := target.ObjectURL("dir/ a&b/report 1.pdf"); got != "https://b.s3.amazonaws.com/dir/%20a&b/report%201.pdf" {
		t.Fatalf("ObjectURL = %s", got)
	}
}

func TestListPaginatesWithContinuationToken(t *testing.T) {
	pagesServed := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != "docs/" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		pagesServed++
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("continuation-token") == "" {
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>tok/=+</NextContinuationToken><Contents><Key>docs/a.pdf</Key><Size>10</Size><LastModified>2024-02-19T16:32:00.000Z</LastModified><ETag>&quot;abc&quot;</ETag></Contents></ListBucketResult>`)
			return
		}
		if r.URL.Query().Get("continuation-token") != "tok/=+" {
			t.Errorf("token not round-tripped: %q", r.URL.Query().Get("continuation-token"))
		}
		fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>docs/sub/b &amp; c.txt</Key><Size>5</Size></Contents></ListBucketResult>`)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/")
	var keys []string
	pages, err := List(context.Background(), server.Client(), Target{Provider: S3, Prefix: "docs/", Endpoint: endpoint}, "test", func(objs []Object) error {
		for _, o := range objs {
			keys = append(keys, o.Key)
			if o.Key == "docs/a.pdf" && (o.ETag != "abc" || o.Size != 10 || o.LastModified.Year() != 2024) {
				t.Errorf("metadata lost: %+v", o)
			}
		}
		return nil
	})
	if err != nil || pages != 2 || pagesServed != 2 || strings.Join(keys, "|") != "docs/a.pdf|docs/sub/b & c.txt" {
		t.Fatalf("pages=%d keys=%v err=%v", pages, keys, err)
	}
}

func TestCheckAccessClassifies(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		provider Provider
		want     Access
	}{
		{"public", http.StatusOK, S3, AccessPublic},
		{"denied", http.StatusForbidden, S3, AccessDenied},
		{"missing s3", http.StatusNotFound, S3, AccessMissing},
		{"denied azure 404", http.StatusNotFound, Azure, AccessDenied},
		{"unexpected", http.StatusInternalServerError, S3, AccessError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sawMaxKeys, sawAzure bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawMaxKeys = r.URL.Query().Get("max-keys") == "1" || r.URL.Query().Get("maxresults") == "1"
				sawAzure = r.URL.Query().Get("comp") == "list"
				if tc.status == http.StatusOK {
					fmt.Fprint(w, `<ListBucketResult></ListBucketResult>`)
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL + "/")
			got, _ := CheckAccess(context.Background(), server.Client(), Target{Provider: tc.provider, Endpoint: endpoint}, "ua")
			if got != tc.want {
				t.Fatalf("CheckAccess = %v, want %v", got, tc.want)
			}
			if !sawMaxKeys {
				t.Error("preflight did not send a max-1 listing bound")
			}
			if tc.provider == Azure && !sawAzure {
				t.Error("azure preflight did not send comp=list")
			}
		})
	}
}

func TestCheckAccessFallsBackToHTTPOnHTTPSFailure(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("max-keys") != "1" {
			t.Errorf("fallback did not keep max-keys=1: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `<ListBucketResult></ListBucketResult>`)
	}))
	defer server.Close()

	endpoint, _ := url.Parse(strings.Replace(server.URL, "http://", "https://", 1) + "/")
	got, err := CheckAccess(context.Background(), server.Client(), Target{Provider: S3, Endpoint: endpoint}, "ua")
	if got != AccessPublic || err != nil {
		t.Fatalf("CheckAccess = %v, %v; want public after HTTP fallback", got, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("HTTP fallback server saw %d requests, want 1", hits.Load())
	}
}

// A dead host (DNS failure) must classify as missing, not a generic error, so
// preflight can drop it with confidence.
func TestCheckAccessDeadHostIsMissing(t *testing.T) {
	endpoint, _ := url.Parse("https://dirhop-nonexistent-host.invalid/")
	got, err := CheckAccess(context.Background(), &http.Client{Timeout: 5 * time.Second}, Target{Provider: S3, Endpoint: endpoint}, "ua")
	if got != AccessMissing {
		t.Fatalf("CheckAccess = %v (err=%v), want missing", got, err)
	}
}

func TestListAccessDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = xml.NewEncoder(w).Encode(errorResult{Code: "AccessDenied", Message: "Access Denied"})
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/")
	_, err := List(context.Background(), server.Client(), Target{Endpoint: endpoint}, "", func([]Object) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("err=%v", err)
	}
}

// fakeBucket implements ListObjectsV2 semantics: sorted keys, prefix,
// delimiter roll-up, exclusive start-after, max-keys truncation and opaque
// continuation tokens. pageSize forces many pages.
func fakeBucket(t *testing.T, keys []string, pageSize int, delay time.Duration, active, peak *atomic.Int64) *httptest.Server {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		defer active.Add(-1)
		time.Sleep(delay)
		q := r.URL.Query()
		prefix, delim := q.Get("prefix"), q.Get("delimiter")
		after := q.Get("start-after")
		if tok := q.Get("continuation-token"); tok != "" {
			after = tok
		}
		var items []string // keys or common prefixes, in order
		isPrefix := map[string]bool{}
		for _, k := range sorted {
			if !strings.HasPrefix(k, prefix) || k <= after {
				continue
			}
			item := k
			if delim != "" {
				if i := strings.Index(k[len(prefix):], delim); i >= 0 {
					item = k[:len(prefix)+i+1]
					if item <= after || (len(items) > 0 && items[len(items)-1] == item) {
						continue
					}
					isPrefix[item] = true
				}
			}
			items = append(items, item)
		}
		truncated := len(items) > pageSize
		if truncated {
			items = items[:pageSize]
		}
		var b strings.Builder
		fmt.Fprintf(&b, "<ListBucketResult><IsTruncated>%v</IsTruncated>", truncated)
		if truncated {
			last := items[len(items)-1]
			if isPrefix[last] {
				last += "\U0010FFFF" // skip everything under the rolled-up prefix
			}
			fmt.Fprintf(&b, "<NextContinuationToken>%s</NextContinuationToken>", html.EscapeString(last))
		}
		for _, it := range items {
			if isPrefix[it] {
				fmt.Fprintf(&b, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", html.EscapeString(it))
			} else {
				fmt.Fprintf(&b, "<Contents><Key>%s</Key><Size>1</Size></Contents>", html.EscapeString(it))
			}
		}
		b.WriteString("</ListBucketResult>")
		fmt.Fprint(w, b.String())
	}))
}

func walkAll(t *testing.T, server *httptest.Server, prefix string, workers int) map[string]int {
	t.Helper()
	endpoint, _ := url.Parse(server.URL + "/")
	got := map[string]int{}
	_, err := Walk(context.Background(), server.Client(), Target{Endpoint: endpoint, Prefix: prefix}, "", workers, func(objs []Object, _ []string) error {
		for _, o := range objs {
			got[o.Key]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertExactlyOnce(t *testing.T, got map[string]int, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d distinct keys, want %d", len(got), len(want))
	}
	for _, k := range want {
		if got[k] != 1 {
			t.Fatalf("key %q listed %d times", k, got[k])
		}
	}
}

func TestWalkSplitsLargeFlatFolderExactlyOnce(t *testing.T) {
	var keys []string
	for i := 0; i < 900; i++ {
		first := "AaBbMmNnSsvz0_-"[i%15]
		keys = append(keys, fmt.Sprintf("big/%c%04d.pdf", first, i))
	}
	keys = append(keys, "big/日本語.pdf", "big/émile.pdf", "big/~tilde", "big/ space.pdf", "big/sub/x.txt", "big/sub/y.txt", "other/z.txt", "root.txt")
	var active, peak atomic.Int64
	server := fakeBucket(t, keys, 25, 2*time.Millisecond, &active, &peak)
	defer server.Close()
	for _, workers := range []int{1, 3, 8, 32} {
		assertExactlyOnce(t, walkAll(t, server, "", workers), keys)
	}
	if peak.Load() < 4 {
		t.Fatalf("flat folder was not listed in parallel: peak=%d", peak.Load())
	}
	var bigOnly []string
	for _, k := range keys {
		if strings.HasPrefix(k, "big/") {
			bigOnly = append(bigOnly, k)
		}
	}
	assertExactlyOnce(t, walkAll(t, server, "big/", 8), bigOnly)
}

func TestWalkFindsAllKeysInParallel(t *testing.T) {
	var keys []string
	for d := 0; d < 12; d++ {
		for f := 0; f < 3; f++ {
			keys = append(keys, fmt.Sprintf("d%02d/sub/f%d.bin", d, f))
		}
	}
	keys = append(keys, "root.txt", "d00/", "weird//double.txt")
	var active, peak atomic.Int64
	server := fakeBucket(t, keys, 1000, 20*time.Millisecond, &active, &peak)
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/")
	got := map[string]int{}
	_, err := Walk(context.Background(), server.Client(), Target{Endpoint: endpoint}, "", 6, func(objs []Object, _ []string) error {
		for _, o := range objs {
			got[o.Key]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(keys) {
		t.Fatalf("got %d keys, want %d: %v", len(got), len(keys), got)
	}
	for k, n := range got {
		if n != 1 {
			t.Fatalf("key %q listed %d times", k, n)
		}
	}
	if p := peak.Load(); p < 2 || p > 6 {
		t.Fatalf("peak concurrency %d, want 2..6", p)
	}
}

func TestWalkStopsOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("prefix") == "" {
			fmt.Fprint(w, `<ListBucketResult><CommonPrefixes><Prefix>a/</Prefix></CommonPrefixes><CommonPrefixes><Prefix>b/</Prefix></CommonPrefixes></ListBucketResult>`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/")
	done := make(chan error, 1)
	go func() {
		_, err := Walk(context.Background(), server.Client(), Target{Endpoint: endpoint}, "", 4, func([]Object, []string) error { return nil })
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Walk hung after error")
	}
}

// Custom/CDN domains CNAMEd to object storage serve the same anonymous
// ListObjectsV2 XML but are not recognizable by hostname, so Probe asks the
// origin. It must accept real bucket endpoints and reject ordinary websites.
func TestProbeDetectsS3CompatibleCustomDomains(t *testing.T) {
	const listing = `<?xml version='1.0' encoding='UTF-8'?><ListBucketResult xmlns='http://doc.s3.amazonaws.com/2006-03-01'><Name>cdn-bucket</Name><Prefix></Prefix><KeyCount>1</KeyCount><MaxKeys>1</MaxKeys><IsTruncated>true</IsTruncated><Contents><Key>a.css</Key><Size>5</Size></Contents></ListBucketResult>`

	cases := []struct {
		name        string
		contentType string
		status      int
		body        string
		wantOK      bool
		wantBucket  string
	}{
		{"s3 compatible cdn", "application/xml; charset=UTF-8", 200, listing, true, "cdn-bucket"},
		{"html website", "text/html", 200, "<html><body><h1>Hello</h1></body></html>", false, ""},
		{"xml but not a listing", "application/xml", 200, "<rss><channel/></rss>", false, ""},
		{"access denied", "application/xml", 403, "<Error><Code>AccessDenied</Code></Error>", false, ""},
		// V1 listings are accepted: listRange falls back to marker paging, so
		// they are indexed in full rather than truncated.
		{"v1 listing without KeyCount", "application/xml", 200, `<ListBucketResult><Name>b</Name><Marker></Marker><IsTruncated>true</IsTruncated></ListBucketResult>`, true, "b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()

			target, ok := Probe(context.Background(), server.Client(), server.URL+"/", "dirhop-test")
			if ok != tc.wantOK {
				t.Fatalf("Probe ok=%v want %v", ok, tc.wantOK)
			}
			if !strings.Contains(gotQuery, "list-type=2") {
				t.Errorf("probe did not use the V2 listing API: %q", gotQuery)
			}
			if ok && target.Bucket != tc.wantBucket {
				t.Errorf("bucket=%q want %q", target.Bucket, tc.wantBucket)
			}
			if ok && target.Endpoint.Path != "/" {
				t.Errorf("endpoint path=%q want /", target.Endpoint.Path)
			}
		})
	}
}

func TestProbeUsesPathAsKeyPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("prefix"); got != "charting_library/" {
			t.Errorf("prefix=%q want charting_library/", got)
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<ListBucketResult><Name>b</Name><KeyCount>0</KeyCount></ListBucketResult>`)
	}))
	defer server.Close()

	target, ok := Probe(context.Background(), server.Client(), server.URL+"/charting_library", "ua")
	if !ok || target.Prefix != "charting_library/" || target.Endpoint.Path != "/" {
		t.Fatalf("target=%+v ok=%v", target, ok)
	}
}

// A V1-only server (no KeyCount, no continuation token) must be paged in full
// via marker, not silently truncated after the first page.
func TestListPaginatesV1WithNextMarker(t *testing.T) {
	var markers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		markers = append(markers, r.URL.Query().Get("marker"))
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Query().Get("marker") {
		case "":
			fmt.Fprint(w, `<ListBucketResult><Name>b</Name><IsTruncated>true</IsTruncated><NextMarker>k1</NextMarker><Contents><Key>k0</Key><Size>1</Size></Contents><Contents><Key>k1</Key><Size>1</Size></Contents></ListBucketResult>`)
		case "k1":
			fmt.Fprint(w, `<ListBucketResult><Name>b</Name><IsTruncated>true</IsTruncated><NextMarker>k3</NextMarker><Contents><Key>k2</Key><Size>1</Size></Contents><Contents><Key>k3</Key><Size>1</Size></Contents></ListBucketResult>`)
		default:
			fmt.Fprint(w, `<ListBucketResult><Name>b</Name><IsTruncated>false</IsTruncated><Contents><Key>k4</Key><Size>1</Size></Contents></ListBucketResult>`)
		}
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL + "/")
	var keys []string
	pages, err := List(context.Background(), server.Client(), Target{Provider: S3, Endpoint: endpoint}, "ua", func(objs []Object) error {
		for _, o := range objs {
			keys = append(keys, o.Key)
		}
		return nil
	})
	if err != nil || pages != 3 {
		t.Fatalf("pages=%d err=%v", pages, err)
	}
	if got := strings.Join(keys, ","); got != "k0,k1,k2,k3,k4" {
		t.Errorf("keys=%q, want all five (V1 listing was truncated)", got)
	}
	if strings.Join(markers, ",") != ",k1,k3" {
		t.Errorf("markers=%v, want ['' k1 k3]", markers)
	}
}

// V1 only guarantees NextMarker when a delimiter is set. Without it, paging
// must resume from the last key of the page.
func TestListPaginatesV1WithoutNextMarker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("marker") == "" {
			fmt.Fprint(w, `<ListBucketResult><Name>b</Name><IsTruncated>true</IsTruncated><Contents><Key>a</Key><Size>1</Size></Contents><Contents><Key>b</Key><Size>1</Size></Contents></ListBucketResult>`)
			return
		}
		if r.URL.Query().Get("marker") != "b" {
			t.Errorf("marker=%q want b (resume from last key)", r.URL.Query().Get("marker"))
		}
		fmt.Fprint(w, `<ListBucketResult><Name>b</Name><IsTruncated>false</IsTruncated><Contents><Key>c</Key><Size>1</Size></Contents></ListBucketResult>`)
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL + "/")
	var keys []string
	if _, err := List(context.Background(), server.Client(), Target{Provider: S3, Endpoint: endpoint}, "ua", func(objs []Object) error {
		for _, o := range objs {
			keys = append(keys, o.Key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keys, ","); got != "a,b,c" {
		t.Errorf("keys=%q want a,b,c", got)
	}
}

// A broken server that claims truncation but never advances must terminate
// rather than loop forever re-fetching the same page.
func TestListV1StopsWhenMarkerCannotAdvance(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		// Always truncated, always the same marker: a cursor that never moves.
		fmt.Fprint(w, `<ListBucketResult><Name>b</Name><IsTruncated>true</IsTruncated><NextMarker>stuck</NextMarker><Contents><Key>stuck</Key><Size>1</Size></Contents></ListBucketResult>`)
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL + "/")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = List(context.Background(), server.Client(), Target{Provider: S3, Endpoint: endpoint}, "ua", func([]Object) error { return nil })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("List looped forever on a non-advancing V1 marker")
	}
	if n := requests.Load(); n > 3 {
		t.Errorf("made %d requests on a stuck cursor, want it to stop quickly", n)
	}
}

// V2 servers must keep using continuation tokens, never the V1 marker.
func TestListPrefersV2ContinuationToken(t *testing.T) {
	var sawMarker bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("marker") != "" {
			sawMarker = true
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("continuation-token") == "" {
			fmt.Fprint(w, `<ListBucketResult><Name>b</Name><KeyCount>1</KeyCount><IsTruncated>true</IsTruncated><NextContinuationToken>tok</NextContinuationToken><Contents><Key>a</Key><Size>1</Size></Contents></ListBucketResult>`)
			return
		}
		fmt.Fprint(w, `<ListBucketResult><Name>b</Name><KeyCount>1</KeyCount><IsTruncated>false</IsTruncated><Contents><Key>b</Key><Size>1</Size></Contents></ListBucketResult>`)
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL + "/")
	var keys []string
	if _, err := List(context.Background(), server.Client(), Target{Provider: S3, Endpoint: endpoint}, "ua", func(objs []Object) error {
		for _, o := range objs {
			keys = append(keys, o.Key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sawMarker {
		t.Error("V2 server was sent a V1 marker parameter")
	}
	if strings.Join(keys, ",") != "a,b" {
		t.Errorf("keys=%v", keys)
	}
}
