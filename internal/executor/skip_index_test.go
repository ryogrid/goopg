package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0005v — btree skip-scan executor proof.
//
// PG18's `_bt_skiparray` mechanism probes an equality qual on a NON-LEADING
// index column by procedurally generating the skipped prefix's distinct
// values and running a bounded descent per value (nbtpreprocesskeys.c).
// goopg's equivalent is `indexScanOp.rescanSkip`/`skipNextGroup` — the
// planner builds the probe shape (`IndexScan.SkipPrefix` + `Keys` on the
// bound run); these fixtures hand-build that node, which also pins the
// executor contract independently of the search-side admission.
//
// Fixture: items(a, b, c) with a btree on (a, b). Prefix values {1,2,3,5,7}
// with duplicates inside each group, including groups whose `b` never
// matches the probe (a=1 has a b=20 member alongside non-matches; a=7 has
// no b=20 at all).
func setupSkipFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)

	if err := runDDL(t, ctx, "CREATE TABLE items (a int, b int, c varchar)"); err != nil {
		cleanup()
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	rel := ctx.Catalog.RelFileNode(tbl)
	rows := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}, NewStringDatum("x")},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}, NewStringDatum("y")},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 30}, NewStringDatum("z")},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 20}, NewStringDatum("p")},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 40}, NewStringDatum("q")},
		{{Kind: KindInt, Int: 3}, {Kind: KindInt, Int: 20}, NewStringDatum("r")},
		{{Kind: KindInt, Int: 5}, {Kind: KindInt, Int: 20}, NewStringDatum("s")},
		{{Kind: KindInt, Int: 7}, {Kind: KindInt, Int: 50}, NewStringDatum("t")},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			cleanup()
			t.Fatalf("writeHeapRow: %v", err)
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX items_ab ON items (a, b)"); err != nil {
		cleanup()
		t.Fatalf("CREATE INDEX: %v", err)
	}
	return ctx, cleanup
}

// skipPlan is the planner-off construction of the shape the search emits:
// `IndexScan{SkipPrefix: 1, Keys: [b = key]}` — an equality probe on the
// second index column with the first enumerated.
func skipPlan(t *testing.T, ctx *Context, keys ...optimizer.Expr) *optimizer.IndexScan {
	t.Helper()
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	if !ok {
		t.Fatal("lookup items")
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "items_ab"})
	if !ok {
		t.Fatal("lookup items_ab")
	}
	return &optimizer.IndexScan{Table: tbl, Index: idx, SkipPrefix: 1, Keys: keys}
}

// skipScanRows drives the built op and returns the (a, b) pairs it emitted,
// which must arrive in index order — ascending (a, b) — because each group
// cursor enumerates prefix groups in order and its own bounded descent is
// ordered inside the group.
func skipScanRows(t *testing.T, ctx *Context, plan *optimizer.IndexScan) [][2]int64 {
	t.Helper()
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	io, ok := op.(*indexScanOp)
	if !ok {
		t.Fatalf("built %T, want *indexScanOp", op)
	}
	if err := io.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer io.Close()
	var out [][2]int64
	for {
		slot, err := io.Next()
		if err == EOF {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		row := slotRow(slot)
		out = append(out, [2]int64{row[0].Int, row[1].Int})
	}
}

func TestSkipScanEnumeratesGroups(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()

	// b = 20 skipping a: prefix groups 1, 2, 3, 5 each have a member; 7
	// does not. The group count (5) exceeds the match count (4) — the
	// enumeration must not emit a row for groups without a match and must
	// not merge groups (each group's descent is bounded to its prefix).
	got := skipScanRows(t, ctx, skipPlan(t, ctx, &optimizer.IntegerConst{Value: 20}))
	want := [][2]int64{{1, 20}, {2, 20}, {3, 20}, {5, 20}}
	if len(got) != len(want) {
		t.Fatalf("rows=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %v, want %v (out=%v)", i, got[i], want[i], got)
		}
	}
}

func TestSkipScanRescanReEnumerates(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()

	// The NLI probe contract: Open once, Rescan per outer row — the
	// enumeration state must rebuild, not append to the prior scan's.
	op, err := Build(skipPlan(t, ctx, &optimizer.IntegerConst{Value: 20}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	io := op.(*indexScanOp)
	if err := io.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer io.Close()
	drain := func() int {
		n := 0
		for {
			_, err := io.Next()
			if err == EOF {
				return n
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			n++
		}
	}
	if n := drain(); n != 4 {
		t.Fatalf("first scan rows=%d, want 4", n)
	}
	if err := io.Rescan(nil, 0); err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	if n := drain(); n != 4 {
		t.Fatalf("rescan rows=%d, want 4", n)
	}
}

func TestSkipScanEarlyStop(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()

	// A semi/anti join or LIMIT consumer reads only the head: the first
	// group's first match must come out without enumerating the tail
	// groups — and Close must be clean after the partial read.
	op, err := Build(skipPlan(t, ctx, &optimizer.IntegerConst{Value: 20}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	io := op.(*indexScanOp)
	if err := io.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	slot, err := io.Next()
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if row := slotRow(slot); row[0].Int != 1 || row[1].Int != 20 {
		t.Fatalf("first row = (%v,%v), want (1,20)", row[0].Int, row[1].Int)
	}
	if err := io.Close(); err != nil {
		t.Fatalf("Close after early stop: %v", err)
	}
}

func TestSkipScanNoMatchAndNullProbe(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()

	// A bound value no suffix carries: enumeration runs, no group
	// matches — the result is empty, not an error and not a spill of
	// neighbouring values.
	if got := skipScanRows(t, ctx, skipPlan(t, ctx, &optimizer.IntegerConst{Value: 99})); len(got) != 0 {
		t.Fatalf("b=99 rows=%v, want empty", got)
	}
	// `b = NULL` is never true in ANY group: the probe empties the whole
	// scan up front, the same rule lookupKeys applies to a NULL probe key.
	if got := skipScanRows(t, ctx, skipPlan(t, ctx, &optimizer.NullConst{})); len(got) != 0 {
		t.Fatalf("b=NULL rows=%v, want empty", got)
	}
}

func TestSkipScanRejectsMixedShapes(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()

	// The node-level contract is fail-closed: SkipPrefix composes with
	// Keys alone. Every other probe field — Key, SAOP, range bounds,
	// RangePrefix — is a planner bug and must error XX000 at Rescan
	// rather than be silently interpreted.
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "items_ab"})
	bad := []*optimizer.IndexScan{
		{Table: tbl, Index: idx, SkipPrefix: 1, Key: &optimizer.IntegerConst{Value: 1}, Keys: []optimizer.Expr{&optimizer.IntegerConst{Value: 20}}},
		{Table: tbl, Index: idx, SkipPrefix: 1, Keys: []optimizer.Expr{&optimizer.IntegerConst{Value: 20}}, LowKey: &optimizer.IntegerConst{Value: 5}},
		{Table: tbl, Index: idx, SkipPrefix: 1, Keys: []optimizer.Expr{&optimizer.IntegerConst{Value: 20}}, SAOPKeys: []optimizer.Expr{&optimizer.IntegerConst{Value: 1}}},
		{Table: tbl, Index: idx, SkipPrefix: 1, Keys: []optimizer.Expr{&optimizer.IntegerConst{Value: 20}}, RangePrefix: []optimizer.Expr{&optimizer.IntegerConst{Value: 1}}},
		// SkipPrefix past the key's own columns is a contract break too.
		{Table: tbl, Index: idx, SkipPrefix: 2, Keys: []optimizer.Expr{&optimizer.IntegerConst{Value: 20}}},
		// And SkipPrefix with no bound run is not a scan shape.
		{Table: tbl, Index: idx, SkipPrefix: 1},
	}
	for i, plan := range bad {
		op, err := Build(plan)
		if err != nil {
			t.Fatalf("case %d Build: %v", i, err)
		}
		io := op.(*indexScanOp)
		if err := io.Open(ctx); err == nil {
			io.Close()
			t.Fatalf("case %d: incompatible skip shape was accepted", i)
		} else if ee, ok := err.(*ExecError); !ok || ee.Code != "XX000" {
			io.Close()
			t.Fatalf("case %d: error = %v, want XX000", i, err)
		} else {
			io.Close()
		}
	}
}

func TestSkipExplainIndexCondNamesBoundColumn(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()

	// EXPLAIN must name the column the probe actually binds — the
	// skip-arm rendering of formatIndexCond — never the leading column
	// the executor enumerates. A `b = 20` probe must not print `a = 20`.
	plan := skipPlan(t, ctx, &optimizer.IntegerConst{Value: 20})
	cond := formatIndexCond(plan, nil)
	if !strings.Contains(cond, "b = ") {
		t.Fatalf("Index Cond = %q, want it to bind column b", cond)
	}
	if strings.Contains(cond, "a = ") {
		t.Fatalf("Index Cond = %q attributes the probe to skipped column a", cond)
	}
}
