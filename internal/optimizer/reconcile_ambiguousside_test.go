package optimizer

// M0127-P5.9-i — `reresolveJoinByName`'s cross-side fallback must ABSTAIN on an
// ambiguous side, not jump to the other one.
//
// The shape below is TPC-DS Q83's outer query, reduced to what matters: three
// CTE scans, each publishing a column named `item_id`, joined pairwise on it.
// Every one of those columns descends from `item.i_item_id` inside its own WITH
// arm, so they carry the SAME `SourceTableIdx` too — the disambiguator
// M0071-0009 added for Q21's `l1/l2/l3` cannot separate them, because those
// aliases were distinct range-table entries of ONE scope while these are the
// same entry of three DIFFERENT scopes.
//
// `resolveSide` therefore misses on the side the reference really belongs to
// (two matches = ambiguous = -1) and hits on the other side (one match), and
// the fallback — written for a reference whose side classification is wrong —
// moved a correctly-bound reference onto a different relation's column. Under
// `GOOPG_PGSHAPED_DP` that surfaced as a plan-time abort in
// `assertSearchedTreeNeedsNoReconcile` on seven TPC-DS queries (Q11, Q31, Q47,
// Q57, Q58, Q74, Q83); on the untagged cost path it was a silent wrong-column
// join. Both are the same defect, and it is the resolver's, not the arms'.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ambCTELeaf is one CTE scan of Q83's family: an `item_id` inherited from the
// `item` scan inside the WITH arm (hence the shared source identity) plus one
// arm-specific measure column.
func ambCTELeaf(alias, measure string) *SeqScan {
	return &SeqScan{
		Table: &catalog.Table{Name: alias},
		Alias: alias,
		schema: Schema{
			{Name: "item_id", Type: catalog.Type{Name: "text"}, SourceTableIdx: 3},
			{Name: measure, Type: catalog.Type{Name: "int8"}, SourceTableIdx: 3},
		},
	}
}

// ambItemIDRef is a reference to `item_id` already bound to merged column idx by
// the coordinate arithmetic of the createPlan arms — the binding this test says
// name resolution must not disturb.
func ambItemIDRef(idx int) *ColumnRef {
	return &ColumnRef{Index: idx, Name: "item_id", Type: catalog.Type{Name: "text"}, SourceTableIdx: 3}
}

func ambEq(l, r Expr) Expr {
	return &BinaryOp{Op: parser.OpEq, Left: l, Right: r}
}

// TestReresolveAbstainsWhenThePreferredSideIsAmbiguous is Q83's top join:
// `sr_items ⋈ cr_items` on the left (four columns, `item_id` twice) and
// `wr_items` on the right. The predicate `sr_items.item_id = wr_items.item_id`
// binds its left operand at column 0 — correctly, it is sr_items' — and the
// left side cannot confirm that because cr_items publishes the same name from
// the same source. Falling through to the right side rebinds it to 4, i.e. onto
// wr_items, which makes the predicate `wr.item_id = wr.item_id`: true for every
// row, and the join degenerates to a cross product.
func TestReresolveAbstainsWhenThePreferredSideIsAmbiguous(t *testing.T) {
	sr, cr, wr := ambCTELeaf("sr_items", "sr_item_qty"), ambCTELeaf("cr_items", "cr_item_qty"), ambCTELeaf("wr_items", "wr_item_qty")
	lower := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: sr, Right: cr,
		LeftKey: ambItemIDRef(0), RightKey: ambItemIDRef(2)}
	top := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: lower, Right: wr,
		LeftKey: ambItemIDRef(0), RightKey: ambItemIDRef(4),
		Predicate: ambEq(ambItemIDRef(0), ambItemIDRef(4))}

	reconcileNLILayoutBody(top)

	bin := top.Predicate.(*BinaryOp)
	if got := bin.Left.(*ColumnRef).Index; got != 0 {
		t.Errorf("sr_items.item_id moved from column 0 to %d; the left side is ambiguous, so resolution must abstain rather than fall through to wr_items", got)
	}
	if got := bin.Right.(*ColumnRef).Index; got != 4 {
		t.Errorf("wr_items.item_id moved from column 4 to %d", got)
	}
}

// TestReresolveAbstainsWhenTheOtherSideIsAmbiguous is the mirrored association
// the search also produces — `sr_items ⋈ (cr_items ⋈ wr_items)` — and the one
// that yields Q83's reported `item_id 2→0`. Here the reference belongs to
// cr_items at merged column 2, the RIGHT side holds two `item_id`s, and the
// left side holds exactly one: the fallback rebinds a cr_items reference onto
// sr_items.
func TestReresolveAbstainsWhenTheOtherSideIsAmbiguous(t *testing.T) {
	sr, cr, wr := ambCTELeaf("sr_items", "sr_item_qty"), ambCTELeaf("cr_items", "cr_item_qty"), ambCTELeaf("wr_items", "wr_item_qty")
	lower := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: cr, Right: wr,
		LeftKey: ambItemIDRef(0), RightKey: ambItemIDRef(2)}
	top := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: sr, Right: lower,
		LeftKey: ambItemIDRef(0), RightKey: ambItemIDRef(2),
		Predicate: ambEq(ambItemIDRef(0), ambItemIDRef(2))}

	reconcileNLILayoutBody(top)

	bin := top.Predicate.(*BinaryOp)
	if got := bin.Right.(*ColumnRef).Index; got != 2 {
		t.Errorf("cr_items.item_id moved from column 2 to %d; the right side is ambiguous, so resolution must abstain rather than fall through to sr_items", got)
	}
}

// TestAssertSearchedTreeAcceptsRepeatedCTEScans is the P5.9-i acceptance: the
// same tree, tagged as the search built it, must pass the boundary assertion.
// It is not a restatement of the two tests above — those pin the resolver, this
// one pins the consequence the seven TPC-DS queries actually hit, which is that
// a blind spot in the checker used to abort the connection at plan time.
func TestAssertSearchedTreeAcceptsRepeatedCTEScans(t *testing.T) {
	sr, cr, wr := ambCTELeaf("sr_items", "sr_item_qty"), ambCTELeaf("cr_items", "cr_item_qty"), ambCTELeaf("wr_items", "wr_item_qty")
	lower := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: sr, Right: cr,
		LeftKey: ambItemIDRef(0), RightKey: ambItemIDRef(2)}
	top := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: lower, Right: wr,
		LeftKey: ambItemIDRef(0), RightKey: ambItemIDRef(4),
		Predicate: ambEq(ambItemIDRef(0), ambItemIDRef(4))}
	top.markFromJoinSearch()

	defer func() {
		if r := recover(); r != nil {
			msg, _ := r.(string)
			if strings.Contains(msg, "needed reconciliation") {
				t.Fatalf("boundary assertion rejected a correctly-bound tree of repeated CTE scans: %v", r)
			}
			panic(r)
		}
	}()
	assertSearchedTreeNeedsNoReconcile(top)
}

// TestReresolveStillCrossesSidesOnAPlainMiss is the guard on the fix's blast
// radius. The fallback exists for `pushOneConjunct`'s residuals, whose operands
// arrive with a side classification an earlier pass invalidated; abstaining on
// AMBIGUITY must not turn into abstaining on a plain MISS, or those residuals
// keep reading the wrong column. Here `b1` is unique, lives on the right, and
// is bound as if it were on the left: it must still move.
func TestReresolveStillCrossesSidesOnAPlainMiss(t *testing.T) {
	a := &SeqScan{Table: &catalog.Table{Name: "a"}, Alias: "a", schema: cpjSchema("a", 2)}
	b := &SeqScan{Table: &catalog.Table{Name: "b"}, Alias: "b", schema: cpjSchema("b", 3)}
	ref := &ColumnRef{Index: 1, Name: "b1", Type: catalog.Type{Name: "int4"}}
	j := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: a, Right: b,
		Predicate: ambEq(&ColumnRef{Index: 0, Name: "a0", Type: catalog.Type{Name: "int4"}}, ref)}

	reconcileNLILayoutBody(j)

	if ref.Index != 3 {
		t.Errorf("b1 stayed at column %d; a name that is ABSENT from the preferred side must still resolve on the other side (pushOneConjunct residuals depend on it)", ref.Index)
	}
}
