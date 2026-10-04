package httpclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRetriesRetryableStatusAndHonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := New(Config{Retries: 2, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})
	response, err := client.Do(mustRequest(t, http.MethodGet, server.URL, nil))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestClientDoesNotRetryNonReplayablePost(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := New(Config{Retries: 3, BaseBackoff: time.Millisecond})
	response, err := client.Do(mustRequest(t, http.MethodPost, server.URL, strings.NewReader("payload")))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	response.Body.Close()

	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestNewConfiguresPooledTransport(t *testing.T) {
	client := New(Config{
		DialTimeout:           2 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
		IdleConnTimeout:       5 * time.Second,
		MaxIdleConns:          17,
		MaxIdleConnsPerHost:   7,
		MaxConnsPerHost:       9,
	})

	transport, ok := client.HTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.HTTPClient().Transport)
	}
	if transport.MaxIdleConns != 17 || transport.MaxIdleConnsPerHost != 7 || transport.MaxConnsPerHost != 9 {
		t.Fatalf("pool settings = (%d,%d,%d), want (17,7,9)", transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.MaxConnsPerHost)
	}
	if transport.TLSHandshakeTimeout != 3*time.Second || transport.ResponseHeaderTimeout != 4*time.Second || transport.IdleConnTimeout != 5*time.Second {
		t.Fatalf("transport timeouts not applied: %#v", transport)
	}
}

func mustRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
