package testport

// TestPort_CreateTableInherits* are the guards for M0134-0005z:
// `CREATE TABLE child (...) INHERITS (parent)` never copied the parent's
// CHECK constraints onto the child, so they went unenforced on child INSERT
// (internal/executor/operators_ddl.go execCreateTable, the s.Inherits loop —
// no walk over parent.NamedChecks). Also covers the sibling naming bug: a
// column-level CHECK is auto-named by PostgreSQL's *distinct-referenced-
// column* rule, not the syntactic column it's attached to
// (postgres/src/backend/catalog/heap.c:2546-2582, ChooseConstraintName).
// Fixture drawn from postgres/src/test/regress/sql/constraints.sql:172-183
// (INSERT_TBL/INSERT_CHILD) and :199-211 (ATACC1/ATACC2 NO INHERIT).

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_CreateTableInheritsChecksEnforcedOnChild is the headline repro
// from constraints.sql:172-183. Pre-fix, goopg silently accepted the INSERT
// (INSERT 0 1) because c1.CheckConstraints/NamedChecks never got p1_con
// copied in; enforcement itself (operators_fk.go checkConstraints) was
// already data-driven and needed no change.
func TestPort_CreateTableInheritsChecksEnforcedOnChild(t *testing.T) {
	c := newCluster(t, "inherits-check-enforced")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c,
		"CREATE TABLE p1 (x int, CONSTRAINT p1_con CHECK (x >= 3 AND x < 8))"); err != nil {
		t.Fatalf("CREATE TABLE p1: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE TABLE c1 (cy int CHECK (cy > x)) INHERITS (p1)"); err != nil {
		t.Fatalf("CREATE TABLE c1: %v", err)
	}

	err := runSQLSimple(t, c, "INSERT INTO c1 (x, cy) VALUES (1, 5)")
	if err == nil {
		t.Fatal("INSERT INTO c1 (x=1, cy=5) succeeded; want ERROR 23514 " +
			"(x=1 violates inherited CHECK p1_con: x >= 3 AND x < 8) — the " +
			"parent's CHECK constraint was never copied onto the child")
	}
	if !strings.Contains(err.Error(), "p1_con") || !strings.Contains(err.Error(), "check constraint") {
		t.Fatalf("INSERT INTO c1 rejected, but not with the expected inherited "+
			"check-constraint naming p1_con: %v", err)
	}

	// A row that satisfies BOTH the inherited parent CHECK and the child's
	// own column-level CHECK must still succeed.
	if err := runSQLSimple(t, c, "INSERT INTO c1 (x, cy) VALUES (5, 6)"); err != nil {
		t.Fatalf("INSERT INTO c1 (x=5, cy=6) [satisfies both CHECKs]: %v", err)
	}
}

// TestPort_CreateTableInheritsNoInheritCheckNotPropagated proves the
// `CHECK (...) NO INHERIT` exemption: a NO INHERIT parent CHECK must NOT be
// copied to an INHERITS child, so a child row that would violate the
// parent's constraint (were it inherited) must still succeed.
func TestPort_CreateTableInheritsNoInheritCheckNotPropagated(t *testing.T) {
	c := newCluster(t, "inherits-check-noinherit")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c,
		"CREATE TABLE noinh_p (a int, CONSTRAINT noinh_con CHECK (a > 0) NO INHERIT)"); err != nil {
		t.Fatalf("CREATE TABLE noinh_p: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE noinh_c () INHERITS (noinh_p)"); err != nil {
		t.Fatalf("CREATE TABLE noinh_c: %v", err)
	}

	// a = -1 would violate noinh_con if it were inherited; since NO INHERIT
	// exempts it, the INSERT must succeed on the child.
	if err := runSQLSimple(t, c, "INSERT INTO noinh_c (a) VALUES (-1)"); err != nil {
		t.Fatalf("INSERT INTO noinh_c (a=-1) [NO INHERIT check must not propagate]: %v", err)
	}
	// The constraint must still be enforced directly on the parent.
	if err := runSQLSimple(t, c, "INSERT INTO noinh_p (a) VALUES (-1)"); err == nil {
		t.Fatal("INSERT INTO noinh_p (a=-1) succeeded; want ERROR 23514 " +
			"(NO INHERIT does not disable enforcement on the defining table itself)")
	}
}

// TestPort_CreateTableInheritsChecksDedupedAcrossParents proves that when a
// child inherits from two parents that both carry a CHECK constraint of the
// SAME name (PG's MergeAttributes union-by-name), the child ends up with
// exactly ONE copy — not two, which would otherwise show up as a duplicate
// entry in the constraint footer / double enforcement bookkeeping.
func TestPort_CreateTableInheritsChecksDedupedAcrossParents(t *testing.T) {
	c := newCluster(t, "inherits-check-dedup")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c,
		"CREATE TABLE dedup_p1 (a int, CONSTRAINT dedup_con CHECK (a > 0))"); err != nil {
		t.Fatalf("CREATE TABLE dedup_p1: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE TABLE dedup_p2 (a int, CONSTRAINT dedup_con CHECK (a > 0))"); err != nil {
		t.Fatalf("CREATE TABLE dedup_p2: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE TABLE dedup_c () INHERITS (dedup_p1, dedup_p2)"); err != nil {
		t.Fatalf("CREATE TABLE dedup_c: %v", err)
	}

	rows := runSQL(t, c,
		"SELECT count(*) FROM pg_constraint WHERE conrelid = 'dedup_c'::regclass AND conname = 'dedup_con'")
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "1" {
		t.Fatalf("pg_constraint rows for dedup_c/dedup_con = %v, want exactly 1 "+
			"(two INHERITS parents carrying the same constraint name must merge into one child constraint)", rows)
	}

	// Enforcement still fires exactly once (not double-checked, but that
	// wouldn't be visible from the error alone — the count above is the
	// load-bearing assertion).
	if err := runSQLSimple(t, c, "INSERT INTO dedup_c (a) VALUES (-1)"); err == nil {
		t.Fatal("INSERT INTO dedup_c (a=-1) succeeded; want ERROR 23514 (dedup_con)")
	}
}

// TestPort_CreateTableColumnCheckSingleColumnAutoName is the single-column
// half of the PG distinct-referenced-column auto-naming rule: a column-level
// CHECK whose expression references exactly the column it's attached to
// (and no other) is named "<table>_<col>_check". goopg already got this case
// right (both the syntactic-column shortcut and autoCheckName agree here) —
// this test documents that the fix did not regress it.
func TestPort_CreateTableColumnCheckSingleColumnAutoName(t *testing.T) {
	c := newCluster(t, "colcheck-single-col-name")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE cc_single (v int CHECK (v > 0))"); err != nil {
		t.Fatalf("CREATE TABLE cc_single: %v", err)
	}

	rows := runSQL(t, c,
		"SELECT conname FROM pg_constraint WHERE conrelid = 'cc_single'::regclass AND contype = 'c'")
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "cc_single_v_check" {
		t.Fatalf("pg_constraint.conname for cc_single's column CHECK = %v, want [[cc_single_v_check]]", rows)
	}
}

// TestPort_CreateTableColumnCheckMultiColumnAutoName is the multi-column half:
// a column-level CHECK syntactically attached to one column, but whose
// expression references a DIFFERENT (or additional) column, must be named
// "<table>_check" — NOT "<table>_<attached-col>_check". Pre-fix, goopg named
// it after the syntactic column unconditionally (operators_ddl.go:3846-3850),
// matching neither PG's rule nor autoCheckName (the algorithm goopg already
// used correctly for anonymous table-level CHECKs).
func TestPort_CreateTableColumnCheckMultiColumnAutoName(t *testing.T) {
	c := newCluster(t, "colcheck-multi-col-name")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c,
		"CREATE TABLE cc_multi (a int, b int CHECK (a < b))"); err != nil {
		t.Fatalf("CREATE TABLE cc_multi: %v", err)
	}

	rows := runSQL(t, c,
		"SELECT conname FROM pg_constraint WHERE conrelid = 'cc_multi'::regclass AND contype = 'c'")
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "cc_multi_check" {
		t.Fatalf("pg_constraint.conname for cc_multi's column CHECK (a < b) = %v, "+
			"want [[cc_multi_check]] (2 distinct columns referenced -> table-shaped name, "+
			"not the syntactic-column shortcut cc_multi_b_check)", rows)
	}
}
