package httpclient

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRedactURLHidesSecrets(t *testing.T) {
	u, _ := url.Parse("https://user:pass@example.test/a?X-Amz-Signature=secret&continuation-token=zz9tokvalue&list-type=2")
	got := RedactURL(u)
	for _, leaked := range []string{"user", "pass", "secret", "zz9tokvalue"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURL leaked %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "list-type=2") {
		t.Fatalf("RedactURL dropped harmless parameter: %s", got)
	}
}

func TestLoggingTransportOmitsHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	var log bytes.Buffer
	client := New(Config{})
	client.EnableLogging(&log)
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/x", nil)
	req.Header.Set("Authorization", "Bearer hunter2")
	req.Header.Set("Cookie", "session=hunter2")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(log.String(), "GET") || !strings.Contains(log.String(), " 200 ") || strings.Contains(log.String(), "hunter2") {
		t.Fatalf("log = %q", log.String())
	}
}
