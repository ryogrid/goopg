package optimizer

// M0127-P2.3 — the PLANNER half of merge-join multi-column keys (design
// leftdeep-joins/07 §2): which of the published `HashKeys` pairs the merge
// comparator may sort and group on, and what is left of the ON clause once
// they are.
//
// The executor half (group narrowing, the NULL route) is pinned in
// internal/executor/join_merge_key_test.go. The two are a sibling pair in the
// sense of the project's hard-won rule #2: this file's `Residual == nil` is
// exactly the licence that lets `mergeResidualMatch` short-circuit, so a change
// that widened the key set without narrowing the residual (or the reverse)
// would leave one of these two tests green and the join wrong.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// mkMergeJoin builds the two-conjunct merge join whose second key column has
// the given type, with the same schema'd inputs the inner-join-pushdown tests
// use (`y`, `cnt`). Predicate holds the FULL conjunction, as the planner leaves
// it, so the residual split is observable.
func mkMergeJoin(t *testing.T, secondKeyType string) *Join {
	t.Helper()
	ss, ws := ijCTEScan("ss"), ijCTEScan("ws")
	col := func(idx int, name, typ string) *ColumnRef {
		return &ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: typ}}
	}
	j := &Join{
		Type:  JoinTypeLeft,
		Algo:  JoinAlgoMerge,
		Left:  ss,
		Right: ws,
		Predicate: combineAnd([]Expr{
			&BinaryOp{Op: parser.OpEq, Left: col(0, "y", "int4"), Right: col(2, "y", "int4")},
			&BinaryOp{Op: parser.OpEq, Left: col(1, "cnt", secondKeyType), Right: col(3, "cnt", secondKeyType)},
		}),
		LeftKey:  col(0, "y", "int4"),
		RightKey: col(2, "y", "int4"),
		schema:   append(append(Schema{}, ss.Output()...), ws.Output()...),
	}
	fillOneJoinHashKeys(j)
	return j
}

// TestExecMergeKeyPlanAdoptsEveryMergeSafePair is the counterpart to
// TestDegenerateHashKeyCoveredByFullKeyList: goopg's merge join sorted on ONE
// conjunct and re-checked the whole Predicate against every pair of an
// equal-key group, so a low-cardinality lead built one huge group and walked
// its cartesian product (M0125-0011's TPC-DS Q97 shape, kept correct by that
// residual re-check at O(n·m) cost). Both pairs must now be keys, and the
// residual must be empty — PG's `joinqual` is empty on an all-equijoin merge
// join (postgres/src/backend/executor/nodeMergejoin.c).
func TestExecMergeKeyPlanAdoptsEveryMergeSafePair(t *testing.T) {
	j := mkMergeJoin(t, "int4")
	mk := j.ExecMergeKeyPlan()
	if len(mk.Keys) != 2 {
		t.Fatalf("merge join keys on %d pair(s), want both (y, cnt): %+v", len(mk.Keys), mk.Keys)
	}
	want := [][2]int{{0, 2}, {1, 3}}
	for i, k := range mk.Keys {
		l, lok := k.Left.(*ColumnRef)
		r, rok := k.Right.(*ColumnRef)
		if !lok || !rok {
			t.Fatalf("non-ColumnRef key pair %d: %+v", i, k)
		}
		if got := [2]int{l.Index, r.Index}; got != want[i] {
			t.Errorf("key pair %d = %v, want %v", i, got, want[i])
		}
	}
	if mk.Residual != nil {
		t.Errorf("residual should be empty once both equalities are merge keys, got %v", mk.Residual)
	}
	// P2.3 adds keys; it does not re-pick the lead the executor sorts on.
	if lk, _ := j.LeftKey.(*ColumnRef); lk == nil || lk.Index != 0 {
		t.Errorf("lead merge key moved: LeftKey=%v", j.LeftKey)
	}
	// Int64Keys is a hash-encoding question; a comparator-driven merge has
	// no fixed-width lane to select.
	if mk.Int64Keys {
		t.Errorf("ExecMergeKeyPlan set Int64Keys, which selects the hash packing lane")
	}
}

// TestExecMergeKeyPlanDeclinesUnsafePair pins the conservatism that makes the
// residual narrowing safe. float8 datums are held as TEXT (`PGFloatOut`), so
// `compareDatum` orders and equates them lexicographically — `0.0` and `-0.0`
// are `=`-equal but compare unequal. Such a pair must stay OUT of the key
// tuple and keep its conjunct in the residual; dropping it from both would
// make the join emit rows the ON clause rejects.
func TestExecMergeKeyPlanDeclinesUnsafePair(t *testing.T) {
	j := mkMergeJoin(t, "float8")
	mk := j.ExecMergeKeyPlan()
	if len(mk.Keys) != 1 {
		t.Fatalf("float8 pair was folded into the merge key tuple: %+v", mk.Keys)
	}
	if mk.Residual == nil {
		t.Fatal("the declined pair lost its conjunct from the residual — the join would over-emit")
	}
	// Exactly the declined conjunct survives: the accepted lead pair is
	// discharged by the key.
	if got := len(splitAnd(mk.Residual)); got != 1 {
		t.Errorf("residual holds %d conjunct(s), want only the declined float8 equality: %v",
			got, mk.Residual)
	}
}
