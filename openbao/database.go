package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DatabaseCredentials is sensitive controller memory, never a job result or log.
// LeaseID is metadata used only by the trusted controller.
type DatabaseCredentials struct {
	LeaseID  string        `json:"lease_id"`
	TTL      time.Duration `json:"-"`
	Username string        `json:"username"`
	Password string        `json:"password"`
}

// DatabaseIssuer and LeaseRevoker deliberately accept separate token configs.
// Only a fixed database mount and role are supported.
type DatabaseIssuer struct {
	client *Client
	prefix string
}
type LeaseRevoker struct {
	client *Client
	prefix string
}

var databaseName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var leaseSuffix = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func databaseClient(config Config, mount, role string) (*Client, string, error) {
	if !databaseName.MatchString(mount) || !databaseName.MatchString(role) {
		return nil, "", ErrConfiguration
	}
	c, err := NewClient(config)
	if err != nil {
		return nil, "", err
	}
	// NewClient already validates and normalizes the origin and HTTP policy.
	c.endpoint = strings.TrimSuffix(c.endpoint, demoPath)
	return c, mount + "/creds/" + role + "/", nil
}
func NewDatabaseIssuer(config Config, mount, role string) (*DatabaseIssuer, error) {
	c, p, err := databaseClient(config, mount, role)
	if err != nil {
		return nil, err
	}
	return &DatabaseIssuer{c, p}, nil
}
func NewLeaseRevoker(config Config, mount, role string) (*LeaseRevoker, error) {
	c, p, err := databaseClient(config, mount, role)
	if err != nil {
		return nil, err
	}
	return &LeaseRevoker{c, p}, nil
}
func validLease(prefix, id string) bool {
	return strings.HasPrefix(id, prefix) && leaseSuffix.MatchString(strings.TrimPrefix(id, prefix))
}

// databaseRequest never returns response bodies or raw transport errors in errors.
func (c *Client) databaseRequest(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	if ctx == nil {
		return nil, ErrConfiguration
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+"/v1/"+path, bytes.NewReader(payload))
	if err != nil {
		return nil, ErrRequest
	}
	req.Header.Set("X-Vault-Token", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		return nil, ErrPermissionDenied
	}
	if method == http.MethodPost {
		// sync=true must complete successfully. Missing/expired leases are successful
		// only if OpenBao acknowledges the synchronous operation, never by local TTL.
		if resp.StatusCode != http.StatusNoContent {
			return nil, ErrHTTP
		}
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ErrHTTP
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, requestError(ctx, err)
	}
	if len(body) > maxResponseBytes {
		return nil, ErrResponse
	}
	return body, nil
}

// Issue is intentionally not retried: a lost response can leave an orphan until TTL.
// On a partially malformed response, a trustworthy lease ID is returned alongside
// the error so the controller can attempt cleanup without using its credentials.
func (i *DatabaseIssuer) Issue(ctx context.Context) (DatabaseCredentials, error) {
	if i == nil || i.client == nil {
		return DatabaseCredentials{}, ErrConfiguration
	}
	body, err := i.client.databaseRequest(ctx, http.MethodGet, strings.TrimSuffix(i.prefix, "/"), nil)
	if err != nil {
		return DatabaseCredentials{}, err
	}
	var response struct {
		LeaseID  string `json:"lease_id"`
		Duration int64  `json:"lease_duration"`
		Data     struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || !validLease(i.prefix, response.LeaseID) {
		return DatabaseCredentials{}, ErrResponse
	}
	credentials := DatabaseCredentials{LeaseID: response.LeaseID}
	if response.Duration <= 0 || response.Duration > 86400 || response.Data.Username == "" || response.Data.Password == "" {
		return credentials, ErrResponse
	}
	credentials.TTL = time.Duration(response.Duration) * time.Second
	credentials.Username = response.Data.Username
	credentials.Password = response.Data.Password
	return credentials, nil
}

// Revoke submits only an exact, validated lease, using a path-scoped ACL and no
// body lease_id override. Only a synchronous 204 response confirms completion.
func (r *LeaseRevoker) Revoke(ctx context.Context, id string) error {
	if r == nil || r.client == nil || !validLease(r.prefix, id) {
		return ErrConfiguration
	}
	_, err := r.client.databaseRequest(ctx, http.MethodPost, "sys/leases/revoke/"+id, []byte(`{"sync":true}`))
	return err
}
