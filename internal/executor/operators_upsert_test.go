package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// upsertSeed installs the items fixture rows AND a unique index on
// id, then re-creates the index so backfill picks up the seeded
// tuples. CREATE INDEX is the only path that maintains an index in
// v0; we therefore seed-then-index to guarantee the arbiter btree
// holds entries for the existing rows. Returns the resolved table
// + the resolved unique index.
func upsertSeed(t *testing.T, ctx *Context) (*catalog.Table, *catalog.Index) {
	t.Helper()
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX items_pkey ON items (id)"); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "items_pkey"})
	if !ok {
		t.Fatal("items_pkey not in catalog")
	}
	return tbl, idx
}

// runUpsertSQL plans + executes an INSERT…ON CONFLICT statement
// end-to-end through the parser → analyzer → planner → executor
// pipeline so this test exercises the same path production goes
// through.
func runUpsertSQL(t *testing.T, ctx *Context, sql string) (rowsAffected int64) {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	node, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(node)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Next: %v", err)
	}
	if rc, ok := op.(*upsertOp); ok {
		rowsAffected = rc.RowsAffected()
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return rowsAffected
}

// scanItems returns the visible rows of the items table as an
// id→label map for cheap content assertions.
func scanItems(t *testing.T, ctx *Context, tbl *catalog.Table) map[int64]string {
	t.Helper()
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatalf("scan.Open: %v", err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatalf("drainScan: %v", err)
	}
	out := map[int64]string{}
	for _, r := range rows {
		out[r[0].Int] = r[1].StringValue()
	}
	return out
}

// TestUpsertNoConflictInsertsRow — inserting a row with a key not
// already in the heap takes the no-conflict path: the new row
// lands and the arbiter index gets a new entry so subsequent
// UPSERTs see it.
func TestUpsertNoConflictInsertsRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := upsertSeed(t, ctx) // seeds id=1,2,3

	rc := runUpsertSQL(t, ctx, "INSERT INTO items (id, label) VALUES (4, 'delta') ON CONFLICT (id) DO UPDATE SET label = excluded.label")
	if rc != 1 {
		t.Errorf("RowsAffected = %d, want 1", rc)
	}
	got := scanItems(t, ctx, tbl)
	want := map[int64]string{1: "alpha", 2: "beta", 3: "gamma", 4: "delta"}
	if len(got) != len(want) {
		t.Fatalf("rows=%v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%d]=%q want %q", k, got[k], v)
		}
	}
}

// TestUpsertConflictDoUpdate — the headline case. Inserting a row
// with an existing key triggers DO UPDATE: the existing row's
// xmax is stamped, a new row is written with `excluded.label`
// applied. The row count stays at 3 (stamping kills the old
// version, the new version replaces it).
func TestUpsertConflictDoUpdate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := upsertSeed(t, ctx)

	rc := runUpsertSQL(t, ctx, "INSERT INTO items (id, label) VALUES (2, 'beta-replaced') ON CONFLICT (id) DO UPDATE SET label = excluded.label")
	if rc != 1 {
		t.Errorf("RowsAffected = %d, want 1", rc)
	}
	got := scanItems(t, ctx, tbl)
	want := map[int64]string{1: "alpha", 2: "beta-replaced", 3: "gamma"}
	if len(got) != len(want) {
		t.Fatalf("rows=%v, want %v", got, want)
	}
	if got[2] != "beta-replaced" {
		t.Errorf("conflict row not updated: got[2]=%q", got[2])
	}
}

// TestUpsertConflictDoNothing — the conflicting row is left
// completely unchanged. RowsAffected stays 0 (DO NOTHING does
// NOT count as a "row affected" per upstream).
func TestUpsertConflictDoNothing(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := upsertSeed(t, ctx)

	rc := runUpsertSQL(t, ctx, "INSERT INTO items (id, label) VALUES (2, 'never-applied') ON CONFLICT (id) DO NOTHING")
	if rc != 0 {
		t.Errorf("RowsAffected = %d, want 0 (DO NOTHING)", rc)
	}
	got := scanItems(t, ctx, tbl)
	if got[2] != "beta" {
		t.Errorf("DO NOTHING modified the existing row: got[2]=%q want beta", got[2])
	}
}

func TestPlanUpsertDoNothingNoTargetUsesExpressionUniqueIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (key text, data text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE OR REPLACE FUNCTION idx_key(text) RETURNS text IMMUTABLE LANGUAGE plpgsql AS $$ BEGIN RETURN $1; END $$"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX items_key_expr_idx ON items ((idx_key(key)))"); err != nil {
		t.Fatal(err)
	}
	stmts, err := parser.Parse("INSERT INTO items VALUES ('k1', 'v1') ON CONFLICT DO NOTHING")
	if err != nil {
		t.Fatal(err)
	}
	node, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := node.(*planner.Insert)
	if !ok {
		t.Fatalf("node = %T, want *planner.Insert", node)
	}
	if ins.OnConflict == nil || ins.OnConflict.ArbiterIndex == nil {
		t.Fatalf("OnConflict arbiter missing: %+v", ins.OnConflict)
	}
	if ins.OnConflict.ArbiterIndex.Name != "items_key_expr_idx" {
		t.Fatalf("ArbiterIndex = %q, want items_key_expr_idx", ins.OnConflict.ArbiterIndex.Name)
	}
	if len(ins.OnConflict.ArbiterColumns) != 1 || ins.OnConflict.ArbiterColumns[0] != -1 {
		t.Fatalf("ArbiterColumns = %v, want [-1]", ins.OnConflict.ArbiterColumns)
	}
	if len(ins.OnConflict.ArbiterExprs) != 1 || ins.OnConflict.ArbiterExprs[0] == nil {
		t.Fatalf("ArbiterExprs = %+v, want one resolved expression", ins.OnConflict.ArbiterExprs)
	}
}

// TestUpsertDoUpdateMixingExistingAndExcluded — the SET expression
// references both the target's column (bare `label`) and
// `excluded.label`. Pins the planner-side merged 2N row layout:
// existing tuple at indices 0..N-1, inserted tuple at N..2N-1.
// The string-concat expression confirms both bindings round-trip
// through the runtime evaluator.
func TestUpsertDoUpdateMixingExistingAndExcluded(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := upsertSeed(t, ctx)

	// SET label = label || '/' || excluded.label
	rc := runUpsertSQL(t, ctx, "INSERT INTO items (id, label) VALUES (2, 'NEW') ON CONFLICT (id) DO UPDATE SET label = label || '/' || excluded.label")
	if rc != 1 {
		t.Errorf("RowsAffected = %d, want 1", rc)
	}
	got := scanItems(t, ctx, tbl)
	if got[2] != "beta/NEW" {
		t.Errorf("got[2]=%q, want beta/NEW", got[2])
	}
}

// TestUpsertDoUpdateWithWhereSkipsRow — UpdateWhere predicate
// filters which conflicting rows actually update. When the
// predicate is false the row is left alone (no rowsAffected
// bump, mirrors upstream's silent-skip rule).
func TestUpsertDoUpdateWithWhereSkipsRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := upsertSeed(t, ctx)

	// WHERE existing label = 'alpha' — only id=1 would match,
	// but we're upserting id=2, so the predicate evaluates false.
	rc := runUpsertSQL(t, ctx,
		"INSERT INTO items (id, label) VALUES (2, 'replaced') "+
			"ON CONFLICT (id) DO UPDATE SET label = excluded.label WHERE items.label = 'alpha'")
	if rc != 0 {
		t.Errorf("RowsAffected = %d, want 0 (predicate false)", rc)
	}
	got := scanItems(t, ctx, tbl)
	if got[2] != "beta" {
		t.Errorf("WHERE-skipped row got modified: got[2]=%q want beta", got[2])
	}
}

// TestUpsertConflictByConstraintName — Stage B end-to-end. The
// arbiter is resolved through `ON CONFLICT ON CONSTRAINT
// items_pkey` instead of column inference; behaviour matches the
// column-inference path (existing row updated, RowsAffected=1).
func TestUpsertConflictByConstraintName(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := upsertSeed(t, ctx)

	rc := runUpsertSQL(t, ctx,
		"INSERT INTO items (id, label) VALUES (2, 'beta-replaced') "+
			"ON CONFLICT ON CONSTRAINT items_pkey DO UPDATE SET label = excluded.label")
	if rc != 1 {
		t.Errorf("RowsAffected = %d, want 1", rc)
	}
	got := scanItems(t, ctx, tbl)
	if got[2] != "beta-replaced" {
		t.Errorf("constraint-name conflict not applied: got[2]=%q want beta-replaced", got[2])
	}
}

// TestUpsertConflictKeyModificationRejected — Stage A scope guard:
// a SET expression that targets the conflict-key column is
// rejected at upsertOp.Open with 0A000. Without this, the
// arbiter index entry would point at a tuple whose actual key
// differs, breaking future probes on the original key.
func TestUpsertConflictKeyModificationRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	upsertSeed(t, ctx)

	stmts, err := parser.Parse("INSERT INTO items (id, label) VALUES (2, 'x') ON CONFLICT (id) DO UPDATE SET id = excluded.id + 100")
	if err != nil {
		t.Fatal(err)
	}
	node, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	op, err := Build(node)
	if err != nil {
		t.Fatal(err)
	}
	err = op.Open(ctx)
	_ = op.Close()
	if err == nil {
		t.Fatal("expected error rejecting conflict-key modification, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type = %T, want *ExecError", err)
	}
	if ee.Code != "0A000" {
		t.Errorf("err code = %q, want 0A000", ee.Code)
	}
}
