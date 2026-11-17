package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDatabaseIssuanceAndRevocation(t *testing.T) {
	var issued, revoked int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/db-test/creds/reader":
			if r.Header.Get("X-Vault-Token") != "issuer" {
				t.Error("wrong issuance privilege")
			}
			issued++
			_, _ = w.Write([]byte(`{"lease_id":"db-test/creds/reader/unique","lease_duration":120,"data":{"username":"sensitive-user","password":"sensitive-password"}}`))
		case "POST /v1/sys/leases/revoke/db-test/creds/reader/unique":
			if r.Header.Get("X-Vault-Token") != "revoker" {
				t.Error("wrong revocation privilege")
			}
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["sync"] != true {
				t.Error("unsafe revocation payload")
			}
			revoked++
			w.WriteHeader(204)
		default:
			t.Error("unexpected path")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	i, err := NewDatabaseIssuer(Config{Address: server.URL, Token: "issuer"}, "db-test", "reader")
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewLeaseRevoker(Config{Address: server.URL, Token: "revoker"}, "db-test", "reader")
	if err != nil {
		t.Fatal(err)
	}
	c, err := i.Issue(context.Background())
	if err != nil || c.Username != "sensitive-user" || c.TTL.Seconds() != 120 {
		t.Fatal("issuance failed")
	}
	if r.Revoke(context.Background(), c.LeaseID) != nil {
		t.Fatal("revocation failed")
	}
	for _, id := range []string{"db-other/creds/reader/unique", "db-test/creds/reader/", "db-test/creds/reader/a/../b", "db-test/creds/reader/a?x=y"} {
		if r.Revoke(context.Background(), id) != ErrConfiguration {
			t.Fatal("unsafe lease accepted")
		}
	}
	if issued != 1 || revoked != 1 {
		t.Fatal("unexpected requests")
	}
}
func TestDatabaseMalformedAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		lease  bool
	}{
		{200, `{"lease_id":"db-test/creds/reader/id","lease_duration":0,"data":{"username":"u","password":"sensitive-token"}}`, true},
		{200, `{"lease_id":"wrong/id","lease_duration":30,"data":{"username":"u","password":"sensitive-token"}}`, false},
		{200, `{`, false}, {200, strings.Repeat("s", maxResponseBytes+1), false}, {403, "sensitive-token", false}, {500, "sensitive-token", false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		i, _ := NewDatabaseIssuer(Config{Address: server.URL, Token: "issuer"}, "db-test", "reader")
		c, err := i.Issue(context.Background())
		server.Close()
		if err == nil || strings.Contains(err.Error(), "sensitive-token") || c.Password != "" || ((c.LeaseID != "") != tc.lease) {
			t.Fatal("unsafe malformed response")
		}
	}
}
func TestRevocationRequiresConfirmation(t *testing.T) {
	for _, status := range []int{200, 202, 400, 403, 404, 500} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("sensitive-token"))
		}))
		r, _ := NewLeaseRevoker(Config{Address: server.URL, Token: "revoker"}, "db-test", "reader")
		err := r.Revoke(context.Background(), "db-test/creds/reader/id")
		server.Close()
		if err == nil || strings.Contains(err.Error(), "sensitive-token") {
			t.Fatal("unconfirmed revocation accepted")
		}
	}
}
func TestDatabaseCancellationAndRedirect(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Redirect(w, r, "http://127.0.0.1:1", 302) }))
	defer server.Close()
	i, _ := NewDatabaseIssuer(Config{Address: server.URL, Token: "issuer"}, "db-test", "reader")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := i.Issue(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	if _, err := i.Issue(context.Background()); err != ErrHTTP {
		t.Fatal("redirect accepted")
	}
	if calls != 1 {
		t.Fatal("unexpected request")
	}
	for _, name := range []string{"../db", "a/b", "db?query", ""} {
		if _, err := NewDatabaseIssuer(Config{Address: server.URL, Token: "issuer"}, name, "reader"); err != ErrConfiguration {
			t.Fatal("unsafe mount accepted")
		}
	}
}
