package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// nndNewTable creates a fresh table with two nullable int4 columns (a, b) in the
// fixture catalog plus a UNIQUE btree index over `idxCols`. When nnd is true the
// index is marked NULLS NOT DISTINCT. Design 0119-0004.
func nndNewTable(t *testing.T, ctx *Context, cat catalog.Catalog, name string, idxCols []string, nnd bool) *catalog.Table {
	t.Helper()
	tbl, err := cat.(*catalog.InMemory).CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatalf("CreateTable %s: %v", name, err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: name + "_key"}, tbl, idxCols, true /*unique*/, "btree", false /*primary*/)
	if err != nil {
		t.Fatalf("CreateIndex %s: %v", name, err)
	}
	idx.NullsNotDistinct = nnd
	if _, err := btree.Create(ctx.Pool, cat.IndexRelFileNode(idx)); err != nil {
		t.Fatalf("btree.Create %s: %v", name, err)
	}
	return tbl
}

// nndInsert runs a single-row INSERT through the executor (which invokes the
// uniqueness check) and returns its error (nil on success).
func nndInsert(t *testing.T, ctx *Context, tbl *catalog.Table, vals ...planner.Expr) error {
	t.Helper()
	op, err := Build(&planner.Insert{
		Table:       tbl,
		Source:      &planner.Values{Rows: [][]planner.Expr{vals}},
		ColumnIndex: []int{0, 1},
	})
	if err != nil {
		return err
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	_, err = op.Next()
	_ = op.Close()
	if err == EOF {
		return nil
	}
	return err
}

func nndAssert23505(t *testing.T, err error, wantConstraint, wantDetailSub string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected 23505 duplicate-key error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23505" {
		t.Fatalf("Code=%q want 23505 (err=%v)", ee.Code, err)
	}
	if !strings.Contains(ee.Message, "duplicate key value violates unique constraint") {
		t.Errorf("Message=%q missing upstream prefix", ee.Message)
	}
	if !strings.Contains(ee.Message, wantConstraint) {
		t.Errorf("Message=%q does not name constraint %q", ee.Message, wantConstraint)
	}
	if wantDetailSub != "" && !strings.Contains(ee.Detail, wantDetailSub) {
		t.Errorf("Detail=%q missing %q", ee.Detail, wantDetailSub)
	}
}

// TestNullsNotDistinctSingleColEnforced: a UNIQUE NULLS NOT DISTINCT index over
// one nullable column rejects a second NULL-keyed row (PG 18.3 treats the NULLs
// as equal), while still rejecting duplicate non-NULL keys.
func TestNullsNotDistinctSingleColEnforced(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nnd1", []string{"a"}, true)

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("first NULL insert should succeed: %v", err)
	}
	// Second NULL key collides under NULLS NOT DISTINCT.
	nndAssert23505(t, nndInsert(t, ctx, tbl, null(), i(2)), "nnd1_key", "null")

	// Non-NULL keys keep the ordinary btree behaviour.
	if err := nndInsert(t, ctx, tbl, i(5), i(3)); err != nil {
		t.Fatalf("non-NULL insert should succeed: %v", err)
	}
	nndAssert23505(t, nndInsert(t, ctx, tbl, i(5), i(4)), "nnd1_key", "5")
}

// TestNullsDistinctControlAllowsDuplicateNulls: the default (NULLS DISTINCT)
// index must continue to admit multiple NULL-keyed rows — proving the new branch
// is strictly gated on idx.NullsNotDistinct.
func TestNullsDistinctControlAllowsDuplicateNulls(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nnd2", []string{"a"}, false)

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("first NULL insert: %v", err)
	}
	if err := nndInsert(t, ctx, tbl, null(), i(2)); err != nil {
		t.Fatalf("second NULL insert under NULLS DISTINCT should succeed: %v", err)
	}
}

// TestNullsNotDistinctMultiColEnforced: a two-column NND index treats two rows
// equal iff their NULL pattern AND non-NULL values both match.
func TestNullsNotDistinctMultiColEnforced(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nnd3", []string{"a", "b"}, true)

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(5)); err != nil {
		t.Fatalf("(NULL,5): %v", err)
	}
	// Same NULL pattern + equal non-NULL tail → conflict.
	nndAssert23505(t, nndInsert(t, ctx, tbl, null(), i(5)), "nnd3_key", "null")
	// Different non-NULL tail → no conflict.
	if err := nndInsert(t, ctx, tbl, null(), i(6)); err != nil {
		t.Fatalf("(NULL,6) should not conflict with (NULL,5): %v", err)
	}
	// NULL in the other position is a distinct pattern from (NULL,5)/(NULL,6).
	if err := nndInsert(t, ctx, tbl, i(5), null()); err != nil {
		t.Fatalf("(5,NULL): %v", err)
	}
	nndAssert23505(t, nndInsert(t, ctx, tbl, i(5), null()), "nnd3_key", "null")
	if err := nndInsert(t, ctx, tbl, i(6), null()); err != nil {
		t.Fatalf("(6,NULL) should not conflict with (5,NULL): %v", err)
	}
}

// TestNullsNotDistinctUpdateNoSelfConflict: a no-key-change NULL→NULL UPDATE must
// not self-conflict (the old version is stamped dead before the check), yet the
// NND constraint stays enforced for a subsequent duplicate NULL INSERT.
func TestNullsNotDistinctUpdateNoSelfConflict(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nnd4", []string{"a"}, true)

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("seed (NULL,1): %v", err)
	}

	// UPDATE nnd4 SET b = 2 WHERE b = 1  (a stays NULL — no key change).
	upd := &planner.Update{
		Table: tbl,
		Child: &planner.Filter{
			Child: &planner.SeqScan{Table: tbl},
			Predicate: &planner.BinaryOp{
				Op:    parser.OpEq,
				Left:  &planner.ColumnRef{Index: 1, Name: "b", Type: catalog.Type{Name: "int4"}},
				Right: &planner.IntegerConst{Value: 1},
			},
		},
		Set: []planner.Expr{nil, &planner.IntegerConst{Value: 2}},
	}
	op, err := Build(upd)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("no-key-change NULL→NULL UPDATE should succeed, got: %v", err)
	}
	if uo := op.(*updateOp); uo.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1", uo.RowsAffected())
	}
	_ = op.Close()

	// Constraint still enforced: a fresh duplicate NULL INSERT must fail.
	nndAssert23505(t, nndInsert(t, ctx, tbl, null(), i(9)), "nnd4_key", "null")
}

// nndUpsert runs a single-row INSERT … ON CONFLICT (idx) <action> through the
// executor. action selects DO NOTHING vs DO UPDATE SET b = setB. It returns the
// number of rows affected by the upsert op.
func nndUpsert(t *testing.T, ctx *Context, tbl *catalog.Table, idx *catalog.Index, action planner.OnConflictAction, setB planner.Expr, vals ...planner.Expr) int64 {
	t.Helper()
	oc := &planner.OnConflictPlan{
		Action:         action,
		ArbiterIndex:   idx,
		ArbiterColumns: []int{0}, // arbiter over column "a" (ordinal 0)
	}
	if action == planner.OnConflictActionUpdate {
		oc.UpdateSet = []planner.Expr{nil, setB}
	}
	op, err := Build(&planner.Insert{
		Table:       tbl,
		Source:      &planner.Values{Rows: [][]planner.Expr{vals}},
		ColumnIndex: []int{0, 1},
		OnConflict:  oc,
	})
	if err != nil {
		t.Fatalf("Build upsert: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open upsert: %v", err)
	}
	if _, err := op.Next(); err != nil && err != EOF {
		t.Fatalf("upsert Next: %v", err)
	}
	ra := op.(*upsertOp).RowsAffected()
	_ = op.Close()
	return ra
}

// nndScanColB returns the b-column values of every live row, used to assert the
// heap content after an upsert.
func nndScanColB(t *testing.T, ctx *Context, tbl *catalog.Table) []Datum {
	t.Helper()
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatalf("scan Open: %v", err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatalf("drainScan: %v", err)
	}
	out := make([]Datum, len(rows))
	for i, r := range rows {
		out[i] = r[1]
	}
	return out
}

// TestNullsNotDistinctOnConflictDoNothing: INSERT … ON CONFLICT (a) DO NOTHING
// with a NULL key must SKIP the insert when an NND arbiter index already holds a
// NULL-keyed row — the M0119-0004 follow-up. Without the heap-scan fallback the
// nil arbiter key reported "no conflict" and a duplicate NULL row was inserted.
func TestNullsNotDistinctOnConflictDoNothing(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nndc1", []string{"a"}, true)
	idx, ok := cat.(*catalog.InMemory).LookupIndex(parser.ObjectName{Name: "nndc1_key"})
	if !ok {
		t.Fatal("index nndc1_key not found")
	}

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("seed (NULL,1): %v", err)
	}
	if ra := nndUpsert(t, ctx, tbl, idx, planner.OnConflictActionNothing, nil, null(), i(2)); ra != 0 {
		t.Fatalf("DO NOTHING on NULL conflict: RowsAffected=%d want 0", ra)
	}
	if got := nndScanColB(t, ctx, tbl); len(got) != 1 || got[0].Int != 1 {
		t.Fatalf("heap after DO NOTHING = %+v, want exactly one row with b=1 (no duplicate NULL insert)", got)
	}
}

// TestNullsNotDistinctOnConflictDoUpdate: INSERT … ON CONFLICT (a) DO UPDATE on
// a NULL key must TARGET the existing NULL-keyed row (not insert a duplicate).
func TestNullsNotDistinctOnConflictDoUpdate(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nndc2", []string{"a"}, true)
	idx, ok := cat.(*catalog.InMemory).LookupIndex(parser.ObjectName{Name: "nndc2_key"})
	if !ok {
		t.Fatal("index nndc2_key not found")
	}

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("seed (NULL,1): %v", err)
	}
	if ra := nndUpsert(t, ctx, tbl, idx, planner.OnConflictActionUpdate, i(7), null(), i(2)); ra != 1 {
		t.Fatalf("DO UPDATE on NULL conflict: RowsAffected=%d want 1", ra)
	}
	if got := nndScanColB(t, ctx, tbl); len(got) != 1 || got[0].Int != 7 {
		t.Fatalf("heap after DO UPDATE = %+v, want exactly one row with b=7 (existing NULL row updated)", got)
	}
}

// nndBuildAssert23505 asserts that a CREATE [UNIQUE] INDEX build raised the
// could-not-create-unique-index duplicate-key error.
func nndBuildAssert23505(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected 23505 build failure, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23505" {
		t.Fatalf("Code=%q want 23505 (err=%v)", ee.Code, err)
	}
	if !strings.Contains(ee.Message, "could not create unique index") {
		t.Errorf("Message=%q missing 'could not create unique index'", ee.Message)
	}
}

// TestNullsNotDistinctBuildAdmitsNullDefault: building an ordinary (non-unique)
// index over a table that already contains NULL key values must SUCCEED. The
// pre-0119-0004 build path raised `42804 "column is null and cannot be indexed"`,
// rejecting CREATE INDEX over any NULL-containing column — a divergence from PG,
// which admits NULLs in indexes (distinct by default). Design 0119-0004 sub (b).
func TestNullsNotDistinctBuildAdmitsNullDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE bnd1 (a int4, b int4)"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO bnd1 VALUES (1, 10)",
		"INSERT INTO bnd1 VALUES (NULL, 20)",
		"INSERT INTO bnd1 VALUES (NULL, 30)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX bnd1_a ON bnd1 (a)"); err != nil {
		t.Fatalf("CREATE INDEX over NULL-containing column should succeed: %v", err)
	}
}

// TestNullsNotDistinctBuildUniqueAdmitsDuplicateNulls: a default (NULLS DISTINCT)
// UNIQUE index build admits multiple NULL-keyed rows (NULLs are distinct), so the
// build succeeds even with two NULL keys present.
func TestNullsNotDistinctBuildUniqueAdmitsDuplicateNulls(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE bnd2 (a int4, b int4)"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO bnd2 VALUES (NULL, 1)",
		"INSERT INTO bnd2 VALUES (NULL, 2)",
		"INSERT INTO bnd2 VALUES (5, 3)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX bnd2_a ON bnd2 (a)"); err != nil {
		t.Fatalf("UNIQUE (NULLS DISTINCT) build with duplicate NULLs should succeed: %v", err)
	}
}

// TestNullsNotDistinctBuildNNDRejectsDuplicateNulls: a NULLS NOT DISTINCT unique
// index build over pre-existing duplicate-NULL rows must FAIL 23505 (PG rejects
// "could not create unique index ... Key (a)=(null) is duplicated").
func TestNullsNotDistinctBuildNNDRejectsDuplicateNulls(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE bnd3 (a int4, b int4)"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO bnd3 VALUES (NULL, 1)",
		"INSERT INTO bnd3 VALUES (NULL, 2)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	nndBuildAssert23505(t, runDDL(t, ctx, "CREATE UNIQUE INDEX bnd3_a ON bnd3 (a) NULLS NOT DISTINCT"))
}

// TestNullsNotDistinctBuildNNDAdmitsDistinctTail: a multi-column NND unique build
// over rows that share a NULL pattern but differ in their non-NULL tail must
// SUCCEED — NND collapses NULLs only when the remaining key values are equal too.
func TestNullsNotDistinctBuildNNDAdmitsDistinctTail(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE bnd4 (a int4, b int4)"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO bnd4 VALUES (NULL, 1)",
		"INSERT INTO bnd4 VALUES (NULL, 2)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX bnd4_ab ON bnd4 (a, b) NULLS NOT DISTINCT"); err != nil {
		t.Fatalf("NND build with distinct non-NULL tail should succeed: %v", err)
	}
	// But a genuine duplicate NULL pattern + equal tail is still rejected.
	if err := runDDL(t, ctx, "CREATE TABLE bnd5 (a int4, b int4)"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO bnd5 VALUES (NULL, 7)",
		"INSERT INTO bnd5 VALUES (NULL, 7)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	nndBuildAssert23505(t, runDDL(t, ctx, "CREATE UNIQUE INDEX bnd5_ab ON bnd5 (a, b) NULLS NOT DISTINCT"))
}

// nndIntCol returns the int value of column ord across the result rows, asserting
// exactly one row and a non-NULL value at that column.
func nndSingleIntCol(t *testing.T, rows []Row, ord int) int64 {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 row, got %d: %+v", len(rows), rows)
	}
	if rows[0][ord].IsNull() {
		t.Fatalf("col %d is NULL, want non-NULL: %+v", ord, rows[0])
	}
	return rows[0][ord].Int
}

// TestNullsNotDistinctOnConflictReorderedPartitionLeaf: an NND ON CONFLICT upsert
// that routes to a partition leaf whose column order differs from the parent must
// honour the NULLS-NOT-DISTINCT arbiter on the leaf. The proposed row is in parent
// order while the heap-scan matcher (probeArbiterNND → checkNullsNotDistinctViaHeapScan)
// resolves key columns by name against the *leaf* columns, so the caller passes the
// leaf-ordered candidate (insertedForLeaf). Before design 0119-0004 §10 this path
// was guarded out (`partLeaf == nil`), so a duplicate NULL row was wrongly inserted.
// Covers both DO NOTHING (skip) and DO UPDATE (target + leaf→parent conflict-row
// remap of the SET expression). Design 0119-0004 sub (c).
func TestNullsNotDistinctOnConflictReorderedPartitionLeaf(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	// Parent column order (a, b, c); leaf (c, b, a) is a reordered partition leaf.
	for _, s := range []string{
		"CREATE TABLE rpnnd (a int4, b int4, c int4, UNIQUE NULLS NOT DISTINCT (a, b)) PARTITION BY RANGE (b)",
		"CREATE TABLE rpnnd1 (c int4, b int4, a int4)",
		"ALTER TABLE rpnnd ATTACH PARTITION rpnnd1 FOR VALUES FROM (0) TO (100)",
		"INSERT INTO rpnnd (a, b, c) VALUES (NULL, 5, 1)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	// DO NOTHING on the NULL (a) key must skip — no duplicate insert.
	if err := runDDL(t, ctx, "INSERT INTO rpnnd (a, b, c) VALUES (NULL, 5, 2) ON CONFLICT (a, b) DO NOTHING"); err != nil {
		t.Fatalf("DO NOTHING upsert: %v", err)
	}
	if got := nndSingleIntCol(t, runQueryRows(t, ctx, "SELECT c FROM rpnnd"), 0); got != 1 {
		t.Fatalf("after DO NOTHING: c=%d want 1 (original row, no duplicate NULL insert)", got)
	}

	// DO UPDATE on the NULL (a) key must target the existing leaf row and apply the
	// SET against the (leaf→parent remapped) conflict row.
	if err := runDDL(t, ctx, "INSERT INTO rpnnd (a, b, c) VALUES (NULL, 5, 9) ON CONFLICT (a, b) DO UPDATE SET c = 99"); err != nil {
		t.Fatalf("DO UPDATE upsert: %v", err)
	}
	if got := nndSingleIntCol(t, runQueryRows(t, ctx, "SELECT c FROM rpnnd"), 0); got != 99 {
		t.Fatalf("after DO UPDATE: c=%d want 99 (existing NULL row updated, still one row)", got)
	}
}

// TestNullsDistinctOnConflictControlInserts: the default (NULLS DISTINCT) arbiter
// must keep PG semantics — a NULL key never conflicts, so DO NOTHING INSERTS the
// new row. Proves the heap-scan fallback is strictly gated on idx.NullsNotDistinct.
func TestNullsDistinctOnConflictControlInserts(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nndc3", []string{"a"}, false)
	idx, ok := cat.(*catalog.InMemory).LookupIndex(parser.ObjectName{Name: "nndc3_key"})
	if !ok {
		t.Fatal("index nndc3_key not found")
	}

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("seed (NULL,1): %v", err)
	}
	if ra := nndUpsert(t, ctx, tbl, idx, planner.OnConflictActionNothing, nil, null(), i(2)); ra != 1 {
		t.Fatalf("DO NOTHING under NULLS DISTINCT: RowsAffected=%d want 1 (NULL never conflicts)", ra)
	}
	if got := nndScanColB(t, ctx, tbl); len(got) != 2 {
		t.Fatalf("heap after DO NOTHING = %+v, want 2 rows (both NULL keys admitted)", got)
	}
}
