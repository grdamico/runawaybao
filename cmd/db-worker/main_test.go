package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSafeProtocolFailures(t *testing.T) {
	for _, s := range []string{"", `{"username":"sensitive","password":"secret","sql":"DELETE"}` + "\n", `{"username":"sensitive","password":"secret"}` + "\n" + `{"operation":"arbitrary-sql"}` + "\n", `{"username":"sensitive","password":"secret"}` + "\n"} {
		var b bytes.Buffer
		if serve(context.Background(), strings.NewReader(s), &b) != 2 {
			t.Fatal("invalid input accepted")
		}
		if strings.Contains(b.String(), "secret") || strings.Contains(b.String(), "sensitive") {
			t.Fatal("credential leaked")
		}
	}
}
func TestFixedFinish(t *testing.T) {
	var b bytes.Buffer
	if serve(context.Background(), strings.NewReader("{\"username\":\"u\",\"password\":\"p\"}\n{\"operation\":\"finish\"}\n"), &b) != 0 {
		t.Fatal("finish failed")
	}
	if b.String() != "{\"status\":\"ready\"}\n{\"status\":\"finished\"}\n" {
		t.Fatal("unexpected response")
	}
}
func TestDatabaseFailureIsSanitized(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var b bytes.Buffer
	if serve(ctx, strings.NewReader("{\"username\":\"sensitive-user\",\"password\":\"sensitive-password\"}\n{\"operation\":\"read\"}\n"), &b) != 3 {
		t.Fatal("cancelled database connection not rejected")
	}
	if b.String() != "{\"status\":\"ready\"}\n{\"status\":\"database_failed\"}\n" {
		t.Fatal("database failure exposed diagnostics")
	}
}
