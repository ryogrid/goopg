package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestFilterRowsRoundLikeClampRowEst pins M0146-0226: a restricted relation's
// row count is clamp_row_est(tuples * selectivity) (set_baserel_size_estimates),
// which rounds with rint. EstimateRows(*Filter) truncated, so TPC-DS Q4's
// year_total scans (1194684 tuples, two default equalities, 0.005^2) were sized
// at 29 rows where PG says 30, and with the extra `year_total > 0` at 9 against
// 10. The smaller inputs priced PG's Merge Join + Incremental Sort out of Q4.
func TestFilterRowsRoundLikeClampRowEst(t *testing.T) {
	scan := numGroupsScan("year_total", 1194684, 200, 200)
	two := &Filter{Child: scan, LeafLocal: true, Predicate: &BinaryOp{
		Op: parser.OpAnd, Left: numGroupsEq(0, 7), Right: numGroupsEq(1, 9)}}
	// 1194684 x (1/200)^2 = 29.8671
	if got, want := EstimateRows(two), int64(30); got != want {
		t.Errorf("two equalities: rows = %d, want %d (clamp_row_est rounds 29.87)", got, want)
	}
	// 1194684 / 200 = 5973.42 rounds down, as before.
	one := &Filter{Child: scan, LeafLocal: true, Predicate: numGroupsEq(0, 7)}
	if got, want := EstimateRows(one), int64(5973); got != want {
		t.Errorf("one equality: rows = %d, want %d", got, want)
	}
}
