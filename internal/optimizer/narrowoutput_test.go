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
