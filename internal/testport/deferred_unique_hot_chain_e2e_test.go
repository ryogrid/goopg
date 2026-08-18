package testport

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_DeferredUniqueCommitCatchesHOTUpdatedDuplicate is M0134-0005e's
// primary guard. It reproduces PG's own regression case
// (postgres/src/test/regress/sql/constraints.sql:510-517, "test a HOT update
// that invalidates the conflicting tuple"): a DEFERRABLE INITIALLY DEFERRED
// UNIQUE index, a transaction that inserts a genuine duplicate, and a
// subsequent UPDATE of a NON-key column that goopg (correctly) applies via the
// HOT path. tryApplyHOTUpdate does no index maintenance, so the b-tree's only
// entry for the candidate key still points at the pre-HOT-update slot; a
// per-pointer recheck (the pre-fix behaviour) sees that slot's stamped xmax,
// judges it dead, and misses the live HOT successor one t_ctid hop away —
// live count 1 instead of 2, so the duplicate silently commits.
//
// PG 18.3: COMMIT raises 23505 naming t_i_key; the transaction rolls back and
// only the original row remains. docs/design/0134-0005-constraints-sql-divergence.md
// §12.1/§12.2.
func TestPort_DeferredUniqueCommitCatchesHOTUpdatedDuplicate(t *testing.T) {
	c := newCluster(t, "deferreduniqhotdup")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE t (i int, t text)"); err != nil {
		t.Fatalf("create t: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO t VALUES (3, 'three')"); err != nil {
		t.Fatalf("seed t: %v", err)
	}
	if err := runSQLSimple(t, c,
		"ALTER TABLE t ADD CONSTRAINT t_i_key UNIQUE (i) DEFERRABLE INITIALLY DEFERRED"); err != nil {
		t.Fatalf("add deferred unique: %v", err)
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
	if err := ex("INSERT INTO t VALUES (3, 'Three')"); err != nil {
		t.Fatalf("insert duplicate (deferred, must queue not raise): %v", err)
	}
	// Non-key column ⇒ HOT path. The candidate's only b-tree entry now points
	// at a dead slot (xmax = own XID) while the live successor is one t_ctid
	// hop away.
	if err := ex("UPDATE t SET t = 'THREE' WHERE i = 3 AND t = 'Three'"); err != nil {
		t.Fatalf("HOT update of non-key column: %v", err)
	}
	err := ex("COMMIT")
	if !isUniqueErr(err) {
		t.Fatalf("COMMIT over a HOT-updated duplicate should raise 23505 naming t_i_key, got: %v", err)
	}
	if err == nil {
		t.Fatalf("expected an error, got none")
	}

	rows := runSQL(t, c, "SELECT i, t FROM t ORDER BY i")
	if len(rows) != 1 || rows[0][0] != "3" || rows[0][1] != "three" {
		t.Fatalf("rolled-back duplicate txn must leave only the original row, got %v", rows)
	}
}

// TestPort_DeferredUniqueCommitAllowsHOTResolvedDuplicate is the companion
// negative guard: a HOT update that RESOLVES the transient duplicate (by
// changing the colliding row's key to a free value before COMMIT) must still
// let the transaction commit. This protects against a chain-follow bug in the
// other direction — over-counting the tail as an extra live row, or counting a
// stale intermediate hop as well as the tail — which criterion 1 alone would
// not catch. b4-s1-stmt-end-unique / M0134-0005e acceptance criterion 2.
func TestPort_DeferredUniqueCommitAllowsHOTResolvedDuplicate(t *testing.T) {
	c := newCluster(t, "deferreduniqhotresolved")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE t2 (i int, t text)"); err != nil {
		t.Fatalf("create t2: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO t2 VALUES (3, 'three')"); err != nil {
		t.Fatalf("seed t2: %v", err)
	}
	if err := runSQLSimple(t, c,
		"ALTER TABLE t2 ADD CONSTRAINT t2_i_key UNIQUE (i) DEFERRABLE INITIALLY DEFERRED"); err != nil {
		t.Fatalf("add deferred unique: %v", err)
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
	if err := ex("INSERT INTO t2 VALUES (3, 'Three')"); err != nil {
		t.Fatalf("insert duplicate (deferred, must queue not raise): %v", err)
	}
	// Non-key HOT update of the duplicate row — must NOT be mistaken for a
	// surviving conflict once the key itself is changed below.
	if err := ex("UPDATE t2 SET t = 'THREE' WHERE i = 3 AND t = 'Three'"); err != nil {
		t.Fatalf("HOT update of non-key column: %v", err)
	}
	// Now resolve the collision by moving the duplicate off the shared key.
	// This UPDATE touches the key column, so it is NOT HOT-eligible and takes
	// the normal update+index-maintenance path.
	if err := ex("UPDATE t2 SET i = 4 WHERE i = 3 AND t = 'THREE'"); err != nil {
		t.Fatalf("key-changing UPDATE to resolve duplicate: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("COMMIT should succeed once the duplicate is resolved, got: %v", err)
	}

	rows := runSQL(t, c, "SELECT i, t FROM t2 ORDER BY i")
	if len(rows) != 2 || rows[0][0] != "3" || rows[0][1] != "three" || rows[1][0] != "4" || rows[1][1] != "THREE" {
		t.Fatalf("resolved-duplicate txn should commit both rows, got %v", rows)
	}
}
