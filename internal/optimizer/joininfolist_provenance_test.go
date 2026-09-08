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
// since been RELAXED (C-04a's LEFT, C-04b's RIGHT) the walk goes dark and only
// the accumulation is right; those rows are the whole reason the split exists,
// so they are pinned as a DIVERGENCE rather than dropped.
//
// The cases used to run at both values of `GOOPG_PGSHAPED_COLLAPSE`, with the
// divergence expected only on the ON arm. Take3 C-06 retired the flag, so the
// ON arm is the only arm and `wantWalkDark` is unconditional.
func TestJoinInfoListProvenanceMatchesJoinlistWalk(t *testing.T) {
	cases := []struct {
		from string
		// wantWalkDark is the number of SpecialJoinInfos the joinlist walk
		// CANNOT see, because their join no longer pins an item to carry
		// them. These are exactly the constraints join_is_legal would have
		// lost.
		wantWalkDark int
	}{
		{"a LEFT JOIN b ON a.x = b.x", 1},
		{"a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON b.y = c.y", 2},
		{"a JOIN b ON a.x = b.x LEFT JOIN c ON b.y = c.y", 1},
		{"a LEFT JOIN b ON a.x = b.x, c JOIN d ON c.x = d.x", 1},
		{"a RIGHT JOIN b ON a.x = b.x", 1}, // C-04b: RIGHT flattens like LEFT
		{"a FULL JOIN b ON a.x = b.x", 0},
		{"a JOIN b ON a.x = b.x", 0},
		{"a, b, c", 0},
	}
	for _, tc := range cases {
		fromExprs := parseFrom(t, tc.from)
		jl, list := deconstructJointreeScopedSJI(fromExprs, defaultCollapseLimits(), nil)
		walk := jl.collectSpecialJoinInfos(nil)
		if len(walk) != len(list)-tc.wantWalkDark {
			t.Fatalf("%q: walk has %d SJIs, accumulation has %d, want the walk %d short",
				tc.from, len(walk), len(list), tc.wantWalkDark)
		}
		// Whatever the walk DOES see must be the accumulation's own
		// pointers, in the accumulation's order.
		j := 0
		for _, sj := range walk {
			for j < len(list) && list[j] != sj {
				j++
			}
			if j == len(list) {
				t.Fatalf("%q: the walk produced an SJI the accumulation does not have, or out of order",
					tc.from)
			}
		}
	}
}
