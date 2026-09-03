package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func noSchema(names ...string) Schema {
	s := make(Schema, len(names))
	for i, n := range names {
		s[i] = SchemaColumn{Name: n, Type: catalog.Type{Name: "int4"}, SourceTableIdx: int16(i + 1)}
	}
	return s
}

// noNode is a minimal Node with a settable Output, enough to drive the
// transformation without building a real scan.
type noNode struct{ sch Schema }

func (n *noNode) Output() Schema { return n.sch }
func (n *noNode) Pos() int       { return 0 }
func (n *noNode) planNode()      {}

// TestNarrowPlanOutputNarrowsSchemaAndLayoutTogether is the core property:
// take2 P4-01 chose a Project precisely because the row, the schema and the
// layout all derive from one list. If they can drift here, the mechanism is
// pointless.
func TestNarrowPlanOutputNarrowsSchemaAndLayoutTogether(t *testing.T) {
	n := &noNode{sch: noSchema("a", "b", "c", "d")}
	lay := outputLayout{10, 11, 12, 13}

	got, gotLay := narrowPlanOutput(n, lay, []int{0, 2})

	p, ok := got.(*Project)
	if !ok {
		t.Fatalf("expected a *Project, got %T", got)
	}
	if len(p.Targets) != 2 || len(p.Output()) != 2 || len(gotLay) != 2 {
		t.Fatalf("targets/schema/layout widths disagree: %d/%d/%d",
			len(p.Targets), len(p.Output()), len(gotLay))
	}
	// The layout must be the SAME subset, in the same order — this is what
	// createplanjoin.go:289 checks and what rev 8 identified as the constraint.
	if gotLay[0] != 10 || gotLay[1] != 12 {
		t.Errorf("layout = %v, want [10 12] (the kept coordinates)", gotLay)
	}
	if p.Output()[0].Name != "a" || p.Output()[1].Name != "c" {
		t.Errorf("schema = %v, want a,c", p.Output())
	}
	// Targets must point at the ORIGINAL child indices, not the new ones.
	for i, want := range []int{0, 2} {
		cr, ok := p.Targets[i].(*ColumnRef)
		if !ok {
			t.Fatalf("target %d is %T, want *ColumnRef", i, p.Targets[i])
		}
		if cr.Index != want {
			t.Errorf("target %d indexes child column %d, want %d", i, cr.Index, want)
		}
		// SourceTableIdx must survive, or a self-join cannot disambiguate.
		if cr.SourceTableIdx != int16(want+1) {
			t.Errorf("target %d lost SourceTableIdx: got %d want %d",
				i, cr.SourceTableIdx, want+1)
		}
	}
}

// TestNarrowPlanOutputDeclinesRatherThanCorrupts: every input that would permute
// or corrupt instead of narrow must return the pair untouched. A wrong answer
// here is exactly the P4-01b failure class.
func TestNarrowPlanOutputDeclinesRatherThanCorrupts(t *testing.T) {
	n := &noNode{sch: noSchema("a", "b", "c")}
	lay := outputLayout{7, 8, 9}

	for _, tc := range []struct {
		name string
		keep []int
	}{
		{"descending", []int{2, 0}},
		{"duplicated", []int{1, 1}},
		{"out of range high", []int{0, 5}},
		{"negative", []int{-1, 1}},
		{"keeps everything", []int{0, 1, 2}},
		{"empty", nil},
	} {
		got, gotLay := narrowPlanOutput(n, lay, tc.keep)
		if got != Node(n) {
			t.Errorf("%s: returned a rewritten node; it must decline", tc.name)
		}
		if len(gotLay) != len(lay) {
			t.Errorf("%s: layout changed to %v; it must decline", tc.name, gotLay)
		}
	}

	// A layout that already disagrees with the schema is the caller's bug; the
	// helper must not compound it.
	if got, _ := narrowPlanOutput(n, outputLayout{1}, []int{0}); got != Node(n) {
		t.Error("a layout/schema mismatch must be declined, not narrowed")
	}
}

// TestNarrowBuildFromEnvPolarity: opt-out polarity — the flag defaults ON
// and only "=0" selects the old arm. Matches GOOPG_PGSHAPED_DP's idiom.
func TestNarrowBuildFromEnvPolarity(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", true},
		{"1", true},
		{"0", false},
	} {
		if got := narrowBuildFromEnv(tc.value); got != tc.want {
			t.Errorf("narrowBuildFromEnv(%q) = %t, want %t", tc.value, got, tc.want)
		}
	}
}

// narrowBuildTestPath is a build-side path whose rel carries the given needed
// set, enough to drive narrowBuildInput without a real search.
func narrowBuildTestPath(known bool, names ...string) *Path {
	needed := map[string]bool{}
	for _, n := range names {
		needed[n] = true
	}
	var m map[string]bool
	if known {
		m = needed
	}
	return &Path{Rel: &RelOptInfo{NeededCols: m, NeededColsKnown: known}}
}

// TestNarrowBuildInputWiring pins rev 10 step 3's contract: only a hash join,
// only with the flag on, only with a known needed set — everything else
// returns the pair untouched, so the shipped default path cannot move.
func TestNarrowBuildInputWiring(t *testing.T) {
	old := narrowBuild
	defer func() { narrowBuild = old }()

	node := func() *noNode { return &noNode{sch: noSchema("k", "v", "unused")} }
	lay := func() outputLayout { return outputLayout{4, 5, 6} }
	known := narrowBuildTestPath(true, "k", "v")

	narrowBuild = true
	got, gotLay := narrowBuildInput("PathHashJoin", node(), lay(), known)
	p, ok := got.(*Project)
	if !ok {
		t.Fatalf("flag on + hash join: expected a *Project, got %T", got)
	}
	if len(p.Output()) != 2 || len(gotLay) != 2 || gotLay[0] != 4 || gotLay[1] != 5 {
		t.Errorf("flag on + hash join: schema/layout = %v/%v, want [k v]/[4 5]",
			p.Output(), gotLay)
	}

	for _, tc := range []struct {
		name string
		flag bool
		kind string
		path *Path
	}{
		{"flag off leaves the pair untouched", false, "PathHashJoin", known},
		{"merge join is never narrowed", true, "PathMergeJoin", known},
		{"unknown needed set declines", true, "PathHashJoin", narrowBuildTestPath(false, "k")},
		{"nil rel declines", true, "PathHashJoin", &Path{}},
		{"nil path declines", true, "PathHashJoin", nil},
	} {
		narrowBuild = tc.flag
		n := node()
		got, gotLay := narrowBuildInput(tc.kind, n, lay(), tc.path)
		if got != Node(n) {
			t.Errorf("%s: returned a rewritten node %T; it must decline", tc.name, got)
		}
		if len(gotLay) != 3 {
			t.Errorf("%s: layout changed to %v; it must decline", tc.name, gotLay)
		}
	}

	// A nil node must decline, not panic on Output().
	narrowBuild = true
	if got, _ := narrowBuildInput("PathHashJoin", nil, lay(), known); got != nil {
		t.Errorf("nil node: got %v, want nil", got)
	}
}

// TestNeededKeepSetPreservesJoinKeysAndDropsUnused pins the keep-rule's two
// halves. nil means "the collector declined" and must never be read as
// "keep nothing".
func TestNeededKeepSetPreservesJoinKeysAndDropsUnused(t *testing.T) {
	out := noSchema("k", "v", "unused")

	keep := neededKeepSet(out, map[string]bool{"k": true, "v": true})
	if len(keep) != 2 || keep[0] != 0 || keep[1] != 1 {
		t.Errorf("keep = %v, want [0 1] (k and v, dropping unused)", keep)
	}
	if got := neededKeepSet(out, nil); got != nil {
		t.Errorf("a nil needed set must yield nil (declined), got %v", got)
	}
	// Ascending order is a precondition of narrowPlanOutput; the producer must
	// satisfy it by construction.
	keep = neededKeepSet(out, map[string]bool{"unused": true, "k": true})
	if len(keep) != 2 || keep[0] != 0 || keep[1] != 2 {
		t.Errorf("keep = %v, want ascending [0 2]", keep)
	}
}
