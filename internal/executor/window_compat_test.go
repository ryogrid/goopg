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
