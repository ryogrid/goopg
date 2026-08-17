package executor

// M0127-P2.3 — merge-join multi-column keys, EXECUTOR half (design
// leftdeep-joins/07 §2). The planner half's split — which pairs are folded in
// and what is left of the ON clause — is pinned in
// internal/planner/join_exec_keys_merge_test.go.
//
// Two properties, and neither of them is a plain row count:
//
//   - GROUP NARROWING (the merge-join member of the Q78 degeneracy class).
//     Grouping on the leading key column alone turns a pinned lead — the shape
//     qual placement produces when both inputs are filtered to the same
//     constant — into ONE equal-key group whose cartesian product is then
//     walked pair by pair. The answers stay right, so no row-count test can see
//     it; the group structure has to be inspected directly.
//   - OUTER SEMANTICS SURVIVE THE NULL ROUTE CHANGE. A row with a NULL in a
//     non-leading key column used to be grouped on its non-NULL lead and then
//     rejected by the residual; it is now filed as a NULL key and never
//     grouped. Both routes must null-extend it for a LEFT join, or the change
//     is semantic rather than a cost one — and RIGHT/FULL are forced onto merge
//     by planning rule, so this path carries goopg's outer joins. The test runs
//     both residual regimes (full Predicate, and the all-equijoin steady state
//     where the key tuple alone decides) and demands the same answer.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// twoKeyMergePlan builds a merge join over two int equalities with HashKeys
// populated as fillJoinHashKeys would (that pass fills JoinAlgoMerge too).
//
// `Left`/`Right` are left nil: this package cannot construct a planner node
// with a non-empty Output (the schema fields are unexported), and
// residualExcluding needs the left width. The consequence is that
// `mergeResidual` here is whatever `withPredicate` supplies rather than the
// narrowed split — which is why the residual-emptying assertion lives in the
// planner test and this file parameterises over the residual instead.
func twoKeyMergePlan(jt optimizer.JoinType, leftWidth int, withPredicate bool) *optimizer.Join {
	col := func(idx int) *optimizer.ColumnRef {
		return &optimizer.ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
	}
	eq := func(l, r int) optimizer.Expr {
		return &optimizer.BinaryOp{Op: parser.OpEq, Left: col(l), Right: col(r)}
	}
	j := &optimizer.Join{
		Type:     jt,
		Algo:     optimizer.JoinAlgoMerge,
		LeftKey:  col(0),
		RightKey: col(leftWidth),
		HashKeys: []optimizer.JoinKeyPair{
			{Left: col(0), Right: col(leftWidth)},
			{Left: col(1), Right: col(leftWidth + 1)},
		},
	}
	if withPredicate {
		j.Predicate = &optimizer.BinaryOp{
			Op:    parser.OpAnd,
			Left:  eq(0, leftWidth),
			Right: eq(1, leftWidth+1),
		}
	}
	return j
}

// drainMergeSide keys and sorts one side through the streaming source the
// merge join actually uses (M0127-P4.1 replaced buildMergeSide's arrays with
// it) and returns the ordered stream.
func drainMergeSide(t *testing.T, o *joinOp, child Operator, isLeft bool, selfWidth, otherWidth int) []mergeStreamRow {
	t.Helper()
	if o.ctx == nil {
		o.ctx = NewContext()
	}
	if err := child.Open(o.ctx); err != nil {
		t.Fatalf("child Open: %v", err)
	}
	src, err := newMergeSortedSource(o, child, isLeft)
	if err != nil {
		t.Fatalf("newMergeSortedSource: %v", err)
	}
	if _, err := src.prime(); err != nil {
		t.Fatalf("prime: %v", err)
	}
	src.selfWidth, src.otherWidth = selfWidth, otherWidth
	if err := src.fill(o.ctx); err != nil {
		t.Fatalf("fill: %v", err)
	}
	var out []mergeStreamRow
	for {
		r, err := src.next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, r)
	}
	src.close()
	return out
}

// countMergeGroups walks a keyed side and counts equal-key runs with the same
// comparator the merge advance uses.
func countMergeGroups(t *testing.T, keyed []mergeStreamRow) int {
	t.Helper()
	if len(keyed) == 0 {
		return 0
	}
	groups := 1
	for i := 1; i < len(keyed); i++ {
		cmp, err := compareMergeKeys(keyed[i-1].keys, keyed[i].keys, 0)
		if err != nil {
			t.Fatalf("compareMergeKeys: %v", err)
		}
		if cmp != 0 {
			groups++
		}
	}
	return groups
}

// TestMergeKeyTupleSplitsPinnedLeadColumnGroups is the degeneracy regression.
// Column 0 carries the same value on every row (the `= 1998` shape); only
// column 1 discriminates. Keying on the tuple must give one group per row,
// where keying on the lead alone gave a single group of nRows whose pairwise
// product the join then walked.
func TestMergeKeyTupleSplitsPinnedLeadColumnGroups(t *testing.T) {
	const leftWidth = 2
	const nRows = 64

	o := &joinOp{plan: twoKeyMergePlan(optimizer.JoinTypeInner, leftWidth, true)}
	o.initMergeKeys()
	if len(o.mergeKeys) != 2 {
		t.Fatalf("merge join adopted %d key pair(s), want both", len(o.mergeKeys))
	}

	rows := make([]Row, 0, nRows)
	for i := 0; i < nRows; i++ {
		rows = append(rows, Row{NewIntDatum(1998), NewIntDatum(int64(i))})
	}
	keyed := drainMergeSide(t, o, &rowsOp{rows: rows}, true, leftWidth, 2)
	for _, r := range keyed {
		if r.nullKey {
			t.Fatalf("no key column is NULL, yet a row was filed as NULL-keyed")
		}
	}
	if got := countMergeGroups(t, keyed); got != nRows {
		t.Fatalf("keyed side formed %d equal-key group(s) for %d distinct key tuples — "+
			"the pinned lead column is still collapsing the grouping (Q78-class degeneracy)",
			got, nRows)
	}
}

// TestMergeJoinFullKeyLeftOuterNullSecondKey pins the NULL route. `(1, NULL)`
// on the left shares its LEAD key with a right row but can never satisfy the
// second equality, so a LEFT join must null-extend it — whether it gets there
// by failing the residual or by never being grouped.
func TestMergeJoinFullKeyLeftOuterNullSecondKey(t *testing.T) {
	const width = 2
	leftRows := []Row{
		{NewIntDatum(1), NewIntDatum(10)}, // matches
		{NewIntDatum(1), NullDatum},       // NULL second key -> null-extended
		{NewIntDatum(1), NewIntDatum(99)}, // lead matches, tuple does not
		{NewIntDatum(2), NewIntDatum(10)}, // lead does not match
	}
	rightRows := []Row{
		{NewIntDatum(1), NewIntDatum(10)},
		{NewIntDatum(1), NewIntDatum(11)},
	}

	for _, withPredicate := range []bool{true, false} {
		o := &joinOp{
			plan:  twoKeyMergePlan(optimizer.JoinTypeLeft, width, withPredicate),
			left:  &rowsOp{rows: leftRows},
			right: &rowsOp{rows: rightRows},
		}
		out, err := Run(o, NewContext())
		if err != nil {
			t.Fatalf("residual=%v: merge join: %v", withPredicate, err)
		}
		if len(out) != len(leftRows) {
			t.Fatalf("residual=%v: LEFT join emitted %d row(s), want %d (one per left row)",
				withPredicate, len(out), len(leftRows))
		}
		matched, extended := 0, 0
		for _, r := range out {
			if len(r) != 2*width {
				t.Fatalf("residual=%v: emitted row width %d, want %d", withPredicate, len(r), 2*width)
			}
			if r[width].IsNull() && r[width+1].IsNull() {
				extended++
				continue
			}
			matched++
			if r[0].Int != r[width].Int || r[1].Int != r[width+1].Int {
				t.Errorf("residual=%v: emitted a pair failing one of the equalities: %v", withPredicate, r)
			}
		}
		if matched != 1 || extended != 3 {
			t.Fatalf("residual=%v: LEFT join emitted %d matched / %d null-extended, want 1 / 3",
				withPredicate, matched, extended)
		}
	}
}
