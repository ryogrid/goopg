package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestOuterJoinRowFloor pins C-04a's §4 floor directly on the function, in
// the four shapes that matter. It is asserted here rather than through the
// search because the floor's whole purpose is to be right on shapes the search
// could not reach until C-04a admits them — "an unwinnable path is an untested
// path", so the arm is forced by hand.
func TestOuterJoinRowFloor(t *testing.T) {
	outer := &RelOptInfo{Relids: 0b01, Rows: 1000}
	inner := &RelOptInfo{Relids: 0b10, Rows: 7}

	cases := []struct {
		name string
		sj   *SpecialJoinInfo
		rows float64
		want float64
	}{
		// The reason the floor exists: an inner-join estimate below the
		// preserved side's own cardinality. A LEFT join emits at least one
		// row per LHS row (costsize.c:5610-5611).
		{"left raises to outer", &SpecialJoinInfo{Jointype: parser.JoinLeft}, 3, 1000},
		// Never lowers: the floor is a floor.
		{"left leaves a larger estimate alone", &SpecialJoinInfo{Jointype: parser.JoinLeft}, 5000, 5000},
		// RIGHT preserves the RHS, which `makeJoinRel`'s post-swap
		// orientation puts on `inner`.
		{"right raises to inner", &SpecialJoinInfo{Jointype: parser.JoinRight}, 2, 7},
		// Plain inner joins are untouched — this is the production path
		// today and it must be byte-identical.
		{"inner untouched", nil, 3, 3},
		// SEMI/ANTI's true bound is an UPPER one and the rel width is still
		// the union (C-03c); C-05 owns the pair. Not floored.
		{"semi untouched", &SpecialJoinInfo{Jointype: parser.JoinSemi}, 3, 3},
		{"anti untouched", &SpecialJoinInfo{Jointype: parser.JoinAnti}, 3, 3},
		{"full untouched", &SpecialJoinInfo{Jointype: parser.JoinFull}, 3, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applyOuterJoinRowFloor(c.rows, outer, inner, c.sj); got != c.want {
				t.Fatalf("applyOuterJoinRowFloor(%v) = %v, want %v", c.rows, got, c.want)
			}
		})
	}
}
