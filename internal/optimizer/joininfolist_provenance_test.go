package optimizer

import (
	"testing"
)

// TestJoinInfoListProvenanceMatchesJoinlistWalk pins C-04a's first cut: the
// join_info_list now comes from the deconstruction's own bottom-up
// accumulation (`deconstructJointreeScopedSJI`) rather than from a post-hoc
// walk of the joinlist's `sjinfo` fields (`collectSpecialJoinInfos`).
//
// Wherever a join still PINS the two agree element for element AND in order —
// that agreement is what made the swap inert when it landed. Where the pin has
// since been RELAXED (C-04a's LEFT, with the collapse flag on) the walk goes
// dark and only the accumulation is right; those rows are the whole reason the
// split exists, so they are pinned as a DIVERGENCE rather than dropped.
func TestJoinInfoListProvenanceMatchesJoinlistWalk(t *testing.T) {
	cases := []struct {
		from string
		// wantWalkDark is the number of SpecialJoinInfos the joinlist walk
		// CANNOT see with the collapse flag ON, because their join no longer
		// pins an item to carry them. These are exactly the constraints
		// join_is_legal would have lost.
		wantWalkDarkOnCollapse int
	}{
		{"a LEFT JOIN b ON a.x = b.x", 1},
		{"a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON b.y = c.y", 2},
		{"a JOIN b ON a.x = b.x LEFT JOIN c ON b.y = c.y", 1},
		{"a LEFT JOIN b ON a.x = b.x, c JOIN d ON c.x = d.x", 1},
		{"a RIGHT JOIN b ON a.x = b.x", 0},
		{"a FULL JOIN b ON a.x = b.x", 0},
		{"a JOIN b ON a.x = b.x", 0},
		{"a, b, c", 0},
	}
	for _, tc := range cases {
		for _, collapse := range []bool{false, true} {
			fromExprs := parseFrom(t, tc.from)
			jl, list := deconstructJointreeScopedSJI(fromExprs, defaultCollapseLimits(), collapse, nil)
			walk := jl.collectSpecialJoinInfos(nil)
			wantDark := 0
			if collapse {
				wantDark = tc.wantWalkDarkOnCollapse
			}
			if len(walk) != len(list)-wantDark {
				t.Fatalf("%q collapse=%v: walk has %d SJIs, accumulation has %d, want the walk %d short",
					tc.from, collapse, len(walk), len(list), wantDark)
			}
			// Whatever the walk DOES see must be the accumulation's own
			// pointers, in the accumulation's order.
			j := 0
			for _, sj := range walk {
				for j < len(list) && list[j] != sj {
					j++
				}
				if j == len(list) {
					t.Fatalf("%q collapse=%v: the walk produced an SJI the accumulation does not have, or out of order",
						tc.from, collapse)
				}
			}
		}
	}
}
