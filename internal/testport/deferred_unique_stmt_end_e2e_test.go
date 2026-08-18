package testport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_DeferrableUniqueStmtEndAutocommit is the canonical Bucket 4 case:
// a UNIQUE index declared DEFERRABLE (no INITIALLY clause, so INITIALLY
// IMMEDIATE by default) must never block per-row — PostgreSQL sets
// pg_index.indimmediate = false for ANY deferrable index regardless of its
// INITIALLY mode (postgres/src/backend/catalog/index.c:2080-2082) — so
// `UPDATE unique_tbl SET i = i+1` over a contiguous key range must succeed
// even with NO explicit BEGIN (autocommit), because the per-row checks queue
// instead of raising and are only rechecked once at the END of this one
// statement, by which point every row has already shifted and no two rows
// collide. b4-s1-stmt-end-unique acceptance criterion 1 (autocommit half).
func TestPort_DeferrableUniqueStmtEndAutocommit(t *testing.T) {
	c := newCluster(t, "stmtenduniqauto")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE unique_tbl (i integer UNIQUE DEFERRABLE, t text)"); err != nil {
		t.Fatalf("create unique_tbl: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO unique_tbl VALUES (0,'one'),(1,'two'),(2,'tree'),(3,'four'),(4,'five')"); err != nil {
		t.Fatalf("seed unique_tbl: %v", err)
	}

	// No BEGIN at all — this single statement is its own autocommit
	// transaction. Before this slice this failed at the very first transient
	// duplicate (i=0→1 collides with the existing i=1 row) with 23505.
	if err := runSQLSimple(t, c, "UPDATE unique_tbl SET i = i + 1"); err != nil {
		t.Fatalf("autocommit UPDATE i=i+1 should succeed (queue+stmt-end recheck), got: %v", err)
	}

	rows := runSQL(t, c, "SELECT i FROM unique_tbl ORDER BY i")
	want := []string{"1", "2", "3", "4", "5"}
	if len(rows) != len(want) {
		t.Fatalf("row count after shift: got %v, want %v", rows, want)
	}
	for idx, w := range want {
		if rows[idx][0] != w {
			t.Fatalf("row %d after shift: got %q, want %q (full: %v)", idx, rows[idx][0], w, rows)
		}
	}
}

// TestPort_DeferrableUniqueStmtEndExplicitTxn is acceptance criterion 1's
// explicit-transaction half: wrapping the same statement in BEGIN … COMMIT
// must ALSO succeed (ruling out "the enqueue gate wrongly requires an
// explicit transaction" as a sufficient fix — b4 research report B.8 showed
// goopg previously failed identically either way).
func TestPort_DeferrableUniqueStmtEndExplicitTxn(t *testing.T) {
	c := newCluster(t, "stmtenduniqtxn")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE unique_tbl2 (i integer UNIQUE DEFERRABLE, t text)"); err != nil {
		t.Fatalf("create unique_tbl2: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO unique_tbl2 VALUES (0,'one'),(1,'two'),(2,'tree'),(3,'four'),(4,'five')"); err != nil {
		t.Fatalf("seed unique_tbl2: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}

	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := ex("UPDATE unique_tbl2 SET i = i + 1"); err != nil {
		t.Fatalf("in-txn UPDATE i=i+1 should succeed, got: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("commit: unexpected error %v", err)
	}

	rows := runSQL(t, c, "SELECT i FROM unique_tbl2 ORDER BY i")
	want := []string{"1", "2", "3", "4", "5"}
	if len(rows) != len(want) {
		t.Fatalf("row count after shift: got %v, want %v", rows, want)
	}
	for idx, w := range want {
		if rows[idx][0] != w {
			t.Fatalf("row %d after shift: got %q, want %q (full: %v)", idx, rows[idx][0], w, rows)
		}
	}
}

// TestPort_DeferrableUniqueStmtEndExtendedProtocol is the extended-protocol
// twin of the autocommit case (Rule #2): binding a parameter forces lib/pq to
// use Parse/Bind/Execute instead of the simple-query message (see scConn's
// doc comment), which exercises dispatch_extended.go's drain hook instead of
// dispatch.go's.
func TestPort_DeferrableUniqueStmtEndExtendedProtocol(t *testing.T) {
	c := newCluster(t, "stmtenduniqext")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE unique_tbl3 (i integer UNIQUE DEFERRABLE, t text)"); err != nil {
		t.Fatalf("create unique_tbl3: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO unique_tbl3 VALUES (0,'one'),(1,'two'),(2,'tree'),(3,'four'),(4,'five')"); err != nil {
		t.Fatalf("seed unique_tbl3: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	// A parameterized statement with no explicit BEGIN: lib/pq drives this
	// over the extended protocol, and the connection has no explicit block —
	// dispatch_extended.go's out-of-block ownTx path.
	if _, err := conn.ExecContext(ctx, "UPDATE unique_tbl3 SET i = i + $1", 1); err != nil {
		t.Fatalf("extended-protocol UPDATE i=i+1 should succeed, got: %v", err)
	}

	rows := runSQL(t, c, "SELECT i FROM unique_tbl3 ORDER BY i")
	want := []string{"1", "2", "3", "4", "5"}
	if len(rows) != len(want) {
		t.Fatalf("row count after shift: got %v, want %v", rows, want)
	}
	for idx, w := range want {
		if rows[idx][0] != w {
			t.Fatalf("row %d after shift: got %q, want %q (full: %v)", idx, rows[idx][0], w, rows)
		}
	}
}

// TestPort_DeferrableUniqueStmtEndGenuineDuplicate is acceptance criterion 2:
// a genuine duplicate that survives to the end of the statement must still
// raise 23505 — at that statement, not at COMMIT and not never. The UPDATE
// moves i=0 onto the already-existing i=1, a collision that is NOT resolved
// by the end of the statement (unlike the ring-shift case), so the
// end-of-statement recheck must fail it. Runs both in autocommit (where "end
// of statement" and "end of transaction" coincide) and inside an explicit
// transaction where a later statement never gets to run — proving the error
// surfaces at the UPDATE itself, not deferred all the way to COMMIT.
func TestPort_DeferrableUniqueStmtEndGenuineDuplicate(t *testing.T) {
	c := newCluster(t, "stmtenduniqdup")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE dup_tbl (i integer UNIQUE DEFERRABLE, t text)"); err != nil {
		t.Fatalf("create dup_tbl: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO dup_tbl VALUES (0,'zero'),(1,'one')"); err != nil {
		t.Fatalf("seed dup_tbl: %v", err)
	}

	// Autocommit: the duplicate-producing UPDATE itself must fail with 23505.
	if err := runSQLSimple(t, c, "UPDATE dup_tbl SET i = 1 WHERE i = 0"); !isUniqueErr(err) {
		t.Fatalf("autocommit genuine duplicate: expected 23505, got %v", err)
	}
	// The failed autocommit UPDATE must not have applied.
	rows := runSQL(t, c, "SELECT i FROM dup_tbl ORDER BY i")
	if len(rows) != 2 || rows[0][0] != "0" || rows[1][0] != "1" {
		t.Fatalf("failed duplicate UPDATE must leave rows unchanged, got %v", rows)
	}

	// Explicit transaction: the UPDATE itself must report the error (proving
	// the check ran at end-of-statement, not deferred to COMMIT); a later
	// statement in the same block must be rejected with 25P02 (aborted block),
	// never reached — matching "the enclosing block must ROLLBACK" (PG
	// expected/constraints.out:531-540 for the immediate case).
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := ex("UPDATE dup_tbl SET i = 1 WHERE i = 0"); !isUniqueErr(err) {
		t.Fatalf("in-txn genuine duplicate: expected 23505 at the UPDATE itself, got %v", err)
	}
	// The block is now aborted; a follow-up statement must be rejected, not
	// silently deferred through to a COMMIT that then fails instead.
	if err := ex("SELECT 1"); err == nil {
		t.Fatalf("statement after an aborted block should be rejected (25P02), got no error")
	}
	_ = ex("ROLLBACK")

	rows = runSQL(t, c, "SELECT i FROM dup_tbl ORDER BY i")
	if len(rows) != 2 || rows[0][0] != "0" || rows[1][0] != "1" {
		t.Fatalf("rolled-back duplicate txn must leave rows unchanged, got %v", rows)
	}
}

// TestPort_NonDeferrableUniqueStillImmediate is acceptance criterion 3: a
// plain (NOT DEFERRABLE) unique index must be byte-identically unchanged —
// still a synchronous per-row check, so the SAME ring-shift UPDATE that now
// succeeds for a DEFERRABLE index must still fail immediately for a
// non-deferrable one, with PG's usual message shape.
func TestPort_NonDeferrableUniqueStillImmediate(t *testing.T) {
	c := newCluster(t, "stmtenduniqnondef")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE nd_tbl (i integer UNIQUE, t text)"); err != nil {
		t.Fatalf("create nd_tbl: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO nd_tbl VALUES (0,'one'),(1,'two'),(2,'tree')"); err != nil {
		t.Fatalf("seed nd_tbl: %v", err)
	}

	err := runSQLSimple(t, c, "UPDATE nd_tbl SET i = i + 1")
	if !isUniqueErr(err) {
		t.Fatalf("non-deferrable UPDATE i=i+1 should still fail synchronously with 23505, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "duplicate key value violates unique constraint") {
		t.Fatalf("non-deferrable duplicate message shape changed: %v", err)
	}

	rows := runSQL(t, c, "SELECT i FROM nd_tbl ORDER BY i")
	if len(rows) != 3 || rows[0][0] != "0" || rows[1][0] != "1" || rows[2][0] != "2" {
		t.Fatalf("failed non-deferrable UPDATE must leave rows unchanged, got %v", rows)
	}
}
