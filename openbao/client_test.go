package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, address string) *Client {
	t.Helper()
	c, err := NewClient(Config{Address: address, Token: "synthetic-" + t.Name()})
	if err != nil {
		t.Fatal("could not create test client")
	}
	return c
}

func TestReadDemo(t *testing.T) {
	want := DemoSecret{Username: "dummy-user", Password: "dummy-value"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != demoPath {
			t.Error("unexpected request method or path")
		}
		if r.Header.Get("X-Vault-Token") != "synthetic-"+t.Name() || r.Header.Get("Authorization") != "" {
			t.Error("incorrect authentication headers")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": want, "metadata": map[string]any{"version": 1}}})
	}))
	defer server.Close()
	got, err := newTestClient(t, server.URL+"/").ReadDemo(context.Background())
	if err != nil || got != want {
		t.Fatal("demo read did not return the expected fixture")
	}
}

func TestHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   error
	}{
		{"permission denied", 403, ErrPermissionDenied},
		{"not found", 404, ErrNotFound},
		{"unauthenticated", 401, ErrHTTP},
		{"server failure", 500, ErrHTTP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, r.Header.Get("X-Vault-Token")+" sensitive-fixture")
			}))
			defer server.Close()
			got, err := newTestClient(t, server.URL).ReadDemo(context.Background())
			if !errors.Is(err, tc.want) || got != (DemoSecret{}) {
				t.Fatal("unexpected HTTP failure result")
			}
			if strings.Contains(err.Error(), "synthetic-") || strings.Contains(err.Error(), "sensitive-fixture") {
				t.Fatal("error exposed sensitive response data")
			}
		})
	}
}

func TestMalformedResponses(t *testing.T) {
	for _, body := range []string{
		`not JSON sensitive-fixture`, `{`, `{}`, `null`, `{"data":null}`,
		`{"data":{"data":null}}`, `{"data":{"data":{}}}`,
		`{"data":{"data":{"username":"dummy-user"}}}`,
		`{"data":{"data":{"username":42,"password":"dummy-value"}}}`,
		`{"data":{"data":{"username":"dummy-user","password":"dummy-value"}}} {}`,
		strings.Repeat("x", maxResponseBytes+1),
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		got, err := newTestClient(t, server.URL).ReadDemo(context.Background())
		server.Close()
		if err != ErrResponse || got != (DemoSecret{}) {
			t.Fatal("malformed response was not safely rejected")
		}
	}
}

func TestConfiguration(t *testing.T) {
	for _, config := range []Config{
		{}, {Address: "http://localhost"}, {Token: "synthetic"},
		{Address: "ftp://localhost", Token: "synthetic"},
		{Address: "http://user:password@localhost", Token: "synthetic"},
		{Address: "http://localhost/base", Token: "synthetic"},
		{Address: "http://localhost?token=sensitive", Token: "synthetic"},
		{Address: "http://localhost#fragment", Token: "synthetic"},
		{Address: "http://localhost", Token: " \t"},
		{Address: "http://localhost", Token: "synthetic\r\nheader"},
		{Address: "http://localhost", Token: "synthetic", Timeout: -time.Second},
	} {
		if _, err := NewClient(config); err != ErrConfiguration {
			t.Fatal("invalid configuration was not safely rejected")
		}
	}
	c := newTestClient(t, "http://localhost")
	if c.http.Timeout != 5*time.Second {
		t.Fatal("default timeout missing")
	}
	if _, err := c.ReadDemo(nil); err != ErrConfiguration {
		t.Fatal("nil context should fail safely")
	}
	var zero Client
	if _, err := zero.ReadDemo(context.Background()); err != ErrConfiguration {
		t.Fatal("zero client should fail safely")
	}
}

func TestCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestClient(t, server.URL)
	result := make(chan error, 1)
	go func() { _, err := c.ReadDemo(ctx); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("request cancellation was not preserved")
	}
}

func TestContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("expired context should not send a request")
	}))
	defer server.Close()
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	_, err := newTestClient(t, server.URL).ReadDemo(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("context deadline was not preserved")
	}
}

func TestTimeout(t *testing.T) {
	for _, flushHeaders := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if flushHeaders {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		}))
		c, err := NewClient(Config{Address: server.URL, Token: "synthetic", Timeout: 100 * time.Millisecond})
		if err != nil {
			t.Fatal("could not create timeout test client")
		}
		_, err = c.ReadDemo(context.Background())
		server.Close()
		if !errors.Is(err, ErrTimeout) {
			t.Fatal("request or body timeout was not classified safely")
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	visited := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { visited <- struct{}{} }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	_, err := newTestClient(t, server.URL).ReadDemo(context.Background())
	if !errors.Is(err, ErrHTTP) {
		t.Fatal("redirect should fail")
	}
	select {
	case <-visited:
		t.Fatal("client followed a redirect")
	default:
	}
}

func TestTransportErrorIsSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	_, err := newTestClient(t, server.URL).ReadDemo(context.Background())
	if err != ErrRequest {
		t.Fatal("transport failure was not sanitized")
	}
}
