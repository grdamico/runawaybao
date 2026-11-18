// db-worker has no OpenBao dependency and accepts only fixed operations.
package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/grdamico/runawaybao/internal/dboperation"
	"github.com/grdamico/runawaybao/internal/workerprotocol"
	"github.com/jackc/pgx/v5"
)

func serve(ctx context.Context, input io.Reader, output io.Writer) int {
	r := bufio.NewReaderSize(input, workerprotocol.MaxFrame)
	var credentials workerprotocol.Credentials
	if workerprotocol.Read(r, &credentials) != nil || !workerprotocol.Valid(credentials) {
		return 2
	}
	var conn *pgx.Conn
	defer func() { dboperation.Close(conn) }()
	if workerprotocol.Write(output, workerprotocol.Response{Status: "ready"}) != nil {
		return 2
	}
	for {
		var command workerprotocol.Command
		if workerprotocol.Read(r, &command) != nil {
			return 2
		}
		switch command.Operation {
		case "read":
			dboperation.Close(conn)
			conn = nil
			var err error
			conn, err = dboperation.Connect(ctx, "postgres", 5432, credentials.Username, credentials.Password)
			if err != nil || dboperation.Read(ctx, conn) != nil {
				_ = workerprotocol.Write(output, workerprotocol.Response{Status: "database_failed"})
				return 3
			}
			if workerprotocol.Write(output, workerprotocol.Response{Status: "read_ok", BackendPID: conn.PgConn().PID()}) != nil {
				return 2
			}
		case "demo-child":
			cmd := exec.CommandContext(ctx, "/runawaybao-unexpected")
			cmd.Env = []string{}
			cmd.Stdin = nil
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			if cmd.Run() != nil {
				return 4
			}
			if workerprotocol.Write(output, workerprotocol.Response{Status: "child_ok"}) != nil {
				return 2
			}
		case "finish":
			dboperation.Close(conn)
			conn = nil
			credentials = workerprotocol.Credentials{}
			if workerprotocol.Write(output, workerprotocol.Response{Status: "finished"}) != nil {
				return 2
			}
			return 0
		default:
			return 2
		}
	}
}
func main() {
	if len(os.Args) == 2 && os.Args[1] == "bootstrap" {
		// Read exactly one byte, with no buffered read-ahead into credential data.
		deadline := time.AfterFunc(90*time.Second, func() { os.Exit(2) })
		var gate [1]byte
		if _, err := io.ReadFull(os.Stdin, gate[:]); err != nil || gate[0] != 'G' {
			os.Exit(2)
		}
		deadline.Stop()
		if syscall.Exec("/runawaybao-db-worker", []string{"/runawaybao-db-worker", "worker"}, []string{}) != nil {
			os.Exit(2)
		}
	}
	if len(os.Args) != 2 || os.Args[1] != "worker" {
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	// Bounds blocked stdin and database work even if the controller disappears.
	timer := time.AfterFunc(180*time.Second, func() { os.Exit(2) })
	defer timer.Stop()
	os.Exit(serve(ctx, os.Stdin, os.Stdout))
}
