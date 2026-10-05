// Package grayhat is a minimal client for the GrayHatWarfare public-buckets
// search API (https://buckets.grayhatwarfare.com/docs/api/v2). GrayHatWarfare
// indexes publicly exposed object-storage buckets (AWS S3, Azure Blob,
// DigitalOcean Spaces, Google Cloud, Alibaba) and their files. dirhop uses it
// as a discovery front-end: search returns bucket and file URLs that dirhop can
// then index and browse with its existing bucket crawler.
//
// Only read-only search endpoints are used, and a valid API key is required.
package grayhat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultBaseURL is the documented v2 API root.
const DefaultBaseURL = "https://buckets.grayhatwarfare.com/api/v2"

// ErrNoAPIKey is returned when a search is attempted without credentials.
var ErrNoAPIKey = errors.New("grayhatwarfare: no API key (set grayhatwarfare_api_key in config or GRAYHATWARFARE_API_KEY)")

// ErrUnauthorized means the API rejected the key (HTTP 401/403).
var ErrUnauthorized = errors.New("grayhatwarfare: API key was rejected")

// Doer is satisfied by *http.Client and the shared retrying client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client talks to the GrayHatWarfare v2 API.
type Client struct {
	http      Doer
	baseURL   string
	apiKey    string
	userAgent string
}

// New builds a client. baseURL may be "" to use DefaultBaseURL.
func New(httpClient Doer, baseURL, apiKey, userAgent string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{http: httpClient, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, userAgent: userAgent}
}

// Bucket is one bucket entry in the index.
type Bucket struct {
	ID        int64  `json:"id"`
	Bucket    string `json:"bucket"`
	FileCount int64  `json:"fileCount"`
	Type      string `json:"type"`
}

// File is one file entry in the index.
type File struct {
	ID           string `json:"id"`
	Bucket       string `json:"bucket"`
	BucketID     int64  `json:"bucketId"`
	Filename     string `json:"filename"`
	FullPath     string `json:"fullPath"`
	URL          string `json:"url"`
	Size         int64  `json:"size"`
	Type         string `json:"type"`
	LastModified int64  `json:"lastModified"`
}

// BucketQuery filters a bucket search. Zero values are omitted.
type BucketQuery struct {
	Keywords  string
	Type      string // aws, azure, dos, gcp, ali
	Order     string // fileCount, bucketName
	Direction string // asc, desc
	Start     int
	Limit     int
}

// FileQuery filters a file search. Zero values are omitted.
type FileQuery struct {
	Keywords   string
	Bucket     string // bucket url(s) or id(s), comma-separated
	Types      string // comma-separated: aws,azure,dos,gcp,ali
	Extensions string // comma-separated
	FullPath   bool
	Order      string // size, last_modified
	Direction  string // asc, desc
	Start      int
	Limit      int
}

// bucketResponse is the /buckets payload (subset we use).
type bucketResponse struct {
	Meta struct {
		Results int64 `json:"results"`
	} `json:"meta"`
	Notice  string   `json:"notice"`
	Buckets []Bucket `json:"buckets"`
}

// fileResponse is the /files payload (subset we use).
type fileResponse struct {
	Meta struct {
		Results int64 `json:"results"`
	} `json:"meta"`
	Notice string `json:"notice"`
	Files  []File `json:"files"`
}

// SearchBuckets lists buckets matching q. It returns the page of buckets, the
// total number of matches in the index, and an optional notice from the API
// (for example about package limits).
func (c *Client) SearchBuckets(ctx context.Context, q BucketQuery) ([]Bucket, int64, string, error) {
	values := url.Values{}
	setStr(values, "keywords", q.Keywords)
	setStr(values, "type", q.Type)
	setStr(values, "order", q.Order)
	setStr(values, "direction", q.Direction)
	setInt(values, "start", q.Start)
	setInt(values, "limit", q.Limit)
	var out bucketResponse
	if err := c.get(ctx, "/buckets", values, &out); err != nil {
		return nil, 0, "", err
	}
	return out.Buckets, out.Meta.Results, out.Notice, nil
}

// SearchFiles lists files matching q, with totals and an optional notice.
func (c *Client) SearchFiles(ctx context.Context, q FileQuery) ([]File, int64, string, error) {
	values := url.Values{}
	setStr(values, "keywords", q.Keywords)
	setStr(values, "buckets", q.Bucket)
	setStr(values, "types", q.Types)
	setStr(values, "extensions", q.Extensions)
	setStr(values, "order", q.Order)
	setStr(values, "direction", q.Direction)
	if q.FullPath {
		values.Set("full-path", "1")
	}
	setInt(values, "start", q.Start)
	setInt(values, "limit", q.Limit)
	var out fileResponse
	if err := c.get(ctx, "/files", values, &out); err != nil {
		return nil, 0, "", err
	}
	return out.Files, out.Meta.Results, out.Notice, nil
}

func (c *Client) get(ctx context.Context, path string, values url.Values, dst any) error {
	if c.apiKey == "" {
		return ErrNoAPIKey
	}
	u := c.baseURL + path
	if encoded := values.Encode(); encoded != "" {
		u += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("grayhatwarfare request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("grayhatwarfare: rate limited (HTTP 429)")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("grayhatwarfare: HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("grayhatwarfare: parse response: %w", err)
	}
	return nil
}

// BucketURL returns a scheme-qualified URL for a bucket entry. The API returns
// hostnames (sometimes with a path for Azure containers) without a scheme;
// dirhop needs https:// to detect and crawl them.
func BucketURL(bucketHostOrURL string) string {
	s := strings.TrimSpace(bucketHostOrURL)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return "https://" + s
}

func setStr(v url.Values, key, val string) {
	if val != "" {
		v.Set(key, val)
	}
}

func setInt(v url.Values, key string, n int) {
	if n > 0 {
		v.Set(key, strconv.Itoa(n))
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
