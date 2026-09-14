package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestIsNarrowableLeaf pins exactly the leaf shapes the M0139-S1 hook
// recognises: a bare base-relation scan of any kind goopg has, optionally
// wrapped in one or more *Filter (attachRelationLocalFilters). Anything
// else — a join, a Project, an unrecognised node, a Filter with no child —
// must decline, since those are either not this seam's job or would panic
// walking a nil child.
func TestIsNarrowableLeaf(t *testing.T) {
	seq := &SeqScan{schema: noSchema("a")}
	idx := &IndexScan{schema: noSchema("a")}
	ios := &IndexOnlyScan{schema: noSchema("a")}
	bhs := &BitmapHeapScan{schema: noSchema("a")}

	for _, tc := range []struct {
		name string
		n    Node
		want bool
	}{
		{"bare SeqScan", seq, true},
		{"bare IndexScan", idx, true},
		{"bare IndexOnlyScan", ios, true},
		{"bare BitmapHeapScan", bhs, true},
		{"Filter over SeqScan", &Filter{Child: seq}, true},
		{"Filter over Filter over SeqScan (nested)", &Filter{Child: &Filter{Child: seq}}, true},
		{"Filter with nil child", &Filter{Child: nil}, false},
		{"a join, not a leaf", &Join{}, false},
		{"already a Project", &Project{Child: seq}, false},
		{"an unrelated Node kind", &noNode{sch: noSchema("a")}, false},
	} {
		if got := isNarrowableLeaf(tc.n); got != tc.want {
			t.Errorf("%s: isNarrowableLeaf = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestNarrowJoinLegDeclinesButCounts pins S1's whole contract in one place:
// the function NEVER changes its (Node, outputLayout) pair — the pointer
// and the layout slice's contents must survive byte-for-byte — but it DOES
// count exactly the calls where a real narrowing pass (M0139-S2) would have
// found something to do.
func TestNarrowJoinLegDeclinesButCounts(t *testing.T) {
	old := narrowLegHook
	defer func() { narrowLegHook = old }()

	lay := func() outputLayout { return outputLayout{7, 8} }

	for _, tc := range []struct {
		name      string
		flag      bool
		n         Node
		wantCount int64
	}{
		{"flag on + bare scan: counts", true, &SeqScan{schema: noSchema("a", "b")}, 1},
		{"flag on + Filter-wrapped scan: counts", true, &Filter{Child: &SeqScan{schema: noSchema("a", "b")}}, 1},
		{"flag on + already a Project: declines silently", true, &Project{Child: &SeqScan{schema: noSchema("a", "b")}, schema: noSchema("a", "b")}, 0},
		{"flag on + not a leaf at all: declines silently", true, &noNode{sch: noSchema("a", "b")}, 0},
		{"flag off + bare scan: declines and does not count", false, &SeqScan{schema: noSchema("a", "b")}, 0},
	} {
		narrowLegHook = tc.flag
		legHookFireCountAndReset() // clear any carry from a prior case

		gotN, gotLay := narrowJoinLeg(tc.n, lay())
		if gotN != tc.n {
			t.Errorf("%s: node changed (%T -> %T); S1 must never mutate", tc.name, tc.n, gotN)
		}
		if len(gotLay) != 2 || gotLay[0] != 7 || gotLay[1] != 8 {
			t.Errorf("%s: layout changed to %v, want [7 8]", tc.name, gotLay)
		}
		if got := legHookFireCountAndReset(); got != tc.wantCount {
			t.Errorf("%s: fire count = %d, want %d", tc.name, got, tc.wantCount)
		}
	}

	// A nil node must decline without panicking on isNarrowableLeaf/Output().
	narrowLegHook = true
	legHookFireCountAndReset()
	if gotN, gotLay := narrowJoinLeg(nil, lay()); gotN != nil || len(gotLay) != 2 {
		t.Errorf("nil node: got (%v, %v), want (nil, unchanged layout)", gotN, gotLay)
	}
	if got := legHookFireCountAndReset(); got != 0 {
		t.Errorf("nil node: fire count = %d, want 0", got)
	}
}

// TestNarrowJoinLegFiresOnLiveJoinSearch is the corpus-measurement half of
// M0139-S1's Definition of Done: "a hook point exists inside the join tree
// and fires on a stated, non-zero number of corpus queries." A plain
// two-table equi-join plans a hash join whose OUTER (probe) side is exactly
// the leg narrowBuildInput never reaches (narrowoutput.go only narrows the
// INNER/build side) — the gap M0139-S1 exists to close.
//
// It also mechanically re-proves the "no parity movement" prediction
// end-to-end rather than by argument: the plan tree's shape (join/scan node
// kinds and nesting, via unaDump) must be byte-identical whether the hook
// is on or off, since a pure-decline hook cannot move a plan by
// construction.
func TestNarrowJoinLegFiresOnLiveJoinSearch(t *testing.T) {
	cat := catalog.NewInMemory()
	mk := func(name string) *catalog.Table {
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
			{Name: name + "_k", Type: catalog.Type{Name: "int4"}},
			{Name: name + "_v", Type: catalog.Type{Name: "int4"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: 10_000, Analyzed: true, Columns: []catalog.ColumnStats{
			{AvgWidth: 4}, {AvgWidth: 4},
		}}
		return tbl
	}
	mk("jlh1")
	mk("jlh2")

	stmts, err := parser.Parse(
		"select jlh1.jlh1_v from jlh1, jlh2 where jlh1.jlh1_k = jlh2.jlh2_k")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*parser.SelectStmt)

	old := narrowLegHook
	defer func() { narrowLegHook = old }()

	narrowLegHook = false
	legHookFireCountAndReset()
	planOff, err := PlanWithSettings(sel, cat, DefaultPlannerSettings())
	if err != nil {
		t.Fatalf("plan (hook off): %v", err)
	}
	if got := legHookFireCountAndReset(); got != 0 {
		t.Fatalf("hook off: fire count = %d, want 0 (the flag must gate the hook)", got)
	}

	narrowLegHook = true
	legHookFireCountAndReset()
	planOn, err := PlanWithSettings(sel, cat, DefaultPlannerSettings())
	if err != nil {
		t.Fatalf("plan (hook on): %v", err)
	}
	fired := legHookFireCountAndReset()
	if fired == 0 {
		t.Fatalf("hook on: fire count = 0 on a two-table equi-join — expected the " +
			"hash join's outer/probe leg (or a nested-loop leg) to be an eligible, " +
			"currently-unhooked scan")
	}
	t.Logf("M0139-S1 hook fired %d time(s) on this query", fired)

	if gotOff, gotOn := unaDump(planOff, 0), unaDump(planOn, 0); gotOff != gotOn {
		t.Fatalf("plan shape moved between hook off and on — S1 must not move a "+
			"plan by construction:\noff:\n%s\non:\n%s", gotOff, gotOn)
	}
}
