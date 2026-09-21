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

// --- slice 2: the merge-join top -------------------------------------------

// mergeJoinSchema is a two-relation layout whose merge key columns share one
// type: `pairIsHashSafe` requires that, and an unsafe pair is dropped from the
// executor's key tuple, so a fixture with mismatched key types would be
// testing the wrong refusal.
func mergeJoinSchema() Schema {
	return Schema{
		{Name: "k", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		{Name: "v2", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		{Name: "rk", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2},
	}
}

// mergeJoinOver builds the legacy-constructor shape: a `*Join` with no search
// stamp, merging on the first output column.
func mergeJoinOver(jt JoinType, algo JoinAlgo) *Join {
	out := mergeJoinSchema()
	left := &ColumnRef{Index: 0, Name: "k", SourceTableIdx: 1}
	right := &ColumnRef{Index: 2, Name: "rk", SourceTableIdx: 2}
	return &Join{
		pos:      0,
		Type:     jt,
		Algo:     algo,
		Left:     orderedInputLeaf(out[:2]),
		Right:    orderedInputLeaf(out[2:]),
		LeftKey:  left,
		RightKey: right,
		HashKeys: []JoinKeyPair{{Left: left, Right: right}},
		schema:   out,
	}
}

// TestInputNodePathkeysReadsALegacyMergeJoinsOrdering pins the arm that serves
// the trees the PG-shaped search declined: goopg's merge operator sorts both
// inputs itself and emits ascending / NULLs-last, so the join delivers the
// merge-key ordering even with no path and no stamp behind it.
func TestInputNodePathkeysReadsALegacyMergeJoinsOrdering(t *testing.T) {
	j := mergeJoinOver(JoinTypeInner, JoinAlgoMerge)

	got := inputNodePathkeys(j)
	if len(got) != 1 {
		t.Fatalf("got %d pathkeys, want the merge-key ordering (1)", len(got))
	}
	if cr := got[0].Expr.(*ColumnRef); cr.Index != 0 || cr.Name != "k" {
		t.Fatalf("key = %#v, want the outer merge key (column 0, k)", got[0])
	}
	if !got[0].SortAsc || got[0].NullsFirst {
		t.Fatalf("key = %#v, want ascending / NULLs last — the comparator is fixed", got[0])
	}
}

// TestInputNodePathkeysRefusesNonMergeJoins is the gate on `Algo`: a hash or
// nested-loop join emits in probe order and claims nothing.
func TestInputNodePathkeysRefusesNonMergeJoins(t *testing.T) {
	for _, algo := range []JoinAlgo{JoinAlgoHash, JoinAlgoNestedLoop} {
		j := mergeJoinOver(JoinTypeInner, algo)
		if got := inputNodePathkeys(j); got != nil {
			t.Fatalf("algo %v must claim no ordering, got %d keys", algo, len(got))
		}
	}
}

// TestInputNodePathkeysDropsFullAndRightMergeOrderings is the wrong-answer
// guard, on the node side this time: a FULL or RIGHT merge join injects its
// unmatched rows wherever the merge reaches them, so `build_join_pathkeys`
// returns NIL and so must this arm. An over-claim here deletes the ORDER BY
// Sort and returns rows out of order with a correct row count.
func TestInputNodePathkeysDropsFullAndRightMergeOrderings(t *testing.T) {
	for _, jt := range []JoinType{JoinTypeFull, JoinTypeRight} {
		j := mergeJoinOver(jt, JoinAlgoMerge)
		if got := inputNodePathkeys(j); got != nil {
			t.Fatalf("jointype %v must claim no ordering, got %d keys", jt, len(got))
		}
	}
}

// TestInputNodePathkeysDefersToAnEmptySearchStamp pins that the arm never
// overrules the search: a searched join whose stamp is empty was given none on
// purpose — the winning path's pathkeys were nil or failed validation against
// this same schema — so re-deriving a claim from the node would be a weaker
// mechanism silently overriding a stronger one.
func TestInputNodePathkeysDefersToAnEmptySearchStamp(t *testing.T) {
	j := mergeJoinOver(JoinTypeInner, JoinAlgoMerge)
	markSearchedTree(j)
	if got := inputNodePathkeys(j); got != nil {
		t.Fatalf("a searched join with an empty stamp must claim nothing, got %d keys", len(got))
	}
}

// TestMergeJoinEmissionPathkeysTruncatesRatherThanGuessing pins rule 3: the
// keys are validated against the node's published schema, and a key that does
// not address the column it names truncates the list instead of being
// repaired or skipped.
func TestMergeJoinEmissionPathkeysTruncatesRatherThanGuessing(t *testing.T) {
	j := mergeJoinOver(JoinTypeInner, JoinAlgoMerge)
	good := j.HashKeys[0]
	// A second pair whose outer key names a column that is not at that index.
	// Same int4 type on both sides, so the pair IS merge-safe and reaches the
	// validator; it fails there because index 1 holds `v2`, not `k`.
	bad := JoinKeyPair{Left: &ColumnRef{Index: 1, Name: "k", SourceTableIdx: 1}, Right: &ColumnRef{Index: 2, Name: "rk", SourceTableIdx: 2}}

	j.HashKeys = []JoinKeyPair{good, bad}
	if got := mergeJoinEmissionPathkeys(j); len(got) != 1 {
		t.Fatalf("got %d keys, want the confirmed prefix (1)", len(got))
	}
	j.HashKeys = []JoinKeyPair{bad, good}
	if got := mergeJoinEmissionPathkeys(j); len(got) != 0 {
		t.Fatalf("a leading unconfirmable key must reduce the claim to nothing, got %d keys", len(got))
	}
}

// TestMergeJoinEmissionPathkeysRefusesAnUnsafeLeadKey is the other
// wrong-ordering guard. `ExecMergeKeyPlan` keeps `Keys[0]` unconditionally,
// merge-safe or not, so an exotic-typed lead key is sorted by `compareDatum`
// while the SQL `=` it stands for may disagree (float's -0.0, the cases
// `pairIsHashSafe` excludes). The emitted order is then not the SQL ascending
// order, so no claim may be made from it.
func TestMergeJoinEmissionPathkeysRefusesAnUnsafeLeadKey(t *testing.T) {
	j := mergeJoinOver(JoinTypeInner, JoinAlgoMerge)
	out := mergeJoinSchema()
	out[0].Type = catalog.Type{Name: "float8"}
	out[2].Type = catalog.Type{Name: "float8"}
	j.schema = out
	j.Left = orderedInputLeaf(out[:2])
	j.Right = orderedInputLeaf(out[2:])

	if got := mergeJoinEmissionPathkeys(j); got != nil {
		t.Fatalf("an unsafe lead key must claim nothing, got %d keys", len(got))
	}
}
