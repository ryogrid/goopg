package optimizer

import (
	"reflect"
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

// TestNarrowJoinLegCountsAndNarrows pins M0139-S2's whole contract in one
// place: the hook counts every eligible leg it reaches (the same counter S1
// introduced) and, whenever a *Path with a known needed set is available,
// narrows to exactly that set via the SAME three-tier chain narrowBuildInput
// (narrowoutput.go) already uses — joinKeepSet, then buildKeepSet, then the
// neededKeepSet fallback — never permuting, corrupting, or narrowing below
// what that chain reports. `narrowBuildTestPath` (narrowoutput_test.go)
// builds a Path whose Rel carries only a NeededCols set, so every case here
// exercises the neededKeepSet fallback tier specifically; the other two
// tiers are narrowBuildInput's/narrowMergeInput's own tests' job, and this
// hook calls the identical functions.
func TestNarrowJoinLegCountsAndNarrows(t *testing.T) {
	old := narrowLegHook
	defer func() { narrowLegHook = old }()

	lay := func() outputLayout { return outputLayout{7, 8, 9} }
	scan := func() Node { return &SeqScan{schema: noSchema("k", "v", "unused")} }

	knownSubset := narrowBuildTestPath(true, "k", "v") // drops "unused"
	knownAll := narrowBuildTestPath(true, "k", "v", "unused")
	unknown := narrowBuildTestPath(false, "k")

	for _, tc := range []struct {
		name      string
		flag      bool
		n         Node
		p         *Path
		nliInner  bool
		wantCount int64
		wantKeep  []string // nil = the pair must be returned unchanged
	}{
		{"flag off + bare scan: declines and does not count", false, scan(), knownSubset, false, 0, nil},
		{"flag on + already a Project: declines silently", true,
			&Project{Child: scan(), schema: noSchema("k")}, knownSubset, false, 0, nil},
		{"flag on + not a leaf at all: declines silently", true,
			&noNode{sch: noSchema("k", "v", "unused")}, knownSubset, false, 0, nil},
		{"flag on + nil path: counts but declines (unknown needed set)", true, scan(), nil, false, 1, nil},
		{"flag on + nil rel: counts but declines", true, scan(), &Path{}, false, 1, nil},
		{"flag on + unknown needed set: counts but declines", true, scan(), unknown, false, 1, nil},
		{"flag on + needed set names everything: counts but declines (no-op cut)", true,
			scan(), knownAll, false, 1, nil},
		{"flag on + needed set names a subset: counts AND narrows", true,
			scan(), knownSubset, false, 1, []string{"k", "v"}},
		{"flag on + NLI inner + a narrowable subset: counts but declines (structural, not a keep-set refusal)", true,
			scan(), knownSubset, true, 1, nil},
	} {
		narrowLegHook = tc.flag
		legHookFireCountAndReset() // clear any carry from a prior case

		gotN, gotLay := narrowJoinLeg(tc.n, lay(), tc.p, tc.nliInner)

		if tc.wantKeep == nil {
			if gotN != tc.n {
				t.Errorf("%s: node changed (%T -> %T); want unchanged", tc.name, tc.n, gotN)
			}
			if len(gotLay) != 3 {
				t.Errorf("%s: layout changed to %v, want unchanged len 3", tc.name, gotLay)
			}
		} else {
			p, ok := gotN.(*Project)
			if !ok {
				t.Fatalf("%s: expected a *Project, got %T", tc.name, gotN)
			}
			var got []string
			for _, c := range p.Output() {
				got = append(got, c.Name)
			}
			if !reflect.DeepEqual(got, tc.wantKeep) {
				t.Errorf("%s: kept columns = %v, want %v", tc.name, got, tc.wantKeep)
			}
			if len(gotLay) != len(tc.wantKeep) {
				t.Errorf("%s: layout width = %d, want %d", tc.name, len(gotLay), len(tc.wantKeep))
			}
		}

		if got := legHookFireCountAndReset(); got != tc.wantCount {
			t.Errorf("%s: fire count = %d, want %d", tc.name, got, tc.wantCount)
		}
	}

	// A nil node must decline without panicking on isNarrowableLeaf/Output().
	narrowLegHook = true
	legHookFireCountAndReset()
	if gotN, gotLay := narrowJoinLeg(nil, lay(), knownSubset, false); gotN != nil || len(gotLay) != 3 {
		t.Errorf("nil node: got (%v, %v), want (nil, unchanged layout)", gotN, gotLay)
	}
	if got := legHookFireCountAndReset(); got != 0 {
		t.Errorf("nil node: fire count = %d, want 0", got)
	}
}

// findFirstJoin (small_dim_buildside_test.go, same package) walks down the
// single-child wrapper nodes above a hash/merge/plain-NL *Join and returns
// it.
//
// joinChildColumnNames returns the union of column names in j's own two
// DIRECT children's schemas — exactly the (Node, outputLayout) pairs
// joinInputsFor/narrowJoinLeg produce. A base-relation scan's OWN schema
// never narrows (that is the structural gap this milestone exists to close:
// there is no Project above the scan at all); what narrows is whether a new
// Project now sits BETWEEN the scan and the join, so this check must look at
// the join's immediate children, not every schema anywhere in the tree.
func joinChildColumnNames(j Node) map[string]bool {
	names := map[string]bool{}
	for _, c := range upperNarrowChildren(j) {
		for _, col := range c.Output() {
			names[col.Name] = true
		}
	}
	return names
}

// TestNarrowJoinLegFiresAndNarrowsOnLiveJoinSearch is the corpus-measurement
// half of M0139-S2's Definition of Done: "scans inside join trees emit
// narrowed rows, with the existing narrowing machinery reused rather than
// duplicated." jlh1 carries an extra column (`jlh1_unused`) that nothing
// above the join ever reads; on whichever side the search makes jlh1 the
// hash join's OUTER/probe leg, narrowBuildInput never reaches it (it only
// narrows the INNER/build side), so before S2 that column reached the join
// unnarrowed — the gap M0139-S1 built the attachment point for and S2 now
// fills.
func TestNarrowJoinLegFiresAndNarrowsOnLiveJoinSearch(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl1, err := cat.CreateTable(parser.ObjectName{Name: "jlh1"}, []catalog.Column{
		{Name: "jlh1_k", Type: catalog.Type{Name: "int4"}},
		{Name: "jlh1_v", Type: catalog.Type{Name: "int4"}},
		{Name: "jlh1_unused", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tbl1.Stats = &catalog.TableStats{RowCount: 10_000, Analyzed: true, Columns: []catalog.ColumnStats{
		{AvgWidth: 4}, {AvgWidth: 4}, {AvgWidth: 4},
	}}
	tbl2, err := cat.CreateTable(parser.ObjectName{Name: "jlh2"}, []catalog.Column{
		{Name: "jlh2_k", Type: catalog.Type{Name: "int4"}},
		{Name: "jlh2_v", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tbl2.Stats = &catalog.TableStats{RowCount: 10_000, Analyzed: true, Columns: []catalog.ColumnStats{
		{AvgWidth: 4}, {AvgWidth: 4},
	}}

	stmts, err := parser.Parse(
		"select jlh1.jlh1_v from jlh1, jlh2 where jlh1.jlh1_k = jlh2.jlh2_k")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*parser.SelectStmt)

	// EnableMergeJoin=false: on this tiny, tied-cardinality fixture the
	// default settings pick a MERGE join, whose BOTH sides `narrowMergeInput`
	// already narrows (B-01a, narrowoutput.go) — unrelated to this hook and
	// not a useful witness. Forcing NestedLoop (still enabled) reproduces
	// the actual gap: `narrowBuildInput` is hash-only and `narrowMergeInput`
	// is merge-only, so neither ever reaches a plain nested loop's OUTER
	// leg, which is what this hook exists to close.
	settings := DefaultPlannerSettings()
	settings.EnableMergeJoin = false

	old := narrowLegHook
	defer func() { narrowLegHook = old }()

	narrowLegHook = false
	legHookFireCountAndReset()
	planOff, err := PlanWithSettings(sel, cat, settings)
	if err != nil {
		t.Fatalf("plan (hook off): %v", err)
	}
	if got := legHookFireCountAndReset(); got != 0 {
		t.Fatalf("hook off: fire count = %d, want 0 (the flag must gate the hook)", got)
	}
	joinOff := findFirstJoin(planOff)
	if joinOff == nil {
		t.Fatalf("test fixture broken: no join node found (hook off)\n%s", unaDump(planOff, 0))
	}
	if !joinChildColumnNames(joinOff)["jlh1_unused"] {
		t.Fatalf("test fixture broken: hook-off join's children already lack jlh1_unused " +
			"before this hook could be blamed for narrowing it")
	}

	narrowLegHook = true
	legHookFireCountAndReset()
	planOn, err := PlanWithSettings(sel, cat, settings)
	if err != nil {
		t.Fatalf("plan (hook on): %v", err)
	}
	fired := legHookFireCountAndReset()
	if fired == 0 {
		t.Fatalf("hook on: fire count = 0 on a two-table equi-join — expected the " +
			"hash join's outer/probe leg (or a nested-loop leg) to be an eligible, " +
			"currently-unhooked scan")
	}
	t.Logf("M0139-S2 hook fired %d time(s) on this query", fired)

	joinOn := findFirstJoin(planOn)
	if joinOn == nil {
		t.Fatalf("no join node found (hook on)\n%s", unaDump(planOn, 0))
	}
	if joinChildColumnNames(joinOn)["jlh1_unused"] {
		t.Errorf("hook on: jlh1_unused still reaches the join's child; S2 must narrow it away\n%s",
			unaDump(planOn, 0))
	}
}
