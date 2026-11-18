package workerprotocol

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestStrictFrames(t *testing.T) {
	for _, s := range []string{"", `{"username":"secret","password":"value"}`, "{} {}\n", "null\n", `{"Username":"u","password":"p"}` + "\n", "[]\n", `{"username":"a","username":"b","password":"c"}` + "\n", `{"username":"a","\u0075sername":"b","password":"c"}` + "\n", `{"username":"a","password":"b","token":"secret"}` + "\n", strings.Repeat("x", MaxFrame) + "\n", "{\xff}\n"} {
		var c Credentials
		if err := Read(bufio.NewReaderSize(strings.NewReader(s), MaxFrame), &c); err != ErrProtocol {
			t.Fatal("invalid input accepted")
		}
	}
}
func TestRoundTripAndShortWrite(t *testing.T) {
	var b bytes.Buffer
	c := Credentials{"user", "sensitive-password"}
	if Write(&b, c) != nil {
		t.Fatal("write")
	}
	var got Credentials
	if Read(bufio.NewReaderSize(&b, MaxFrame), &got) != nil || got != c {
		t.Fatal("roundtrip")
	}
	if Write(shortWriter{}, c) != ErrProtocol {
		t.Fatal("partial write accepted")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return 1, io.ErrShortWrite }
