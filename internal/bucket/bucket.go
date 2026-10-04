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
	"sync"
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
	CommonPrefixes        []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
	Contents []struct {
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
	return listRange(ctx, client, t, task{prefix: t.Prefix}, userAgent, func(objs []Object, _ []string, _ string, _ bool) error { return page(objs) })
}

// task is one listing unit: a prefix, optionally bounded to the key range
// (startAfter, endAt]: exclusive start, inclusive end. Bounded tasks come from splitting a large prefix.
type task struct {
	prefix, delimiter, startAfter, endAt string
}

// listRange pages through one task. The callback receives objects, common
// prefixes, the last key returned, and whether more pages follow. Returning
// errStopRange ends the task early without error.
func listRange(ctx context.Context, client Doer, t Target, tk task, userAgent string, page func(objs []Object, dirs []string, last string, more bool) error) (pages int, err error) {
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "max-keys": {"1000"}}
		if tk.prefix != "" {
			q.Set("prefix", tk.prefix)
		}
		if tk.delimiter != "" {
			q.Set("delimiter", tk.delimiter)
		}
		if token != "" {
			q.Set("continuation-token", token)
		} else if tk.startAfter != "" {
			q.Set("start-after", tk.startAfter)
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
		last := ""
		stop := false
		for _, c := range r.Contents {
			if tk.endAt != "" && c.Key > tk.endAt {
				stop = true
				break
			}
			modified, _ := time.Parse(time.RFC3339Nano, c.LastModified)
			objects = append(objects, Object{Key: c.Key, Size: c.Size, LastModified: modified, ETag: strings.Trim(c.ETag, `"`)})
			last = c.Key
		}
		prefixes := make([]string, 0, len(r.CommonPrefixes))
		for _, p := range r.CommonPrefixes {
			if p.Prefix == "" || p.Prefix == tk.prefix {
				continue
			}
			if tk.endAt != "" && p.Prefix > tk.endAt {
				stop = true
				continue
			}
			if tk.startAfter != "" && p.Prefix <= tk.startAfter {
				continue
			}
			prefixes = append(prefixes, p.Prefix)
			if p.Prefix > last {
				last = p.Prefix
			}
		}
		more := r.IsTruncated && r.NextContinuationToken != "" && !stop
		pages++
		if err := page(objects, prefixes, last, more); err != nil {
			if errors.Is(err, errStopRange) {
				return pages, nil
			}
			return pages, err
		}
		if !more {
			return pages, nil
		}
		token = r.NextContinuationToken
	}
}

var errStopRange = errors.New("stop range")

// LooksLikeListing reports whether a response body is an S3-style bucket list.
func LooksLikeListing(contentType string, body []byte) bool {
	return strings.Contains(contentType, "xml") && strings.Contains(string(body[:min(len(body), 512)]), "<ListBucketResult")
}

// Walk lists every object under t.Prefix with a bounded worker pool.
//
// Flat listing is strictly sequential (each page needs the previous
// continuation token), so latency dominates on stores like GCS (~1-3 s/page).
// Walk overlaps requests two ways:
//   - directories: delimiter="/" common prefixes become independent tasks;
//   - large flat folders: when a folder's first page is truncated, the rest of
//     its key space is split at byte boundaries into start-after/end-before
//     ranges listed in parallel.
//
// onPage runs on one goroutine (calls are serialized); dirs are sub-prefixes
// discovered on that page.
func Walk(ctx context.Context, client Doer, t Target, userAgent string, workers int, onPage func(objs []Object, dirs []string) error) (pages int, err error) {
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Pages go to a single consumer so slow onPage work (database writes)
	// never holds the scheduling lock or stalls network workers.
	type pageMsg struct {
		objs []Object
		dirs []string
	}
	pagesCh := make(chan pageMsg, workers*4)
	consumerErr := make(chan error, 1)
	go func() {
		var err error
		for m := range pagesCh {
			if err == nil {
				if err = onPage(m.objs, m.dirs); err != nil {
					cancel()
				}
			}
		}
		consumerErr <- err
	}()

	var (
		mu       sync.Mutex // guards the scheduling fields below
		queue    = []task{{prefix: t.Prefix, delimiter: "/"}}
		inflight int
		firstErr error
		cond     = sync.NewCond(&mu)
	)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for len(queue) == 0 && inflight > 0 && firstErr == nil {
					cond.Wait()
				}
				if len(queue) == 0 || firstErr != nil {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				tk := queue[0]
				queue = queue[1:]
				inflight++
				mu.Unlock()

				first := true
				n, e := listRange(ctx, client, t, tk, userAgent, func(objs []Object, dirs []string, last string, more bool) error {
					mu.Lock()
					defer mu.Unlock()
					if firstErr != nil {
						return firstErr
					}
					for _, d := range dirs {
						queue = append(queue, task{prefix: d, delimiter: "/"})
					}
					var stopErr error
					if first && more && tk.endAt == "" && workers > 1 {
						// Big flat folder: partition the remaining key space at
						// boundaries b1<b2<...: (last,b1], (b1,b2], ..., (bn,inf).
						bounds := splitBounds(tk.prefix, last, workers)
						if len(bounds) > 0 {
							lo := last
							for _, b := range bounds {
								queue = append(queue, task{prefix: tk.prefix, delimiter: tk.delimiter, startAfter: lo, endAt: b})
								lo = b
							}
							queue = append(queue, task{prefix: tk.prefix, delimiter: tk.delimiter, startAfter: lo})
							stopErr = errStopRange
						}
					}
					first = false
					cond.Broadcast()
					mu.Unlock()
					select {
					case pagesCh <- pageMsg{objs, dirs}:
					case <-ctx.Done():
						mu.Lock()
						return ctx.Err()
					}
					mu.Lock()
					return stopErr
				})

				mu.Lock()
				pages += n
				inflight--
				if e != nil && firstErr == nil {
					firstErr = e
					cancel()
				}
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()
	close(pagesCh)
	if err := <-consumerErr; err != nil {
		return pages, err
	}
	return pages, firstErr
}

// splitBounds returns up to n-1 increasing boundary keys strictly greater than
// last, each prefix+one ASCII byte, spread evenly over the bytes above last's
// first byte after prefix. Ranges split at a boundary b are (lo,b] and (b,hi],
// an exact partition for any b, so every key is listed exactly once even if
// names fall outside the ASCII range (they land in the final open range).
func splitBounds(prefix, last string, n int) []string {
	if n < 2 || len(last) <= len(prefix) || !strings.HasPrefix(last, prefix) {
		return nil
	}
	lo := int(last[len(prefix)]) + 1
	const hi = 0x7e
	if lo > hi {
		return nil
	}
	n = min(n, hi-lo+2)
	step := float64(hi-lo+1) / float64(n)
	var bounds []string
	for i := 1; i < n; i++ {
		b := prefix + string(rune(lo+int(float64(i)*step)-1))
		if b > last && (len(bounds) == 0 || b > bounds[len(bounds)-1]) {
			bounds = append(bounds, b)
		}
	}
	return bounds
}
