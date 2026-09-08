package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// B-14 (P2-09a) — ScalarArrayOp index probe execution proof.
//
// Fixture: items(id int, name varchar) with a btree PK on id.
// Rows: (1,'a'), (2,'b'), (3,'c'), (4,'d'), (NULL,'z').
func setupSAOPFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)

	if err := runDDL(t, ctx, "CREATE TABLE items (id int, name varchar)"); err != nil {
		cleanup()
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// Rows first, index second: writeHeapRow is storage-level and does
	// not maintain indexes, so the index must backfill (same order as
	// the range-scan fixture).
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	rel := ctx.Catalog.RelFileNode(tbl)
	rows := []Row{
		{{Kind: KindInt, Int: 1}, NewStringDatum("a")},
		{{Kind: KindInt, Int: 2}, NewStringDatum("b")},
		{{Kind: KindInt, Int: 3}, NewStringDatum("c")},
		{{Kind: KindInt, Int: 4}, NewStringDatum("d")},
		{NullDatum, NewStringDatum("z")},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			cleanup()
			t.Fatalf("writeHeapRow: %v", err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX items_pkey ON items (id)"); err != nil {
		cleanup()
		t.Fatalf("CREATE INDEX: %v", err)
	}
	return ctx, cleanup
}

// saopPlanAsserts runs sql, asserts the plan probes via SAOPKeys, and
// returns the result rows.
func saopPlanAsserts(t *testing.T, ctx *Context, sql string, wantKeys int) []Row {
	t.Helper()
	plan := planOne(t, sql, ctx.Catalog)
	proj, ok := plan.(*optimizer.Project)
	if !ok {
		t.Fatalf("%s: root=%T want *optimizer.Project", sql, plan)
	}
	idx, ok := proj.Child.(*optimizer.IndexScan)
	if !ok {
		t.Fatalf("%s: child=%T want *optimizer.IndexScan (SAOP probe)", sql, proj.Child)
	}
	if len(idx.SAOPKeys) != wantKeys {
		t.Fatalf("%s: SAOPKeys=%d want %d", sql, len(idx.SAOPKeys), wantKeys)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("%s: Build: %v", sql, err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("%s: Run: %v", sql, err)
	}
	return rows
}

func saopResultIDs(rows []Row) map[int64]bool {
	out := make(map[int64]bool, len(rows))
	for _, r := range rows {
		if !r[0].IsNull() {
			out[r[0].Int] = true
		}
	}
	return out
}

func TestSAOPProbeRows(t *testing.T) {
	ctx, cleanup := setupSAOPFixture(t)
	defer cleanup()

	rows := saopPlanAsserts(t, ctx, "SELECT id FROM items WHERE id IN (2, 4)", 2)
	if got := saopResultIDs(rows); len(got) != 2 || !got[2] || !got[4] {
		t.Fatalf("IN (2,4) ids=%v want {2 4}", got)
	}
}

func TestSAOPDuplicateElementsDeduped(t *testing.T) {
	ctx, cleanup := setupSAOPFixture(t)
	defer cleanup()

	// `IN (2,2,4)` descends twice to 2's leaf: the row must still come
	// out once.
	rows := saopPlanAsserts(t, ctx, "SELECT id FROM items WHERE id IN (2, 2, 4)", 3)
	if len(rows) != 2 {
		t.Fatalf("IN (2,2,4) rows=%d want 2 (deduplicated)", len(rows))
	}
	if got := saopResultIDs(rows); !got[2] || !got[4] {
		t.Fatalf("IN (2,2,4) ids=%v want {2 4}", got)
	}
}

func TestSAOPNullElementSkipped(t *testing.T) {
	ctx, cleanup := setupSAOPFixture(t)
	defer cleanup()

	// A NULL element matches nothing but must not poison the probe:
	// `x IN (2, NULL)` matches x=2 and excludes everything else (the
	// Filter(InExpr) semantics the probe replaces).
	rows := saopPlanAsserts(t, ctx, "SELECT id FROM items WHERE id IN (2, NULL)", 2)
	if got := saopResultIDs(rows); len(got) != 1 || !got[2] {
		t.Fatalf("IN (2,NULL) ids=%v want {2}", got)
	}
}

func TestSAOPNoMatchEmpty(t *testing.T) {
	ctx, cleanup := setupSAOPFixture(t)
	defer cleanup()

	rows := saopPlanAsserts(t, ctx, "SELECT id FROM items WHERE id IN (99, 100)", 2)
	if len(rows) != 0 {
		t.Fatalf("IN (99,100) rows=%d want 0", len(rows))
	}
}

func TestSAOPExplainRendersAnyCond(t *testing.T) {
	ctx, cleanup := setupSAOPFixture(t)
	defer cleanup()

	// The Q45 gate's `= ANY` cond, at unit scale.
	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT id FROM items WHERE id IN (2, 4)")
	found := false
	for _, ln := range lines {
		if strings.Contains(ln, "Index Cond: (id = ANY (2, 4))") {
			found = true
		}
	}
	if !found {
		t.Fatalf("EXPLAIN missing `Index Cond: (id = ANY (2, 4))`, got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSAOPUpdateDeleteWhereIn(t *testing.T) {
	ctx, cleanup := setupSAOPFixture(t)
	defer cleanup()

	// UPDATE/DELETE plan the SAOP IndexScan as their child but execute
	// the SeqScan+predicate fallback (updateViaIndex requires Key):
	// indexScanPredicate rebuilds the IN qual, so exactly the probed
	// rows are written.
	plan := planOne(t, "UPDATE items SET name = 'u' WHERE id IN (1, 3)", ctx.Catalog)
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("UPDATE Build: %v", err)
	}
	if _, err := Run(op, ctx); err != nil {
		t.Fatalf("UPDATE Run: %v", err)
	}

	plan = planOne(t, "DELETE FROM items WHERE id IN (2, 4)", ctx.Catalog)
	op, err = Build(plan)
	if err != nil {
		t.Fatalf("DELETE Build: %v", err)
	}
	if _, err := Run(op, ctx); err != nil {
		t.Fatalf("DELETE Run: %v", err)
	}

	// Remaining: (1,'u'), (3,'u'), (NULL,'z').
	plan = planOne(t, "SELECT id, name FROM items", ctx.Catalog)
	op, err = Build(plan)
	if err != nil {
		t.Fatalf("SELECT Build: %v", err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("SELECT Run: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("remaining rows=%d want 3, rows=%v", len(rows), rows)
	}
	byID := map[int64]string{}
	nulls := 0
	for _, r := range rows {
		if r[0].IsNull() {
			nulls++
			continue
		}
		byID[r[0].Int] = r[1].StringValue()
	}
	if nulls != 1 || byID[1] != "u" || byID[3] != "u" {
		t.Fatalf("remaining=%v nulls=%d want {1:u 3:u}+1 null", byID, nulls)
	}
}

func TestSAOPNotInStaysSeqScan(t *testing.T) {
	ctx, cleanup := setupSAOPFixture(t)
	defer cleanup()

	// NOT IN is ALL semantics: no probe, and the Filter path keeps
	// three-valued logic (`NOT IN (2, NULL)` matches nothing — every
	// comparison is false or NULL, never true).
	plan := planOne(t, "SELECT id FROM items WHERE id NOT IN (2, NULL)", ctx.Catalog)
	proj, ok := plan.(*optimizer.Project)
	if !ok {
		t.Fatalf("root=%T want *optimizer.Project", plan)
	}
	if _, isIdx := proj.Child.(*optimizer.IndexScan); isIdx {
		t.Fatalf("NOT IN must stay on SeqScan, got IndexScan")
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("NOT IN (2,NULL) rows=%d want 0 (NULL poisons ALL)", len(rows))
	}
}
