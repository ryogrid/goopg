package testport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/lib/pq"
)

// isExclusionErr reports whether err is an exclusion_violation (23P01).
func isExclusionErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "exclusion constraint")
}

// TestPort_InitiallyDeferredExclusionCommit exercises an EXCLUDE (c WITH =)
// constraint declared DEFERRABLE INITIALLY DEFERRED over the simple-query COMMIT
// path. As with deferred UNIQUE/FK, a transient conflict is tolerated
// mid-transaction and only the final committed state is enforced. The cases (and
// goldens) are captured from PostgreSQL 18.3 (./postgres/local_install):
//
//   - a transient conflict resolved before COMMIT succeeds (under an immediate
//     EXCLUDE the second INSERT would raise 23P01);
//   - a conflict surviving to COMMIT raises 23P01 at COMMIT with DETAIL
//     "Key (c)=(5) conflicts with existing key (c)=(5).";
//   - a plain (NOT DEFERRABLE) EXCLUDE still raises immediately at the conflicting
//     INSERT — deferral must not weaken the default.
//
// Design 0119-0004-deferred-exclusion.
func TestPort_InitiallyDeferredExclusionCommit(t *testing.T) {
	c := newCluster(t, "initdeferredexcl")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c,
		"CREATE TABLE t1 (c int, EXCLUDE (c WITH =) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create t1: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}

	// Transient conflict resolved before COMMIT → success (PG: 1 row).
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin transient: %v", err)
	}
	if err := ex("INSERT INTO t1 VALUES (1)"); err != nil {
		t.Fatalf("first insert should not fail: %v", err)
	}
	if err := ex("INSERT INTO t1 VALUES (1)"); err != nil {
		t.Fatalf("transient conflicting INSERT should not fail (deferred): %v", err)
	}
	if err := ex("DELETE FROM t1 WHERE ctid = (SELECT min(ctid) FROM t1)"); err != nil {
		t.Fatalf("delete resolving the conflict: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("transient conflict commit: unexpected error %v", err)
	}
	rows := runSQL(t, c, "SELECT count(*) FROM t1")
	if len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("transient final state wrong: %v", rows)
	}

	// Conflict surviving to COMMIT → 23P01 at COMMIT, then rollback (PG: 0 rows).
	if err := ex("TRUNCATE t1"); err != nil {
		t.Fatalf("truncate t1: %v", err)
	}
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin dup: %v", err)
	}
	if err := ex("INSERT INTO t1 VALUES (5)"); err != nil {
		t.Fatalf("first deferred insert should not fail: %v", err)
	}
	if err := ex("INSERT INTO t1 VALUES (5)"); err != nil {
		t.Fatalf("second deferred insert should not fail at INSERT: %v", err)
	}
	err := ex("COMMIT")
	if !isExclusionErr(err) {
		t.Fatalf("conflict at COMMIT: expected 23P01 exclusion violation, got %v", err)
	}
	if pe, ok := err.(*pq.Error); ok {
		if !strings.Contains(pe.Detail, "(c)=(5) conflicts with existing key (c)=(5)") {
			t.Fatalf("exclusion DETAIL mismatch: %q", pe.Detail)
		}
	}
	// The aborted transaction left no rows.
	rows = runSQL(t, c, "SELECT count(*) FROM t1")
	if len(rows) != 1 || rows[0][0] != "0" {
		t.Fatalf("after-rollback final state wrong: %v", rows)
	}

	// A plain (NOT DEFERRABLE) EXCLUDE must still raise immediately — deferral
	// machinery must not weaken the default. (PG raises 23P01 at the 2nd INSERT.)
	if err := runSQLSimple(t, c,
		"CREATE TABLE t2 (c int, EXCLUDE (c WITH =))"); err != nil {
		t.Fatalf("create t2: %v", err)
	}
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	if err := ex("INSERT INTO t2 VALUES (1)"); err != nil {
		t.Fatalf("t2 first insert: %v", err)
	}
	if e := ex("INSERT INTO t2 VALUES (1)"); !isExclusionErr(e) {
		t.Fatalf("NOT DEFERRABLE EXCLUDE: expected immediate 23P01 at 2nd INSERT, got %v", e)
	}
	_ = ex("ROLLBACK")
}

// TestPort_SetConstraintsExclusionDeferral exercises runtime deferral control of
// an EXCLUDE constraint declared DEFERRABLE INITIALLY IMMEDIATE. Captured from
// PostgreSQL 18.3:
//
//   - SET CONSTRAINTS ALL DEFERRED makes the (immediate-by-default) EXCLUDE defer,
//     so a transient conflict survives both INSERTs;
//   - SET CONSTRAINTS ALL IMMEDIATE then runs the queued check at once, raising
//     23P01 right there (not at COMMIT).
//
// Design 0119-0004-deferred-exclusion.
func TestPort_SetConstraintsExclusionDeferral(t *testing.T) {
	c := newCluster(t, "scexcl")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c,
		"CREATE TABLE t3 (c int, EXCLUDE (c WITH =) DEFERRABLE INITIALLY IMMEDIATE)"); err != nil {
		t.Fatalf("create t3: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set constraints deferred: %v", err)
	}
	if err := ex("INSERT INTO t3 VALUES (7)"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Under SET CONSTRAINTS ALL DEFERRED the conflicting INSERT is tolerated even
	// though the constraint is INITIALLY IMMEDIATE.
	if err := ex("INSERT INTO t3 VALUES (7)"); err != nil {
		t.Fatalf("deferred conflicting insert should not fail: %v", err)
	}
	// SET CONSTRAINTS ALL IMMEDIATE runs the now-immediate check right away → 23P01.
	if err := ex("SET CONSTRAINTS ALL IMMEDIATE"); !isExclusionErr(err) {
		t.Fatalf("SET CONSTRAINTS ALL IMMEDIATE: expected 23P01, got %v", err)
	}
	_ = ex("ROLLBACK")
}
