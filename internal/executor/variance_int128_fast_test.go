package executor

import (
	"math"
	"math/bits"
	"math/rand"
	"testing"
)

// TestIntVarianceMatchesBig pins M0122-0012 C31: the fixed-width integer
// variance gives exactIntVariance's digits and display scale for every
// state it accepts, and declines (falls back) otherwise. Groups cover TPC-DS
// Q39's shape (small non-negative quantities, a few to dozens of rows),
// negative and mixed values, int32 extremes, constant groups (zero
// variance), single- and two-row groups, and large counts.
func TestIntVarianceMatchesBig(t *testing.T) {
	r := rand.New(rand.NewSource(20261027))
	var cases [][]int64
	cases = append(cases, []int64{5}, []int64{5, 5}, []int64{1, 2}, []int64{1, 2, 4}, []int64{0, 0, 0, 1},
		[]int64{math.MaxInt32, math.MinInt32}, []int64{-7, 3, -5, 0})
	for range 3000 {
		n := 1 + r.Intn(40)
		var v []int64
		switch r.Intn(4) {
		case 0: // Q39: inv_quantity_on_hand-like
			for range n {
				v = append(v, int64(r.Intn(1001)))
			}
		case 1:
			for range n {
				v = append(v, int64(r.Intn(21)-10))
			}
		case 2:
			c := int64(r.Intn(100))
			for range n {
				v = append(v, c)
			}
		default:
			for range n {
				v = append(v, int64(int32(r.Uint32())))
			}
		}
		cases = append(cases, v)
	}
	big := make([]int64, 0, 20000)
	for range 20000 {
		big = append(big, int64(r.Intn(1000000)))
	}
	cases = append(cases, big)
	fixed := 0
	for i, vals := range cases {
		st, sx, sxx := int128Fed(vals)
		for _, sample := range []bool{false, true} {
			for _, sqrt := range []bool{false, true} {
				want := exactIntVariance(sx, sxx, st.count, sample, sqrt)
				if d, ok := st.intVarianceFixed(sample, sqrt); ok {
					fixed++
					if formatDatumDateStyle(d, nil) != formatDatumDateStyle(want, nil) || d.Scale != want.Scale || d.Kind != want.Kind {
						t.Fatalf("case %d %v sample=%v sqrt=%v: fixed %s (scale %d), big %s (scale %d)", i, vals, sample, sqrt,
							formatDatumDateStyle(d, nil), d.Scale, formatDatumDateStyle(want, nil), want.Scale)
					}
				}
				if got := st.intVariance(sample, sqrt); formatDatumDateStyle(got, nil) != formatDatumDateStyle(want, nil) {
					t.Fatalf("case %d sample=%v sqrt=%v: intVariance %s, want %s", i, sample, sqrt, formatDatumDateStyle(got, nil), formatDatumDateStyle(want, nil))
				}
			}
		}
	}
	if fixed < len(cases) {
		t.Fatalf("fixed-width path taken only %d times over %d cases", fixed, len(cases))
	}
}

// TestIsqrt128 pins the integer square root on perfect squares, their
// neighbours and random 128-bit values.
func TestIsqrt128(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	check := func(hi, lo uint64) {
		s, ok := isqrt128(hi, lo)
		if !ok {
			return
		}
		if sqGreater(s, hi, lo) || (s < math.MaxUint64 && !sqGreater(s+1, hi, lo)) {
			t.Fatalf("isqrt128(%d,%d) = %d is not the floor", hi, lo, s)
		}
	}
	for range 100000 {
		s := r.Uint64() >> uint(r.Intn(64))
		hi, lo := bits.Mul64(s, s)
		check(hi, lo)
		if lo > 0 {
			check(hi, lo-1)
		}
		check(r.Uint64()>>1, r.Uint64())
	}
}
