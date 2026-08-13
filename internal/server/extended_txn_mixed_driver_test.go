package server

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// M0132-S8 — the real-world mixed shape, driven through the lib/pq driver that
// actually emits it. pgx v5 and lib/pq send argument-less statements (BEGIN /
// COMMIT / ROLLBACK included) down the SIMPLE protocol and parameterised DML
// down the EXTENDED protocol, so a driver-level test is the only end-to-end proof
// that the two compose into ONE block. The raw-frame tests above pin the wire
// shape precisely; these pin that the vendored driver (the same one
// cmd/tpch-runner uses) works unmodified.
//
// Each subtest pins a dedicated connection (sql.Conn) so BEGIN/DML/COMMIT stay on
// one connection, and a second connection observes cross-session visibility —
// which is also the interleaving half of the S8 gate (the D-002 runner itself is
// simple-only, a fidelity point upstream PQexec shares, so the mixed interleaving
// is pinned here rather than by a .spec).

// mixedDriverDSN opens a lib/pq database handle against the addr startCopyExecServer
// returned, then checks out two dedicated connections (writer and observer).
func mixedDriverDSN(t *testing.T, addr string) (*sql.DB, *sql.Conn, *sql.Conn) {
	t.Helper()
	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	dsn := "host=" + addr[:colonIdx] + " port=" + addr[colonIdx+1:] + " user=postgres dbname=postgres sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	writer, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	observer, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = observer.Close()
		_ = db.Close()
	})
	return db, writer, observer
}

// countWhere returns the row count of items matching id via the given connection.
//
// The query is argument-less on purpose: it must take the SIMPLE path. A
// parameterised `WHERE id = $1` would route through lib/pq's extended protocol,
// which requests binary result formats for int8 `count(*)` — and goopg's Bind
// handler rejects any non-zero result format with 0A000 "binary result formats
// are not supported" (extended.go). That gap is pre-existing and out of M0132's
// transaction scope; the mixed-shape assertion here is about the DML, so the
// observation SELECT stays simple. (id is an int from test code, never
// client-controlled, so string interpolation is safe.)
func countWhere(t *testing.T, conn *sql.Conn, id int) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(context.Background(), fmt.Sprintf("SELECT count(*) FROM items WHERE id = %d", id)).Scan(&n); err != nil {
		t.Fatalf("count items id=%d: %v", id, err)
	}
	return n
}

// TestM0132S8_DriverMixedBlockRollback proves the real driver shape end to end:
// BEGIN (simple) → parameterised INSERT (extended) → ROLLBACK (simple). The
// parameterised INSERT must join the block (invisible to the observer), and the
// ROLLBACK must discard it.
func TestM0132S8_DriverMixedBlockRollback(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	_, w, o := mixedDriverDSN(t, addr)
	ctx := context.Background()

	if _, err := w.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := w.ExecContext(ctx, "INSERT INTO items VALUES ($1, $2)", 7, "seven"); err != nil {
		t.Fatalf("parameterised INSERT: %v", err)
	}
	if got := countWhere(t, o, 7); got != 0 {
		t.Errorf("mixed block INSERT visible to observer before COMMIT: %d rows, want 0", got)
	}
	if got := countWhere(t, w, 7); got != 1 {
		t.Errorf("mixed block INSERT not self-visible: %d rows, want 1", got)
	}
	if _, err := w.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
	if got := countWhere(t, o, 7); got != 0 {
		t.Errorf("after mixed ROLLBACK: %d rows, want 0", got)
	}
}

// TestM0132S8_DriverMixedBlockCommit is the positive half: BEGIN (simple) →
// parameterised INSERT (extended) → COMMIT (simple), visible to the observer only
// after the COMMIT.
func TestM0132S8_DriverMixedBlockCommit(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	_, w, o := mixedDriverDSN(t, addr)
	ctx := context.Background()

	if _, err := w.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if _, err := w.ExecContext(ctx, "INSERT INTO items VALUES ($1, $2)", 8, "eight"); err != nil {
		t.Fatalf("parameterised INSERT: %v", err)
	}
	if got := countWhere(t, o, 8); got != 0 {
		t.Errorf("mixed block INSERT visible to observer before COMMIT: %d rows, want 0", got)
	}
	if _, err := w.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	if got := countWhere(t, o, 8); got != 1 {
		t.Errorf("after mixed COMMIT: %d rows, want 1", got)
	}
}

// TestM0132S8_DriverInBlockErrorAbortsLaterStmt pins (d) through the driver: an
// extended-protocol statement error inside a block aborts the block, so a later
// simple-protocol statement is rejected 25P02.
func TestM0132S8_DriverInBlockErrorAbortsLaterStmt(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	_, w, _ := mixedDriverDSN(t, addr)
	ctx := context.Background()

	if _, err := w.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	// NULL id violates the NOT NULL constraint — an executor error on the
	// extended path (parameterised), inside the block.
	if _, err := w.ExecContext(ctx, "INSERT INTO items VALUES ($1, $2)", nil, "x"); err == nil {
		t.Fatal("INSERT of NULL id did not error")
	}
	// A later argument-less statement goes down the SIMPLE path and must be
	// rejected with 25P02 because the block is now failed.
	if _, err := w.ExecContext(ctx, "INSERT INTO items VALUES (4, 'd')"); err == nil || !strings.Contains(err.Error(), "25P02") {
		t.Errorf("simple INSERT after an in-block extended error: want 25P02, got %v", err)
	}
	if _, err := w.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
}
