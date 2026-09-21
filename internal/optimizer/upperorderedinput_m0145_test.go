package optimizer

// M0145-0006 slice 1 — the three order-delivering tops `inputNodePathkeys`'
// walk used to swallow in its `default: nil`.
//
// Each arm is pinned on both sides: the ordering it DOES deliver, and the
// shape where the claim would be wrong and must come back nil. The WindowAgg
// arm additionally pins the coordinate rule the other two do not need — the
// window functions are appended to the child's schema, so the walk narrows
// the space it must agree with instead of refusing on the width mismatch.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// windowOutputSchema is upperOrderedSchema plus one appended window-function
// column, i.e. what the planner's window builder publishes.
func windowOutputSchema() Schema {
	return append(upperOrderedSchema(), SchemaColumn{Name: "row_number", Type: catalog.Type{Name: "int8"}})
}

func upperOrderedSortKeys() []SortKey {
	return []SortKey{{Expr: &ColumnRef{Index: 0, Name: "k", SourceTableIdx: 1}}}
}

// orderedInputLeaf is a schema-carrying leaf the walk stops at (a `*SeqScan`
// hits the `default: nil` arm), used as the child of the nodes under test.
func orderedInputLeaf(s Schema) Node {
	return &Project{pos: 0, Child: &SeqScan{}, schema: s, Targets: projectIdentityExprs(s)}
}

func projectIdentityExprs(s Schema) []Expr {
	ex := make([]Expr, len(s))
	for i, c := range s {
		ex[i] = &ColumnRef{Index: i, Name: c.Name, SourceTableIdx: c.SourceTableIdx}
	}
	return ex
}

// TestInputNodePathkeysReadsAnIncrementalSortsFullOrdering pins that the arm
// claims all of `Keys` and not the presorted prefix: `PresortedCount` says how
// much work the node avoids, never how little order it emits.
func TestInputNodePathkeysReadsAnIncrementalSortsFullOrdering(t *testing.T) {
	out := upperOrderedSchema()
	keys := []SortKey{
		{Expr: &ColumnRef{Index: 0, Name: "k", SourceTableIdx: 1}},
		{Expr: &ColumnRef{Index: 1, Name: "v", SourceTableIdx: 1}, Desc: true},
	}
	is := &IncrementalSort{pos: 0, Child: orderedInputLeaf(out), Keys: keys, PresortedCount: 1}

	got := inputNodePathkeys(is)
	if len(got) != 2 {
		t.Fatalf("got %d pathkeys, want the full 2-key ordering the node delivers", len(got))
	}
	if cr := got[0].Expr.(*ColumnRef); cr.Index != 0 || !got[0].SortAsc {
		t.Fatalf("first key = %#v, want ascending column 0", got[0])
	}
	if cr := got[1].Expr.(*ColumnRef); cr.Index != 1 || got[1].SortAsc {
		t.Fatalf("second key = %#v, want descending column 1", got[1])
	}
}

// TestInputNodePathkeysReadsAGatherMergesPreservedOrdering pins the arm that
// distinguishes GatherMerge from Gather: the leader's merge preserves the
// worker ordering, so a claim exists here where a plain Gather has none.
func TestInputNodePathkeysReadsAGatherMergesPreservedOrdering(t *testing.T) {
	out := upperOrderedSchema()
	gm := NewGatherMerge(0, orderedInputLeaf(out), 2, upperOrderedSortKeys())

	got := inputNodePathkeys(gm)
	if len(got) != 1 {
		t.Fatalf("got %d pathkeys, want the merge ordering (1)", len(got))
	}
	if cr := got[0].Expr.(*ColumnRef); cr.Index != 0 || cr.Name != "k" {
		t.Fatalf("key = %#v, want the merge key (column 0, k)", got[0])
	}

	// The non-merging twin still claims nothing: a `*Gather` interleaves its
	// workers' streams, so no ordering survives it.
	g := NewGather(0, orderedInputLeaf(out), 2)
	if got := inputNodePathkeys(g); got != nil {
		t.Fatalf("a plain Gather must claim no ordering, got %d keys", len(got))
	}
}

// TestInputNodePathkeysCrossesAPresortedWindowAgg pins the PG rule
// (create_windowagg_path: "WindowAgg preserves the input sort order") together
// with the coordinate narrowing the appended window columns force.
func TestInputNodePathkeysCrossesAPresortedWindowAgg(t *testing.T) {
	childSchema := upperOrderedSchema()
	sorted := &Sort{pos: 0, Child: orderedInputLeaf(childSchema), Keys: upperOrderedSortKeys()}
	wa := &WindowAgg{
		pos:       0,
		Child:     sorted,
		OrderBy:   upperOrderedSortKeys(),
		Funcs:     []WindowFunc{{Name: "row_number"}},
		Presorted: true,
		schema:    windowOutputSchema(),
	}

	got := inputNodePathkeys(wa)
	if len(got) != 1 {
		t.Fatalf("got %d pathkeys, want the child's preserved ordering (1)", len(got))
	}
	if cr := got[0].Expr.(*ColumnRef); cr.Index != 0 || cr.Name != "k" {
		t.Fatalf("key = %#v, want the child's key (column 0, k) in the published coordinates", got[0])
	}
}

// TestInputNodePathkeysRefusesAWindowAggThatSortsPrivately is the fail-closed
// half: with Presorted false the executor sorts the input itself, so the rows
// leaving the node are in the window's order and the child's claim is void.
// The node records no direction for that private sort, so the walk refuses
// rather than inventing one.
func TestInputNodePathkeysRefusesAWindowAggThatSortsPrivately(t *testing.T) {
	childSchema := upperOrderedSchema()
	sorted := &Sort{pos: 0, Child: orderedInputLeaf(childSchema), Keys: upperOrderedSortKeys()}
	wa := &WindowAgg{
		pos:     0,
		Child:   sorted,
		OrderBy: upperOrderedSortKeys(),
		Funcs:   []WindowFunc{{Name: "row_number"}},
		schema:  windowOutputSchema(),
	}
	if got := inputNodePathkeys(wa); got != nil {
		t.Fatalf("a self-sorting WindowAgg must claim nothing, got %d keys", len(got))
	}
}

// TestInputNodePathkeysRefusesAWindowAggThatDoesNotAppend pins that the
// prefix rule is CHECKED, not assumed: a node whose published schema does not
// start with its child's columns re-assigns positions, and a positional claim
// from below would then address the wrong column.
func TestInputNodePathkeysRefusesAWindowAggThatDoesNotAppend(t *testing.T) {
	childSchema := upperOrderedSchema()
	sorted := &Sort{pos: 0, Child: orderedInputLeaf(childSchema), Keys: upperOrderedSortKeys()}
	// The window column is PREPENDED, so every child column shifts by one.
	shifted := append(Schema{{Name: "row_number", Type: catalog.Type{Name: "int8"}}}, childSchema...)
	wa := &WindowAgg{
		pos:       0,
		Child:     sorted,
		OrderBy:   upperOrderedSortKeys(),
		Funcs:     []WindowFunc{{Name: "row_number"}},
		Presorted: true,
		schema:    shifted,
	}
	if got := inputNodePathkeys(wa); got != nil {
		t.Fatalf("a WindowAgg that re-assigns positions must claim nothing, got %d keys", len(got))
	}
}

// TestWindowAggNarrowingComposesWithTheNewArms pins that the narrowed
// coordinate space is what the arms BELOW a crossed `*WindowAgg` are checked
// against: the GatherMerge here publishes the child width, not the window
// width, and its claim is still delivered because those columns sit at the
// same positions in the window node's output.
func TestWindowAggNarrowingComposesWithTheNewArms(t *testing.T) {
	childSchema := upperOrderedSchema()
	gm := NewGatherMerge(0, orderedInputLeaf(childSchema), 2, upperOrderedSortKeys())
	wa := &WindowAgg{
		pos:       0,
		Child:     gm,
		OrderBy:   upperOrderedSortKeys(),
		Funcs:     []WindowFunc{{Name: "row_number"}},
		Presorted: true,
		schema:    windowOutputSchema(),
	}

	got := inputNodePathkeys(wa)
	if len(got) != 1 {
		t.Fatalf("got %d pathkeys, want the merge ordering carried through the window node (1)", len(got))
	}
	if cr := got[0].Expr.(*ColumnRef); cr.Index != 0 || cr.Name != "k" {
		t.Fatalf("key = %#v, want column 0 (k) — the same position in both schemas", got[0])
	}

	// A window node whose child is a plain `*Gather` still claims nothing:
	// narrowing the coordinate space never manufactures an ordering.
	wa.Child = NewGather(0, orderedInputLeaf(childSchema), 2)
	if got := inputNodePathkeys(wa); got != nil {
		t.Fatalf("no ordering below the window node must stay no ordering, got %d keys", len(got))
	}
}
