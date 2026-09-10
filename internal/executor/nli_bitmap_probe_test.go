package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// R49 Slice B pins: the parameterised NLI-bitmap probe end-to-end.
//
// Doll-house fixture (mirrors the R48 ord/line shape, bitmap-sized): 6-row
// outer (keys 1..5 + NULL; only 1..4 match) over a 20k-row inner with 4
// distinct keys (5000 rows per key), so every per-probe bitmap holds 5000
// TIDs. The planner serves the NLI-bitmap shape on NATURAL costs (no
// toggles): per-probe bitmap cost clears the full-seq prebuilt seed and beats
// the per-probe index descent. A filterless INNER join never reaches the
// search (legacy path), so every query carries a WHERE clause — even an
// outer-only one admits the shape.
//
// Assertion chain (why text + counts is sufficient): the OP1-3 unit pin
// (`createplannl_bitmap_recheck_test.go`) proves BitmapQual orientation and
// merged coordinates exactly; here EXPLAIN text proves the Predicate is nil
// (no join-level Filter line — the renderer nil-guards) and the BitmapQual
// survived with PG's orientation (`Recheck Cond:` renders FROM BitmapQual).
// Merged-coord CORRECTNESS is proved by the lossy tests: on exact pages with
// a full-key probe the recheck is skipped entirely, so only the lossy path
// (tiny work_mem, recheck forced per tuple) exercises the combined-row eval —
// wrong coordinates would mis-filter or error, and exact-vs-lossy agreement
// plus exact counts closes the loop.

// mkBitmapProbeFixture builds the doll-house with the given index set.
func mkBitmapProbeFixture(t *testing.T, indexes ...string) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	stmts := []string{
		"CREATE TABLE ord (o_key int, o_val int)",
		"CREATE TABLE line (l_key int, l_c int, l_r int)",
	}
	stmts = append(stmts, indexes...)
	stmts = append(stmts,
		"INSERT INTO ord VALUES (1, 1)",
		"INSERT INTO ord VALUES (2, 4)",
		"INSERT INTO ord VALUES (3, 5)",
		"INSERT INTO ord VALUES (4, 7)",
		"INSERT INTO ord VALUES (5, 9)",
		"INSERT INTO ord VALUES (NULL, 0)",
	)
	for _, s := range stmts {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("fixture %q: %v", s, err)
		}
	}
	for i := 0; i < 20000; i++ {
		s := fmt.Sprintf("INSERT INTO line VALUES (%d, %d, %d)", 1+(i%4), i, i+1)
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "ord"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 6, Columns: []catalog.ColumnStats{
			{NDistinct: 6}, {NDistinct: 6},
		}}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "line"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 20000, Columns: []catalog.ColumnStats{
			{NDistinct: 4}, {NDistinct: 20000}, {NDistinct: 20000},
		}}
	}
	return ctx, cleanup
}

// runProbeQuery plans (default settings) + builds + executes, returning rows.
func runProbeQuery(ctx *Context, t *testing.T, sql string) []Row {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, optimizer.DefaultPlannerSettings())
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("build %q: %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open %q: %v", sql, err)
	}
	defer op.Close()
	var rows []Row
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next %q: %v", sql, err)
		}
		// Copy: Slot.Row returns the producer's reused buffer.
		rows = append(rows, append(Row(nil), slot.Row()...))
	}
	return rows
}

func explainProbeQuery(ctx *Context, t *testing.T, sql string) string {
	t.Helper()
	var b strings.Builder
	for _, r := range runProbeQuery(ctx, t, "EXPLAIN "+sql) {
		b.WriteString(datumTestString(r[0]))
		b.WriteString("\n")
	}
	return b.String()
}

const bitmapProbeInnerJoin = "SELECT o_key, l_c FROM ord JOIN line ON l_key = o_key WHERE o_key BETWEEN 1 AND 4"

// TestNLIBitmapProbeBasic pins the Slice-B plan shape live: PG's two Cond
// lines, no join line, exact row counts.
func TestNLIBitmapProbeBasic(t *testing.T) {
	ctx, cleanup := mkBitmapProbeFixture(t, "CREATE INDEX line_key_idx ON line (l_key)")
	defer cleanup()

	if rows := runProbeQuery(ctx, t, bitmapProbeInnerJoin); len(rows) != 20000 {
		t.Fatalf("live NLI-bitmap rows = %d, want 20000", len(rows))
	}
	plan := explainProbeQuery(ctx, t, bitmapProbeInnerJoin)
	t.Logf("plan:\n%s", plan)
	if !strings.Contains(plan, "Recheck Cond: (l_key = o_key)") {
		t.Errorf("missing PG-orientation Recheck Cond line:\n%s", plan)
	}
	if !strings.Contains(plan, "Index Cond:") {
		t.Errorf("missing Slice-A Index Cond line:\n%s", plan)
	}
	// No JOIN-level Filter: PG renders a join's own Filter directly under
	// the Nested Loop line, before the first "->" child line. (The outer
	// Seq Scan's Filter below its own child line is unrelated.)
	lines := strings.Split(plan, "\n")
	joinIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "Nested Loop") {
			joinIdx = i
			break
		}
	}
	if joinIdx < 0 {
		t.Fatalf("plan shape unrecognised:\n%s", plan)
	}
	if !strings.Contains(plan, "Bitmap Heap Scan") {
		t.Fatalf("not an NLI-bitmap plan:\n%s", plan)
	}
	for _, l := range lines[joinIdx+1:] {
		if strings.Contains(l, "->") {
			break
		}
		if strings.Contains(l, "Filter:") {
			t.Errorf("join-level Filter line survived Slice B: %q\n%s", l, plan)
		}
	}
}

// TestNLIBitmapProbeLossyRecheck is the exact regression OP1-3 was written
// against, now through the new channel: with work_mem far below the
// per-probe bitmap size, tbmLossify degrades pages to lossy and every tuple
// on such a page is re-checked via the combined-row BitmapQual. Agreement
// with the exact path plus exact counts proves the recheck fires per outer
// row through the new channel.
func TestNLIBitmapProbeLossyRecheck(t *testing.T) {
	ctx, cleanup := mkBitmapProbeFixture(t, "CREATE INDEX line_key_idx ON line (l_key)")
	defer cleanup()

	if rows := runProbeQuery(ctx, t, bitmapProbeInnerJoin); len(rows) != 20000 {
		t.Fatalf("exact-path rows = %d, want 20000", len(rows))
	}
	// Self-validating fixture: the lossy path must be STRUCTURALLY forced,
	// not hoped for. Each probe's bitmap holds 5000 TIDs; maxEntries at
	// 4KB is 16, so tbmLossify must degrade pages on every probe.
	ctx.WorkMem = 4 << 10
	if got := tbmCalculateMaxEntries(ctx.WorkMem); got >= 5000 {
		t.Fatalf("maxEntries@4KB = %d, want < 5000 per-probe TIDs (lossy not forced)", got)
	}
	if rows := runProbeQuery(ctx, t, bitmapProbeInnerJoin); len(rows) != 20000 {
		t.Fatalf("lossy-path rows = %d, want 20000 (recheck leaked or dropped)", len(rows))
	}
}

// TestNLIBitmapProbeNullKey pins the Slice-B merge blocker (DESIGN §0): a
// NULL probe key matches nothing (PG btree semantics). Pre-fix, the NULL
// produced open-ended (nil, nil) bounds — a FULL scan per NULL outer row —
// and on exact pages with needsRecheck()==false nothing re-checked, so every
// inner row emitted. Both the single-column full-key index (the leaking
// shape) and the composite-prefix probe (the always-rechecks safe shape) must
// yield zero rows for the NULL outer key: 4 keys × 5000, nothing else.
func TestNLIBitmapProbeNullKey(t *testing.T) {
	const q = "SELECT o_key, l_c FROM ord JOIN line ON l_key = o_key WHERE o_key BETWEEN 1 AND 4 OR o_key IS NULL"

	t.Run("single-column-full-key", func(t *testing.T) {
		ctx, cleanup := mkBitmapProbeFixture(t, "CREATE INDEX line_key_idx ON line (l_key)")
		defer cleanup()
		plan := explainProbeQuery(ctx, t, q)
		if !strings.Contains(plan, "Bitmap Heap Scan") {
			t.Fatalf("not an NLI-bitmap plan:\n%s", plan)
		}
		if rows := runProbeQuery(ctx, t, q); len(rows) != 20000 {
			t.Fatalf("rows = %d, want 20000 (NULL key must match nothing)", len(rows))
		}
	})

	t.Run("composite-prefix", func(t *testing.T) {
		ctx, cleanup := mkBitmapProbeFixture(t, "CREATE INDEX line_comp_idx ON line (l_key, l_c)")
		defer cleanup()
		plan := explainProbeQuery(ctx, t, q)
		if !strings.Contains(plan, "Bitmap Heap Scan") {
			t.Fatalf("not an NLI-bitmap plan:\n%s", plan)
		}
		if rows := runProbeQuery(ctx, t, q); len(rows) != 20000 {
			t.Fatalf("rows = %d, want 20000 (NULL prefix key must match nothing)", len(rows))
		}
	})
}

// TestNLIBitmapProbeCompositeKeys exercises multi-key combined-row eval: two
// probe pairs over the composite index. Expected count is hand-computed:
// ord pairs are (1,1),(2,4),(3,5),(4,7); line rows are (1+i%4, i, i+1), so
// l_c = o_val with a matching key holds only at i=7 (key 4, l_c 7 = o_val
// 7) — exactly 1 row.
func TestNLIBitmapProbeCompositeKeys(t *testing.T) {
	ctx, cleanup := mkBitmapProbeFixture(t, "CREATE INDEX line_comp_idx ON line (l_key, l_c)")
	defer cleanup()
	const q = "SELECT o_key, l_c FROM ord JOIN line ON l_key = o_key AND l_c = o_val WHERE o_key BETWEEN 1 AND 4"
	plan := explainProbeQuery(ctx, t, q)
	if !strings.Contains(plan, "Recheck Cond: ((l_key = o_key) AND (l_c = o_val))") {
		t.Errorf("missing two-pair Recheck Cond:\n%s", plan)
	}
	if rows := runProbeQuery(ctx, t, q); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (only i=7 matches both pairs)", len(rows))
	}
}

// TestNLIBitmapProbeLeft pins review-note-8(a): the LEFT null-padding
// fallback emits without probe eval (PG never evaluates the join cond
// against synthesized nulls), so moving the recheck into the scan is safe by
// construction. Outer key 5 matches nothing → exactly one null-padded row.
func TestNLIBitmapProbeLeft(t *testing.T) {
	ctx, cleanup := mkBitmapProbeFixture(t, "CREATE INDEX line_key_idx ON line (l_key)")
	defer cleanup()
	const q = "SELECT o_key, l_c FROM ord LEFT JOIN line ON l_key = o_key WHERE o_key BETWEEN 1 AND 5"
	if plan := explainProbeQuery(ctx, t, q); !strings.Contains(plan, "Bitmap Heap Scan") {
		t.Fatalf("not an NLI-bitmap plan:\n%s", plan)
	}
	rows := runProbeQuery(ctx, t, q)
	if len(rows) != 20001 {
		t.Fatalf("rows = %d, want 20001 (20000 matches + 1 null-padded)", len(rows))
	}
	nulls := 0
	for _, r := range rows {
		if r[1].IsNull() {
			nulls++
			if !r[0].IsNull() && (r[0].Kind != KindInt || r[0].Int != 5) {
				t.Fatalf("null-padded row has o_key = %v, want 5", r[0])
			}
		}
	}
	if nulls != 1 {
		t.Fatalf("null-padded rows = %d, want 1", nulls)
	}
}

// TestNLIBitmapProbeCondAndRecheck pins the two-slot discipline (review note
// 3) under recheck: the leaf-local Cond (l_r > 0, always true here — counts
// stay 20000) evaluates against the inner scanRow while BitmapQual evaluates
// against the combined row. Tiny work_mem forces the recheck path so BOTH
// slots are live on every tuple.
func TestNLIBitmapProbeCondAndRecheck(t *testing.T) {
	ctx, cleanup := mkBitmapProbeFixture(t, "CREATE INDEX line_key_idx ON line (l_key)")
	defer cleanup()
	const q = "SELECT o_key, l_c FROM ord JOIN line ON l_key = o_key AND l_r > 0 WHERE o_key BETWEEN 1 AND 4"
	plan := explainProbeQuery(ctx, t, q)
	if !strings.Contains(plan, "Recheck Cond: (l_key = o_key)") {
		t.Fatalf("missing Recheck Cond:\n%s", plan)
	}
	if !strings.Contains(plan, "Filter: (l_r > 0)") {
		t.Fatalf("missing leaf-local Cond Filter:\n%s", plan)
	}
	ctx.WorkMem = 4 << 10
	if rows := runProbeQuery(ctx, t, q); len(rows) != 20000 {
		t.Fatalf("lossy cond+recheck rows = %d, want 20000", len(rows))
	}
}

// TestBitmapLookupBoundsNullKey units-pins the NULL→empty-TBM threading at
// its narrowest point: lookupBounds reports nullKey for a NULL single key
// and for a NULL composite part, and reports clean full-scan bounds (no
// nullKey) for the key-less shape.
func TestBitmapLookupBoundsNullKey(t *testing.T) {
	ctx, cleanup := mkBitmapProbeFixture(t, "CREATE INDEX line_key_idx ON line (l_key)")
	defer cleanup()
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "line"})
	if !ok {
		t.Fatal("line table missing")
	}
	var idx *catalog.Index
	for _, in := range ctx.Catalog.IndexesOnTable(tbl) {
		if len(in.Columns) == 1 && in.Columns[0] == "l_key" {
			idx = in
			break
		}
	}
	if idx == nil {
		t.Fatal("line_key_idx missing")
	}
	outerSchema := optimizer.Schema{}
	outerSlot := SlotFromRow(outerSchema, Row{})

	t.Run("null-single-key", func(t *testing.T) {
		op := &bitmapIndexScanOp{
			plan: &optimizer.BitmapIndexScan{Table: tbl, Index: idx, Key: &optimizer.NullConst{}},
			ctx:  ctx,
		}
		op.BindOuter(outerSlot, 1)
		lo, hi, nullKey, err := op.lookupBounds()
		if err != nil {
			t.Fatalf("lookupBounds: %v", err)
		}
		if !nullKey || lo != nil || hi != nil {
			t.Fatalf("lookupBounds = (%v, %v, nullKey=%v), want (nil, nil, true)", lo, hi, nullKey)
		}
	})

	t.Run("null-composite-part", func(t *testing.T) {
		op := &bitmapIndexScanOp{
			plan: &optimizer.BitmapIndexScan{Table: tbl, Index: idx,
				Keys: []optimizer.Expr{&optimizer.NullConst{}}},
			ctx: ctx,
		}
		op.BindOuter(outerSlot, 1)
		_, _, nullKey, err := op.lookupBounds()
		if err != nil {
			t.Fatalf("lookupBounds: %v", err)
		}
		if !nullKey {
			t.Fatal("nullKey = false for NULL composite part, want true")
		}
	})

	t.Run("keyless-full-scan", func(t *testing.T) {
		op := &bitmapIndexScanOp{
			plan: &optimizer.BitmapIndexScan{Table: tbl, Index: idx},
			ctx:  ctx,
		}
		op.BindOuter(outerSlot, 1)
		lo, hi, nullKey, err := op.lookupBounds()
		if err != nil {
			t.Fatalf("lookupBounds: %v", err)
		}
		if nullKey || lo != nil || hi != nil {
			t.Fatalf("lookupBounds = (%v, %v, nullKey=%v), want (nil, nil, false)", lo, hi, nullKey)
		}
	})
}
