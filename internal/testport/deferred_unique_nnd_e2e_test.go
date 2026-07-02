package testport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/lib/pq"
)

// TestPort_InitiallyDeferredNNDUniqueCommit exercises a UNIQUE NULLS NOT
// DISTINCT constraint declared DEFERRABLE INITIALLY DEFERRED over the
// simple-query COMMIT path. NULL-keyed rows are not stored in the btree, so the
// deferred recheck is a heap scan over the candidate's NULL pattern. The cases
// (and goldens) are captured from PostgreSQL 18.3 (./postgres/local_install):
//
//   - a transient NULL duplicate resolved before COMMIT succeeds (under an
//     immediate NND constraint the second INSERT would raise 23505);
//   - a NULL duplicate that survives to COMMIT raises 23505 at COMMIT with
//     "Key (a)=(null) already exists.";
//   - distinct NULL patterns (null,1) vs (null,2) on a two-column NND index do
//     NOT collide.
//
// Design 0119-0004-deferred-unique-nnd.
func TestPort_InitiallyDeferredNNDUniqueCommit(t *testing.T) {
	c := newCluster(t, "initdeferrednnduniq")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE t1 (a integer, b integer, "+
		"UNIQUE NULLS NOT DISTINCT (a) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
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

	// Transient NULL duplicate resolved before COMMIT → success (PG: 1 row).
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin transient: %v", err)
	}
	if err := ex("INSERT INTO t1 VALUES (NULL, 1)"); err != nil {
		t.Fatalf("first NULL insert should not fail: %v", err)
	}
	if err := ex("INSERT INTO t1 VALUES (NULL, 2)"); err != nil {
		t.Fatalf("transient NULL duplicate INSERT should not fail (deferred): %v", err)
	}
	if err := ex("DELETE FROM t1 WHERE b = 2"); err != nil {
		t.Fatalf("delete resolving the duplicate: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("transient NULL duplicate commit: unexpected error %v", err)
	}
	rows := runSQL(t, c, "SELECT count(*) FROM t1")
	if len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("transient NULL final state wrong: %v", rows)
	}

	// NULL duplicate surviving to COMMIT → 23505 at COMMIT, then rollback.
	if err := ex("TRUNCATE t1"); err != nil {
		t.Fatalf("truncate t1: %v", err)
	}
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin dup: %v", err)
	}
	if err := ex("INSERT INTO t1 VALUES (NULL, 1)"); err != nil {
		t.Fatalf("first deferred NULL insert should not fail: %v", err)
	}
	if err := ex("INSERT INTO t1 VALUES (NULL, 2)"); err != nil {
		t.Fatalf("second deferred NULL insert should not fail at INSERT: %v", err)
	}
	err := ex("COMMIT")
	if !isUniqueErr(err) {
		t.Fatalf("NULL duplicate at COMMIT: expected 23505, got %v", err)
	}
	// PG renders the DETAIL as `Key (a)=(null) already exists.` — verify the
	// NULL is printed literally (the DETAIL field is not part of err.Error()).
	if pe, ok := err.(*pq.Error); ok {
		if !strings.Contains(strings.ToLower(pe.Detail), "(null) already exists") {
			t.Fatalf("NULL duplicate DETAIL mismatch: %q", pe.Detail)
		}
	}
	_ = ex("ROLLBACK")
	rows = runSQL(t, c, "SELECT count(*) FROM t1")
	if len(rows) != 1 || rows[0][0] != "0" {
		t.Fatalf("NULL duplicate should have rolled back, got %v", rows)
	}
}

// TestPort_DeferredNNDMultiColumn exercises a two-column UNIQUE NULLS NOT
// DISTINCT (a,b) DEFERRABLE INITIALLY DEFERRED constraint: a transient (1,null)
// duplicate resolved by a key-changing UPDATE before COMMIT succeeds, while two
// distinct NULL patterns coexist. Goldens from PG 18.3. Design
// 0119-0004-deferred-unique-nnd.
func TestPort_DeferredNNDMultiColumn(t *testing.T) {
	c := newCluster(t, "deferrednndmulti")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE t2 (a integer, b integer, c integer, "+
		"UNIQUE NULLS NOT DISTINCT (a, b) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create t2: %v", err)
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

	// Transient (1,null) duplicate resolved by a key-changing UPDATE → success.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin multi transient: %v", err)
	}
	if err := ex("INSERT INTO t2 VALUES (1, NULL, 10)"); err != nil {
		t.Fatalf("multi first insert: %v", err)
	}
	if err := ex("INSERT INTO t2 VALUES (1, NULL, 20)"); err != nil {
		t.Fatalf("multi transient (1,null) duplicate should not fail at INSERT: %v", err)
	}
	if err := ex("UPDATE t2 SET a = 2 WHERE c = 20"); err != nil {
		t.Fatalf("multi resolving UPDATE: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("multi transient commit: unexpected error %v", err)
	}
	rows := runSQL(t, c, "SELECT count(*) FROM t2")
	if len(rows) != 1 || rows[0][0] != "2" {
		t.Fatalf("multi transient final state wrong: %v", rows)
	}

	// Distinct NULL patterns (null,1) and (null,2) do NOT collide.
	if err := ex("TRUNCATE t2"); err != nil {
		t.Fatalf("truncate t2: %v", err)
	}
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin distinct: %v", err)
	}
	if err := ex("INSERT INTO t2 VALUES (NULL, 1, 1)"); err != nil {
		t.Fatalf("distinct first insert: %v", err)
	}
	if err := ex("INSERT INTO t2 VALUES (NULL, 2, 2)"); err != nil {
		t.Fatalf("distinct second insert: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("distinct NULL patterns commit: unexpected error %v", err)
	}
	rows = runSQL(t, c, "SELECT count(*) FROM t2")
	if len(rows) != 1 || rows[0][0] != "2" {
		t.Fatalf("distinct NULL patterns should both persist, got %v", rows)
	}
}

// TestPort_SetConstraintsNNDDeferral exercises SET CONSTRAINTS ALL DEFERRED on a
// UNIQUE NULLS NOT DISTINCT constraint declared DEFERRABLE INITIALLY IMMEDIATE: a
// NULL duplicate that fails immediately under the IMMEDIATE default is deferred
// to COMMIT once SET CONSTRAINTS ALL DEFERRED is in effect. Golden from PG 18.3.
// Design 0119-0004-deferred-unique-nnd.
func TestPort_SetConstraintsNNDDeferral(t *testing.T) {
	c := newCluster(t, "setconstraintsnnd")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE t3 (a integer, b integer, "+
		"UNIQUE NULLS NOT DISTINCT (a) DEFERRABLE INITIALLY IMMEDIATE)"); err != nil {
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

	// Control: INITIALLY IMMEDIATE NND — the second NULL row fails at the INSERT.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin control: %v", err)
	}
	if err := ex("INSERT INTO t3 VALUES (NULL, 1)"); err != nil {
		t.Fatalf("control first insert: %v", err)
	}
	if err := ex("INSERT INTO t3 VALUES (NULL, 2)"); !isUniqueErr(err) {
		t.Fatalf("control: expected immediate 23505 on NULL duplicate, got %v", err)
	}
	_ = ex("ROLLBACK")

	// SET CONSTRAINTS ALL DEFERRED defers the NND check: both NULL INSERTs succeed
	// and the violation only surfaces at COMMIT.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin deferred: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set constraints all deferred: %v", err)
	}
	if err := ex("INSERT INTO t3 VALUES (NULL, 1)"); err != nil {
		t.Fatalf("deferred first NULL insert: %v", err)
	}
	if err := ex("INSERT INTO t3 VALUES (NULL, 2)"); err != nil {
		t.Fatalf("deferred NULL duplicate insert should not fail at INSERT: %v", err)
	}
	if err := ex("COMMIT"); !isUniqueErr(err) {
		t.Fatalf("deferred NULL duplicate: expected 23505 at COMMIT, got %v", err)
	}
	_ = ex("ROLLBACK")
	rows := runSQL(t, c, "SELECT count(*) FROM t3")
	if len(rows) != 1 || rows[0][0] != "0" {
		t.Fatalf("deferred NND duplicate should have rolled back, got %v", rows)
	}
}
