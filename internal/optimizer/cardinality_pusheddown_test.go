package optimizer

// M0127-P5.6-f-vi — a pushed-down restriction must be priced ONCE.
//
// `pushInnerJoinInputQuals` (and its MHJ sibling
// `pushResidualQualsIntoMHJTables`) copy a single-relation restriction from
// the residual `*Filter` down onto the relation it references, and both
// deliberately leave `f.Predicate` untouched — their documented "property
// 2", which keeps the join's own residual evaluation correct. The estimator
// then charged the clause at BOTH ends.
//
// The visible damage is on the JOIN, not on the scan: a join above a
// filtered scan came out scaled by the selectivity its own scan had already
// applied. On TPC-DS SF0.5 the row-preserving `store_sales ⋈ store`
// (`s_store_sk` unique, 12 rows) estimated 367 128 rows against a 726 987-row
// left input with `d_year > 1999` pushed down, and 1 439 608 — its left input
// exactly — with no restriction at all. PG's equivalent join is 2 583 → 2 465:
// `distribute_restrictinfo_to_rels` MOVES the clause into the baserel's
// baserestrictinfo, so `calc_joinrel_size_estimate` never re-prices it.
//
// The two tests below are the discriminating pair: same tree, same
// conjunct, only `PushedBelow` differs.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// pushedDownFixture builds the SF0.5 probe's shape at unit scale:
//
//	Filter(d_year > 1999)            ← the residual copy left above the join
//	  └ Join(ss_store_sk = s_store_sk)
//	      ├ Filter(d_year > 1999)    ← the copy the pushdown pass attached
//	      │   └ SeqScan dim
//	      └ SeqScan store
//
// `dim` is 1000 rows with nd(c1)=200 so the restriction's selectivity is a
// round 1/200; the join is row-preserving (both key columns nd=12), so any
// deviation from the left input's count is the double-charge and nothing else.
func pushedDownFixture() (outer *Filter, join *Join, restrictAbove Expr) {
	dim := scanWithStats("dim", 1000, 12, 200)
	store := scanWithStats("store", 12, 12)

	// Leaf coordinates: c1 is dim's own second column.
	leafCopy := &BinaryOp{Op: parser.OpEq, Left: jrCol(1), Right: &IntegerConst{Value: 1999}}
	filtered := &Filter{Child: dim, Predicate: leafCopy, LeafLocal: true}

	join = mergedJoin(JoinTypeInner, filtered, store)
	lw := len(filtered.Output())
	join.Predicate = &BinaryOp{Op: parser.OpEq, Left: jrCol(0), Right: jrCol(lw)}
	join.LeftKey, join.RightKey = jrCol(0), jrCol(lw)

	// Merged coordinates: the same clause as the residual Filter holds it.
	restrictAbove = &BinaryOp{Op: parser.OpEq, Left: jrCol(1), Right: &IntegerConst{Value: 1999}}
	outer = &Filter{Child: join, Predicate: restrictAbove}
	return outer, join, restrictAbove
}

func TestPushedDownRestrictionIsPricedOnce(t *testing.T) {
	outer, join, restrict := pushedDownFixture()

	// Baseline: the join is row-preserving, so it carries its filtered
	// left input unchanged. (If this drifts the fixture is wrong, not the
	// behaviour under test.)
	if got, want := EstimateRows(join), int64(5); got != want {
		t.Fatalf("join estimate = %d, want %d (filtered dim 1000/200 = 5, × 12 / 12)", got, want)
	}

	// Unrecorded — the historical behaviour, kept as the control: the
	// clause is charged a second time and the estimate collapses.
	if got := EstimateRows(outer); got != 1 {
		t.Fatalf("control (PushedBelow empty): estimate = %d, want the double-charged 1", got)
	}

	// Recorded: priced exactly once, so the Filter is a pass-through over
	// a join whose input was already restricted.
	outer.notePushedBelow(restrict)
	if got, want := EstimateRows(outer), EstimateRows(join); got != want {
		t.Fatalf("with PushedBelow: estimate = %d, want %d (the join's own count — "+
			"the restriction was already charged at the scan)", got, want)
	}
}

// A conjunct that was NOT pushed down still has to be charged, or the fix
// would trade a double-charge for no charge at all.
func TestUnpushedConjunctIsStillCharged(t *testing.T) {
	outer, join, restrict := pushedDownFixture()

	// A second, independent conjunct on the store side (nd = 12), never
	// pushed anywhere.
	lw := len(join.Left.Output())
	extra := &BinaryOp{Op: parser.OpEq, Left: jrCol(lw), Right: &IntegerConst{Value: 7}}
	outer.Predicate = &BinaryOp{Op: parser.OpAnd, Left: restrict, Right: extra}
	outer.notePushedBelow(restrict)

	// 5 rows × 1/12 → below 1, which scaleByFloat floors at 1. Assert the
	// selectivity directly so the floor cannot mask a missing charge.
	if got := filterSelectivity(outer); got >= 1.0 {
		t.Fatalf("filterSelectivity = %v, want the unpushed conjunct's 1/12 charge", got)
	}
	if got, want := filterSelectivity(outer), 1.0/12.0; got != want {
		t.Fatalf("filterSelectivity = %v, want exactly %v (only the unpushed conjunct)", got, want)
	}
}

// notePushedBelow must be idempotent: both pushdown passes are re-walk safe
// and a second walk over the same plan must not grow the list.
func TestNotePushedBelowIsIdempotent(t *testing.T) {
	outer, _, restrict := pushedDownFixture()
	outer.notePushedBelow(restrict)
	outer.notePushedBelow(restrict)
	// A structurally equal CLONE, which is what a rewriter hands back.
	clone := &BinaryOp{Op: parser.OpEq, Left: jrCol(1), Right: &IntegerConst{Value: 1999}}
	outer.notePushedBelow(clone)
	if got := len(outer.PushedBelow); got != 1 {
		t.Fatalf("PushedBelow has %d entries, want 1", got)
	}
	if !outer.pricedBelow(clone) {
		t.Fatalf("pricedBelow must match a structural clone, not just the original pointer")
	}
}
