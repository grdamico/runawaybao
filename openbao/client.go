// Package openbao reads only the development demo secret from OpenBao KV v2.
package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const demoPath = "/v1/secret/data/demo"
const maxResponseBytes = 64 * 1024

var (
	ErrConfiguration    = errors.New("invalid OpenBao client configuration")
	ErrPermissionDenied = errors.New("OpenBao permission denied")
	ErrNotFound         = errors.New("OpenBao demo secret not found")
	ErrHTTP             = errors.New("OpenBao HTTP failure")
	ErrRequest          = errors.New("OpenBao request failed")
	ErrTimeout          = errors.New("OpenBao request timed out")
	ErrResponse         = errors.New("invalid OpenBao demo response")
)

// Config must be supplied explicitly. Token must be a restricted read-only
// token, never a development root token. The client cannot infer token ACLs.
type Config struct {
	Address string
	Token   string
	// Timeout defaults to five seconds; negative values are invalid.
	Timeout time.Duration
}

type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// DemoSecret contains sensitive values. Callers must not log or print it.
type DemoSecret struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func NewClient(config Config) (*Client, error) {
	u, err := url.Parse(config.Address)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrConfiguration
	}
	if strings.TrimSpace(config.Token) == "" || strings.ContainsAny(config.Token, "\r\n") || config.Timeout < 0 {
		return nil, ErrConfiguration
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	u.Path = demoPath
	return &Client{
		endpoint: u.String(),
		token:    config.Token,
		http: &http.Client{
			Timeout: timeout,
			// Never forward the authentication header to a redirect destination.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// ReadDemo reads the fixed demo path. Errors exclude response bodies, tokens,
// URLs, and underlying transport diagnostics, which may contain sensitive data.
func (c *Client) ReadDemo(ctx context.Context) (DemoSecret, error) {
	if c == nil || c.http == nil || ctx == nil {
		return DemoSecret{}, ErrConfiguration
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return DemoSecret{}, ErrRequest
	}
	req.Header.Set("X-Vault-Token", c.token)
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return DemoSecret{}, requestError(ctx, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return DemoSecret{}, ErrPermissionDenied
	case http.StatusNotFound:
		return DemoSecret{}, ErrNotFound
	default:
		return DemoSecret{}, fmt.Errorf("%w (status %d)", ErrHTTP, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return DemoSecret{}, requestError(ctx, err)
	}
	if len(body) > maxResponseBytes {
		return DemoSecret{}, ErrResponse
	}
	var envelope struct {
		Data *struct {
			Data *DemoSecret `json:"data"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Data == nil || envelope.Data.Data == nil || envelope.Data.Data.Username == "" || envelope.Data.Data.Password == "" {
		return DemoSecret{}, ErrResponse
	}
	return *envelope.Data.Data, nil
}

func requestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return ErrTimeout
	}
	return ErrRequest
}
