package bucket

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Detection for the newly supported stores: DigitalOcean Spaces (S3-compatible,
// virtual-hosted / CDN alias / path-style) and Azure Blob Storage.
func TestDetectSpacesAndAzure(t *testing.T) {
	tests := []struct {
		raw, provider, bucket, prefix, endpoint string
	}{
		// DigitalOcean Spaces, virtual-hosted.
		{"https://assets.nyc3.digitaloceanspaces.com/", "s3", "assets", "", "https://assets.nyc3.digitaloceanspaces.com/"},
		{"https://assets.nyc3.digitaloceanspaces.com/img/logos", "s3", "assets", "img/logos/", "https://assets.nyc3.digitaloceanspaces.com/"},
		// DigitalOcean Spaces, CDN alias: the listing endpoint falls back to the
		// origin host (the .cdn. host does not serve the listing API).
		{"https://assets.nyc3.cdn.digitaloceanspaces.com/", "s3", "assets", "", "https://assets.nyc3.digitaloceanspaces.com/"},
		// DigitalOcean Spaces, path-style.
		{"https://nyc3.digitaloceanspaces.com/mybucket/docs", "s3", "mybucket", "docs/", "https://nyc3.digitaloceanspaces.com/mybucket/"},
		// Azure Blob Storage.
		{"https://acct.blob.core.windows.net/container/", "azure", "container", "", "https://acct.blob.core.windows.net/container/"},
		{"https://acct.blob.core.windows.net/container/data/sub", "azure", "container", "data/sub/", "https://acct.blob.core.windows.net/container/"},
	}
	for _, tt := range tests {
		got, ok := Detect(tt.raw)
		if !ok || string(got.Provider) != tt.provider || got.Bucket != tt.bucket || got.Prefix != tt.prefix || got.Endpoint.String() != tt.endpoint {
			t.Errorf("Detect(%q) = %+v %v", tt.raw, got, ok)
		}
	}
	// A bare region host with no bucket in the path is not addressable.
	for _, raw := range []string{"https://nyc3.digitaloceanspaces.com/", "https://acct.blob.core.windows.net/"} {
		if _, ok := Detect(raw); ok {
			t.Errorf("Detect(%q) unexpectedly matched", raw)
		}
	}
}

// Azure uses a different URI, XML schema, and opaque NextMarker paging than S3.
// List must send restype=container&comp=list, follow the marker across pages,
// and map <Blob>/<BlobPrefix> metadata correctly.
func TestAzureListPaginatesWithNextMarker(t *testing.T) {
	var markers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("restype") != "container" || q.Get("comp") != "list" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		markers = append(markers, q.Get("marker"))
		w.Header().Set("Content-Type", "application/xml")
		if q.Get("marker") == "" {
			fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<EnumerationResults ServiceEndpoint="http://acct.blob.core.windows.net/" ContainerName="c">
  <Blobs>
    <Blob><Name>a.txt</Name><Properties><Last-Modified>Mon, 19 Feb 2024 16:32:00 GMT</Last-Modified><Etag>"0x8D"</Etag><Content-Length>10</Content-Length></Properties></Blob>
  </Blobs>
  <NextMarker>M2</NextMarker>
</EnumerationResults>`)
			return
		}
		fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<EnumerationResults ServiceEndpoint="http://acct.blob.core.windows.net/" ContainerName="c">
  <Blobs>
    <Blob><Name>b/c.bin</Name><Properties><Content-Length>5</Content-Length></Properties></Blob>
  </Blobs>
  <NextMarker/>
</EnumerationResults>`)
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL + "/")
	var keys []string
	pages, err := List(context.Background(), server.Client(), Target{Provider: Azure, Endpoint: endpoint}, "ua", func(objs []Object) error {
		for _, o := range objs {
			keys = append(keys, o.Key)
			if o.Key == "a.txt" && (o.Size != 10 || o.ETag != "0x8D" || o.LastModified.Year() != 2024) {
				t.Errorf("metadata lost: %+v", o)
			}
		}
		return nil
	})
	if err != nil || pages != 2 {
		t.Fatalf("pages=%d err=%v", pages, err)
	}
	if strings.Join(keys, ",") != "a.txt,b/c.bin" {
		t.Errorf("keys=%v", keys)
	}
	if strings.Join(markers, ",") != ",M2" {
		t.Errorf("markers=%v, want ['' M2]", markers)
	}
}

// Azure Walk must parallelize across BlobPrefix directories (it has no key-range
// splitting) and surface every blob exactly once.
func TestAzureWalkDescendsPrefixes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := r.URL.Query().Get("prefix")
		w.Header().Set("Content-Type", "application/xml")
		switch prefix {
		case "":
			fmt.Fprint(w, `<EnumerationResults><Blobs>
				<Blob><Name>root.txt</Name><Properties><Content-Length>1</Content-Length></Properties></Blob>
				<BlobPrefix><Name>docs/</Name></BlobPrefix>
				<BlobPrefix><Name>img/</Name></BlobPrefix>
			</Blobs><NextMarker/></EnumerationResults>`)
		case "docs/":
			fmt.Fprint(w, `<EnumerationResults><Blobs>
				<Blob><Name>docs/a.pdf</Name><Properties><Content-Length>2</Content-Length></Properties></Blob>
			</Blobs><NextMarker/></EnumerationResults>`)
		case "img/":
			fmt.Fprint(w, `<EnumerationResults><Blobs>
				<Blob><Name>img/b.png</Name><Properties><Content-Length>3</Content-Length></Properties></Blob>
			</Blobs><NextMarker/></EnumerationResults>`)
		default:
			t.Errorf("unexpected prefix %q", prefix)
		}
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL + "/")
	got := map[string]int{}
	_, err := Walk(context.Background(), server.Client(), Target{Provider: Azure, Endpoint: endpoint}, "ua", 4, func(objs []Object, _ []string) error {
		for _, o := range objs {
			got[o.Key]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"root.txt", "docs/a.pdf", "img/b.png"} {
		if got[k] != 1 {
			t.Errorf("key %q listed %d times", k, got[k])
		}
	}
	if len(got) != 3 {
		t.Errorf("got %d keys, want 3: %v", len(got), got)
	}
}

// A private or Blob-only-public container answers 403/404; Azure listing must
// map that to ErrAccessDenied so the crawler reports "not public" rather than a
// raw HTTP error.
func TestAzureListAccessDenied(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(status)
			fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>PublicAccessNotPermitted</Code><Message>no anon listing</Message></Error>`)
		}))
		endpoint, _ := url.Parse(server.URL + "/")
		_, err := List(context.Background(), server.Client(), Target{Provider: Azure, Endpoint: endpoint}, "", func([]Object) error { return nil })
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "not public") {
			t.Fatalf("status %d: err=%v", status, err)
		}
	}
}
