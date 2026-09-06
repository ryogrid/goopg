package optimizer

import (
	"testing"
)

// TestJoinInfoListProvenanceMatchesJoinlistWalk pins C-04a's first cut: the
// join_info_list now comes from the deconstruction's own bottom-up
// accumulation (`deconstructJointreeScopedSJI`) rather than from a post-hoc
// walk of the joinlist's `sjinfo` fields (`collectSpecialJoinInfos`).
//
// While every outer/semi/anti join still PINS, the two must agree element for
// element AND in order — that agreement is what makes the change inert, and it
// is the reason the swap can be landed on its own. Once §3.2 relaxes the LEFT
// pin the walk goes dark for LEFT joins and only the accumulation is right; the
// LEFT rows below therefore become the divergence witness at that point rather
// than a silent behaviour change.
func TestJoinInfoListProvenanceMatchesJoinlistWalk(t *testing.T) {
	for _, from := range []string{
		"a LEFT JOIN b ON a.x = b.x",
		"a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON b.y = c.y",
		"a JOIN b ON a.x = b.x LEFT JOIN c ON b.y = c.y",
		"a LEFT JOIN b ON a.x = b.x, c JOIN d ON c.x = d.x",
		"a RIGHT JOIN b ON a.x = b.x",
		"a FULL JOIN b ON a.x = b.x",
		"a JOIN b ON a.x = b.x",
		"a, b, c",
	} {
		for _, collapse := range []bool{false, true} {
			fromExprs := parseFrom(t, from)
			jl, list := deconstructJointreeScopedSJI(fromExprs, defaultCollapseLimits(), collapse, nil)
			walk := jl.collectSpecialJoinInfos(nil)
			if len(walk) != len(list) {
				t.Fatalf("%q collapse=%v: walk has %d SJIs, accumulation has %d",
					from, collapse, len(walk), len(list))
			}
			for i := range walk {
				if walk[i] != list[i] {
					t.Fatalf("%q collapse=%v: SJI %d differs: walk %+v vs accumulation %+v",
						from, collapse, i, walk[i], list[i])
				}
			}
		}
	}
}
