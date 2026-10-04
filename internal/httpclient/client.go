// Package httpclient provides the shared bounded, pooled, retrying HTTP client.
package httpclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxRetryDrain = 64 << 10

// Config controls transport pooling, request timeouts, and retry behavior.
// Zero values use conservative defaults. Retries is the number of retries after
// the initial attempt.
type Config struct {
	Timeout               time.Duration
	DialTimeout           time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	IdleConnTimeout       time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	Retries               int
	BaseBackoff           time.Duration
	MaxBackoff            time.Duration
}

// Client wraps one pooled http.Client with bounded retry behavior.
type Client struct {
	httpClient  *http.Client
	retries     int
	baseBackoff time.Duration
	maxBackoff  time.Duration
}

// New constructs a pooled HTTP client. Each call owns its transport and may be
// closed independently with CloseIdleConnections.
func New(config Config) *Client {
	config = withDefaults(config)
	dialer := &net.Dialer{Timeout: config.DialTimeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          config.MaxIdleConns,
		MaxIdleConnsPerHost:   config.MaxIdleConnsPerHost,
		MaxConnsPerHost:       config.MaxConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ResponseHeaderTimeout: config.ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		httpClient:  &http.Client{Transport: transport, Timeout: config.Timeout},
		retries:     config.Retries,
		baseBackoff: config.BaseBackoff,
		maxBackoff:  config.MaxBackoff,
	}
}

// HTTPClient exposes the configured standard client for integrations that need
// standard-library APIs while sharing the same transport.
func (client *Client) HTTPClient() *http.Client { return client.httpClient }

// CloseIdleConnections closes pooled idle connections owned by this client.
func (client *Client) CloseIdleConnections() { client.httpClient.CloseIdleConnections() }

// Do executes a request and retries replayable idempotent requests after
// transient transport failures or retryable HTTP statuses.
func (client *Client) Do(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("httpclient: nil request")
	}

	replayable := isIdempotent(request.Method) && (request.Body == nil || request.GetBody != nil)
	attempts := 1
	if replayable && client.retries > 0 {
		attempts += client.retries
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		attemptRequest, err := requestForAttempt(request, attempt)
		if err != nil {
			return nil, err
		}
		response, err := client.httpClient.Do(attemptRequest)
		if err == nil && !isRetryableStatus(response.StatusCode) {
			return response, nil
		}
		if attempt == attempts-1 {
			if err != nil {
				return nil, err
			}
			return response, nil
		}

		lastErr = err
		var retryAfter time.Duration
		if response != nil {
			retryAfter = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
			drainAndClose(response.Body)
		}
		delay := client.backoff(attempt)
		if retryAfter > delay {
			delay = retryAfter
		}
		if err := wait(request.Context(), delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func requestForAttempt(request *http.Request, attempt int) (*http.Request, error) {
	clone := request.Clone(request.Context())
	if request.Body == nil {
		return clone, nil
	}
	if attempt == 0 {
		clone.Body = request.Body
		return clone, nil
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, err
	}
	clone.Body = body
	return clone, nil
}

func (client *Client) backoff(attempt int) time.Duration {
	delay := client.baseBackoff
	for i := 0; i < attempt && delay < client.maxBackoff; i++ {
		if delay > client.maxBackoff/2 {
			return client.maxBackoff
		}
		delay *= 2
	}
	if delay > client.maxBackoff {
		return client.maxBackoff
	}
	return delay
}

func isIdempotent(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxRetryDrain))
	_ = body.Close()
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func withDefaults(config Config) Config {
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = 10 * time.Second
	}
	if config.TLSHandshakeTimeout <= 0 {
		config.TLSHandshakeTimeout = 10 * time.Second
	}
	if config.ResponseHeaderTimeout <= 0 {
		config.ResponseHeaderTimeout = 15 * time.Second
	}
	if config.IdleConnTimeout <= 0 {
		config.IdleConnTimeout = 90 * time.Second
	}
	if config.MaxIdleConns <= 0 {
		config.MaxIdleConns = 100
	}
	if config.MaxIdleConnsPerHost <= 0 {
		config.MaxIdleConnsPerHost = 10
	}
	if config.MaxConnsPerHost <= 0 {
		config.MaxConnsPerHost = 20
	}
	if config.Retries < 0 {
		config.Retries = 0
	}
	if config.BaseBackoff <= 0 {
		config.BaseBackoff = 200 * time.Millisecond
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 5 * time.Second
	}
	if config.MaxBackoff < config.BaseBackoff {
		config.MaxBackoff = config.BaseBackoff
	}
	return config
}
