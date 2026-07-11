package executor

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

func TestCompatWindowRowNumberPartitionOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 7}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, row_number() OVER (PARTITION BY grp ORDER BY val) AS rn FROM t ORDER BY grp, val, rn")
	want := []struct{ grp, val, rn int64 }{
		{grp: 1, val: 10, rn: 1},
		{grp: 1, val: 10, rn: 2},
		{grp: 1, val: 20, rn: 3},
		{grp: 2, val: 5, rn: 1},
		{grp: 2, val: 7, rn: 2},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w.grp {
			t.Fatalf("row[%d] grp=%+v want %d", i, rows[i][0], w.grp)
		}
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w.val {
			t.Fatalf("row[%d] val=%+v want %d", i, rows[i][1], w.val)
		}
		if rows[i][2].Kind != KindInt || rows[i][2].Int != w.rn {
			t.Fatalf("row[%d] rn=%+v want %d", i, rows[i][2], w.rn)
		}
	}
}

// TestCompatWindowNamedWindowClause pins the M0020 named-window slice
// end to end: a trailing `WINDOW w AS (...)` clause plus a bare
// `OVER w` reference on two different functions must produce exactly
// the same rows as writing the same PARTITION BY/ORDER BY spec out
// twice inline — the analyzer's resolveNamedWindowRefs copies the
// definition into each reference before the planner ever groups
// window functions by spec.
func TestCompatWindowNamedWindowClause(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 7}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	named := runQuery(t, ctx,
		"SELECT grp, val, row_number() OVER w AS rn, rank() OVER w AS rk FROM t "+
			"WINDOW w AS (PARTITION BY grp ORDER BY val) ORDER BY grp, val, rn")
	inline := runQuery(t, ctx,
		"SELECT grp, val, row_number() OVER (PARTITION BY grp ORDER BY val) AS rn, "+
			"rank() OVER (PARTITION BY grp ORDER BY val) AS rk FROM t ORDER BY grp, val, rn")

	if len(named) != len(inline) {
		t.Fatalf("named rows=%d inline rows=%d", len(named), len(inline))
	}
	for i := range inline {
		for col := range inline[i] {
			if named[i][col].Format() != inline[i][col].Format() {
				t.Fatalf("row[%d] col[%d] = %+v, want %+v (inline)", i, col, named[i][col], inline[i][col])
			}
		}
	}
}

// TestCompatWindowCombiningForms — the M0122-0004 combining-forms
// follow-up: an inline `OVER (win_name ORDER BY ...)` extending a named
// window's PARTITION BY, and a named window built on top of another named
// window, both compared byte-for-byte against the same spec written out
// fully inline (same fixture/shape as TestCompatWindowNamedWindowClause).
func TestCompatWindowCombiningForms(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 7}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	compare := func(sqlA, sqlB string) {
		t.Helper()
		a := runQuery(t, ctx, sqlA)
		b := runQuery(t, ctx, sqlB)
		if len(a) != len(b) {
			t.Fatalf("%q rows=%d, %q rows=%d", sqlA, len(a), sqlB, len(b))
		}
		for i := range a {
			for col := range a[i] {
				if a[i][col].Format() != b[i][col].Format() {
					t.Fatalf("row[%d] col[%d] = %+v, want %+v (from %q)", i, col, a[i][col], b[i][col], sqlB)
				}
			}
		}
	}

	// Inline OVER combining form: adds its own ORDER BY to a
	// PARTITION-BY-only named window.
	compare(
		"SELECT grp, val, row_number() OVER (w ORDER BY val) AS rn FROM t "+
			"WINDOW w AS (PARTITION BY grp) ORDER BY grp, val, rn",
		"SELECT grp, val, row_number() OVER (PARTITION BY grp ORDER BY val) AS rn FROM t "+
			"ORDER BY grp, val, rn")

	// Named window based on another named window.
	compare(
		"SELECT grp, val, rank() OVER w2 AS rk FROM t "+
			"WINDOW w1 AS (PARTITION BY grp), w2 AS (w1 ORDER BY val) ORDER BY grp, val, rk",
		"SELECT grp, val, rank() OVER (PARTITION BY grp ORDER BY val) AS rk FROM t "+
			"ORDER BY grp, val, rk")
}

func TestCompatWindowRankPeerGroups(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, rank() OVER (PARTITION BY grp ORDER BY val) AS rk FROM t ORDER BY grp, val, rk")
	want := []struct{ grp, val, rk int64 }{
		{grp: 1, val: 10, rk: 1},
		{grp: 1, val: 10, rk: 1},
		{grp: 1, val: 20, rk: 3},
		{grp: 2, val: 5, rk: 1},
		{grp: 2, val: 5, rk: 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w.grp {
			t.Fatalf("row[%d] grp=%+v want %d", i, rows[i][0], w.grp)
		}
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w.val {
			t.Fatalf("row[%d] val=%+v want %d", i, rows[i][1], w.val)
		}
		if rows[i][2].Kind != KindInt || rows[i][2].Int != w.rk {
			t.Fatalf("row[%d] rk=%+v want %d", i, rows[i][2], w.rk)
		}
	}
}

// TestCompatWindowDenseRankPeerGroups pins the M0122-0004 dense_rank()
// window function: unlike rank(), it never skips a value after a tie —
// consecutive peer groups are numbered 1, 2, 3, ... with no gaps.
// Expected values verified against upstream PostgreSQL 18.3.
func TestCompatWindowDenseRankPeerGroups(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, dense_rank() OVER (PARTITION BY grp ORDER BY val) AS dr FROM t ORDER BY grp, val, dr")
	want := []struct{ grp, val, dr int64 }{
		{grp: 1, val: 10, dr: 1},
		{grp: 1, val: 10, dr: 1},
		{grp: 1, val: 20, dr: 2},
		{grp: 2, val: 5, dr: 1},
		{grp: 2, val: 5, dr: 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w.grp {
			t.Fatalf("row[%d] grp=%+v want %d", i, rows[i][0], w.grp)
		}
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w.val {
			t.Fatalf("row[%d] val=%+v want %d", i, rows[i][1], w.val)
		}
		if rows[i][2].Kind != KindInt || rows[i][2].Int != w.dr {
			t.Fatalf("row[%d] dr=%+v want %d", i, rows[i][2], w.dr)
		}
	}
}

// TestCompatWindowAggregatesDefaultFrame pins the M0122-0004
// frame-consuming aggregate window functions (sum/count/avg/min/max)
// against their default frame: RANGE UNBOUNDED PRECEDING (cumulative,
// peer-inclusive) when ORDER BY is present. Expected values verified
// against upstream PostgreSQL 18.3 (see the Follow-up section of
// docs/design/0020-0001-window-parser-and-ast.md).
func TestCompatWindowAggregatesDefaultFrame(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, "+
			"sum(val) OVER (PARTITION BY grp ORDER BY val) AS s, "+
			"count(*) OVER (PARTITION BY grp ORDER BY val) AS c, "+
			"min(val) OVER (PARTITION BY grp ORDER BY val) AS mn, "+
			"max(val) OVER (PARTITION BY grp ORDER BY val) AS mx "+
			"FROM t ORDER BY grp, val")
	want := []struct{ grp, val, s, c, mn, mx int64 }{
		{1, 10, 20, 2, 10, 10},
		{1, 10, 20, 2, 10, 10},
		{1, 20, 40, 3, 10, 20},
		{2, 5, 10, 2, 5, 5},
		{2, 5, 10, 2, 5, 5},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		got := []int64{rows[i][0].Int, rows[i][1].Int, rows[i][2].Int, rows[i][3].Int, rows[i][4].Int, rows[i][5].Int}
		wantVals := []int64{w.grp, w.val, w.s, w.c, w.mn, w.mx}
		for col, g := range got {
			if rows[i][col].Kind != KindInt || g != wantVals[col] {
				t.Fatalf("row[%d]=%+v want %v", i, rows[i], wantVals)
			}
		}
	}
}

// TestCompatWindowAggregateNoOrderByWholePartition pins the other half
// of the default-frame rule: with no ORDER BY, the frame is the entire
// partition, so every row in a partition sees the same aggregate value.
func TestCompatWindowAggregateNoOrderByWholePartition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, sum(val) OVER (PARTITION BY grp) AS s FROM t ORDER BY grp, val")
	want := []struct{ grp, val, s int64 }{
		{1, 10, 40}, {1, 10, 40}, {1, 20, 40},
		{2, 5, 10}, {2, 5, 10},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Int != w.grp || rows[i][1].Int != w.val || rows[i][2].Kind != KindInt || rows[i][2].Int != w.s {
			t.Fatalf("row[%d]=%+v want grp=%d val=%d s=%d", i, rows[i], w.grp, w.val, w.s)
		}
	}
}

// TestCompatWindowAggregateFilterClause pins sum(x) FILTER (WHERE ...)
// OVER (...): rows failing the filter are excluded from the frame
// entirely, same as an ordinary FILTER aggregate (M0097-0007).
func TestCompatWindowAggregateFilterClause(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, sum(val) FILTER (WHERE val > 5) OVER (PARTITION BY grp ORDER BY val) AS sf FROM t ORDER BY grp, val")
	want := []struct {
		grp, val int64
		sfNull   bool
		sf       int64
	}{
		{1, 10, false, 20},
		{1, 10, false, 20},
		{1, 20, false, 40},
		{2, 5, true, 0},
		{2, 5, true, 0},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Int != w.grp || rows[i][1].Int != w.val {
			t.Fatalf("row[%d]=%+v want grp=%d val=%d", i, rows[i], w.grp, w.val)
		}
		if w.sfNull {
			if !rows[i][2].IsNull() {
				t.Fatalf("row[%d] sf=%+v want NULL", i, rows[i][2])
			}
		} else if rows[i][2].Kind != KindInt || rows[i][2].Int != w.sf {
			t.Fatalf("row[%d] sf=%+v want %d", i, rows[i][2], w.sf)
		}
	}
}

// TestCompatWindowValueFunctionsDefaultFrame pins the M0122-0004
// first_value/last_value/nth_value window functions against the same
// default frame (RANGE UNBOUNDED PRECEDING AND CURRENT ROW, peer-group
// inclusive) evalFrameAggFuncs already established for sum/count/etc.
// Expected values verified against upstream PostgreSQL 18.3.
func TestCompatWindowValueFunctionsDefaultFrame(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, "+
			"first_value(val) OVER (PARTITION BY grp ORDER BY val) AS fv, "+
			"last_value(val) OVER (PARTITION BY grp ORDER BY val) AS lv, "+
			"nth_value(val, 2) OVER (PARTITION BY grp ORDER BY val) AS nv "+
			"FROM t ORDER BY grp, val")
	want := []struct{ grp, val, fv, lv, nv int64 }{
		{1, 10, 10, 10, 10},
		{1, 10, 10, 10, 10},
		{1, 20, 10, 20, 10},
		{2, 5, 5, 5, 5},
		{2, 5, 5, 5, 5},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		got := []int64{rows[i][0].Int, rows[i][1].Int, rows[i][2].Int, rows[i][3].Int, rows[i][4].Int}
		wantVals := []int64{w.grp, w.val, w.fv, w.lv, w.nv}
		for col, g := range got {
			if rows[i][col].Kind != KindInt || g != wantVals[col] {
				t.Fatalf("row[%d]=%+v want %v", i, rows[i], wantVals)
			}
		}
	}
}

// TestCompatWindowNthValueOutOfFrameAndInvalidN pins nth_value's
// out-of-frame NULL result and the 22016 error for a non-positive n
// (matches window_nth_value in postgres/src/backend/utils/adt/windowfuncs.c).
func TestCompatWindowNthValueOutOfFrameAndInvalidN(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 20}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT val, nth_value(val, 5) OVER (ORDER BY val) AS nv FROM t ORDER BY val")
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	for i, r := range rows {
		if !r[1].IsNull() {
			t.Fatalf("row[%d] nv=%+v want NULL (n beyond frame)", i, r[1])
		}
	}

	sql := "SELECT val, nth_value(val, 0) OVER (ORDER BY val) FROM t"
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err == nil {
		t.Fatal("nth_value(val, 0) expected error, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22016" {
		t.Fatalf("nth_value(val, 0) err=%v want ExecError 22016", err)
	}
}

func TestCompatWindowRankNullPeersAsc(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 10}},
		{NullDatum},
		{NullDatum},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	// PostgreSQL ORDER BY val ASC (default) puts NULLs LAST.
	// So: 10 gets rank=1, NULLs (peers) get rank=2.
	// Outer ORDER BY rk, val: (10, 1) first, then (NULL, 2), (NULL, 2).
	rows := runQuery(t, ctx,
		"SELECT val, rank() OVER (ORDER BY val) AS rk FROM t ORDER BY rk, val")
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 10 || rows[0][1].Kind != KindInt || rows[0][1].Int != 1 {
		t.Fatalf("row0=%+v want 10 rank=1", rows[0])
	}
	if !rows[1][0].IsNull() || rows[1][1].Kind != KindInt || rows[1][1].Int != 2 {
		t.Fatalf("row1=%+v want NULL rank=2", rows[1])
	}
	if !rows[2][0].IsNull() || rows[2][1].Kind != KindInt || rows[2][1].Int != 2 {
		t.Fatalf("row2=%+v want NULL rank=2", rows[2])
	}
}

// TestCompatWindowNtileBuckets pins ntile()'s bucket-sizing algorithm: the
// first `total % nbuckets` buckets get one extra row (matches window_ntile
// in postgres/src/backend/utils/adt/windowfuncs.c) rather than
// concentrating the remainder in the last bucket.
func TestCompatWindowNtileBuckets(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, v := range []int64{1, 2, 3, 4, 5, 6, 7} {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: v}}); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx, "SELECT val, ntile(3) OVER (ORDER BY val) AS nt FROM t ORDER BY val")
	want := []int64{1, 1, 1, 2, 2, 3, 3}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w {
			t.Fatalf("row[%d] nt=%+v want %d", i, rows[i][1], w)
		}
	}
}

// TestCompatWindowNtileMoreBucketsThanRows pins the nbuckets > total case:
// each row becomes its own bucket (1..total); the remaining buckets are
// simply never assigned to any row.
func TestCompatWindowNtileMoreBucketsThanRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, v := range []int64{1, 2, 3} {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: v}}); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx, "SELECT val, ntile(10) OVER (ORDER BY val) AS nt FROM t ORDER BY val")
	want := []int64{1, 2, 3}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w {
			t.Fatalf("row[%d] nt=%+v want %d", i, rows[i][1], w)
		}
	}
}

// TestCompatWindowNtileInvalidArgument pins the 22014
// invalid_argument_for_ntile_function error for a non-positive bucket
// count (matches window_ntile's ERRCODE_INVALID_ARGUMENT_FOR_NTILE).
func TestCompatWindowNtileInvalidArgument(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: 1}}); err != nil {
		t.Fatal(err)
	}

	sql := "SELECT val, ntile(0) OVER (ORDER BY val) FROM t"
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err == nil {
		t.Fatal("ntile(0) expected error, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22014" {
		t.Fatalf("ntile(0) err=%v want ExecError 22014", err)
	}
}

// TestCompatWindowPercentRankAndCumeDist pins percent_rank()/cume_dist()'s
// tie-aware formulas: (rank-1)/(total-1) and NP/NR where NP is the 1-based
// end position of the current row's peer group (matches
// window_percent_rank/window_cume_dist in
// postgres/src/backend/utils/adt/windowfuncs.c).
func TestCompatWindowPercentRankAndCumeDist(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 20}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT val, percent_rank() OVER (ORDER BY val) AS pr, cume_dist() OVER (ORDER BY val) AS cd FROM t ORDER BY val")
	want := []struct{ pr, cd float64 }{
		{0, 0.4}, {0, 0.4}, {0.5, 0.8}, {0.5, 0.8}, {1, 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		pr, err := strconv.ParseFloat(rows[i][1].StringValue(), 64)
		if err != nil {
			t.Fatalf("row[%d] pr parse: %v (%+v)", i, err, rows[i][1])
		}
		cd, err := strconv.ParseFloat(rows[i][2].StringValue(), 64)
		if err != nil {
			t.Fatalf("row[%d] cd parse: %v (%+v)", i, err, rows[i][2])
		}
		if pr != w.pr {
			t.Fatalf("row[%d] pr=%v want %v", i, pr, w.pr)
		}
		if cd != w.cd {
			t.Fatalf("row[%d] cd=%v want %v", i, cd, w.cd)
		}
	}
}

// TestCompatWindowPercentRankSingleRow pins the "return zero if there's
// only one row, per spec" special case in window_percent_rank.
func TestCompatWindowPercentRankSingleRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: 1}}); err != nil {
		t.Fatal(err)
	}

	rows := runQuery(t, ctx, "SELECT percent_rank() OVER (ORDER BY val) AS pr FROM t")
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	pr, err := strconv.ParseFloat(rows[0][0].StringValue(), 64)
	if err != nil {
		t.Fatalf("pr parse: %v", err)
	}
	if pr != 0 {
		t.Fatalf("pr=%v want 0", pr)
	}
}

// TestCompatWindowExplicitRowsFrameSliding pins an explicit `ROWS
// BETWEEN 1 PRECEDING AND 1 FOLLOWING` sliding frame for sum/
// first_value/last_value/nth_value across a partition boundary
// (M0122-0004 frame-clause slice) — cross-checked row-for-row against
// a scratch upstream PostgreSQL 18.3 instance.
func TestCompatWindowExplicitRowsFrameSliding(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 30}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 40}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 50}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 100}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 200}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, "+
			"sum(val) OVER w AS sliding, "+
			"first_value(val) OVER w AS fv, "+
			"last_value(val) OVER w AS lv, "+
			"nth_value(val, 2) OVER w AS nv2 "+
			"FROM t "+
			"WINDOW w AS (PARTITION BY grp ORDER BY val ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING) "+
			"ORDER BY grp, val")
	want := []struct{ grp, val, sliding, fv, lv, nv2 int64 }{
		{1, 10, 30, 10, 20, 20},
		{1, 20, 60, 10, 30, 20},
		{1, 30, 90, 20, 40, 30},
		{1, 40, 120, 30, 50, 40},
		{1, 50, 90, 40, 50, 50},
		{2, 100, 300, 100, 200, 200},
		{2, 200, 300, 100, 200, 200},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		got := []int64{rows[i][0].Int, rows[i][1].Int, rows[i][2].Int, rows[i][3].Int, rows[i][4].Int, rows[i][5].Int}
		wantVals := []int64{w.grp, w.val, w.sliding, w.fv, w.lv, w.nv2}
		for col, g := range got {
			if rows[i][col].Kind != KindInt || g != wantVals[col] {
				t.Fatalf("row[%d]=%+v want %v", i, rows[i], wantVals)
			}
		}
	}
}

// TestCompatWindowExplicitFrameExcludeCurrentRow pins `ROWS BETWEEN 1
// PRECEDING AND 1 FOLLOWING EXCLUDE CURRENT ROW` and `ROWS BETWEEN
// UNBOUNDED PRECEDING AND CURRENT ROW EXCLUDE CURRENT ROW` — the
// second shape starts at count()=0 on the first row since UNBOUNDED
// PRECEDING..CURRENT ROW is just row 0 itself and EXCLUDE CURRENT ROW
// removes it — cross-checked against a scratch upstream PostgreSQL
// 18.3 instance.
func TestCompatWindowExplicitFrameExcludeCurrentRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 30}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 40}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 50}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT val, sum(val) OVER (PARTITION BY grp ORDER BY val ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING EXCLUDE CURRENT ROW) AS excl_cur "+
			"FROM t ORDER BY val")
	wantExclCur := []struct{ val, exclCur int64 }{
		{10, 20},
		{20, 40},
		{30, 60},
		{40, 80},
		{50, 40},
	}
	if len(rows) != len(wantExclCur) {
		t.Fatalf("rows=%d want %d", len(rows), len(wantExclCur))
	}
	for i, w := range wantExclCur {
		if rows[i][0].Int != w.val || rows[i][1].Kind != KindInt || rows[i][1].Int != w.exclCur {
			t.Fatalf("row[%d]=%+v want val=%d excl_cur=%d", i, rows[i], w.val, w.exclCur)
		}
	}

	rows = runQuery(t, ctx,
		"SELECT val, count(*) OVER (PARTITION BY grp ORDER BY val ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW EXCLUDE CURRENT ROW) AS cnt_excl "+
			"FROM t ORDER BY val")
	wantCntExcl := []struct{ val, cntExcl int64 }{
		{10, 0},
		{20, 1},
		{30, 2},
		{40, 3},
		{50, 4},
	}
	if len(rows) != len(wantCntExcl) {
		t.Fatalf("rows=%d want %d", len(rows), len(wantCntExcl))
	}
	for i, w := range wantCntExcl {
		if rows[i][0].Int != w.val || rows[i][1].Kind != KindInt || rows[i][1].Int != w.cntExcl {
			t.Fatalf("row[%d]=%+v want val=%d cnt_excl=%d", i, rows[i], w.val, w.cntExcl)
		}
	}
}

// TestCompatWindowExplicitFrameExcludeGroupAndTies pins EXCLUDE GROUP
// vs EXCLUDE TIES against a tied ORDER BY value: GROUP removes the
// whole peer group (including the current row), TIES removes the
// peer group except the current row itself — cross-checked against a
// scratch upstream PostgreSQL 18.3 instance.
func TestCompatWindowExplicitFrameExcludeGroupAndTies(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 30}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT val, sum(val) OVER (ORDER BY val ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING EXCLUDE GROUP) AS excl_group "+
			"FROM t ORDER BY val")
	wantGroup := []struct{ val, exclGroup int64 }{
		{10, 70},
		{20, 40},
		{20, 40},
		{30, 50},
	}
	if len(rows) != len(wantGroup) {
		t.Fatalf("rows=%d want %d", len(rows), len(wantGroup))
	}
	for i, w := range wantGroup {
		if rows[i][0].Int != w.val || rows[i][1].Kind != KindInt || rows[i][1].Int != w.exclGroup {
			t.Fatalf("row[%d]=%+v want val=%d excl_group=%d", i, rows[i], w.val, w.exclGroup)
		}
	}

	rows = runQuery(t, ctx,
		"SELECT val, sum(val) OVER (ORDER BY val ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING EXCLUDE TIES) AS excl_ties "+
			"FROM t ORDER BY val")
	wantTies := []struct{ val, exclTies int64 }{
		{10, 80},
		{20, 60},
		{20, 60},
		{30, 80},
	}
	if len(rows) != len(wantTies) {
		t.Fatalf("rows=%d want %d", len(rows), len(wantTies))
	}
	for i, w := range wantTies {
		if rows[i][0].Int != w.val || rows[i][1].Kind != KindInt || rows[i][1].Int != w.exclTies {
			t.Fatalf("row[%d]=%+v want val=%d excl_ties=%d", i, rows[i], w.val, w.exclTies)
		}
	}
}

// TestCompatWindowFrameNegativeOffsetRejected pins nodeWindowAgg.c's
// runtime negative-offset check (22013) — a negative frame offset
// can't be caught until it's evaluated, so this is an executor-time
// error like LIMIT/OFFSET's type check, not a parse/analyze error.
// Matches upstream's exact wording ("frame starting offset must not
// be negative").
func TestCompatWindowFrameNegativeOffsetRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: 1}}); err != nil {
		t.Fatal(err)
	}

	sql := "SELECT sum(val) OVER (ORDER BY val ROWS BETWEEN -1 PRECEDING AND CURRENT ROW) FROM t"
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err == nil {
		t.Fatal("expected error, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22013" {
		t.Fatalf("err=%v want ExecError 22013", err)
	}
}

// TestCompatWindowExplicitGroupsFrameSliding pins GROUPS-mode frame
// arithmetic — bounds counted in ORDER BY peer groups rather than rows
// (M0122-0004 RANGE/GROUPS follow-up) — against duplicate-key data so
// GROUPS genuinely diverges from an equivalent ROWS frame. grp=1 has
// three peer groups on val (10,10 / 20 / 30,30); grp=2 has two
// singleton groups (100 / 200). Cross-checked against a real
// PostgreSQL 18.3 instance (`GROUPS BETWEEN 1 PRECEDING AND 1
// FOLLOWING`): sliding={20,20,20,30,30}→{40,40,100,80,80},
// fv={10,10,10,20,20}, lv={20,20,30,30,30}.
func TestCompatWindowExplicitGroupsFrameSliding(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 30}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 30}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 100}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 200}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, "+
			"sum(val) OVER w AS sliding, "+
			"first_value(val) OVER w AS fv, "+
			"last_value(val) OVER w AS lv, "+
			"count(*) OVER w AS cnt "+
			"FROM t "+
			"WINDOW w AS (PARTITION BY grp ORDER BY val GROUPS BETWEEN 1 PRECEDING AND 1 FOLLOWING) "+
			"ORDER BY grp, val")
	want := []struct{ grp, val, sliding, fv, lv, cnt int64 }{
		{1, 10, 40, 10, 20, 3},
		{1, 10, 40, 10, 20, 3},
		{1, 20, 100, 10, 30, 5},
		{1, 30, 80, 20, 30, 3},
		{1, 30, 80, 20, 30, 3},
		{2, 100, 300, 100, 200, 2},
		{2, 200, 300, 100, 200, 2},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		got := []int64{rows[i][0].Int, rows[i][1].Int, rows[i][2].Int, rows[i][3].Int, rows[i][4].Int, rows[i][5].Int}
		wantVals := []int64{w.grp, w.val, w.sliding, w.fv, w.lv, w.cnt}
		for col, g := range got {
			if rows[i][col].Kind != KindInt || g != wantVals[col] {
				t.Fatalf("row[%d]=%+v want %v", i, rows[i], wantVals)
			}
		}
	}
}

// TestCompatWindowGroupsUnboundedPrecedingCumulative pins the default
// end-bound (CURRENT ROW, which in GROUPS mode means the current row's
// last peer) for a cumulative `GROUPS UNBOUNDED PRECEDING` frame —
// cross-checked against a real PostgreSQL 18.3 instance: val
// {10,10,20,30,30} → cum {20,20,40,100,100}.
func TestCompatWindowGroupsUnboundedPrecedingCumulative(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 30}},
		{{Kind: KindInt, Int: 30}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT val, sum(val) OVER (ORDER BY val GROUPS UNBOUNDED PRECEDING) AS cum FROM t ORDER BY val")
	want := []int64{20, 20, 40, 100, 100}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w {
			t.Fatalf("row[%d]=%+v want cum=%d", i, rows[i], w)
		}
	}
}

// TestCompatWindowExplicitRangePeers pins RANGE-mode frame arithmetic
// for the non-offset bound kinds (M0122-0004 RANGE follow-up). RANGE
// CURRENT ROW means "the current row and ALL its ORDER BY peers"
// (unlike ROWS, where CURRENT ROW is the single row) — so on
// duplicate-key data it genuinely diverges from an equivalent ROWS
// frame. Uses the same seed as the GROUPS test; grp=1 has three peer
// groups on val (10,10 / 20 / 30,30). Every expectation below was
// cross-checked against a real PostgreSQL 18.3 instance.
func TestCompatWindowExplicitRangePeers(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 30}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 30}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 100}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 200}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	// RANGE BETWEEN CURRENT ROW AND CURRENT ROW: the whole peer group.
	rows := runQuery(t, ctx,
		"SELECT grp, val, "+
			"sum(val) OVER w AS s, "+
			"count(*) OVER w AS c, "+
			"first_value(val) OVER w AS fv, "+
			"last_value(val) OVER w AS lv "+
			"FROM t "+
			"WINDOW w AS (PARTITION BY grp ORDER BY val RANGE BETWEEN CURRENT ROW AND CURRENT ROW) "+
			"ORDER BY grp, val")
	want := []struct{ grp, val, s, c, fv, lv int64 }{
		{1, 10, 20, 2, 10, 10},
		{1, 10, 20, 2, 10, 10},
		{1, 20, 20, 1, 20, 20},
		{1, 30, 60, 2, 30, 30},
		{1, 30, 60, 2, 30, 30},
		{2, 100, 100, 1, 100, 100},
		{2, 200, 200, 1, 200, 200},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		got := []int64{rows[i][0].Int, rows[i][1].Int, rows[i][2].Int, rows[i][3].Int, rows[i][4].Int, rows[i][5].Int}
		wantVals := []int64{w.grp, w.val, w.s, w.c, w.fv, w.lv}
		for col, g := range got {
			if rows[i][col].Kind != KindInt || g != wantVals[col] {
				t.Fatalf("row[%d]=%+v want %v", i, rows[i], wantVals)
			}
		}
	}

	// RANGE BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING: from the start
	// of the current peer group through the partition end — grp=1 →
	// {100,100,80,60,60} (contrast the cumulative default frame).
	rows2 := runQuery(t, ctx,
		"SELECT val, sum(val) OVER (PARTITION BY grp ORDER BY val "+
			"RANGE BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) AS r "+
			"FROM t WHERE grp = 1 ORDER BY val")
	wantR := []int64{100, 100, 80, 60, 60}
	if len(rows2) != len(wantR) {
		t.Fatalf("rows2=%d want %d", len(rows2), len(wantR))
	}
	for i, w := range wantR {
		if rows2[i][1].Kind != KindInt || rows2[i][1].Int != w {
			t.Fatalf("rows2[%d]=%+v want r=%d", i, rows2[i], w)
		}
	}

	// RANGE without ORDER BY: all rows are peers → the whole partition
	// for every row (legal for non-offset RANGE, unlike GROUPS).
	rows3 := runQuery(t, ctx,
		"SELECT val, sum(val) OVER (PARTITION BY grp "+
			"RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS s "+
			"FROM t WHERE grp = 1 ORDER BY val")
	for i := range rows3 {
		if rows3[i][1].Kind != KindInt || rows3[i][1].Int != 100 {
			t.Fatalf("rows3[%d]=%+v want s=100", i, rows3[i])
		}
	}
}

// TestCompatWindowRangeUnboundedPrecedingCumulative pins the default
// frame spelled explicitly (RANGE BETWEEN UNBOUNDED PRECEDING AND
// CURRENT ROW): cumulative, peer-inclusive. Cross-checked against a
// real PostgreSQL 18.3 instance: val {10,10,20,30,30} → cum
// {20,20,40,100,100} (identical to the GROUPS UNBOUNDED PRECEDING
// case, since both treat CURRENT ROW as the whole peer group).
func TestCompatWindowRangeUnboundedPrecedingCumulative(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 30}},
		{{Kind: KindInt, Int: 30}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT val, sum(val) OVER (ORDER BY val RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS cum FROM t ORDER BY val")
	want := []int64{20, 20, 40, 100, 100}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w {
			t.Fatalf("row[%d]=%+v want cum=%d", i, rows[i], w)
		}
	}
}

// TestCompatWindowRangeValueOffset pins RANGE-mode frames with a value
// offset bound (M0122-0004 follow-up): a row is in the frame when its
// ORDER BY value falls within currentValue±offset (PostgreSQL's in_range),
// unlike ROWS (physical row count) or GROUPS (peer-group count). Every
// expectation below was cross-checked byte-for-byte against a live
// PostgreSQL 18.3 instance.
func TestCompatWindowRangeValueOffset(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	// Distinct-value seed 1,3,5,7 exercises gapped value arithmetic.
	for _, v := range []int64{1, 3, 5, 7} {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: v}}); err != nil {
			t.Fatal(err)
		}
	}

	// Symmetric: RANGE BETWEEN 2 PRECEDING AND 2 FOLLOWING → value in
	// [cur-2, cur+2]. 1→{1,3}=4; 3→{1,3,5}=9; 5→{3,5,7}=15; 7→{5,7}=12.
	assertRangeSums(t, ctx,
		"SELECT val, sum(val) OVER (ORDER BY val RANGE BETWEEN 2 PRECEDING AND 2 FOLLOWING) AS s FROM t ORDER BY val",
		[]int64{4, 9, 15, 12})

	// Preceding-and-current: [cur-2, cur]. 1→{1}=1; 3→{1,3}=4;
	// 5→{3,5}=8; 7→{5,7}=12.
	assertRangeSums(t, ctx,
		"SELECT val, sum(val) OVER (ORDER BY val RANGE BETWEEN 2 PRECEDING AND CURRENT ROW) AS s FROM t ORDER BY val",
		[]int64{1, 4, 8, 12})

	// Asymmetric: [cur-1, cur+2]. 1→{1,3}=4; 3→{3,5}=8; 5→{5,7}=12;
	// 7→{7}=7.
	assertRangeSums(t, ctx,
		"SELECT val, sum(val) OVER (ORDER BY val RANGE BETWEEN 1 PRECEDING AND 2 FOLLOWING) AS s FROM t ORDER BY val",
		[]int64{4, 8, 12, 7})

	// DESC ordering: the frame set is the same value window [cur-2,cur+2],
	// so the per-row sums match the ASC symmetric case, in val-desc order.
	// 7→{5,7}=12; 5→{3,5,7}=15; 3→{1,3,5}=9; 1→{1,3}=4.
	assertRangeSums(t, ctx,
		"SELECT val, sum(val) OVER (ORDER BY val DESC RANGE BETWEEN 2 PRECEDING AND 2 FOLLOWING) AS s FROM t ORDER BY val DESC",
		[]int64{12, 15, 9, 4})
}

// TestCompatWindowRangeValueOffsetPeersAndFrameFuncs pins peer handling
// (duplicate ORDER BY values share a frame) and first_value/last_value
// under a value-offset RANGE frame, cross-checked against PostgreSQL 18.3.
func TestCompatWindowRangeValueOffsetPeersAndFrameFuncs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, v := range []int64{1, 3, 3, 5} {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: v}}); err != nil {
			t.Fatal(err)
		}
	}

	// RANGE BETWEEN 1 PRECEDING AND 1 FOLLOWING → value in [cur-1, cur+1].
	// 1→{1}=1,c1; 3→{3,3}=6,c2; 3→{3,3}=6,c2; 5→{5}=5,c1.
	rows := runQuery(t, ctx,
		"SELECT val, sum(val) OVER w AS s, count(*) OVER w AS c, "+
			"first_value(val) OVER w AS fv, last_value(val) OVER w AS lv "+
			"FROM t WINDOW w AS (ORDER BY val RANGE BETWEEN 1 PRECEDING AND 1 FOLLOWING) "+
			"ORDER BY val")
	want := []struct{ val, s, c, fv, lv int64 }{
		{1, 1, 1, 1, 1},
		{3, 6, 2, 3, 3},
		{3, 6, 2, 3, 3},
		{5, 5, 1, 5, 5},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		got := []int64{rows[i][0].Int, rows[i][1].Int, rows[i][2].Int, rows[i][3].Int, rows[i][4].Int}
		wv := []int64{w.val, w.s, w.c, w.fv, w.lv}
		for col, g := range got {
			if rows[i][col].Kind != KindInt || g != wv[col] {
				t.Fatalf("row[%d]=%+v want %v", i, rows[i], wv)
			}
		}
	}
}

// TestCompatWindowRangeValueOffsetNulls pins NULL handling for value-offset
// RANGE frames: a non-null current row never frames a null-valued row, and
// a null current row's frame is exactly its null peer block. Cross-checked
// against PostgreSQL 18.3 (ORDER BY val → NULLS LAST for ASC).
func TestCompatWindowRangeValueOffsetNulls(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{{{Kind: KindInt, Int: 1}}, {{Kind: KindInt, Int: 3}}, {NullDatum}}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	// Sorted ASC NULLS LAST: 1, 3, NULL. RANGE BETWEEN 1 PRECEDING AND 1
	// FOLLOWING. 1→{1}=1,c1; 3→{3}=3,c1; NULL→null peer block {NULL}:
	// sum(val)=NULL, count(*)=1.
	rows := runQuery(t, ctx,
		"SELECT val, sum(val) OVER w AS s, count(*) OVER w AS c "+
			"FROM t WINDOW w AS (ORDER BY val RANGE BETWEEN 1 PRECEDING AND 1 FOLLOWING) "+
			"ORDER BY val")
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	// row 0: val=1
	if rows[0][0].Int != 1 || rows[0][1].Int != 1 || rows[0][2].Int != 1 {
		t.Fatalf("row0=%+v want val=1 s=1 c=1", rows[0])
	}
	// row 1: val=3
	if rows[1][0].Int != 3 || rows[1][1].Int != 3 || rows[1][2].Int != 1 {
		t.Fatalf("row1=%+v want val=3 s=3 c=1", rows[1])
	}
	// row 2: val=NULL, sum=NULL, count=1
	if !rows[2][0].IsNull() {
		t.Fatalf("row2 val=%+v want NULL", rows[2][0])
	}
	if !rows[2][1].IsNull() {
		t.Fatalf("row2 sum=%+v want NULL", rows[2][1])
	}
	if rows[2][2].Kind != KindInt || rows[2][2].Int != 1 {
		t.Fatalf("row2 count=%+v want 1", rows[2][2])
	}
}

// TestWindowRangeOffsetNegative pins the runtime 22013 for a negative RANGE
// offset (PostgreSQL rejects it inside every in_range function).
func TestWindowRangeOffsetNegative(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: 1}}); err != nil {
		t.Fatal(err)
	}
	_, err := runQueryWithErr(ctx,
		"SELECT sum(val) OVER (ORDER BY val RANGE BETWEEN -1 PRECEDING AND CURRENT ROW) FROM t")
	if err == nil {
		t.Fatal("want error for negative RANGE offset, got nil")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "22013" {
		t.Fatalf("err=%v want ExecError 22013", err)
	}
}

// TestRangeOffsetNegativeIntervalSign pins that a RANGE interval offset's
// validity is decided by its linear span (interval_sign), NOT the sign of any
// individual month/day/micro component — matching in_range_interval_interval /
// interval_cmp_value in PostgreSQL's timestamp.c. An offset like '1 mon -10 days'
// has a +20-day span and is a valid (positive) offset even though its day field
// is negative; the pre-fix per-component heuristic wrongly rejected it (22013).
func TestRangeOffsetNegativeIntervalSign(t *testing.T) {
	const usecPerDay = int64(24 * 60 * 60 * 1_000_000)
	cases := []struct {
		name         string
		months       int32
		days         int32
		micros       int64
		wantNegative bool
	}{
		// Net span > 0 → accepted (this is the edge the old heuristic broke).
		{"1mon_minus_10days", 1, -10, 0, false}, // 30-10 = +20 days
		{"minus_10days_plus_1mon_micros", 1, -10, 5, false},
		{"pure_positive_days", 0, 5, 0, false},
		{"pure_positive_micros", 0, 0, 123, false},
		{"zero_interval", 0, 0, 0, false}, // span 0 is not negative
		// Net span < 0 → rejected.
		{"minus_1mon_10days", -1, 10, 0, true}, // -30+10 = -20 days
		{"pure_negative_day", 0, -1, 0, true},
		{"pure_negative_micros", 0, 0, -1, true},
		{"positive_days_negative_micros_net_pos", 0, 1, -usecPerDay / 2, false}, // +0.5 day
		{"one_day_minus_one_day_micros_net_zero", 0, 1, -usecPerDay, false},     // +1 day -1 day = span 0
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			iv := NewIntervalDatumFull(c.months, c.days, c.micros)
			got := rangeOffsetNegative(iv)
			if got != c.wantNegative {
				t.Fatalf("rangeOffsetNegative(%d mon %d day %d us) = %v, want %v",
					c.months, c.days, c.micros, got, c.wantNegative)
			}
		})
	}
}

// assertRangeSums runs sql (whose second projected column is a sum) and
// checks the per-row sums against want.
func assertRangeSums(t *testing.T, ctx *Context, sql string, want []int64) {
	t.Helper()
	rows := runQuery(t, ctx, sql)
	if len(rows) != len(want) {
		t.Fatalf("%s: rows=%d want %d", sql, len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w {
			t.Fatalf("%s: row[%d]=%+v want s=%d", sql, i, rows[i], w)
		}
	}
}
