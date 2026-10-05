package bucket

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Azure Blob Storage speaks a different listing dialect from S3: the request is
// GET /<container>?restype=container&comp=list, paging is by an opaque
// <NextMarker> (no start-after/range support), and the response is an
// <EnumerationResults> document whose blob metadata lives under <Properties>.
// Public ("anonymous") listing works only when the container's access level is
// "Container"; "Blob"-level or private containers answer 403/404 and map to
// ErrAccessDenied, same as a locked-down S3 bucket.

// azureAPIVersion pins a modern, widely deployed stable REST version. Anonymous
// requests otherwise fall back to the 2009-09-19 default, which 409s on
// containers that hold blob types introduced after it.
const azureAPIVersion = "2021-12-02"

type azureListResult struct {
	XMLName xml.Name `xml:"EnumerationResults"`
	Prefix  string   `xml:"Prefix"`
	Blobs   struct {
		Blob []struct {
			Name       string `xml:"Name"`
			Properties struct {
				LastModified  string `xml:"Last-Modified"`
				ETag          string `xml:"Etag"`
				ContentLength int64  `xml:"Content-Length"`
			} `xml:"Properties"`
		} `xml:"Blob"`
		BlobPrefix []struct {
			Name string `xml:"Name"`
		} `xml:"BlobPrefix"`
	} `xml:"Blobs"`
	NextMarker string `xml:"NextMarker"`
}

// listAzureRange pages one task (prefix + optional delimiter) through the Azure
// List Blobs API. Azure has no key-range parameters, so startAfter/endAt on the
// task are ignored; Walk never sets them for Azure and instead parallelizes via
// delimiter-discovered sub-prefixes. The callback mirrors listRange so the same
// Walk scheduler drives both providers.
func listAzureRange(ctx context.Context, client Doer, t Target, tk task, userAgent string, page func(objs []Object, dirs []string, last string, more bool) error) (pages int, err error) {
	marker := ""
	for {
		u := *t.Endpoint
		q := url.Values{"restype": {"container"}, "comp": {"list"}, "maxresults": {"5000"}}
		if tk.prefix != "" {
			q.Set("prefix", tk.prefix)
		}
		if tk.delimiter != "" {
			q.Set("delimiter", tk.delimiter)
		}
		if marker != "" {
			q.Set("marker", marker)
		}
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return pages, err
		}
		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}
		// Anonymous requests default to the 2009-09-19 API, which rejects
		// containers holding blob types it predates with 409
		// FeatureVersionMismatch ("type of a blob ... is unrecognized").
		// Pinning a modern stable version lets listing enumerate every blob.
		req.Header.Set("x-ms-version", azureAPIVersion)
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
			// A container that is not public to listing answers 403
			// (AuthorizationFailure) or 404 (PublicAccessNotPermitted /
			// ContainerNotFound); treat all as "listing not public".
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
				return pages, fmt.Errorf("%w: %s %s", ErrAccessDenied, e.Code, e.Message)
			}
			return pages, fmt.Errorf("azure blob list returned HTTP %d %s %s", resp.StatusCode, e.Code, e.Message)
		}
		var r azureListResult
		if err := xml.Unmarshal(body, &r); err != nil {
			return pages, fmt.Errorf("parse azure blob listing: %w", err)
		}
		objects := make([]Object, 0, len(r.Blobs.Blob))
		for _, b := range r.Blobs.Blob {
			if b.Name == "" {
				continue
			}
			// Azure stamps Last-Modified in RFC1123 (HTTP-date), not RFC3339.
			modified, _ := time.Parse(time.RFC1123, b.Properties.LastModified)
			objects = append(objects, Object{
				Key:          b.Name,
				Size:         b.Properties.ContentLength,
				LastModified: modified,
				ETag:         trimETag(b.Properties.ETag),
			})
		}
		prefixes := make([]string, 0, len(r.Blobs.BlobPrefix))
		for _, p := range r.Blobs.BlobPrefix {
			if p.Name == "" || p.Name == tk.prefix {
				continue
			}
			prefixes = append(prefixes, p.Name)
		}
		more := r.NextMarker != ""
		pages++
		if err := page(objects, prefixes, "", more); err != nil {
			if err == errStopRange {
				return pages, nil
			}
			return pages, err
		}
		if !more {
			return pages, nil
		}
		marker = r.NextMarker
	}
}

func trimETag(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
