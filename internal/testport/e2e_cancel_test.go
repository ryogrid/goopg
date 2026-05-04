package testport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestE2E_QueryCancellation_DoDPgSleep is the DoD test for M0049-0001.
//
// DoD: "psql Ctrl-C against pg_sleep(60) returns within 200ms".
//
// Implementation: use lib/pq's built-in CancelRequest support — when the
// Go context passed to QueryContext is cancelled, lib/pq dials a fresh TCP
// connection and sends a CancelRequest (magic code 80877102) carrying the
// backend's (pid, secretKey) pair captured from BackendKeyData at startup.
// goopg's cancel handler fires the per-query cancel function, which unblocks
// pg_sleep's select on ctx.Ctx.Done(), and returns SQLSTATE 57014
// ("canceling statement due to user request").
func TestE2E_QueryCancellation_DoDPgSleep(t *testing.T) {
	c, err := cluster.New("e2e_cancel", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	host, port, err := splitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	dsn := fmt.Sprintf("host=%s port=%s user=postgres dbname=postgres sslmode=disable", host, port)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Cancel the context 100ms after the query starts; lib/pq will
	// send a CancelRequest on a new TCP connection.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = db.QueryContext(ctx, "SELECT pg_sleep(60)")
	elapsed := time.Since(start)

	t.Logf("pg_sleep(60) returned in %v with err=%v", elapsed, err)

	// DoD: must return within 200ms.
	const maxElapsed = 200 * time.Millisecond
	if elapsed > maxElapsed {
		t.Errorf("pg_sleep(60) took %v > %v after cancel; cancel not fast enough", elapsed, maxElapsed)
	}

	// The error must be a PostgreSQL SQLSTATE 57014 or a context error.
	// lib/pq wraps the server's ErrorResponse as *pq.Error; when the
	// connection itself times out before the server replies, we may get
	// context.DeadlineExceeded or driver.ErrBadConn instead.
	if err == nil {
		t.Error("expected error after cancel, got nil")
		return
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		if pqErr.Code != "57014" {
			t.Errorf("expected SQLSTATE 57014, got %s: %s", pqErr.Code, pqErr.Message)
		}
	}
	// context.Canceled / context.DeadlineExceeded are also acceptable
	// if the network timeout fires before the server's error reply arrives.
}

func splitHostPort(addr string) (string, string, error) {
	// Reuse existing helper if visible; otherwise parse manually.
	// cluster.go already defines this as a package-private function;
	// we replicate the minimal version here.
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("invalid addr %q", addr)
}
