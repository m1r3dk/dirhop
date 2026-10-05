package grayhat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchBucketsSendsAuthAndParsesResponse(t *testing.T) {
	var gotAuth, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/buckets" {
			t.Errorf("path=%s want /buckets", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"meta":{"results":2},"notice":"free plan limited","buckets":[
			{"id":1,"bucket":"acme.s3.amazonaws.com","fileCount":42,"type":"aws"},
			{"id":2,"bucket":"acct.blob.core.windows.net/pub","fileCount":7,"type":"azure"}
		]}`)
	}))
	defer server.Close()

	c := New(server.Client(), server.URL, "secret-key", "dirhop-test")
	buckets, total, notice, err := c.SearchBuckets(context.Background(), BucketQuery{
		Keywords: "acme backup", Type: "aws", Order: "fileCount", Direction: "desc", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth header=%q", gotAuth)
	}
	for _, want := range []string{"keywords=acme+backup", "type=aws", "order=fileCount", "direction=desc", "limit=10"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if total != 2 || notice != "free plan limited" || len(buckets) != 2 {
		t.Fatalf("total=%d notice=%q buckets=%d", total, notice, len(buckets))
	}
	if buckets[0].Bucket != "acme.s3.amazonaws.com" || buckets[0].FileCount != 42 || buckets[0].Type != "aws" {
		t.Errorf("bucket[0]=%+v", buckets[0])
	}
}

func TestSearchFilesSendsFileParams(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			t.Errorf("path=%s want /files", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"meta":{"results":1},"files":[
			{"id":"9","bucket":"acme.s3.amazonaws.com","bucketId":1,"filename":"dump.sql","fullPath":"db/dump.sql","url":"http://acme.s3.amazonaws.com/db/dump.sql","size":2048,"type":"aws","lastModified":1666666666}
		]}`)
	}))
	defer server.Close()

	c := New(server.Client(), server.URL, "k", "")
	files, total, _, err := c.SearchFiles(context.Background(), FileQuery{
		Keywords: "dump", Types: "aws,azure", Extensions: "sql,zip", FullPath: true, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keywords=dump", "types=aws%2Cazure", "extensions=sql%2Czip", "full-path=1", "limit=5"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if total != 1 || len(files) != 1 || files[0].Filename != "dump.sql" || files[0].Size != 2048 {
		t.Fatalf("files=%+v total=%d", files, total)
	}
}

func TestSearchRequiresAPIKey(t *testing.T) {
	c := New(http.DefaultClient, "http://example.invalid", "", "")
	if _, _, _, err := c.SearchBuckets(context.Background(), BucketQuery{}); err != ErrNoAPIKey {
		t.Fatalf("err=%v want ErrNoAPIKey", err)
	}
}

func TestRejectedKeyMapsToUnauthorized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":"bad key"}`)
		}))
		_, _, _, err := New(server.Client(), server.URL, "bad", "").SearchBuckets(context.Background(), BucketQuery{})
		server.Close()
		if err != ErrUnauthorized {
			t.Fatalf("status %d: err=%v want ErrUnauthorized", status, err)
		}
	}
}

func TestBucketURLAddsScheme(t *testing.T) {
	cases := map[string]string{
		"acme.s3.amazonaws.com":          "https://acme.s3.amazonaws.com",
		"acct.blob.core.windows.net/pub": "https://acct.blob.core.windows.net/pub",
		"http://already.example/":        "http://already.example/",
		"https://already.example/":       "https://already.example/",
		"":                               "",
	}
	for in, want := range cases {
		if got := BucketURL(in); got != want {
			t.Errorf("BucketURL(%q)=%q want %q", in, got, want)
		}
	}
}
