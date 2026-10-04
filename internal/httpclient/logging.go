package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// LoggingTransport logs one line per HTTP request. It never logs headers,
// cookies, credentials, or query strings (which may carry signed tokens).
type LoggingTransport struct {
	Base http.RoundTripper
	Out  io.Writer
}

func (t LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.Base.RoundTrip(req)
	target := RedactURL(req.URL)
	if err != nil {
		fmt.Fprintf(t.Out, "[http] %s %s error=%v (%s)\n", req.Method, target, err, time.Since(start).Round(time.Millisecond))
		return nil, err
	}
	fmt.Fprintf(t.Out, "[http] %s %s %d (%s)\n", req.Method, target, resp.StatusCode, time.Since(start).Round(time.Millisecond))
	return resp, nil
}

// RedactURL removes userinfo and replaces query values with "REDACTED" while
// keeping parameter names, which are useful for diagnosing pagination.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	if c.RawQuery != "" {
		q := c.Query()
		for k := range q {
			if k != "list-type" && k != "max-keys" {
				q.Set(k, "REDACTED")
			}
		}
		c.RawQuery = q.Encode()
	}
	return c.String()
}

// EnableLogging wraps the client's transport with request logging.
func (client *Client) EnableLogging(out io.Writer) {
	base := client.httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.httpClient.Transport = LoggingTransport{Base: base, Out: out}
}
