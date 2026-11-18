// Package workerprotocol defines the bounded, private stdin protocol. Credentials
// appear only in the initial frame; endpoints, SQL and lease IDs are never fields.
package workerprotocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const MaxFrame = 16 * 1024

var ErrProtocol = errors.New("invalid worker protocol")

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type Command struct {
	Operation string `json:"operation"`
}
type Response struct {
	Status     string `json:"status"`
	BackendPID uint32 `json:"backend_pid,omitempty"`
}

func Read(r *bufio.Reader, v any) error {
	b, err := r.ReadSlice('\n')
	if err != nil || len(b) > MaxFrame {
		return ErrProtocol
	}
	if !utf8.Valid(b) {
		return ErrProtocol
	}
	// Reject duplicate keys, including escaped spellings, rather than accepting
	// encoding/json's last-key-wins behavior for credential frames.
	keys := json.NewDecoder(bytes.NewReader(b))
	t, e := keys.Token()
	if e != nil || t != json.Delim('{') {
		return ErrProtocol
	}
	allowed := map[string]bool{}
	switch v.(type) {
	case *Credentials:
		allowed["username"] = true
		allowed["password"] = true
	case *Command:
		allowed["operation"] = true
	case *Response:
		allowed["status"] = true
		allowed["backend_pid"] = true
	default:
		return ErrProtocol
	}
	seen := map[string]bool{}
	for keys.More() {
		k, e := keys.Token()
		name, ok := k.(string)
		if e != nil || !ok || seen[name] || !allowed[name] {
			return ErrProtocol
		}
		seen[name] = true
		var value json.RawMessage
		if keys.Decode(&value) != nil {
			return ErrProtocol
		}
	}
	if t, e = keys.Token(); e != nil || t != json.Delim('}') {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrProtocol
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrProtocol
	}
	return nil
}
func Write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil || len(b)+1 > MaxFrame {
		return ErrProtocol
	}
	b = append(b, '\n')
	n, err := w.Write(b)
	if err != nil || n != len(b) {
		return ErrProtocol
	}
	return nil
}
func Valid(c Credentials) bool {
	return c.Username != "" && len(c.Username) <= 256 && c.Password != "" && len(c.Password) <= 4096
}
