// Package bucket indexes public object-storage buckets through their
// documented, unauthenticated listing APIs. Amazon S3, Google Cloud Storage,
// DigitalOcean Spaces, and any other S3-compatible store share the
// ListObjectsV2 XML API; Azure Blob Storage uses its own List Blobs API. Only
// listing requests are made; objects are never fetched during indexing.
package bucket

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Provider identifies the object store family.
type Provider string

const (
	S3    Provider = "s3"
	GCS   Provider = "gcs"
	Azure Provider = "azure"
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
//	https://<bucket>.<region>.digitaloceanspaces.com/[prefix/]
//	https://<bucket>.<region>.cdn.digitaloceanspaces.com/[prefix/]
//	https://<region>.digitaloceanspaces.com/<bucket>/[prefix/]
//	https://<account>.blob.core.windows.net/<container>/[prefix/]
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
	case strings.HasSuffix(host, ".digitaloceanspaces.com"):
		return detectSpaces(host, path, endpoint)
	case strings.HasSuffix(host, ".blob.core.windows.net"):
		return detectAzure(path, endpoint)
	}
	return Target{}, false
}

// detectSpaces handles DigitalOcean Spaces, which is S3-compatible. The store
// supports both virtual-hosted (<bucket>.<region>.digitaloceanspaces.com, with
// an optional .cdn. segment) and path-style (<region>.digitaloceanspaces.com/
// <bucket>) addressing, so the bucket may live in the host or the first path
// segment. The CDN alias (.cdn.) only serves objects; its listing API lives on
// the origin host, so the endpoint drops the cdn label while object URLs are
// still rebuilt from that same (origin) endpoint.
func detectSpaces(host, path string, endpoint *url.URL) (Target, bool) {
	inner := strings.TrimSuffix(host, ".digitaloceanspaces.com")
	labels := strings.Split(inner, ".")
	// Virtual-hosted hosts carry at least <bucket>.<region>; the CDN alias adds
	// a "cdn" label (<bucket>.<region>.cdn). Path-style is a bare <region>.
	if len(labels) >= 2 {
		bucket := labels[0]
		if bucket == "" {
			return Target{}, false
		}
		endpoint.Host = strings.Replace(endpoint.Host, ".cdn.digitaloceanspaces.com", ".digitaloceanspaces.com", 1)
		return Target{S3, bucket, normPrefix(path), endpoint}, true
	}
	bucket, prefix, _ := strings.Cut(path, "/")
	if bucket == "" {
		return Target{}, false
	}
	endpoint.Path = "/" + bucket + "/"
	return Target{S3, bucket, normPrefix(prefix), endpoint}, true
}

// detectAzure handles Azure Blob Storage. The container is always the first
// path segment under <account>.blob.core.windows.net, and the listing endpoint
// is that container's root; everything deeper is the blob-name prefix.
func detectAzure(path string, endpoint *url.URL) (Target, bool) {
	container, prefix, _ := strings.Cut(path, "/")
	if container == "" {
		return Target{}, false
	}
	endpoint.Path = "/" + container + "/"
	return Target{Azure, container, normPrefix(prefix), endpoint}, true
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
	XMLName xml.Name `xml:"ListBucketResult"`
	Name    string   `xml:"Name"`
	// KeyCount is V2-only and is how we tell the two API versions apart.
	KeyCount              *int   `xml:"KeyCount"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	// NextMarker is the V1 cursor. Servers may omit it even when truncated,
	// in which case V1 requires resuming from the last key returned.
	NextMarker     string `xml:"NextMarker"`
	CommonPrefixes []struct {
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

// List streams every object under t.Prefix page by page, so arbitrarily large
// buckets never sit in memory at once.
func List(ctx context.Context, client Doer, t Target, userAgent string, page func([]Object) error) (pages int, err error) {
	return rangeLister(t)(ctx, client, t, task{prefix: t.Prefix}, userAgent, func(objs []Object, _ []string, _ string, _ bool) error { return page(objs) })
}

// Access is the outcome of a preflight accessibility check.
type Access int

const (
	// AccessPublic means the bucket answered an anonymous listing request, so a
	// full scan can proceed.
	AccessPublic Access = iota
	// AccessDenied means the bucket exists but refuses anonymous listing
	// (private, or object-only public access).
	AccessDenied
	// AccessMissing means the host or bucket does not exist (DNS failure or a
	// definitive not-found from the store).
	AccessMissing
	// AccessError means the check could not reach a verdict (timeout, transport
	// error, or an unexpected status); the caller may still try a full scan.
	AccessError
)

func (a Access) String() string {
	switch a {
	case AccessPublic:
		return "public"
	case AccessDenied:
		return "denied"
	case AccessMissing:
		return "missing"
	default:
		return "error"
	}
}

// CheckAccess issues a single, minimal listing request (max 1 key) to classify
// whether a bucket is publicly listable right now. It is the cheap preflight
// used to filter a large list before committing to full scans: one round trip
// per bucket instead of a full crawl, and dead hosts fail fast on their own
// timeout. The HTTP client's own timeout bounds how long a slow host can block.
func CheckAccess(ctx context.Context, client Doer, t Target, userAgent string) (Access, error) {
	return checkAccess(ctx, client, t, userAgent, true)
}

func checkAccess(ctx context.Context, client Doer, t Target, userAgent string, allowHTTPFallback bool) (Access, error) {
	u := *t.Endpoint
	q := url.Values{}
	if t.Provider == Azure {
		q.Set("restype", "container")
		q.Set("comp", "list")
		q.Set("maxresults", "1")
	} else {
		q.Set("list-type", "2")
		q.Set("max-keys", "1")
	}
	if t.Prefix != "" {
		q.Set("prefix", t.Prefix)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return AccessError, err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if t.Provider == Azure {
		req.Header.Set("x-ms-version", azureAPIVersion)
	}
	resp, err := client.Do(req)
	if err != nil {
		if allowHTTPFallback && t.Provider != Azure && t.Endpoint.Scheme == "https" && isTLSError(err) {
			fallback := t
			endpoint := *t.Endpoint
			endpoint.Scheme = "http"
			fallback.Endpoint = &endpoint
			return checkAccess(ctx, client, fallback, userAgent, false)
		}
		// Transport-level failure. A DNS "no such host" is a definitive miss;
		// anything else (timeout, reset) is an inconclusive error.
		if isNoSuchHost(err) {
			return AccessMissing, err
		}
		return AccessError, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode == http.StatusOK:
		return AccessPublic, nil
	case resp.StatusCode == http.StatusForbidden:
		return AccessDenied, nil
	case resp.StatusCode == http.StatusNotFound:
		// S3 returns 404 NoSuchBucket for a missing bucket, but Azure returns
		// 404 for a private container too. Treat 404 as missing for S3-style and
		// denied for Azure, matching how the full crawl classifies them.
		if t.Provider == Azure {
			return AccessDenied, nil
		}
		return AccessMissing, nil
	default:
		return AccessError, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

func isTLSError(err error) bool {
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return true
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return true
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		return true
	}
	var verify *tls.CertificateVerificationError
	if errors.As(err, &verify) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "certificate") || strings.Contains(msg, "tls") || strings.Contains(msg, "ssl") || strings.Contains(msg, "http response to https client")
}

// isNoSuchHost reports whether err is a DNS "no such host" lookup failure.
func isNoSuchHost(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsNotFound
	}
	return false
}

// rangeLister selects the paging implementation for the target's provider. S3,
// GCS, and DigitalOcean Spaces share the ListObjectsV2 dialect; Azure Blob has
// its own List Blobs API.
func rangeLister(t Target) func(context.Context, Doer, Target, task, string, func([]Object, []string, string, bool) error) (int, error) {
	if t.Provider == Azure {
		return listAzureRange
	}
	return listRange
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
	// V1 servers have no continuation token and page on an opaque marker
	// instead. Detected from the first response, then used for the rest of
	// this task so a V1-only endpoint is paged fully rather than truncated.
	marker, v1 := "", false
	for {
		q := url.Values{"max-keys": {"1000"}}
		if !v1 {
			q.Set("list-type", "2")
		}
		if tk.prefix != "" {
			q.Set("prefix", tk.prefix)
		}
		if tk.delimiter != "" {
			q.Set("delimiter", tk.delimiter)
		}
		switch {
		case v1:
			// V1 folds "resume here" and "start after" into one parameter.
			if cursor := maxString(marker, tk.startAfter); cursor != "" {
				q.Set("marker", cursor)
			}
		case token != "":
			q.Set("continuation-token", token)
		case tk.startAfter != "":
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
		// A server that ignores list-type=2 answers V1 (no KeyCount). Switch
		// this task to marker paging so the remaining pages are fetched
		// instead of silently dropped.
		if !v1 && r.KeyCount == nil && r.NextContinuationToken == "" {
			v1 = true
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
		more := r.IsTruncated && !stop
		if v1 {
			// NextMarker is only guaranteed when a delimiter is set; otherwise
			// V1 says to resume from the last key of this page.
			next := r.NextMarker
			if next == "" {
				next = last
			}
			// No way to advance means no safe way to continue: stop rather
			// than loop forever on the same page.
			if next == "" || next <= marker {
				more = false
			} else {
				marker = next
			}
		} else {
			more = more && r.NextContinuationToken != ""
		}
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

// maxString returns the greater of a and b in byte order.
func maxString(a, b string) string {
	if a > b {
		return a
	}
	return b
}

var errStopRange = errors.New("stop range")

// LooksLikeListing reports whether a response body is an S3-style bucket list.
func LooksLikeListing(contentType string, body []byte) bool {
	return strings.Contains(contentType, "xml") && strings.Contains(string(body[:min(len(body), 512)]), "<ListBucketResult")
}

// Probe asks an unrecognized host whether it is actually an S3-compatible
// bucket endpoint. CDN domains (cdn.example.com) are routinely CNAMEd to S3,
// GCS, R2, or MinIO, and serve the same anonymous ListObjectsV2 XML as the
// native hostname, so hostname matching alone misses them.
//
// A single bounded listing request is made. The target is accepted only when
// the body really is a <ListBucketResult>, so ordinary websites and SPA
// index.html responses are rejected and fall through to the HTML crawler.
func Probe(ctx context.Context, client Doer, raw, userAgent string) (Target, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Target{}, false
	}
	// The bucket root is the URL root; any path becomes the key prefix.
	endpoint := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}
	target := Target{Provider: S3, Prefix: normPrefix(strings.TrimPrefix(u.Path, "/")), Endpoint: endpoint}

	query := url.Values{"list-type": {"2"}, "max-keys": {"1"}}
	if target.Prefix != "" {
		query.Set("prefix", target.Prefix)
	}
	probeURL := *endpoint
	probeURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL.String(), nil)
	if err != nil {
		return Target{}, false
	}
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return Target{}, false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return Target{}, false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8192))
	if err != nil || !LooksLikeListing(response.Header.Get("Content-Type"), body) {
		return Target{}, false
	}
	var result listResult
	if xml.Unmarshal(body, &result) == nil && result.Name != "" {
		target.Bucket = result.Name
	}
	return target, true
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
	// Azure Blob has no key-range (start-after/end) parameters, so its big flat
	// folders cannot be split into parallel byte ranges the way S3/GCS/Spaces
	// can; it still parallelizes across delimiter-discovered sub-prefixes.
	listRangeFn := rangeLister(t)
	canSplit := t.Provider != Azure
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
				n, e := listRangeFn(ctx, client, t, tk, userAgent, func(objs []Object, dirs []string, last string, more bool) error {
					mu.Lock()
					defer mu.Unlock()
					if firstErr != nil {
						return firstErr
					}
					for _, d := range dirs {
						queue = append(queue, task{prefix: d, delimiter: "/"})
					}
					var stopErr error
					if canSplit && first && more && tk.endAt == "" && workers > 1 {
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
