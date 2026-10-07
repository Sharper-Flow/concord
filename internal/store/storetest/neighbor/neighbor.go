// Package neighbor owns the SQLite subprocess fixture shared by store and CLI
// tests. It does not import store: internal store tests cannot import storetest,
// which imports store. Production packages must not use this test support.
package neighbor

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type Mode string

const (
	Reader     Mode = "reader"
	Writer     Mode = "writer"
	WriterHold      = time.Second
	// The child role keys use the TEST_CONCORD_ prefix because each test
	// binary's TestMain scrubs every CONCORD_* key (internal/testenv).
	childEnv   = "TEST_CONCORD_SQLITE_NEIGHBOR"
	childDBEnv = "TEST_CONCORD_SQLITE_NEIGHBOR_DB"
)

// Start re-executes the caller's test binary. Each importing test package must
// declare TestSQLiteNeighborProcess and delegate its body to Child.
// Readiness means BEGIN and SELECT have completed, not merely that Open ran.
func Start(t *testing.T, path string, mode Mode) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSQLiteNeighborProcess$", "-test.v=false") //nolint:gosec // The OS supplies this running test binary's path; all child arguments are fixed, never operator input.
	cmd.Env = append(os.Environ(), childEnv+"="+string(mode), childDBEnv+"="+path)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		t.Fatal(err)
	}
	// Always reap, including readiness failures and Fatal in the calling test.
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- "closed before READY"
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case line := <-ready:
		if line != "READY" {
			t.Fatalf("%s neighbor: %s", mode, line)
		}
	case <-ctx.Done():
		t.Fatalf("%s neighbor readiness: %v", mode, ctx.Err())
	}
}

// Child holds a real WAL snapshot or write lock in a separate OS process.
// The reader releases only on pipe EOF. The writer releases on EOF or its
// bounded hold, shorter than the production busy timeout. No sleep gates ready.
func Child(t *testing.T) {
	t.Helper()
	mode := Mode(os.Getenv(childEnv))
	if mode == "" {
		return
	}
	db, err := sql.Open("sqlite", "file:"+os.Getenv(childDBEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	begin := "BEGIN"
	if mode == Writer {
		begin = "BEGIN IMMEDIATE"
	} else if mode != Reader {
		t.Fatalf("unknown neighbor mode %q", mode)
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM domain_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY")
	stop := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(stop) }()
	if mode == Reader {
		<-stop
		return
	}
	timer := time.NewTimer(WriterHold)
	defer timer.Stop()
	select {
	case <-stop:
	case <-timer.C:
	}
}
