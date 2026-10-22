//go:build integration

package openbao_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/grdamico/runawaybao/openbao"
)

// TestLiveACL deliberately requires both a build tag and explicit opt-in.
// All credentials and response bodies stay in memory; diagnostics are fixed.
func TestLiveACL(t *testing.T) {
	if os.Getenv("RUNAWAYBAO_ACL_INTEGRATION") != "1" {
		t.Skip("explicit live-test opt-in required")
	}
	address, root := os.Getenv("RUNAWAYBAO_ACL_ADDR"), os.Getenv("RUNAWAYBAO_ACL_ROOT_TOKEN")
	u, err := url.Parse(address)
	if err != nil || u == nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || root == "" {
		t.Fatal("loopback development address and setup credential required")
	}
	httpClient := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, token string, payload any) (int, []byte) {
		t.Helper()
		var body []byte
		if payload != nil {
			body, err = json.Marshal(payload)
			if err != nil {
				t.Fatal("could not encode test request")
			}
		}
		req, e := http.NewRequest(method, address+"/v1/"+path, bytes.NewReader(body))
		if e != nil {
			t.Fatal("invalid test request")
		}
		req.Header.Set("X-Vault-Token", token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := httpClient.Do(req)
		if e != nil {
			t.Fatal("live OpenBao request failed; no transport details disclosed")
		}
		defer resp.Body.Close()
		data, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
		if e != nil || len(data) > 65536 {
			t.Fatal("invalid live response")
		}
		return resp.StatusCode, data
	}
	expect := func(method, path, token string, payload any, want int) []byte {
		t.Helper()
		status, data := request(method, path, token, payload)
		if status != want {
			t.Fatalf("live operation failed: expected HTTP %d, received %d", want, status)
		}
		return data
	}
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal("could not generate test identifier")
	}
	name := "runawaybao-acl-" + hex.EncodeToString(suffix)
	policyPath, otherPath := "sys/policies/acl/"+name, "secret/data/"+name
	dummy := openbao.DemoSecret{Username: "lab-user", Password: "not-a-real-password"}
	payload := map[string]any{"data": dummy}
	decodeDemo := func(data []byte) openbao.DemoSecret {
		t.Helper()
		var result struct {
			Data struct {
				Data openbao.DemoSecret `json:"data"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &result) != nil {
			t.Fatal("invalid demo fixture response")
		}
		return result.Data.Data
	}
	status, data := request("GET", "secret/data/demo", root, nil)
	switch status {
	case 200:
		if decodeDemo(data) != dummy {
			t.Fatal("existing demo is not the known disposable fixture; refusing to overwrite")
		}
	case 404:
		// Refuse to replace a soft-deleted or otherwise pre-existing key.
		expect("GET", "secret/metadata/demo", root, nil, 404)
		t.Cleanup(func() { expect("DELETE", "secret/metadata/demo", root, nil, 204) })
		expect("POST", "secret/data/demo", root, payload, 200)
	default:
		t.Fatal("could not inspect development demo fixture")
	}
	expect("GET", otherPath, root, nil, 404)
	t.Cleanup(func() { expect("DELETE", "secret/metadata/"+name, root, nil, 204) })
	expect("POST", otherPath, root, payload, 200)
	expect("GET", otherPath, root, nil, 200)
	expect("GET", policyPath, root, nil, 404)
	t.Cleanup(func() { expect("DELETE", policyPath, root, nil, 204) })
	const policy = `path "secret/data/demo" { capabilities = ["read"] }`
	expect("PUT", policyPath, root, map[string]any{"policy": policy}, 204)
	var created struct {
		Auth struct {
			Token            string   `json:"client_token"`
			Policies         []string `json:"policies"`
			IdentityPolicies []string `json:"identity_policies"`
		} `json:"auth"`
	}
	data = expect("POST", "auth/token/create", root, map[string]any{"policies": []string{name}, "no_default_policy": true, "ttl": "5m", "renewable": false}, 200)
	if json.Unmarshal(data, &created) != nil || created.Auth.Token == "" {
		t.Fatal("invalid token creation response; any issued token expires within five minutes")
	}
	token := created.Auth.Token
	if token == root {
		t.Fatal("refusing to use or revoke the setup credential as a test token")
	}
	t.Cleanup(func() {
		expect("POST", "auth/token/revoke", root, map[string]any{"token": token}, 204)
		expect("GET", "secret/data/demo", token, nil, 403)
		t.Log("PASS: test token revoked; revoked credential rejected with HTTP 403")
	})
	if !reflect.DeepEqual(created.Auth.Policies, []string{name}) || len(created.Auth.IdentityPolicies) != 0 {
		t.Fatal("restricted token policy assertion failed")
	}
	var lookup struct {
		Data struct {
			Policies         []string `json:"policies"`
			IdentityPolicies []string `json:"identity_policies"`
		} `json:"data"`
	}
	data = expect("POST", "auth/token/lookup", root, map[string]any{"token": token}, 200)
	if json.Unmarshal(data, &lookup) != nil || !reflect.DeepEqual(lookup.Data.Policies, []string{name}) || len(lookup.Data.IdentityPolicies) != 0 {
		t.Fatal("effective restricted token policies are not exact")
	}
	t.Log("PASS: token has only the exact demo-read policy; no default, root, or identity policies")
	client, e := openbao.NewClient(openbao.Config{Address: address, Token: token})
	if e != nil {
		t.Fatal("restricted client configuration failed")
	}
	secret, e := client.ReadDemo(context.Background())
	if e != nil || secret != dummy {
		t.Fatal("restricted Go client read failed")
	}
	t.Log("PASS: existing Go client read the expected dummy fixture")
	expect("POST", "secret/data/demo", token, map[string]any{"data": map[string]string{"username": "denied-write", "password": "dummy-only"}}, 403)
	t.Log("PASS: restricted update rejected with HTTP 403")
	expect("GET", otherPath, token, nil, 403)
	t.Log("PASS: restricted read of an existing different path rejected with HTTP 403")
	if decodeDemo(expect("GET", "secret/data/demo", root, nil, 200)) != dummy {
		t.Fatal("demo fixture changed unexpectedly")
	}
}
