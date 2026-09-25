package optimizer

import "testing"

// TestStableSyntheticTailPerm pins M0145-0016's permutation, including the
// property the whole change rests on: STABILITY.
//
// Real leaves carry the emitting column space — `buildLeafSpans` assigns their
// spans in position order — so preserving their relative order preserves every
// column offset exactly. Synthetic leaves are assigned out-of-band after the
// real total, so preserving THEIR relative order does the same. That is what
// makes the permutation column-neutral, and column-neutrality is what lets
// `remapWalkOrderFlatToSpans` compose it with the pulled-splice translation
// without renumbering anything.
func TestStableSyntheticTailPerm(t *testing.T) {
	bits := func(idx ...int) RelSet {
		rs := RelSet(0)
		for _, i := range idx {
			rs |= RelSet(1) << uint(i)
		}
		return rs
	}
	cases := []struct {
		name      string
		synthetic RelSet
		n         int
		want      []int
	}{
		{
			// The measured witness: TPC-DS Q78's demoted-ANTI mid-chain walk
			// `[real, synthetic, real]` (web_sales ANTI web_returns JOIN
			// date_dim), reported live as synthetic=0x0002 want=0x0004.
			"Q78 demoted-ANTI mid-chain", bits(1), 3, []int{0, 2, 1},
		},
		{"already tail is identity", bits(2), 3, []int{0, 1, 2}},
		{"no synthetic at all is identity", 0, 3, []int{0, 1, 2}},
		{"two synthetic, interleaved", bits(1, 3), 5, []int{0, 3, 1, 4, 2}},
		{"all synthetic", bits(0, 1), 2, []int{0, 1}},
	}
	for _, tc := range cases {
		got := stableSyntheticTailPerm(tc.synthetic, tc.n)
		if len(got) != len(tc.want) {
			t.Errorf("%s: perm len = %d, want %d", tc.name, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: perm = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
		// The partition must be TOTAL: it is a permutation, so every target
		// index is hit exactly once. A perm that drops or doubles an index
		// would not fail loudly — it would silently name a different leaf.
		seen := make([]bool, tc.n)
		for _, p := range got {
			if p < 0 || p >= tc.n || seen[p] {
				t.Fatalf("%s: perm %v is not a permutation of [0,%d)", tc.name, got, tc.n)
			}
			seen[p] = true
		}
		// And it must put every synthetic leaf in a tail slot, which is the
		// construction contract the seam declines on.
		if got := permuteRelSet(tc.synthetic, got); got != leafRangeRelSet(tc.n-popcountRelSet(tc.synthetic), tc.n) {
			t.Errorf("%s: permuted synthetic = %#04x, want the tail range", tc.name, uint32(got))
		}
	}
}

// popcountRelSet counts set bits; test-local, the production code never needs
// it as a standalone.
func popcountRelSet(rs RelSet) int {
	n := 0
	for r := rs; r != 0; r >>= 1 {
		n += int(r & 1)
	}
	return n
}

// TestPermuteRelSetRoundTrip pins that a mask survives the permutation with
// its membership intact — the failure mode being silent, not loud.
func TestPermuteRelSetRoundTrip(t *testing.T) {
	perm := stableSyntheticTailPerm(RelSet(1)<<1, 3) // [0, 2, 1]
	// Leaves {0,2} under [0,2,1] become {0,1}: exactly the Q78 case where a
	// folded equality spanning the two REAL leaves must keep naming them.
	if got, want := permuteRelSet(RelSet(1)|RelSet(1)<<2, perm), RelSet(1)|RelSet(1)<<1; got != want {
		t.Fatalf("permuteRelSet({0,2}) = %#04x, want %#04x", uint32(got), uint32(want))
	}
	if got, want := permuteRelSet(RelSet(1)<<1, perm), RelSet(1)<<2; got != want {
		t.Fatalf("permuteRelSet({1}) = %#04x, want %#04x — the synthetic leaf must land in the tail", uint32(got), uint32(want))
	}
	if got := permuteRelSet(0, perm); got != 0 {
		t.Fatalf("permuteRelSet(empty) = %#04x, want 0", uint32(got))
	}
}
