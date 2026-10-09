package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestLoopCountClampsSemijoinRHSToUniqueRows pins M0146-0121 against
// get_loop_count's adjust_rowcount_for_semijoins (indxpath.c): a parameterised
// path on the LHS of `x IN (SELECT y FROM rhs)` is driven by the RHS's
// unique-ified rows, so the loop count is estimate_num_groups(semi_rhs_exprs),
// not the RHS's raw row count. TPC-DS Q23's catalog_sales_pkey probe under
// `cs_item_sk IN (SELECT item_sk FROM frequent_ss_items)` was amortised over
// the CTE's 4564 rows instead of its ~200 groups, which made the nested loop
// look cheaper than PG's parallel hash join.
func TestLoopCountClampsSemijoinRHSToUniqueRows(t *testing.T) {
	s := jslCtx(t, 2)
	probed, rhs := s.findRel(rA), s.findRel(rB)
	rhs.Rows = 4564
	rhs.baseLeaf = &SeqScan{schema: Schema{{Name: "item_sk"}}}
	if got := s.loopCountFor(probed, rB); got != 4564 {
		t.Fatalf("no semijoin: loop count %v, want the outer rel's 4564 rows", got)
	}
	s.joinInfoList = []*SpecialJoinInfo{{
		Jointype:     parser.JoinSemi,
		SynLefthand:  rA,
		SynRighthand: rB,
		SemiRhsExprs: []Expr{&ColumnRef{Index: 0, Name: "item_sk"}},
	}}
	want := float64(estimateNumGroups([]Expr{&ColumnRef{Index: 0, Name: "item_sk"}}, rhs.baseLeaf, 4564))
	if want >= 4564 {
		t.Fatalf("fixture: estimate_num_groups gave %v, want fewer groups than rows", want)
	}
	if got := s.loopCountFor(probed, rB); got != want {
		t.Fatalf("semijoin RHS: loop count %v, want its unique rows %v", got, want)
	}
	// The RHS's own probe is not on the semijoin's LHS: no clamp.
	if got := s.loopCountFor(rhs, rA); got != probed.Rows {
		t.Fatalf("reverse direction: loop count %v, want %v", got, probed.Rows)
	}
}
