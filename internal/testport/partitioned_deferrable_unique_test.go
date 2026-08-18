package testport

import (
	"context"
	"strings"
	"time"

	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/lib/pq"
)

// TestPort_PartitionedDeferrableUniqueDefersToCommitViaSetConstraintsAll
// exercises the probe SQL from docs/design/0134-0005-constraints-sql-divergence.md
// §13 (upstream: postgres/src/test/regress/sql/constraints.sql:432-445): a
// DEFERRABLE UNIQUE constraint on a partitioned parent must propagate its
// Deferrable flag onto the per-partition clone index goopg materialises for
// each child (internal/executor/operators_ddl.go CREATE TABLE … PARTITION
// OF clone loop). Before M0134-0005f the clone silently dropped
// Deferrable/InitiallyDeferred/IsConstraint, so uniqueCheckDeferred
// (internal/executor/deferred_unique.go) saw Deferrable==false on the
// partition's own index and ran the check immediately at INSERT instead of
// queuing it to the commit-tier drain.
//
// This uses the UNNAMED `SET CONSTRAINTS ALL DEFERRED` form, not the named
// form the upstream regress file actually uses
// (`SET CONSTRAINTS parted_uniq_tbl_i_key DEFERRED`). The named form is
// deliberately NOT covered here: goopg has no `conparentid`-equivalent
// parent→child constraint linkage. `SetConstraintsNamed`
// (internal/executor/session.go:383) records the override in a flat
// map[string]bool keyed by whatever name was typed — here the PARENT's
// constraint name, `parted_uniq_tbl_i_key` — but the per-partition clone's
// own index is auto-named `parted_uniq_tbl_1_i_key`. At recheck time
// `uniqueCheckDeferToCommit` (internal/executor/deferred_unique.go:66) looks
// up the override by the CHILD's index name, misses, and silently falls back
// to `idx.InitiallyDeferred` (false), so the check runs at end-of-statement
// instead of COMMIT. PG resolves the named form by walking
// `pg_constraint.conparentid` to collect every descendant (partition-child)
// constraint OID before resolving deferred state
// (postgres/src/backend/commands/trigger.c:AlterConstraint,
// ~trigger.c:5850-5990, comment: "Scan for any possible descendants of the
// constraints"). Adding that linkage is out of this slice's scope; tracked
// as a follow-up under M0134-0005g.
func TestPort_PartitionedDeferrableUniqueDefersToCommitViaSetConstraintsAll(t *testing.T) {
	c := newCluster(t, "partdeferuniq")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE parted_uniq_tbl (i int UNIQUE DEFERRABLE) "+
		"partition by range (i)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE parted_uniq_tbl_1 PARTITION OF parted_uniq_tbl "+
		"FOR VALUES FROM (0) TO (10)"); err != nil {
		t.Fatalf("create child 1: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE parted_uniq_tbl_2 PARTITION OF parted_uniq_tbl "+
		"FOR VALUES FROM (20) TO (30)"); err != nil {
		t.Fatalf("create child 2: %v", err)
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
	if err := ex("INSERT INTO parted_uniq_tbl VALUES (1)"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := ex("SAVEPOINT f"); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	// Still INITIALLY IMMEDIATE at this point (no SET CONSTRAINTS yet), so
	// this duplicate fails right here — matches upstream's own comment
	// ("unique violation") at constraints.sql:439.
	if !isUniqueErr(ex("INSERT INTO parted_uniq_tbl VALUES (1)")) {
		t.Fatalf("second (still-immediate-mode) insert inside savepoint should raise 23505")
	}
	if err := ex("ROLLBACK TO f"); err != nil {
		t.Fatalf("rollback to savepoint: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set constraints all deferred: %v", err)
	}
	// This is the crux of the fix: the duplicate INSERT must be ACCEPTED at
	// INSERT time (the check is now deferred to the partition's own cloned
	// index) and only raise 23505 at COMMIT.
	if err := ex("INSERT INTO parted_uniq_tbl VALUES (1)"); err != nil {
		t.Fatalf("deferred duplicate insert should not fail at INSERT: %v", err)
	}
	err := ex("COMMIT")
	if !isUniqueErr(err) {
		t.Fatalf("expected 23505 at COMMIT, got %v", err)
	}
	if !strings.Contains(err.Error(), `unique constraint "parted_uniq_tbl_1_i_key"`) {
		t.Fatalf("expected error naming the partition's own constraint, got %v", err)
	}
	// DETAIL is not part of err.Error() (lib/pq only surfaces
	// Severity/Message/Code there) — assert it via *pq.Error, matching the
	// sibling pattern at deferred_unique_nnd_e2e_test.go.
	if pe, ok := err.(*pq.Error); ok {
		if !strings.Contains(pe.Detail, "Key (i)=(1) already exists") {
			t.Fatalf("expected DETAIL naming the duplicate key, got %q", pe.Detail)
		}
	} else {
		t.Fatalf("expected *pq.Error, got %T: %v", err, err)
	}
	_ = ex("ROLLBACK")

	// The failed COMMIT rolled back: no row should have landed.
	rows := runSQL(t, c, "SELECT i FROM parted_uniq_tbl WHERE i = 1")
	if len(rows) != 0 {
		t.Fatalf("duplicate row should have rolled back, got %v", rows)
	}

	if err := runSQLSimple(t, c, "DROP TABLE parted_uniq_tbl"); err != nil {
		t.Fatalf("drop parent: %v", err)
	}
}

// TestPort_PartitionedUniqueConstraintFansOutToPgConstraint proves the
// twin site: PARTITION OF and ALTER TABLE … ATTACH PARTITION must BOTH
// produce a pg_constraint row per partition, mirroring PG's DefineIndex
// recursion (postgres/src/backend/commands/indexcmds.c:DefineIndex, dispatch
// at :706 for RELKIND_PARTITIONED_TABLE). Before M0134-0005f the cloned
// per-partition index carried IsConstraint==false, so
// PGConstraintRowsForDBOid (internal/catalog/catalog.go) silently excluded
// it — only the parent's row showed up.
func TestPort_PartitionedUniqueConstraintFansOutToPgConstraint(t *testing.T) {
	c := newCluster(t, "partuniqfanout")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// -- PARTITION OF path --
	if err := runSQLSimple(t, c, "CREATE TABLE parted_uniq_tbl (i int UNIQUE DEFERRABLE) "+
		"partition by range (i)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE parted_uniq_tbl_1 PARTITION OF parted_uniq_tbl "+
		"FOR VALUES FROM (0) TO (10)"); err != nil {
		t.Fatalf("create child 1: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE parted_uniq_tbl_2 PARTITION OF parted_uniq_tbl "+
		"FOR VALUES FROM (20) TO (30)"); err != nil {
		t.Fatalf("create child 2: %v", err)
	}

	rows := runSQL(t, c, "SELECT conname, conrelid::regclass::text FROM pg_constraint "+
		"WHERE conname LIKE 'parted_uniq%' ORDER BY conname")
	wantNames := map[string]string{
		"parted_uniq_tbl_1_i_key": "parted_uniq_tbl_1",
		"parted_uniq_tbl_2_i_key": "parted_uniq_tbl_2",
		"parted_uniq_tbl_i_key":   "parted_uniq_tbl",
	}
	if len(rows) != 3 {
		t.Fatalf("PARTITION OF: expected 3 pg_constraint rows, got %d: %v", len(rows), rows)
	}
	for _, r := range rows {
		if want, ok := wantNames[r[0]]; !ok || want != r[1] {
			t.Fatalf("PARTITION OF: unexpected pg_constraint row %v (want table %v)", r, wantNames[r[0]])
		}
	}
	if err := runSQLSimple(t, c, "DROP TABLE parted_uniq_tbl"); err != nil {
		t.Fatalf("drop PARTITION OF parent: %v", err)
	}

	// -- ATTACH PARTITION path (the twin site) --
	if err := runSQLSimple(t, c, "CREATE TABLE attach_uniq_tbl (i int UNIQUE DEFERRABLE) "+
		"partition by range (i)"); err != nil {
		t.Fatalf("create attach parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE attach_uniq_tbl_1 (i int UNIQUE)"); err != nil {
		t.Fatalf("create standalone child 1: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE attach_uniq_tbl_2 (i int UNIQUE)"); err != nil {
		t.Fatalf("create standalone child 2: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE attach_uniq_tbl ATTACH PARTITION attach_uniq_tbl_1 "+
		"FOR VALUES FROM (0) TO (10)"); err != nil {
		t.Fatalf("attach child 1: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE attach_uniq_tbl ATTACH PARTITION attach_uniq_tbl_2 "+
		"FOR VALUES FROM (20) TO (30)"); err != nil {
		t.Fatalf("attach child 2: %v", err)
	}

	rows = runSQL(t, c, "SELECT conname, conrelid::regclass::text FROM pg_constraint "+
		"WHERE conname LIKE 'attach_uniq%' ORDER BY conname")
	if len(rows) != 3 {
		t.Fatalf("ATTACH PARTITION: expected 3 pg_constraint rows, got %d: %v", len(rows), rows)
	}
	wantAttach := map[string]string{
		"attach_uniq_tbl_1_i_key": "attach_uniq_tbl_1",
		"attach_uniq_tbl_2_i_key": "attach_uniq_tbl_2",
		"attach_uniq_tbl_i_key":   "attach_uniq_tbl",
	}
	for _, r := range rows {
		if want, ok := wantAttach[r[0]]; !ok || want != r[1] {
			t.Fatalf("ATTACH PARTITION: unexpected pg_constraint row %v (want table %v)", r, wantAttach[r[0]])
		}
	}

	if err := runSQLSimple(t, c, "DROP TABLE attach_uniq_tbl"); err != nil {
		t.Fatalf("drop attach parent: %v", err)
	}
}

// TestPort_PartitionedNonDeferrableUniqueErrorsAtInsert is the negative
// guard: a PLAIN (non-DEFERRABLE) UNIQUE constraint on a partitioned parent
// must still error immediately at INSERT time, not defer to COMMIT. A
// one-directional guard here would pass even if the fix accidentally forced
// EVERY partitioned unique index to Deferrable==true regardless of the
// parent's declaration.
func TestPort_PartitionedNonDeferrableUniqueErrorsAtInsert(t *testing.T) {
	c := newCluster(t, "partnondeferuniq")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE parted_nd_tbl (i int UNIQUE) "+
		"partition by range (i)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE parted_nd_tbl_1 PARTITION OF parted_nd_tbl "+
		"FOR VALUES FROM (0) TO (10)"); err != nil {
		t.Fatalf("create child: %v", err)
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
	if err := ex("INSERT INTO parted_nd_tbl VALUES (1)"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// No DEFERRABLE declared and no SET CONSTRAINTS override — the duplicate
	// must fail RIGHT HERE, at INSERT, not be silently accepted and deferred.
	if err := ex("INSERT INTO parted_nd_tbl VALUES (1)"); !isUniqueErr(err) {
		t.Fatalf("expected immediate 23505 at INSERT for non-deferrable partitioned UNIQUE, got %v", err)
	}
	_ = ex("ROLLBACK")

	if err := runSQLSimple(t, c, "DROP TABLE parted_nd_tbl"); err != nil {
		t.Fatalf("drop parent: %v", err)
	}
}
