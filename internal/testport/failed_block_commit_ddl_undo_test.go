package testport

// review/260831-2 CP-1: a COMMIT issued inside a FAILED transaction block is a
// ROLLBACK (PG semantics), but the postmaster's COMMIT-as-ROLLBACK arm went
// straight to TxnMgr.Rollback without first running
// executor.ProcessRollbackUndos — the step the explicit-ROLLBACK arm does run.
// DDL creates are registered in the in-memory catalog non-transactionally, so
// the table stayed alive (and writable) while its pg_class/pg_attribute rows
// were rolled back.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/lib/pq"
)

func TestPort_FailedBlockCommitUndoesInBlockDDL(t *testing.T) {
	c := newCluster(t, "failed-block-commit-ddl-undo")
	mustInitStart(t, c)

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	// One CONNECTION, several messages: the failed block and its COMMIT must
	// share a session for the block state to exist at all.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()

	for _, stmt := range []string{"BEGIN", "CREATE TABLE failblk (a int)"} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "SELECT 1/0"); err == nil {
		t.Fatal("expected `SELECT 1/0` to fail and mark the block failed")
	}
	// COMMIT in a failed block succeeds at the protocol level and reports
	// ROLLBACK.
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT in failed block: %v", err)
	}

	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM pg_class WHERE relname = 'failblk'").Scan(&n); err != nil {
		t.Fatalf("pg_class probe: %v", err)
	}
	if n != 0 {
		t.Errorf("pg_class count for failblk = %d, want 0 (rolled-back CREATE TABLE survived)", n)
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO failblk VALUES (1)")
	if err == nil {
		t.Fatal("INSERT into the rolled-back table succeeded; the catalog entry outlived its heap rows")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("INSERT error = %v, want `relation \"failblk\" does not exist`", err)
	}
}
