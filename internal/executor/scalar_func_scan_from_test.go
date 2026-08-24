package executor

import "testing"

// Tests for M0134-0015c: a non-SETOF (scalar) user routine used as a
// FROM-clause source. Full investigation: tmp/ralph-handoffs/M0134-0015a/
// report.md §"Bucket C detail". Before this change these all raised
// `0A000: table-valued function "..." not supported`
// (internal/optimizer/planner.go planTableFuncRangeVar, terminal rejection).

// TestScalarFuncScanFrom_SubqueryForm pins AC1: a scalar user routine used
// as a FROM item inside a subquery joins normally against a base table.
func TestScalarFuncScanFrom_SubqueryForm(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION f_immutable_int4(x int) RETURNS int IMMUTABLE AS 'SELECT $1' LANGUAGE sql`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE zz_tenk1 (unique1 int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO zz_tenk1 VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT unique1 FROM zz_tenk1, (SELECT * FROM f_immutable_int4(1) x) x WHERE x = unique1`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (rows=%v)", len(rows), rows)
	}
	if rows[0][0].Int != 1 {
		t.Errorf("got unique1=%v, want 1", rows[0][0])
	}
}

// TestScalarFuncScanFrom_DirectAliased pins AC2: a scalar routine used
// directly as a FROM item with an explicit alias yields one row, one
// column named per the alias.
func TestScalarFuncScanFrom_DirectAliased(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION f_immutable_int4(x int) RETURNS int IMMUTABLE AS 'SELECT $1' LANGUAGE sql`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT * FROM f_immutable_int4(1) x`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (rows=%v)", len(rows), rows)
	}
	if len(rows[0]) != 1 {
		t.Fatalf("got %d columns, want 1 (row=%v)", len(rows[0]), rows[0])
	}
	if rows[0][0].Int != 1 {
		t.Errorf("got value=%v, want 1", rows[0][0])
	}
	// Column named per the alias: referencing it by name must resolve.
	rows2 := runQuery(t, ctx, `SELECT x FROM f_immutable_int4(1) x`)
	if len(rows2) != 1 || rows2[0][0].Int != 1 {
		t.Errorf("column not named after alias `x`: rows=%v", rows2)
	}
}

// TestScalarFuncScanFrom_CompositeExpands pins AC3: a routine returning a
// composite/table type expands to multiple columns (the composite's own
// attribute count/order), not a single crammed composite-text datum. This
// is the test that pins the operators_scalar_func_scan.go sibling change —
// without the decomposeCompositeText branch ported into
// scalarFuncScanOp.Next(), this plans two columns but executes with a
// single composite-text datum in column 0 and NullDatum in column 1
// (verified during development: reverting the executor change collapses
// this to a build-clean but wrong-output test failure, not a compile
// error).
//
// Referencing the composite's attribute names directly (`q1`/`q2`) hits a
// separate PRE-EXISTING bug shared with the already-shipped SETOF
// composite-return path (confirmed via
// `SETOF zz_i8_tbl` reproduction — column-ref resolution against a
// composite-return table-function binding raises 42703 even though `SELECT
// *` sees the same columns); out of scope here per the brief's "do not
// chase" guidance — reported as a deferral candidate instead.
func TestScalarFuncScanFrom_CompositeExpands(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE zz_i8_tbl (q1 int8, q2 int8)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION zz_mki8(bigint, bigint) RETURNS zz_i8_tbl AS $$SELECT ROW($1, $2)::zz_i8_tbl$$ LANGUAGE sql`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT * FROM zz_mki8(1, 2)`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (rows=%v)", len(rows), rows)
	}
	if len(rows[0]) != 2 {
		t.Fatalf("got %d columns, want 2 (composite must expand — row=%v)", len(rows[0]), rows[0])
	}
	if rows[0][0].StringValue() != "1" || rows[0][1].StringValue() != "2" {
		t.Errorf("got col0=%v col1=%v, want \"1\", \"2\"", rows[0][0], rows[0][1])
	}
}

// TestScalarFuncScanFrom_NullResultOneRow pins AC4: PG's
// execSRF.c:ExecMakeTableFunctionResult no_function_result: block — a
// non-SETOF function in FROM always yields exactly one row, even when the
// function itself returns NULL (never zero rows).
func TestScalarFuncScanFrom_NullResultOneRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION zz_null_int4() RETURNS int AS 'SELECT NULL::int' LANGUAGE sql`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT * FROM zz_null_int4()`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (NULL result must still yield one row; rows=%v)", len(rows), rows)
	}
	if !rows[0][0].IsNull() {
		t.Errorf("got %v, want a NULL datum", rows[0][0])
	}
}

// TestScalarFuncScanFrom_NoAliasUsesFuncName pins AC5: with no alias, the
// column is named after the function itself.
func TestScalarFuncScanFrom_NoAliasUsesFuncName(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION f_immutable_int4(x int) RETURNS int IMMUTABLE AS 'SELECT $1' LANGUAGE sql`); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT f_immutable_int4 FROM f_immutable_int4(1)`)
	if len(rows) != 1 || rows[0][0].Int != 1 {
		t.Errorf("unaliased column must be named after the function: rows=%v", rows)
	}
}

// TestScalarFuncScanFrom_NoRegression pins AC6: the pre-existing SETOF path
// and the builtin planScalarFuncScan caller (parse_ident) are unaffected by
// the userRoutineColumnSchema refactor.
func TestScalarFuncScanFrom_NoRegression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION zz_srf_ints() RETURNS SETOF int LANGUAGE sql AS $$ SELECT generate_series(1,3) $$`); err != nil {
		t.Fatalf("CREATE FUNCTION zz_srf_ints: %v", err)
	}
	rows := runQuery(t, ctx, `SELECT * FROM zz_srf_ints() ORDER BY 1`)
	if len(rows) != 3 {
		t.Fatalf("SETOF regression: got %d rows, want 3 (rows=%v)", len(rows), rows)
	}
	for i, r := range rows {
		if r[0].Int != int64(i+1) {
			t.Errorf("SETOF regression: row %d = %v, want %d", i, r, i+1)
		}
	}

	piRows := runQuery(t, ctx, `SELECT * FROM parse_ident('foo.bar')`)
	if len(piRows) != 1 {
		t.Fatalf("parse_ident regression: got %d rows, want 1 (rows=%v)", len(piRows), piRows)
	}
}

// TestScalarFuncScanFrom_LateralArgResolvesAgainstLeftFromItem pins the
// M0134-0126 fix end-to-end: a user-defined FROM-clause function argument
// that correlates against an earlier FROM item — `FROM t, LATERAL f(t.col)`
// — must both PLAN (arg resolves against the left sibling's schema, not an
// empty context) and EXECUTE (the arg evaluates per outer row via
// BindLateralOuter, not against a nil slot). Before the fix this raised
// "column ... does not exist" at plan time; a naive fix that only patched
// planning would still panic/misbehave at runtime with a nil-slot column
// lookup. Covers both the SETOF (UserSrfScan) and non-SETOF (ScalarFuncScan)
// plan-node shapes, reproduced originally via gin.sql's
// `lateral explain_query_json(... || query)`.
func TestScalarFuncScanFrom_LateralArgResolvesAgainstLeftFromItem(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE zz_lat_probe (query text)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO zz_lat_probe VALUES ('a'), ('b')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Non-SETOF (ScalarFuncScan).
	if err := runDDL(t, ctx, `CREATE FUNCTION zz_lat_scalar(x text) RETURNS text LANGUAGE sql AS $$ SELECT x $$`); err != nil {
		t.Fatalf("CREATE FUNCTION zz_lat_scalar: %v", err)
	}
	scalarRows := runQuery(t, ctx, `SELECT js FROM zz_lat_probe, LATERAL zz_lat_scalar(zz_lat_probe.query) js ORDER BY 1`)
	if len(scalarRows) != 2 || scalarRows[0][0].StringValue() != "a" || scalarRows[1][0].StringValue() != "b" {
		t.Fatalf("scalar lateral arg: got %v, want [[a] [b]]", scalarRows)
	}

	// SETOF (UserSrfScan), implicit LATERAL correlation (no LATERAL keyword).
	if err := runDDL(t, ctx, `CREATE FUNCTION zz_lat_setof(x text) RETURNS SETOF text LANGUAGE sql AS $$ SELECT x || '1' UNION ALL SELECT x || '2' $$`); err != nil {
		t.Fatalf("CREATE FUNCTION zz_lat_setof: %v", err)
	}
	setofRows := runQuery(t, ctx, `SELECT js FROM zz_lat_probe, zz_lat_setof(zz_lat_probe.query) js ORDER BY 1`)
	want := []string{"a1", "a2", "b1", "b2"}
	if len(setofRows) != len(want) {
		t.Fatalf("setof lateral arg: got %d rows %v, want %d", len(setofRows), setofRows, len(want))
	}
	for i, w := range want {
		if setofRows[i][0].StringValue() != w {
			t.Errorf("setof lateral arg: row %d = %q, want %q", i, setofRows[i][0].StringValue(), w)
		}
	}
}
