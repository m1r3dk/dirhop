// Package bucket indexes public object-storage buckets (Amazon S3 and Google
// Cloud Storage) through their documented, unauthenticated S3-compatible
// ListObjectsV2 XML API. Only listing requests are made; objects are never
// fetched during indexing.
package bucket

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Provider identifies the object store family.
type Provider string

const (
	S3  Provider = "s3"
	GCS Provider = "gcs"
)

// Target is a bucket listing endpoint plus an optional key prefix.
type Target struct {
	Provider Provider
	Bucket   string
	Prefix   string   // key prefix the session is rooted at, "" or ending in "/"
	Endpoint *url.URL // URL that lists the bucket, e.g. https://b.s3.amazonaws.com/
}

// ObjectURL returns the public download URL for key.
func (t Target) ObjectURL(key string) string {
	segments := strings.Split(key, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	u := *t.Endpoint
	u.RawQuery = ""
	u.RawPath = strings.TrimSuffix(u.EscapedPath(), "/") + "/" + strings.Join(segments, "/")
	u.Path, _ = url.PathUnescape(u.RawPath)
	return u.String()
}

// Detect maps a URL to a bucket target. Recognized forms:
//
//	https://<bucket>.s3[.-<region>].amazonaws.com/[prefix/]
//	https://s3[.-<region>].amazonaws.com/<bucket>/[prefix/]
//	https://storage.googleapis.com/<bucket>/[prefix/]
//	https://<bucket>.storage.googleapis.com/[prefix/]
//
// Other hosts return ok=false; callers may still probe the response body with
// LooksLikeListing for S3-compatible servers on custom domains.
func Detect(raw string) (Target, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Target{}, false
	}
	host := strings.ToLower(u.Hostname())
	path := strings.TrimPrefix(u.Path, "/")
	endpoint := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}
	switch {
	case host == "storage.googleapis.com":
		bucket, prefix, _ := strings.Cut(path, "/")
		if bucket == "" {
			return Target{}, false
		}
		endpoint.Path = "/" + bucket + "/"
		return Target{GCS, bucket, normPrefix(prefix), endpoint}, true
	case strings.HasSuffix(host, ".storage.googleapis.com"):
		return Target{GCS, strings.TrimSuffix(host, ".storage.googleapis.com"), normPrefix(path), endpoint}, true
	case strings.HasSuffix(host, ".amazonaws.com") && isS3Host(host):
		if bucket, ok := virtualHostedBucket(host); ok {
			return Target{S3, bucket, normPrefix(path), endpoint}, true
		}
		bucket, prefix, _ := strings.Cut(path, "/")
		if bucket == "" {
			return Target{}, false
		}
		endpoint.Path = "/" + bucket + "/"
		return Target{S3, bucket, normPrefix(prefix), endpoint}, true
	}
	return Target{}, false
}

func isS3Host(host string) bool {
	labels := strings.Split(strings.TrimSuffix(host, ".amazonaws.com"), ".")
	for _, l := range labels {
		if l == "s3" || strings.HasPrefix(l, "s3-") {
			return true
		}
	}
	return false
}

// virtualHostedBucket extracts "<bucket>" from "<bucket>.s3[...].amazonaws.com".
func virtualHostedBucket(host string) (string, bool) {
	for _, marker := range []string{".s3.", ".s3-"} {
		if i := strings.Index(host, marker); i > 0 {
			return host[:i], true
		}
	}
	return "", false
}

func normPrefix(p string) string {
	if p == "" || strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// Object is one listed key with its metadata.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

type listResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
	} `xml:"Contents"`
}

type errorResult struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// ErrAccessDenied means the bucket exists but anonymous listing is disabled.
var ErrAccessDenied = errors.New("bucket listing is not public")

// Doer is satisfied by *http.Client and the shared retrying client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// List streams every object under t.Prefix page by page (1000 keys per page),
// so arbitrarily large buckets never sit in memory at once.
func List(ctx context.Context, client Doer, t Target, userAgent string, page func([]Object) error) (pages int, err error) {
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "max-keys": {"1000"}}
		if t.Prefix != "" {
			q.Set("prefix", t.Prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := *t.Endpoint
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return pages, err
		}
		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}
		resp, err := client.Do(req)
		if err != nil {
			return pages, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return pages, err
		}
		if resp.StatusCode != http.StatusOK {
			var e errorResult
			_ = xml.Unmarshal(body, &e)
			if resp.StatusCode == http.StatusForbidden || e.Code == "AccessDenied" {
				return pages, fmt.Errorf("%w: %s", ErrAccessDenied, e.Message)
			}
			return pages, fmt.Errorf("bucket list returned HTTP %d %s %s", resp.StatusCode, e.Code, e.Message)
		}
		var r listResult
		if err := xml.Unmarshal(body, &r); err != nil {
			return pages, fmt.Errorf("parse bucket listing: %w", err)
		}
		objects := make([]Object, 0, len(r.Contents))
		for _, c := range r.Contents {
			modified, _ := time.Parse(time.RFC3339Nano, c.LastModified)
			objects = append(objects, Object{Key: c.Key, Size: c.Size, LastModified: modified, ETag: strings.Trim(c.ETag, `"`)})
		}
		pages++
		if err := page(objects); err != nil {
			return pages, err
		}
		if !r.IsTruncated || r.NextContinuationToken == "" {
			return pages, nil
		}
		token = r.NextContinuationToken
	}
}

// LooksLikeListing reports whether a response body is an S3-style bucket list.
func LooksLikeListing(contentType string, body []byte) bool {
	return strings.Contains(contentType, "xml") && strings.Contains(string(body[:min(len(body), 512)]), "<ListBucketResult")
}
