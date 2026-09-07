package executor

// upper_narrow_sort_order_test.go — the END-TO-END order oracle for B-01c
// applying slice (c) (`internal/optimizer/upper_narrow_chain.go`).
//
// Slice (c) narrows the input row of a general ORDER BY `*Sort` and re-bases
// every ancestor up to the node that absorbs the change. The sort's key list is
// rewritten onto the narrowed row, and a comparator that ends up reading a
// shifted column produces OUT-OF-ORDER ROWS WITH NO ERROR: `sortOp`'s
// key-vals path checks ordering explicitly, not membership
// (`operators.go`:1010-1015), so a run merged against a spill file compared by
// a different comparator is emitted, not rejected. No row-count gate can see
// that, and neither can a values gate that sorts before comparing.
//
// So this file compares ROW ORDER, through the real planner and the real
// executor, on the shape the cut fires on: an ORDER BY over a table wider than
// the select list, with multi-key ASC/DESC ordering, NULLs on both sides of a
// direction, and ties that force the later keys to decide.
//
// # The vacuity guard
//
// An order test can pass while proving nothing — if the expected order happens
// to equal the physical row order, or if any permutation of the key columns
// would give the same answer. Both are checked here BEFORE the result is
// compared: `TestUpperNarrowSortEndToEndOrder` fails its own fixture if the
// expected order equals insertion order, and
// `TestUpperNarrowSortEndToEndOrderIsNotVacuous` requires that ordering the
// SAME data by the one-column-shifted key list — the exact off-by-one a
// mis-based re-base produces — gives a DIFFERENT answer. If it did not, the
// passing test would be comparing nothing.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// unsFixture creates a six-column table and loads the slice-(a)/(c) fixture
// rows. Only three of the six columns are read by the query below, so the
// narrowing has four columns of dead weight to remove from the sort's payload.
func unsFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	mustRunDDL(t, ctx, "CREATE TABLE wide (a int, b int, c int, d int, e int, f text)")
	for _, r := range unsRows {
		d := "NULL"
		if r.d >= 0 {
			d = fmt.Sprintf("%d", r.d)
		}
		mustRunDDL(t, ctx, fmt.Sprintf(
			"INSERT INTO wide (a, b, c, d, e, f) VALUES (%d, %d, %d, %s, %d, '%s')",
			r.a, r.b, r.c, d, r.e, r.f))
	}
	return ctx, cleanup
}

// unsRows is the fixture, in INSERTION order. `d < 0` means SQL NULL. The
// `f` column is wide text nobody reads: it is the payload the cut removes
// from the sort.
var unsRows = []struct {
	a, b, c, d, e int
	f             string
}{
	{9, 2, 7, 5, 1, "padding-one"},
	{9, 2, 7, 5, 3, "padding-two"},
	{1, 2, 7, -1, 2, "padding-three"}, // NULL in d
	{5, 1, 7, 8, 9, "padding-four"},
	{5, 1, 7, 2, 9, "padding-five"},
	{5, 1, 7, -1, 0, "padding-six"}, // NULL in d
	{2, 3, 7, 6, 6, "padding-seven"},
}

// unsQuery is the shape slice (c) narrows: the ORDER BY reads b, d and e, the
// select list reads the same three, and a, c and f are dead weight in the
// sort's payload. `d DESC NULLS FIRST` and the b-ties are what make the key
// ORDER, not just key membership, decide the answer.
// The `(SELECT ... f ...)` wrapper is load-bearing, not decoration: over a
// bare scan the pre-existing projection pass already hands the Sort a row of
// exactly the columns it needs, so the keep is the identity and slice (c)
// declines (correctly — a Project reproducing its input is pure cost). Pinning
// `f` into the sub-select's output and out of the outer select list is what
// leaves the Sort a column of dead weight to drop, and it is the shape the
// probe confirms fires (4 columns -> 3).
const unsQuery = "SELECT b, d, e FROM (SELECT b, d, e, f FROM wide) t " +
	"ORDER BY b ASC, d DESC NULLS FIRST, e ASC"

// unsWantOrder is the emitted order, as `b|d|e` triples, computed from
// PostgreSQL's ordering rules over unsRows: b ascending; within a b, d
// descending with NULLs first; within that, e ascending.
var unsWantOrder = []string{
	"1|NULL|0",
	"1|8|9",
	"1|2|9",
	"2|NULL|2",
	"2|5|1",
	"2|5|3",
	"3|6|6",
}

// TestUpperNarrowSortEndToEndOrder runs the narrowed shape through the whole
// stack and compares the EMITTED ORDER, row by row.
func TestUpperNarrowSortEndToEndOrder(t *testing.T) {
	ctx, cleanup := unsFixture(t)
	defer cleanup()

	// Vacuity guard 1: the expected order must not be insertion order, or the
	// comparison below would pass against a sort that did nothing at all.
	var inserted []string
	for _, r := range unsRows {
		d := "NULL"
		if r.d >= 0 {
			d = fmt.Sprintf("%d", r.d)
		}
		inserted = append(inserted, fmt.Sprintf("%d|%s|%d", r.b, d, r.e))
	}
	if strings.Join(inserted, ",") == strings.Join(unsWantOrder, ",") {
		t.Fatal("the fixture's expected order equals its insertion order — this test would pass without a working sort")
	}

	// Vacuity guard 2, and the one specific to THIS slice: if the cut stops
	// firing on this shape, the order check below still passes and would be
	// silently testing nothing about slice (c).
	unsAssertNarrowed(t, ctx, unsQuery)

	got := unsOrderOf(t, ctx, unsQuery)
	if strings.Join(got, ",") != strings.Join(unsWantOrder, ",") {
		t.Fatalf("the narrowed ORDER BY emitted the wrong order:\n  got  %v\n  want %v", got, unsWantOrder)
	}
}

// TestUpperNarrowSortEndToEndOrderIsNotVacuous is the paired guard: ordering
// the same data by a ONE-COLUMN-SHIFTED key list must give a DIFFERENT answer.
//
// This is the failure a mis-based re-base actually produces — every surviving
// `ColumnRef.Index` off by one — and if the fixture could not distinguish it,
// the test above would be decoration.
func TestUpperNarrowSortEndToEndOrderIsNotVacuous(t *testing.T) {
	ctx, cleanup := unsFixture(t)
	defer cleanup()

	// The key list rotated one column left: (b,d,e) -> (d,e,b), flags kept.
	shifted := unsOrderOf(t, ctx,
		"SELECT b, d, e FROM (SELECT b, d, e, f FROM wide) t "+
			"ORDER BY d ASC, e DESC NULLS FIRST, b ASC")
	if strings.Join(shifted, ",") == strings.Join(unsWantOrder, ",") {
		t.Fatal("a one-column key mis-base produces the SAME order on this fixture — the order oracle above proves nothing")
	}
}

// TestUpperNarrowSortEndToEndOrderUnderALimit exercises the PROPAGATE chain:
// a `*Limit` above the narrowed Sort, whose own `TiesKeys` is the row read
// `enclosingNodeScopeOf`'s Limit arm does not enumerate. WITH TIES is where a
// dropped or mis-based ties key changes HOW MANY rows come back, so this
// asserts the count and the order together.
func TestUpperNarrowSortEndToEndOrderUnderALimit(t *testing.T) {
	ctx, cleanup := unsFixture(t)
	defer cleanup()

	// b ascending: one row at b=1... except three rows share b=1, so WITH TIES
	// must return all three where a plain LIMIT 1 returns one.
	got := unsOrderOf(t, ctx, "SELECT b, d, e FROM (SELECT b, d, e, f FROM wide) t "+
		"ORDER BY b ASC FETCH FIRST 1 ROWS WITH TIES")
	if len(got) != 3 {
		t.Fatalf("WITH TIES over the narrowed sort returned %d rows (%v), want the 3 rows sharing b=1", len(got), got)
	}
	for _, row := range got {
		if !strings.HasPrefix(row, "1|") {
			t.Fatalf("WITH TIES returned a row outside the tied group: %v", got)
		}
	}
	plain := unsOrderOf(t, ctx, "SELECT b, d, e FROM (SELECT b, d, e, f FROM wide) t "+
		"ORDER BY b ASC LIMIT 1")
	if len(plain) != 1 {
		t.Fatalf("plain LIMIT 1 returned %d rows, want 1 — the WITH TIES check above is only meaningful against it", len(plain))
	}
}

// unsAssertNarrowed plans sql and fails unless the upper narrowing actually
// cut the ORDER BY Sort's input row: the Sort's child must be a `*Project`
// publishing FEWER columns than the row beneath it.
//
// Without this, a change that makes slice (c) decline on this shape would keep
// every order assertion in this file green while removing the thing they exist
// to check.
func unsAssertNarrowed(t *testing.T, ctx *Context, sql string) {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	srt := unsFindSort(plan)
	if srt == nil {
		t.Fatalf("%s: no *Sort in the plan — the fixture no longer produces the shape this file tests", sql)
	}
	proj, ok := srt.Child.(*optimizer.Project)
	if !ok {
		t.Fatalf("%s: Sort.Child is %T, want the narrowing *Project — slice (c) declined this shape", sql, srt.Child)
	}
	if proj.Child == nil || len(proj.Output()) >= len(proj.Child.Output()) {
		t.Fatalf("%s: the Project under the Sort keeps %d of %d columns — it narrows nothing",
			sql, len(proj.Output()), len(proj.Child.Output()))
	}
}

// unsFindSort returns the topmost *Sort in a plan, following only the
// single-child upper chain the fixture builds.
func unsFindSort(n optimizer.Node) *optimizer.Sort {
	for n != nil {
		switch x := n.(type) {
		case *optimizer.Sort:
			return x
		case *optimizer.Project:
			n = x.Child
		case *optimizer.Filter:
			n = x.Child
		case *optimizer.Limit:
			n = x.Child
		default:
			return nil
		}
	}
	return nil
}

// unsOrderOf runs sql and renders each row as `b|d|e`, preserving emission
// order. Rendering as strings (rather than comparing Datums) keeps the
// assertion about ORDER rather than about the codec.
func unsOrderOf(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	rows := runQuery(t, ctx, sql)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if len(r) != 3 {
			t.Fatalf("%s: row has %d columns, want 3", sql, len(r))
		}
		cells := make([]string, 3)
		for i, d := range r {
			if d.IsNull() {
				cells[i] = "NULL"
				continue
			}
			cells[i] = d.Format()
		}
		out = append(out, strings.Join(cells, "|"))
	}
	return out
}
