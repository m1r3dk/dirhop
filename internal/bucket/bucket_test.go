package bucket

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
