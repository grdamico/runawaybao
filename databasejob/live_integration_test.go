//go:build integration

package databasejob_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/grdamico/runawaybao/databasejob"
	"github.com/grdamico/runawaybao/jobrunner"
	"github.com/grdamico/runawaybao/openbao"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type capturingIssuer struct {
	issuer              *openbao.DatabaseIssuer
	mu                  sync.Mutex
	credentials         map[string]openbao.DatabaseCredentials
	deadlines           map[string]time.Time
	observationDeadline time.Time
}

func (i *capturingIssuer) Issue(ctx context.Context) (openbao.DatabaseCredentials, error) {
	c, err := i.issuer.Issue(ctx)
	if err == nil {
		i.mu.Lock()
		i.credentials[c.LeaseID] = c
		if i.deadlines == nil {
			i.deadlines = make(map[string]time.Time)
		}
		i.deadlines[c.LeaseID] = time.Now().Add(c.TTL)
		i.mu.Unlock()
	}
	return c, err
}
func (i *capturingIssuer) get(id string) openbao.DatabaseCredentials {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.credentials[id]
}

// TestLiveDatabase bootstraps only a uniquely named OpenBao mount and policies.
// PostgreSQL schema/manager setup is performed by postgres/init.sh. No root or
// management credential is passed into the runner, manager, or query client.
func TestLiveDatabase(t *testing.T) { runLive(t, false, false) }
func TestLiveRuntime(t *testing.T) {
	if os.Getenv("RUNAWAYBAO_RUNTIME_INTEGRATION") != "1" {
		t.Skip("explicit runtime opt-in required")
	}
	runLive(t, true, false)
}
func TestLivePolicy(t *testing.T) {
	if os.Getenv("RUNAWAYBAO_POLICY_INTEGRATION") != "1" {
		t.Skip("explicit dry-run policy opt-in required")
	}
	runLive(t, true, true)
}
func runLive(t *testing.T, observe, policy bool) {
	if !observe && os.Getenv("RUNAWAYBAO_DATABASE_INTEGRATION") != "1" {
		t.Skip("explicit live database opt-in required")
	}
	address := os.Getenv("RUNAWAYBAO_DATABASE_ADDR")
	root := os.Getenv("RUNAWAYBAO_DATABASE_ROOT_TOKEN")
	password := os.Getenv("RUNAWAYBAO_DATABASE_MANAGER_PASSWORD")
	u, err := url.Parse(address)
	if err != nil || u == nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || root == "" || password == "" {
		t.Fatal("disposable loopback development setup required")
	}
	port, err := strconv.ParseUint(os.Getenv("RUNAWAYBAO_DATABASE_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("invalid database port")
	}
	httpClient := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, token string, payload any) (int, []byte) {
		t.Helper()
		var body []byte
		if payload != nil {
			var e error
			body, e = json.Marshal(payload)
			if e != nil {
				t.Fatal("request encoding failed")
			}
		}
		req, e := http.NewRequest(method, address+"/v1/"+path, bytes.NewReader(body))
		if e != nil {
			t.Fatal("request construction failed")
		}
		req.Header.Set("X-Vault-Token", token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := httpClient.Do(req)
		if e != nil {
			t.Fatal("live OpenBao operation failed; diagnostics suppressed")
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
			t.Fatalf("live OpenBao operation: expected HTTP %d, received %d", want, status)
		}
		return data
	}
	suffix := make([]byte, 12)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal("identifier generation failed")
	}
	mount := "rbd-" + hex.EncodeToString(suffix)
	role := "reader"
	issuerPolicy := mount + "-issue"
	revokerPolicy := mount + "-revoke"
	// Cleanup registered before operations so partially failed bootstrap is covered.
	expect("GET", "sys/mounts", root, nil, 200)
	t.Cleanup(func() { expect("DELETE", "sys/mounts/"+mount, root, nil, 204) })
	expect("POST", "sys/mounts/"+mount, root, map[string]any{"type": "database"}, 204)
	expect("POST", mount+"/config/postgres", root, map[string]any{
		"plugin_name": "postgresql-database-plugin", "allowed_roles": []string{role},
		"connection_url": "postgresql://{{username}}:{{password}}@postgres:5432/runawaybao?sslmode=disable",
		"username":       "runawaybao_manager", "password": password, "max_open_connections": 2,
	}, 204)
	creation := []string{`CREATE ROLE "{{name}}" WITH LOGIN PASSWORD '{{password}}' VALID UNTIL '{{expiration}}' INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS; GRANT runawaybao_reader TO "{{name}}";`}
	roleConfig := func(ttl string) map[string]any {
		return map[string]any{"db_name": "postgres", "creation_statements": creation, "revocation_statements": []string{`DROP ROLE IF EXISTS "{{name}}";`}, "rollback_statements": []string{`DROP ROLE IF EXISTS "{{name}}";`}, "default_ttl": ttl, "max_ttl": "5m"}
	}
	expect("POST", mount+"/roles/"+role, root, roleConfig("2m"), 204)
	policies := map[string]string{
		issuerPolicy:  `path "` + mount + `/creds/reader" { capabilities = ["read"] }`,
		revokerPolicy: `path "sys/leases/revoke/` + mount + `/creds/reader/*" { capabilities = ["update"] allowed_parameters = { "sync" = [true] } }`,
	}
	if observe {
		delete(policies, revokerPolicy)
	}
	for name, policy := range policies {
		name := name
		t.Cleanup(func() { expect("DELETE", "sys/policies/acl/"+name, root, nil, 204) })
		expect("PUT", "sys/policies/acl/"+name, root, map[string]any{"policy": policy}, 204)
	}
	createToken := func(policy string) string {
		t.Helper()
		var response struct {
			Auth struct {
				Token            string   `json:"client_token"`
				Policies         []string `json:"policies"`
				IdentityPolicies []string `json:"identity_policies"`
			} `json:"auth"`
		}
		tokenTTL := "15m"
		if observe {
			tokenTTL = "2m"
		}
		data := expect("POST", "auth/token/create", root, map[string]any{"policies": []string{policy}, "no_default_policy": true, "ttl": tokenTTL, "renewable": false}, 200)
		if json.Unmarshal(data, &response) != nil || response.Auth.Token == "" || response.Auth.Token == root {
			t.Fatal("invalid restricted token")
		}
		token := response.Auth.Token
		if !observe {
			t.Cleanup(func() { expect("POST", "auth/token/revoke", root, map[string]any{"token": token}, 204) })
		}
		if len(response.Auth.Policies) != 1 || response.Auth.Policies[0] != policy || len(response.Auth.IdentityPolicies) != 0 {
			t.Fatal("unexpected effective policies")
		}
		return token
	}
	issueToken := createToken(issuerPolicy)
	revokeToken := ""
	if !observe {
		revokeToken = createToken(revokerPolicy)
	}
	issuer, e := openbao.NewDatabaseIssuer(openbao.Config{Address: address, Token: issueToken}, mount, role)
	if e != nil {
		t.Fatal("issuer setup failed")
	}
	var revoker databasejob.Revoker = forbiddenRevoker{}
	if !observe {
		revoker, e = openbao.NewLeaseRevoker(openbao.Config{Address: address, Token: revokeToken}, mount, role)
		if e != nil {
			t.Fatal("revoker setup failed")
		}
	}
	capture := &capturingIssuer{issuer: issuer, credentials: make(map[string]openbao.DatabaseCredentials)}
	pg, e := databasejob.NewPostgreSQL("127.0.0.1", uint16(port))
	if e != nil {
		t.Fatal("database client setup failed")
	}
	manager, e := databasejob.New(capture, revoker, pg)
	if e != nil {
		t.Fatal("manager setup failed")
	}
	if observe {
		capture.observationDeadline = time.Now().Add(2*time.Minute + 2*time.Second)
		t.Cleanup(func() { waitObservationExpiry(t, capture, uint16(port)) })
		runObservation(t, capture, pg, manager, policy)
		return
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if manager.Close(ctx) != nil {
			t.Error("database lease cleanup unconfirmed")
		}
	})
	runner := jobrunner.NewRunnerWithDatabase(jobrunner.NewPolicy(jobrunner.PermissionDBReadDemo), nil, manager)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, id := range []string{"A", "B"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result := runner.RunContext(ctx, jobrunner.Job{ID: id, Action: jobrunner.ActionDBReadDemo}); result.Status != jobrunner.Succeeded {
				t.Error("authorized database job failed")
			}
		}()
	}
	wg.Wait()
	a, okA := manager.Lookup("A")
	b, okB := manager.Lookup("B")
	if !okA || !okB || a.State != databasejob.Active || b.State != databasejob.Active || a.LeaseID == "" || a.LeaseID == b.LeaseID {
		t.Fatal("independent job lease association failed")
	}
	credA := capture.get(a.LeaseID)
	credB := capture.get(b.LeaseID)
	if credA.Username == credB.Username || credA.TTL <= 0 || credA.TTL > 2*time.Minute || credB.TTL <= 0 {
		t.Fatal("credentials not unique and short-lived")
	}
	t.Log("PASS: A and B obtained independent credentials and completed the fixed read")
	// These intentionally denied requests must not create credentials or revoke B.
	expect("GET", mount+"/creds/reader", revokeToken, nil, 403)
	expect("POST", "sys/leases/revoke/"+b.LeaseID, issueToken, map[string]any{"sync": true}, 403)
	expect("POST", "sys/leases/revoke/"+a.LeaseID, revokeToken, map[string]any{"sync": true, "lease_id": b.LeaseID}, 403)
	expect("POST", "sys/leases/revoke-prefix/"+mount, revokeToken, map[string]any{"sync": true}, 403)
	expect("POST", mount+"/config/postgres", issueToken, map[string]any{}, 403)
	// Use raw credentials in test memory only to distinguish authentication failure
	// from a generic network outage, and to exercise PostgreSQL permission denials.
	connect := func(c openbao.DatabaseCredentials) (*pgx.Conn, error) {
		config, e := pgx.ParseConfig("postgres://localhost/runawaybao?sslmode=disable")
		if e != nil {
			return nil, e
		}
		config.Host = "127.0.0.1"
		config.Port = uint16(port)
		config.Database = "runawaybao"
		config.User = c.Username
		config.Password = c.Password
		config.TLSConfig = nil
		config.Fallbacks = nil
		config.Tracer = nil
		config.ConnectTimeout = 3 * time.Second
		return pgx.ConnectConfig(ctx, config)
	}
	for _, sql := range []string{"INSERT INTO demo.fixture VALUES (2, 'denied')", "SELECT * FROM demo.restricted", "CREATE TABLE demo.denied (id integer)", "CREATE ROLE denied_role"} {
		conn, e := connect(credB)
		if e != nil {
			t.Fatal("permission-test login failed")
		}
		_, e = conn.Exec(ctx, sql)
		_ = conn.Close(ctx)
		var pgError *pgconn.PgError
		if !errors.As(e, &pgError) || pgError.Code != "42501" {
			t.Fatal("least-privilege denial not confirmed")
		}
	}
	t.Log("PASS: separate issuance/revocation ACLs, body override denial, and database least privilege")
	if manager.RevokeJob(ctx, "A") != nil || manager.RevokeJob(ctx, "A") != nil {
		t.Fatal("exact lease revocation not confirmed")
	}
	conn, e := connect(credA)
	if conn != nil {
		_ = conn.Close(ctx)
	}
	var pgError *pgconn.PgError
	if !errors.As(e, &pgError) || (pgError.Code != "28P01" && pgError.Code != "28000") {
		t.Fatal("A authentication rejection not confirmed")
	}
	if manager.Read(ctx, "B") != nil {
		t.Fatal("B access did not survive A revocation")
	}
	t.Log("PASS: A's original credentials cannot authenticate; B's original credentials still read")
	// Exercise backend idempotence directly, bypassing manager's confirmed no-op.
	if revoker.Revoke(ctx, a.LeaseID) != nil {
		t.Fatal("already revoked backend acknowledgement failed")
	}
	if manager.RevokeJob(ctx, "B") != nil {
		t.Fatal("B cleanup unconfirmed")
	}
	expect("POST", mount+"/roles/"+role, root, roleConfig("5s"), 204)
	if manager.Execute(ctx, "expired") != nil {
		t.Fatal("expiry fixture failed")
	}
	expired, _ := manager.Lookup("expired")
	expiredCredentials := capture.get(expired.LeaseID)
	deadline := time.Now().Add(12 * time.Second)
	rejected := false
	for time.Now().Before(deadline) {
		conn, e = connect(expiredCredentials)
		if conn != nil {
			_ = conn.Close(ctx)
		}
		if errors.As(e, &pgError) && (pgError.Code == "28P01" || pgError.Code == "28000") {
			rejected = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !rejected {
		t.Fatal("expiry authentication rejection not confirmed")
	}
	if manager.RevokeJob(ctx, "expired") != nil {
		t.Fatal("expired lease cleanup unconfirmed")
	}
	t.Log("PASS: expired credentials reject authentication and exact cleanup is acknowledged")
}
