package executor

import "testing"

// TestInsertSelectExplicitColumnListOmittedDefaultEvaluatesCurrval —
// M0134-0005m, INSERT…SELECT twin of M0134-0005j's
// TestInsertExplicitColumnListOmittedDefaultColumnEvaluatesCurrval
// (default_omitted_column_test.go). planInsert's SELECT branch never ran
// the M0134-0005j column-list-extension logic, so omitted DEFAULT columns
// were simply absent from Insert.ColumnIndex; the executor then routed them
// to the currval-blind applyDefaultsForMissing mini-evaluator instead of
// the full expression evaluator. Reproduces constraints.sql's INSERT_TBL
// fixture exactly: `INSERT INTO t(y) SELECT 'Y'` must fill BOTH omitted
// columns (x via nextval, z via -1*currval) through the full evaluator, in
// column order, so z sees x's freshly-advanced sequence value (z = -x, not
// NULL). §20.3 of docs/design/0134-0005-constraints-sql-divergence.md.
func TestInsertSelectExplicitColumnListOmittedDefaultEvaluatesCurrval(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE sel_default_seq`); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE sel_default_t (x int DEFAULT nextval('sel_default_seq'), y text DEFAULT '-NULL-', z int DEFAULT -1 * currval('sel_default_seq'))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO sel_default_t(y) SELECT 'Y'`)

	rows := runSQL(t, ctx, `SELECT x, y, z FROM sel_default_t`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 {
		t.Errorf("x: got %v want 1", rows[0][0].Int)
	}
	if string(rows[0][1].Buf) != "Y" {
		t.Errorf("y: got %v want Y", string(rows[0][1].Buf))
	}
	if rows[0][2].IsNull() {
		t.Fatalf("z: got NULL want -1 — DEFAULT with currval() not evaluated on the SELECT-shape INSERT path")
	}
	if rows[0][2].Int != -1 {
		t.Errorf("z: got %v want -1 (must be -x, not NULL)", rows[0][2].Int)
	}
}

// TestInsertSelectNoColumnListNarrowerSelectEvaluatesDefaults — the brief's
// second required sub-case: `INSERT INTO t SELECT …` with NO column list and
// a SELECT narrower than the table (constraints.sql's `INSERT_TBL SELECT *
// FROM tmp` shape). Trailing target columns not covered by the SELECT's
// width must still get their DEFAULT through the full evaluator, exactly
// like the explicit-column-list omission case above.
func TestInsertSelectNoColumnListNarrowerSelectEvaluatesDefaults(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE sel_default_seq2`); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE sel_default_t2 (x int DEFAULT nextval('sel_default_seq2'), y text DEFAULT '-NULL-', z int DEFAULT -1 * currval('sel_default_seq2'))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runSQL(t, ctx, `SELECT nextval('sel_default_seq2')`) // seed currval=1 so z's DEFAULT can resolve
	// No column list, SELECT yields only 1 of 3 columns -> x=99 explicit,
	// y and z fall back to their catalog DEFAULTs (y literal, z currval()).
	runSQL(t, ctx, `INSERT INTO sel_default_t2 SELECT 99`)

	rows := runSQL(t, ctx, `SELECT x, y, z FROM sel_default_t2`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 99 {
		t.Errorf("x: got %v want 99", rows[0][0].Int)
	}
	if string(rows[0][1].Buf) != "-NULL-" {
		t.Errorf("y: got %v want -NULL-", string(rows[0][1].Buf))
	}
	if rows[0][2].IsNull() {
		t.Fatalf("z: got NULL want -1 — no-column-list narrower SELECT must still evaluate trailing DEFAULTs")
	}
	if rows[0][2].Int != -1 {
		t.Errorf("z: got %v want -1", rows[0][2].Int)
	}
}

// TestInsertSelectExplicitNullBeatsDefault — the brief's negative guard:
// present ≠ missing. An explicit NULL supplied in the SELECT for a column
// that has a DEFAULT must store NULL, not silently fall back to the
// DEFAULT expression. Proves the Project-append only touches columns
// genuinely ABSENT from the column list, not ones supplied as NULL.
func TestInsertSelectExplicitNullBeatsDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE sel_default_t3 (a int, b text DEFAULT '-NULL-')`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO sel_default_t3(a, b) SELECT 1, NULL`)

	rows := runSQL(t, ctx, `SELECT a, b FROM sel_default_t3`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 {
		t.Errorf("a: got %v want 1", rows[0][0].Int)
	}
	if !rows[0][1].IsNull() {
		t.Errorf("b: got %v want NULL — explicit NULL must beat the column's DEFAULT", rows[0][1])
	}
}

// TestInsertSelectSerialColumnNoDoubleAdvance — INSERT…SELECT twin of
// M0134-0005j's TestInsertExplicitColumnListOmittedSerialColumnNoDoubleAdvance.
// A SERIAL column's catalog.Column.DefaultExpr is nil by convention (see
// defaultMarkerReplacement's doc comment), so the new
// defaultAppendableColumns-driven Project-append must never touch it;
// autoGenerateSerialValues stays sole owner and the sequence must not
// double-advance under the SELECT source shape either.
func TestInsertSelectSerialColumnNoDoubleAdvance(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE sel_default_s1 (id serial, v text)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO sel_default_s1(v) SELECT 'a'`)
	runSQL(t, ctx, `INSERT INTO sel_default_s1(v) SELECT 'b'`)

	rows := runSQL(t, ctx, `SELECT id FROM sel_default_s1 ORDER BY id`)
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	if rows[0][0].Int != 1 || rows[1][0].Int != 2 {
		t.Fatalf("ids: got %v,%v want 1,2 (double-advance regression if 1,3 or 2,4)", rows[0][0].Int, rows[1][0].Int)
	}
	curr := runSQL(t, ctx, `SELECT currval('sel_default_s1_id_seq')`)
	if curr[0][0].Int != 2 {
		t.Errorf("currval: got %v want 2 (double-advance regression if 4)", curr[0][0].Int)
	}
}
