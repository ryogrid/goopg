package server

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestDropIndexConcurrentlyWaitsForOpenTransaction verifies that
// DROP INDEX CONCURRENTLY blocks until all transactions that were open
// at the time of the DROP have committed or aborted. M0100-0009.
//
// Flow:
//  1. s1 starts a BEGIN (explicit transaction, read-only).
//  2. s2 issues DROP INDEX CONCURRENTLY — should block because s1 is active.
//  3. We verify that s2 is blocked for at least 150 ms.
//  4. s1 commits — s2 should unblock within 10 s.
//  5. The index is gone afterwards.
func TestDropIndexConcurrentlyWaitsForOpenTransaction(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	dsn := "host=" + addrHost(addr) + " port=" + addrPort(addr) + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Setup: create table + index.
	if _, err := db.ExecContext(ctx, `CREATE TABLE cic_test(id int primary key, val text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX cic_test_val_idx ON cic_test(val)`); err != nil {
		t.Fatalf("create index: %v", err)
	}
	defer db.ExecContext(ctx, `DROP TABLE IF EXISTS cic_test`) //nolint:errcheck

	// s1: open an explicit transaction and hold it open.
	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn1: %v", err)
	}
	defer conn1.Close()

	if _, err := conn1.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := conn1.ExecContext(ctx, "SELECT 1"); err != nil {
		t.Fatalf("SELECT: %v", err)
	}

	// s2: DROP INDEX CONCURRENTLY in a goroutine — should block.
	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn2: %v", err)
	}
	defer conn2.Close()

	dropStart := time.Now()
	dropDone := make(chan error, 1)
	go func() {
		_, err := conn2.ExecContext(ctx, "DROP INDEX CONCURRENTLY cic_test_val_idx")
		dropDone <- err
	}()

	// Give s2 time to enter the wait.
	time.Sleep(150 * time.Millisecond)

	// Verify the DROP has not completed yet.
	select {
	case err := <-dropDone:
		t.Fatalf("DROP completed immediately (should have blocked): %v", err)
	default:
	}

	// Commit s1 — the DROP should unblock.
	if _, err := conn1.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}

	var dropErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case dropErr = <-dropDone:
		case <-time.After(10 * time.Second):
			t.Errorf("DROP INDEX CONCURRENTLY did not complete within 10s after COMMIT")
		}
	}()
	wg.Wait()

	if dropErr != nil {
		t.Fatalf("DROP INDEX CONCURRENTLY failed: %v", dropErr)
	}

	elapsed := time.Since(dropStart)
	if elapsed < 100*time.Millisecond {
		t.Errorf("DROP completed too fast (%v) — may not have blocked on s1", elapsed)
	}
}

// TestDropIndexConcurrentlyBlockedInExplicitTx verifies that DROP INDEX
// CONCURRENTLY returns an error when issued inside an explicit transaction.
// M0100-0009.
func TestDropIndexConcurrentlyBlockedInExplicitTx(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	dsn := "host=" + addrHost(addr) + " port=" + addrPort(addr) + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE cic_tx_test(id int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX cic_tx_test_id_idx ON cic_tx_test(id)`); err != nil {
		t.Fatalf("create index: %v", err)
	}
	defer db.ExecContext(ctx, `DROP TABLE IF EXISTS cic_tx_test`) //nolint:errcheck

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}

	_, err = conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY cic_tx_test_id_idx")
	if err == nil {
		t.Fatal("expected error for DROP INDEX CONCURRENTLY inside explicit transaction, got nil")
	}

	conn.ExecContext(ctx, "ROLLBACK") //nolint:errcheck
}

// addrHost extracts host from "host:port".
func addrHost(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

// addrPort extracts port from "host:port".
func addrPort(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return ""
}
