package testport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_AddConstraintUniqueSkipsAbortedXminPhantom is the ADD CONSTRAINT
// half of the M0134-0005c reproducer: a failed-then-rolled-back UPDATE
// leaves an aborted-xmin phantom tuple on the page (the new version the
// aborted UPDATE would have inserted). Before this fix, collectBTreeEntries'
// naive "Xmin != Invalid => alive" test counted that phantom as live,
// colliding with the later genuinely-live row at the same key and raising a
// spurious 23505 on `ADD CONSTRAINT ... UNIQUE`. PG oracle:
// heapam_visibility.c:1205 HeapTupleSatisfiesVacuumHorizon (aborted xmin =>
// HEAPTUPLE_DEAD), consumed by heapam_handler.c:1415
// heapam_index_build_range_scan.
func TestPort_AddConstraintUniqueSkipsAbortedXminPhantom(t *testing.T) {
	c := newCluster(t, "ddlscanaddcon")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE ut4 (i int UNIQUE DEFERRABLE, t text)"); err != nil {
		t.Fatalf("create ut4: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO ut4 VALUES (0,'one'),(1,'two'),(2,'tree'),(3,'four'),(4,'five')"); err != nil {
		t.Fatalf("seed ut4: %v", err)
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
	// This UPDATE fails immediately (real duplicate: i=1 already exists at
	// this point) — it must error, and the leftover debris is exactly the
	// phantom this test targets.
	if err := ex("UPDATE ut4 SET i = 1 WHERE i = 0"); err == nil {
		t.Fatal("UPDATE i=1 WHERE i=0 should fail (real duplicate) before rollback")
	}
	if err := ex("ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if err := runSQLSimple(t, c, "UPDATE ut4 SET i = i + 1"); err != nil {
		t.Fatalf("ring-shift UPDATE should succeed: %v", err)
	}

	rows := runSQL(t, c, "SELECT i FROM ut4 ORDER BY i")
	want := []string{"1", "2", "3", "4", "5"}
	if len(rows) != len(want) {
		t.Fatalf("row count after shift: got %v, want %v", rows, want)
	}
	for idx, w := range want {
		if rows[idx][0] != w {
			t.Fatalf("row %d after shift: got %q, want %q (full: %v)", idx, rows[idx][0], w, rows)
		}
	}

	if err := runSQLSimple(t, c, "ALTER TABLE ut4 DROP CONSTRAINT ut4_i_key"); err != nil {
		t.Fatalf("drop constraint: %v", err)
	}
	// This must succeed: the only tuple with key i=1 on disk is the one live
	// row from the ring-shift; the aborted UPDATE's phantom i=1 tuple must be
	// skipped by the bulk-build scan.
	if err := runSQLSimple(t, c, "ALTER TABLE ut4 ADD CONSTRAINT ut4_i_key UNIQUE (i) DEFERRABLE INITIALLY DEFERRED"); err != nil {
		t.Fatalf("ADD CONSTRAINT ... UNIQUE ... DEFERRABLE INITIALLY DEFERRED should succeed (aborted-xmin phantom must not count as live), got: %v", err)
	}
}

// TestPort_CreateUniqueIndexSkipsAbortedXminPhantom is the CREATE UNIQUE
// INDEX spelling of TestPort_AddConstraintUniqueSkipsAbortedXminPhantom.
// Both DDL forms route through bulkBuildBTreeFull -> collectBTreeEntries
// (the same function — confirmed by the m0134-0005c-index-build-liveness
// research report), so this test measures rather than assumes the sibling
// equivalence per Rule #2.
func TestPort_CreateUniqueIndexSkipsAbortedXminPhantom(t *testing.T) {
	c := newCluster(t, "ddlscancreidx")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE ut5 (i int, t text)"); err != nil {
		t.Fatalf("create ut5: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO ut5 VALUES (0,'one'),(1,'two'),(2,'tree'),(3,'four'),(4,'five')"); err != nil {
		t.Fatalf("seed ut5: %v", err)
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
	// No unique constraint exists yet, so this UPDATE succeeds — but it
	// still leaves the aborted-xact debris we care about once rolled back.
	if err := ex("UPDATE ut5 SET i = 1 WHERE i = 0"); err != nil {
		t.Fatalf("in-txn UPDATE i=1 WHERE i=0: %v", err)
	}
	if err := ex("ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if err := runSQLSimple(t, c, "UPDATE ut5 SET i = i + 1"); err != nil {
		t.Fatalf("ring-shift UPDATE should succeed: %v", err)
	}

	rows := runSQL(t, c, "SELECT i FROM ut5 ORDER BY i")
	want := []string{"1", "2", "3", "4", "5"}
	if len(rows) != len(want) {
		t.Fatalf("row count after shift: got %v, want %v", rows, want)
	}
	for idx, w := range want {
		if rows[idx][0] != w {
			t.Fatalf("row %d after shift: got %q, want %q (full: %v)", idx, rows[idx][0], w, rows)
		}
	}

	// Must succeed: the aborted UPDATE's phantom i=1 tuple must not count
	// as a live duplicate for the CREATE UNIQUE INDEX build scan either.
	if err := runSQLSimple(t, c, "CREATE UNIQUE INDEX ut5_i_key ON ut5 (i)"); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX should succeed (aborted-xmin phantom must not count as live), got: %v", err)
	}
}

// TestPort_AddConstraintUniqueCatchesLiveRowWithAbortedXmax is the inverse
// direction: a rolled-back DELETE leaves the deleted tuple's xmax pointing
// at an aborted transaction, and that tuple is genuinely still live (PG:
// aborted xmax => HEAPTUPLE_LIVE, heapam_visibility.c). Before this fix,
// collectBTreeEntries' naive "Xmax != Invalid => dead" test wrongly dropped
// it, silently hiding a real duplicate and letting ADD CONSTRAINT ... UNIQUE
// succeed when it must ERROR 23505.
func TestPort_AddConstraintUniqueCatchesLiveRowWithAbortedXmax(t *testing.T) {
	c := newCluster(t, "ddlscanabortxmax")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE ut6 (i int, t text)"); err != nil {
		t.Fatalf("create ut6: %v", err)
	}
	// Two rows already share i=1 — a genuine duplicate with no constraint
	// present yet.
	if err := runSQLSimple(t, c, "INSERT INTO ut6 VALUES (1,'a'),(1,'b')"); err != nil {
		t.Fatalf("seed ut6: %v", err)
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
	if err := ex("DELETE FROM ut6 WHERE t = 'b'"); err != nil {
		t.Fatalf("in-txn DELETE: %v", err)
	}
	if err := ex("ROLLBACK"); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Both (1,'a') and (1,'b') must still be visible — the DELETE rolled
	// back.
	rows := runSQL(t, c, "SELECT i, t FROM ut6 ORDER BY t")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows after rolled-back DELETE, got %v", rows)
	}

	// Must ERROR: the (1,'b') row is still genuinely live (its xmax belongs
	// to an aborted transaction) and duplicates (1,'a') on column i.
	err := runSQLSimple(t, c, "ALTER TABLE ut6 ADD CONSTRAINT ut6_i_key UNIQUE (i)")
	if err == nil {
		t.Fatal("ADD CONSTRAINT ... UNIQUE (i) should ERROR 23505 (aborted-xmax row must not be hidden), got success")
	}
	if !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected a duplicate-key error, got: %v", err)
	}
}
