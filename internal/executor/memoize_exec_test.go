package executor

// S7 — end-to-end pins for the memoizeOp parameterized result cache
// under NLI joins. The planner-side attach gate is pinned in
// internal/planner/memoize_test.go; the tests here pin the RUNTIME
// contracts:
//
//   - a Memoize-planned join returns the SAME rows as the bare NLI
//     (planner.SetMemoizeEnabled off/on comparison);
//   - EXPLAIN renders the Memoize node with its Cache Key line, and
//     EXPLAIN ANALYZE reports Hits/Misses/Evictions/Overflows with the
//     exact counts the key stream implies;
//   - a budget too small for even one entry degrades to pure
//     pass-through (Overflows counted, results still correct) — never
//     a partial entry served.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// newMemoizeFixture: mo(k, pad) drives mi(id PRIMARY KEY, v) through
// k = id. 40 outer rows over 10 distinct keys, CLUSTERED (k ascending:
// miss,hit,hit,hit per key) so the expected Hits/Misses split is exact
// even under eviction. Stats are injected directly (the in-process
// fixture's ANALYZE is a no-op) and scaled up — RowCount 4000, nd 10 —
// so the attach gate's outerRows/hit-fraction thresholds accept.
func newMemoizeFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	stmts := []string{
		"CREATE TABLE mo (k int, pad int)",
		"CREATE TABLE mi (id int PRIMARY KEY, v int)",
	}
	for i := 1; i <= 10; i++ {
		stmts = append(stmts, "INSERT INTO mi VALUES ("+itoa(i)+", "+itoa(i*100)+")")
	}
	for i := 0; i < 40; i++ {
		stmts = append(stmts, "INSERT INTO mo VALUES ("+itoa(i/4+1)+", "+itoa(i)+")")
	}
	for _, stmt := range stmts {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mo"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 4000, Columns: []catalog.ColumnStats{
			{NDistinct: 10}, {NDistinct: 4000},
		}}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mi"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 100000, Columns: []catalog.ColumnStats{
			{NDistinct: 100000}, {NDistinct: 100},
		}}
	}
	return ctx, cleanup
}

const memoizeJoinSQL = "SELECT count(*), sum(mi.v) FROM mo JOIN mi ON mi.id = mo.k"

// TestMemoizeExplainAndResults — the join plans a Memoize node under
// the NLI, the results match the bare-NLI plan, and switching the rule
// off removes the node.
func TestMemoizeExplainAndResults(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	planner.SetMemoizeEnabled(true)
	defer planner.SetMemoizeEnabled(true)

	plan := explainText(t, ctx, memoizeJoinSQL)
	if !strings.Contains(plan, "Nested Loop") {
		t.Fatalf("fixture no longer plans an NLI join — the memoize pins are vacuous:\n%s", plan)
	}
	if !strings.Contains(plan, "Memoize") || !strings.Contains(plan, "Cache Key:") {
		t.Fatalf("expected Memoize node with Cache Key line:\n%s", plan)
	}

	withMemo, err := runQueryWithErr(ctx, memoizeJoinSQL)
	if err != nil {
		t.Fatalf("memoized query: %v", err)
	}

	planner.SetMemoizeEnabled(false)
	if off := explainText(t, ctx, memoizeJoinSQL); strings.Contains(off, "Memoize") {
		t.Fatalf("Memoize node survived SetMemoizeEnabled(false):\n%s", off)
	}
	withoutMemo, err := runQueryWithErr(ctx, memoizeJoinSQL)
	if err != nil {
		t.Fatalf("bare query: %v", err)
	}

	// 40 rows; sum = 4 × (100+200+…+1000) = 22000.
	for name, rows := range map[string][]Row{"memoized": withMemo, "bare": withoutMemo} {
		if len(rows) != 1 {
			t.Fatalf("%s: %d result rows, want 1", name, len(rows))
		}
		if got := datumTestString(rows[0][0]); got != "40" {
			t.Errorf("%s: count=%s, want 40", name, got)
		}
		if got := datumTestString(rows[0][1]); got != "22000" {
			t.Errorf("%s: sum=%s, want 22000", name, got)
		}
	}
}

// TestMemoizeAnalyzeCounters — the clustered 40-probe/10-key stream is
// exactly 10 misses (first probe per key) + 30 hits, nothing evicted,
// nothing overflowed; ANALYZE must say so.
func TestMemoizeAnalyzeCounters(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	planner.SetMemoizeEnabled(true)
	defer planner.SetMemoizeEnabled(true)

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE "+memoizeJoinSQL)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Memoize") {
		t.Fatalf("ANALYZE plan lost the Memoize node:\n%s", joined)
	}
	want := "Hits: 30  Misses: 10  Evictions: 0  Overflows: 0"
	if !strings.Contains(joined, want) {
		t.Fatalf("expected counter line %q:\n%s", want, joined)
	}
}

// TestMemoizeOverflowPassThrough — WorkMem so small no entry can be
// stored: every probe misses, every completed entry overflows, and the
// results are still exact (pass-through, never a partial serve).
func TestMemoizeOverflowPassThrough(t *testing.T) {
	ctx, cleanup := newMemoizeFixture(t)
	defer cleanup()

	planner.SetMemoizeEnabled(true)
	defer planner.SetMemoizeEnabled(true)

	savedWorkMem := ctx.WorkMem
	ctx.WorkMem = 100 // budget = WorkMem/4 = 25 bytes < any entry
	defer func() { ctx.WorkMem = savedWorkMem }()

	rows, err := runQueryWithErr(ctx, memoizeJoinSQL)
	if err != nil {
		t.Fatalf("query under tiny budget: %v", err)
	}
	if got := datumTestString(rows[0][0]); got != "40" {
		t.Errorf("count=%s, want 40", got)
	}
	if got := datumTestString(rows[0][1]); got != "22000" {
		t.Errorf("sum=%s, want 22000", got)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE "+memoizeJoinSQL)
	joined := strings.Join(lines, "\n")
	want := "Hits: 0  Misses: 40  Evictions: 0  Overflows: 40"
	if !strings.Contains(joined, want) {
		t.Fatalf("expected counter line %q:\n%s", want, joined)
	}
}
