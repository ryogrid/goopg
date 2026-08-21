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

// TestAlterSequenceRenamePropagatesToImplicitSerialDefault is the SERIAL
// twin of TestAlterSequenceRenamePropagatesToDependentDefault above: a bare
// SERIAL column leaves catalog.Column.DefaultExpr nil, so the planner's
// defaultMarkerReplacement synthesizes nextval('<tbl>_<col>_seq') by naming
// convention instead of reading an explicit DEFAULT. After the synthesized
// sequence is renamed, an INSERT that omits the serial column must still
// advance the RENAMED sequence rather than erroring "sequence does not
// exist" or resetting to 1. M0134-0069 bucket 4.
// PG oracle: postgres/src/test/regress/sql/sequence.sql:143-146 (ALTER TABLE
// serialtest1_f2_seq RENAME TO serialtest1_f2_foo; INSERT INTO serialTest1
// VALUES ('more'); SELECT * FROM serialTest1;), expected output
// postgres/src/test/regress/expected/sequence.out:271-280 — nextval keeps
// advancing the renamed sequence, no reset.
func TestAlterSequenceRenamePropagatesToImplicitSerialDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t (id SERIAL, name text)`); err != nil {
		t.Fatalf("create table t: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO t (name) VALUES ('a')`); err != nil {
		t.Fatalf("INSERT INTO t (name) VALUES ('a'): %v", err)
	}

	if err := runDDL(t, ctx, `ALTER SEQUENCE t_id_seq RENAME TO t_id_foo`); err != nil {
		t.Fatalf("alter sequence rename: %v", err)
	}

	if err := runDDL(t, ctx, `INSERT INTO t (name) VALUES ('b')`); err != nil {
		t.Fatalf("INSERT INTO t (name) VALUES ('b') should succeed after rename, got: %v", err)
	}

	rows := runSQL(t, ctx, `SELECT id, name FROM t ORDER BY id`)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %v", rows)
	}
	if rows[0][0].Int != 1 || string(rows[0][1].Buf) != "a" {
		t.Errorf("expected row 0 = [1 a], got %v", rows[0])
	}
	if rows[1][0].Int != 2 || string(rows[1][1].Buf) != "b" {
		t.Errorf("expected row 1 = [2 b] (sequence must keep advancing after rename, not reset to 1), got %v", rows[1])
	}
}

// TestDropSequenceRestrictBlocksImplicitSerialDefault verifies that DROP
// SEQUENCE (RESTRICT, the default) fails with 2BP01 + DETAIL + HINT when an
// implicit SERIAL column's owned sequence is targeted. M0134-0069 bucket 3.
// PG oracle: postgres/src/test/regress/sql/sequence.sql:151-160,
// postgres/src/test/regress/expected/sequence.out:293-296.
func TestDropSequenceRestrictBlocksImplicitSerialDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t1 (f1 serial)`); err != nil {
		t.Fatalf("create table t1: %v", err)
	}

	err := runDDL(t, ctx, `DROP SEQUENCE t1_f1_seq`)
	if err == nil {
		t.Fatalf("expected DROP SEQUENCE t1_f1_seq to fail with 2BP01, got nil error")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	wantMsg := "cannot drop sequence t1_f1_seq because other objects depend on it"
	wantDetail := "default value for column f1 of table t1 depends on sequence t1_f1_seq"
	wantHint := "Use DROP ... CASCADE to drop the dependent objects too."
	if ee.Code != "2BP01" || ee.Message != wantMsg || ee.Detail != wantDetail || ee.Hint != wantHint {
		t.Errorf("err = %+v, want Code=2BP01 Message=%q Detail=%q Hint=%q", ee, wantMsg, wantDetail, wantHint)
	}
}

// TestDropSequenceRestrictBlocksExplicitNextvalDefault verifies that DROP
// SEQUENCE (RESTRICT) fails with 2BP01 when an explicit nextval('seqname')
// DEFAULT (bare string literal, no cast) references the sequence.
// M0134-0069 bucket 3.
// PG oracle: postgres/src/test/regress/sql/sequence.sql:151-161,
// postgres/src/test/regress/expected/sequence.out:297-300.
func TestDropSequenceRestrictBlocksExplicitNextvalDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE myseq2`); err != nil {
		t.Fatalf("create sequence myseq2: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE t1 (f2 int DEFAULT nextval('myseq2'))`); err != nil {
		t.Fatalf("create table t1: %v", err)
	}

	err := runDDL(t, ctx, `DROP SEQUENCE myseq2`)
	if err == nil {
		t.Fatalf("expected DROP SEQUENCE myseq2 to fail with 2BP01, got nil error")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	wantMsg := "cannot drop sequence myseq2 because other objects depend on it"
	wantDetail := "default value for column f2 of table t1 depends on sequence myseq2"
	wantHint := "Use DROP ... CASCADE to drop the dependent objects too."
	if ee.Code != "2BP01" || ee.Message != wantMsg || ee.Detail != wantDetail || ee.Hint != wantHint {
		t.Errorf("err = %+v, want Code=2BP01 Message=%q Detail=%q Hint=%q", ee, wantMsg, wantDetail, wantHint)
	}
}

// TestDropSequenceRestrictAllowsTextCastNextvalDefault is the regression
// guard for the ::text-cast exemption: PG's dependency-recording
// (parse_utilcmd.c) only fires for a bare literal or an explicit ::regclass
// cast, never for ::text (or any other cast), so DROP SEQUENCE on a
// sequence referenced only via nextval('seqname'::text) must succeed with
// no error. M0134-0069 bucket 3.
// PG oracle: postgres/src/test/regress/sql/sequence.sql:151-155,161-162,
// postgres/src/test/regress/expected/sequence.out:301-302 ("This however
// will work: DROP SEQUENCE myseq3;").
func TestDropSequenceRestrictAllowsTextCastNextvalDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE myseq3`); err != nil {
		t.Fatalf("create sequence myseq3: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE t1 (f3 int DEFAULT nextval('myseq3'::text))`); err != nil {
		t.Fatalf("create table t1: %v", err)
	}

	if err := runDDL(t, ctx, `DROP SEQUENCE myseq3`); err != nil {
		t.Fatalf("DROP SEQUENCE myseq3 should succeed (::text cast is exempt from dependency tracking), got: %v", err)
	}
}
