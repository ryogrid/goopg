package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// runSQLCtxErr runs sql through the full parser → analyzer → planner →
// executor pipeline against an existing ctx (so it shares state — the
// catalog, and crucially ctx.CurrSeqVals — with prior statements in the
// same test), returning the error instead of failing the test. Sibling of
// runSQL (time_text_cast_test.go), which fatals on error.
func runSQLCtxErr(t *testing.T, ctx *Context, sql string) ([]Row, error) {
	t.Helper()
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)
	stmts, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): %d stmts", sql, len(stmts))
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		return nil, err
	}
	op, err := Build(plan)
	if err != nil {
		return nil, err
	}
	return Run(op, ctx)
}

// TestInsertExplicitColumnListOmittedDefaultColumnEvaluatesCurrval —
// M0134-0005j, plain-INSERT twin (operators_storage.go:2440). An explicit
// column list that omits a DEFAULT-bearing column must get that DEFAULT
// evaluated through the normal planner/executor path, not the crippled
// applyDefaultsForMissing mini-evaluator (which has no `currval` case and
// silently returned NULL). Mirrors constraints.sql's INSERT_TBL fixture:
// `z INT DEFAULT -1 * currval('s')`, `INSERT INTO t(y) VALUES ('Y')` must
// store the real, non-NULL, computed integer.
// §17 of docs/design/0134-0005-constraints-sql-divergence.md.
func TestInsertExplicitColumnListOmittedDefaultColumnEvaluatesCurrval(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE default_omit_seq`); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	runSQL(t, ctx, `SELECT nextval('default_omit_seq')`) // seed currval=1
	if err := runDDL(t, ctx, `CREATE TABLE default_omit_t4 (a int, b int DEFAULT -1 * currval('default_omit_seq'))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO default_omit_t4(a) VALUES (99)`)

	rows := runSQL(t, ctx, `SELECT a, b FROM default_omit_t4`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 99 {
		t.Errorf("a: got %v want 99", rows[0][0].Int)
	}
	if rows[0][1].IsNull() {
		t.Fatalf("b: got NULL want -1 — DEFAULT with currval() not evaluated")
	}
	if rows[0][1].Int != -1 {
		t.Errorf("b: got %v want -1", rows[0][1].Int)
	}
}

// TestUpsertExplicitColumnListOmittedDefaultColumnEvaluatesCurrval —
// M0134-0005j, ON CONFLICT twin (operators_upsert.go:213). Same shape as
// the plain-INSERT sibling above but through `INSERT ... ON CONFLICT`, the
// documented sibling gap (root-0020 comment, operators_upsert.go:199-205).
// A Rule-#2 twin pair — both call sites share the fix by construction (the
// plan-time rewrite), but each needs its own proof.
func TestUpsertExplicitColumnListOmittedDefaultColumnEvaluatesCurrval(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE upsert_omit_seq`); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	runSQL(t, ctx, `SELECT nextval('upsert_omit_seq')`) // seed currval=1
	if err := runDDL(t, ctx, `CREATE TABLE upsert_omit_t (id int, a int, b int DEFAULT -1 * currval('upsert_omit_seq'))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE UNIQUE INDEX upsert_omit_t_pkey ON upsert_omit_t (id)`); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO upsert_omit_t(id, a) VALUES (1, 99) ON CONFLICT (id) DO UPDATE SET a = EXCLUDED.a`)

	rows := runSQL(t, ctx, `SELECT id, a, b FROM upsert_omit_t`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 || rows[0][1].Int != 99 {
		t.Errorf("id,a: got %v,%v want 1,99", rows[0][0].Int, rows[0][1].Int)
	}
	if rows[0][2].IsNull() {
		t.Fatalf("b: got NULL want -1 — DEFAULT with currval() not evaluated on the ON CONFLICT insert path")
	}
	if rows[0][2].Int != -1 {
		t.Errorf("b: got %v want -1", rows[0][2].Int)
	}
}

// TestInsertExplicitColumnListOmittedDefaultNextvalUpdatesCurrvalSessionState
// — the secondary bug from the same mechanism (§17.2): the mini-evaluator's
// inline nextval handling called Sequence.nextVal() directly, bypassing
// evalNextval and never writing ctx.CurrSeqVals, so a DEFAULT-triggered
// nextval() left currval() one behind (the regress diff's "eight | 7" vs
// PG's "eight | 8"). Routing the DEFAULT through the ordinary planned
// evalExpr/evalNextval path fixes this for free — assert it directly.
func TestInsertExplicitColumnListOmittedDefaultNextvalUpdatesCurrvalSessionState(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE default_omit_seq2`); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE default_omit_t5 (a int, x int DEFAULT nextval('default_omit_seq2'))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	// x omitted from the column list -> DEFAULT nextval() fires.
	runSQL(t, ctx, `INSERT INTO default_omit_t5(a) VALUES (1)`)

	rows := runSQL(t, ctx, `SELECT currval('default_omit_seq2')`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 {
		t.Errorf("currval: got %v want 1 — DEFAULT-time nextval() must update session currval state", rows[0][0].Int)
	}
}

// TestInsertExplicitColumnListOmittedSerialColumnNoDoubleAdvance —
// M0134-0005j primary regression hazard (brief "The primary regression
// hazard"). A SERIAL column's DefaultExpr IS nextval(...), and
// autoGenerateSerialValues also fills such columns at run time.
// Substituting the DEFAULT at plan time must NOT double-advance the
// sequence. catalog.Column.DefaultExpr is left nil for serial/identity
// columns by convention (defaultMarkerReplacement's doc comment), so the
// planner's new append-omitted-DEFAULT-column logic — keyed strictly on
// DefaultExpr != nil — never touches them; autoGenerateSerialValues stays
// sole owner. Reproduces the brief's exact probe:
// `CREATE TABLE s1(id serial, v text); INSERT INTO s1(v) VALUES('a'),('b')`
// must yield ids 1,2 (not 1,3 or 2,4) and currval must be 2 (not 4).
func TestInsertExplicitColumnListOmittedSerialColumnNoDoubleAdvance(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE default_omit_s1 (id serial, v text)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO default_omit_s1(v) VALUES ('a'), ('b')`)

	rows := runSQL(t, ctx, `SELECT id FROM default_omit_s1 ORDER BY id`)
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	if rows[0][0].Int != 1 || rows[1][0].Int != 2 {
		t.Fatalf("ids: got %v,%v want 1,2 (double-advance regression if 1,3 or 2,4)", rows[0][0].Int, rows[1][0].Int)
	}
	curr := runSQL(t, ctx, `SELECT currval('default_omit_s1_id_seq')`)
	if curr[0][0].Int != 2 {
		t.Errorf("currval: got %v want 2 (double-advance regression if 4)", curr[0][0].Int)
	}
}

// TestInsertExplicitColumnListOmittedColumnNoDefaultStaysNullOrViolatesNotNull
// is the brief's negative guard: a column omitted from an explicit column
// list that has NO DEFAULT must stay NULL when nullable, and must still
// trip the NOT NULL constraint when declared NOT NULL — proving the fix's
// DefaultExpr != nil gate doesn't accidentally widen coverage to
// no-default columns.
func TestInsertExplicitColumnListOmittedColumnNoDefaultStaysNullOrViolatesNotNull(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE default_omit_t6 (a int, bare text)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO default_omit_t6(a) VALUES (1)`)
	rows := runSQL(t, ctx, `SELECT bare FROM default_omit_t6`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if !rows[0][0].IsNull() {
		t.Errorf("bare: got %v want NULL — no-DEFAULT omitted column must stay NULL", rows[0][0])
	}

	if err := runDDL(t, ctx, `CREATE TABLE default_omit_t7 (a int, notnullcol text NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := runSQLCtxErr(t, ctx, `INSERT INTO default_omit_t7(a) VALUES (1)`); err == nil {
		t.Fatal("expected NOT NULL violation for omitted no-DEFAULT NOT NULL column")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "23502" {
		t.Fatalf("err=%v want ExecError 23502 (not_null_violation)", err)
	}
}
