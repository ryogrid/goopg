package executor

import (
	"errors"
	"strings"
	"testing"
)

// TestRoundNumericToIntOverflow verifies that coercing an out-of-range
// numeric literal into an int8 (bigint) column raises 22003 "bigint out
// of range" instead of silently producing a garbage int64 via an
// implementation-defined float64→int64 conversion. M0134-0069 Bug A.
// PG oracle: postgres/src/backend/utils/adt/numeric.c numericvar_to_int64.
func TestRoundNumericToIntOverflow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE m0134_0069_bigint_t (label text, n int8)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	cases := []string{
		`INSERT INTO m0134_0069_bigint_t VALUES ('too-low', -9223372036854775809)`,
		`INSERT INTO m0134_0069_bigint_t VALUES ('too-high', 9223372036854775808)`,
	}
	for _, sql := range cases {
		err := runDDL(t, ctx, sql)
		if err == nil {
			t.Errorf("expected 22003 bigint out of range for %q, got nil", sql)
			continue
		}
		var execErr *ExecError
		if !errors.As(err, &execErr) {
			t.Errorf("expected *ExecError for %q, got %T: %v", sql, err, err)
			continue
		}
		if execErr.Code != "22003" {
			t.Errorf("expected code 22003 for %q, got %s (%v)", sql, execErr.Code, err)
		}
		if !strings.Contains(execErr.Message, "bigint out of range") {
			t.Errorf("expected 'bigint out of range' message for %q, got: %v", sql, err)
		}
	}

	// The overflowing rows must not have been inserted (no garbage rows).
	rows := runSQL(t, ctx, `SELECT label, n FROM m0134_0069_bigint_t`)
	if len(rows) != 0 {
		t.Errorf("expected 0 rows after rejected overflow inserts, got %v", rows)
	}
}

// TestDropTableUnqualifiedIgnoresOtherSchemaDependents verifies that an
// unqualified DROP TABLE resolves its RESTRICT-mode dependency scan against
// the RESOLVED table (schema+name), not the bare parsed name — so a
// same-named table living in an unrelated schema, with its own dependent
// view, cannot falsely block the drop. M0134-0069 Bug B.
//
// The target table lives in a NON-public schema on the session's
// search_path (no public.t1 exists), so LookupTable's unqualified fast
// path (which treats a blank Schema as "public") misses and the
// search_path fallback loop (lockTableSearchSchemas) resolves it,
// returning tbl.Schema explicitly set to that schema — exercising the
// case where the parsed AST name.Schema stays "" but the resolved
// identity is not "public".
//
// PG oracle: postgres/src/backend/catalog/pg_depend.c findDependentObjects
// walks pg_depend by the target relation's OID, never by bare name.
func TestDropTableUnqualifiedIgnoresOtherSchemaDependents(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return "m0134_0069_a, public", true
		}
		return "", false
	}

	if err := runDDL(t, ctx, `CREATE SCHEMA m0134_0069_a`); err != nil {
		t.Fatalf("create schema a: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE SCHEMA m0134_0069_b`); err != nil {
		t.Fatalf("create schema b: %v", err)
	}
	// m0134_0069_a.t1: the target of the unqualified DROP TABLE below (first
	// hit on search_path; no public.t1 exists so the search_path fallback
	// loop resolves it explicitly). No real dependents.
	if err := runDDL(t, ctx, `CREATE TABLE m0134_0069_a.t1 (a int)`); err != nil {
		t.Fatalf("create m0134_0069_a.t1: %v", err)
	}
	// m0134_0069_b.t1: an UNRELATED same-named table in a different schema,
	// with its own dependent view. The buggy scan (raw unqualified name,
	// empty Schema) matched this view as if it depended on m0134_0069_a.t1.
	if err := runDDL(t, ctx, `CREATE TABLE m0134_0069_b.t1 (a int)`); err != nil {
		t.Fatalf("create m0134_0069_b.t1: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE VIEW m0134_0069_b.v1 AS SELECT * FROM m0134_0069_b.t1`); err != nil {
		t.Fatalf("create m0134_0069_b.v1: %v", err)
	}

	// Unqualified DROP TABLE resolves to m0134_0069_a.t1 via search_path and
	// must succeed silently — it has no real dependents.
	if err := runDDL(t, ctx, `DROP TABLE t1`); err != nil {
		t.Fatalf("DROP TABLE t1 (unqualified) should succeed, got: %v", err)
	}

	// The unrelated schema's table+view must be untouched (still queryable,
	// with zero rows since m0134_0069_b.t1 is empty).
	rows := runSQL(t, ctx, `SELECT * FROM m0134_0069_b.v1`)
	if len(rows) != 0 {
		t.Errorf("expected m0134_0069_b.v1 to still exist with 0 rows, got %v", rows)
	}
}

// TestDropTableTempShadowIgnoresPermanentViewDependents verifies the exact
// M0134-0069 sequence.sql scenario: a CREATE TEMP TABLE t1 shadows an
// earlier-created PERMANENT table also named t1 in the same catalog map
// slot (catalog.go key() drops Schema for both "public" and "pg_temp", so
// both key identically as bare "t1" — see docs comment at the RESTRICT
// scan in execDropTable). A permanent view depending on the permanent t1
// must not block DROP TABLE t1 when t1 currently resolves to the TEMP
// table, because PostgreSQL forbids a non-temp view from ever referencing
// a temp relation (so the permanent view can only be depending on the
// permanent t1, not the temp one).
func TestDropTableTempShadowIgnoresPermanentViewDependents(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t1 (num int, name text)`); err != nil {
		t.Fatalf("create permanent t1: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE VIEW m0134_0069_nontemp1 AS SELECT * FROM t1`); err != nil {
		t.Fatalf("create nontemp1: %v", err)
	}
	// Shadow the permanent t1 with a temp table of the same bare name.
	if err := runDDL(t, ctx, `CREATE TEMP TABLE t1 (f1 int)`); err != nil {
		t.Fatalf("create temp t1: %v", err)
	}

	// Unqualified DROP TABLE resolves to the TEMP t1 (shadowing) and must
	// succeed silently — the permanent view depends on the permanent t1,
	// not this temp one.
	if err := runDDL(t, ctx, `DROP TABLE t1`); err != nil {
		t.Fatalf("DROP TABLE t1 (temp shadow) should succeed, got: %v", err)
	}

	// The permanent view must still exist and be queryable (against the
	// restored permanent t1).
	rows := runSQL(t, ctx, `SELECT * FROM m0134_0069_nontemp1`)
	if len(rows) != 0 {
		t.Errorf("expected m0134_0069_nontemp1 to still exist with 0 rows, got %v", rows)
	}
}

// TestAlterSequenceRenamePropagatesToDependentDefault verifies that
// ALTER SEQUENCE ... RENAME rewrites every OTHER table's column DEFAULT
// nextval(...) literal that names the OLD sequence, so a subsequent INSERT
// succeeds against the renamed sequence instead of 42P01ing against the
// stale name. goopg's Column.DefaultExpr stores the sequence reference by
// name literal (unlike PG's OID-based pg_attrdef.adbin), so this textual
// fixup is goopg's own correctness requirement. M0134-0069 Bucket 4.
// PG oracle: postgres/src/test/regress/sql/sequence.sql:143-145 (renaming
// serial sequences).
func TestAlterSequenceRenamePropagatesToDependentDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE s1`); err != nil {
		t.Fatalf("create sequence s1: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE t (x int DEFAULT nextval('s1'))`); err != nil {
		t.Fatalf("create table t: %v", err)
	}
	// Regression guard against over-matching: an unrelated table whose
	// DEFAULT names a DIFFERENT, un-renamed sequence must be untouched.
	if err := runDDL(t, ctx, `CREATE SEQUENCE s_other`); err != nil {
		t.Fatalf("create sequence s_other: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE t_other (y int DEFAULT nextval('s_other'))`); err != nil {
		t.Fatalf("create table t_other: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER SEQUENCE s1 RENAME TO s2`); err != nil {
		t.Fatalf("alter sequence rename: %v", err)
	}

	if err := runDDL(t, ctx, `INSERT INTO t DEFAULT VALUES`); err != nil {
		t.Fatalf("INSERT INTO t DEFAULT VALUES should succeed after rename, got: %v", err)
	}
	rows := runSQL(t, ctx, `SELECT x FROM t`)
	if len(rows) != 1 || rows[0][0].Int != 1 {
		t.Errorf("expected [[1]], got %v", rows)
	}

	// t_other's DEFAULT must still reference s_other, unaffected by the s1
	// rename.
	if err := runDDL(t, ctx, `INSERT INTO t_other DEFAULT VALUES`); err != nil {
		t.Fatalf("INSERT INTO t_other DEFAULT VALUES should still succeed, got: %v", err)
	}
	otherRows := runSQL(t, ctx, `SELECT y FROM t_other`)
	if len(otherRows) != 1 || otherRows[0][0].Int != 1 {
		t.Errorf("expected t_other unaffected: [[1]], got %v", otherRows)
	}
}
