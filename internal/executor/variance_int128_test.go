package executor

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
)

// M0122-0012 slice 67 guards: int2 / int4 variance input accumulates in a
// 128-bit lane, as PG's int4_accum does (do_int128_accum), and every reader
// — finalisation, partial-state serialisation, the parallel combine — sees
// the lane folded into the exact big.Int sums.

// int128Fed returns a state whose lane carries vals, and the exact sums.
func int128Fed(vals []int64) (aggRuntime, *big.Int, *big.Int) {
	var st aggRuntime
	sx, sxx := new(big.Int), new(big.Int)
	for _, v := range vals {
		st.intExact, st.int128On = true, true
		st.sx128.addInt64(v)
		st.sxx128.addUint64(uint64(v * v))
		st.count++
		b := big.NewInt(v)
		sx.Add(sx, b)
		sxx.Add(sxx, new(big.Int).Mul(b, b))
	}
	return st, sx, sxx
}

func TestInt128LaneMatchesBigInt(t *testing.T) {
	r := rand.New(rand.NewSource(67))
	cases := [][]int64{
		{1, 2, 4},
		{-5, 3, -7, 0},
		// Repeated extremes carry the low word past 2^64 in both sums and
		// drive Σx negative through the high word.
		func() []int64 {
			var v []int64
			for range 70000 {
				v = append(v, math.MinInt32)
			}
			return v
		}(),
		func() []int64 {
			var v []int64
			for range 70000 {
				v = append(v, math.MaxInt32, math.MinInt32, math.MinInt32)
			}
			return v
		}(),
	}
	for range 50 {
		var v []int64
		for range 1 + r.Intn(500) {
			v = append(v, int64(int32(r.Uint32())))
		}
		cases = append(cases, v)
	}
	for i, vals := range cases {
		st, sx, sxx := int128Fed(vals)
		gx, gxx := st.intSums()
		if gx.Cmp(sx) != 0 || gxx.Cmp(sxx) != 0 {
			t.Fatalf("case %d: lane sums (%s, %s), want (%s, %s)", i, gx, gxx, sx, sxx)
		}
		for _, sample := range []bool{false, true} {
			for _, sqrt := range []bool{false, true} {
				got := exactIntVariance(gx, gxx, st.count, sample, sqrt)
				want := exactIntVariance(sx, sxx, st.count, sample, sqrt)
				if formatDatumDateStyle(got, nil) != formatDatumDateStyle(want, nil) {
					t.Fatalf("case %d sample=%v sqrt=%v: %s, want %s", i, sample, sqrt, formatDatumDateStyle(got, nil), formatDatumDateStyle(want, nil))
				}
			}
		}
	}
}

func TestIntSumsFoldsWithoutMutating(t *testing.T) {
	st, sx, sxx := int128Fed([]int64{3, -9, 12})
	st.intSx, st.intSxx = big.NewInt(100), big.NewInt(1000)
	gx, gxx := st.intSums()
	if want := new(big.Int).Add(sx, big.NewInt(100)); gx.Cmp(want) != 0 {
		t.Fatalf("Σx %s, want %s", gx, want)
	}
	if want := new(big.Int).Add(sxx, big.NewInt(1000)); gxx.Cmp(want) != 0 {
		t.Fatalf("Σx² %s, want %s", gxx, want)
	}
	if st.intSx.Int64() != 100 || st.intSxx.Int64() != 1000 {
		t.Fatalf("intSums modified the shared big.Int sums: (%s, %s)", st.intSx, st.intSxx)
	}
	var none aggRuntime
	if x, xx := none.intSums(); x != nil || xx != nil {
		t.Fatalf("empty state sums (%v, %v), want nils", x, xx)
	}
}

func TestInt128LaneSerialisesAndCombines(t *testing.T) {
	a, ax, axx := int128Fed([]int64{7, 8, 1000, -2})
	b, bx, bxx := int128Fed([]int64{5, 5, -40})
	// b travels as a partial state: the lane must survive serialisation.
	d, err := serializeAggRuntime("stddev_samp", &b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deserializeAggRuntime("stddev_samp", d.BytesValue())
	if err != nil {
		t.Fatal(err)
	}
	if gx, gxx := got.intSums(); gx.Cmp(bx) != 0 || gxx.Cmp(bxx) != 0 || !got.intExact {
		t.Fatalf("round trip (%v, %v, exact=%v), want (%s, %s, true)", gx, gxx, got.intExact, bx, bxx)
	}
	// Combine a lane state with a lane state and with a deserialised one.
	for name, src := range map[string]aggRuntime{"lane": b, "deserialised": got} {
		dst := a
		combineVariance(&dst, &src)
		wx, wxx := new(big.Int).Add(ax, bx), new(big.Int).Add(axx, bxx)
		if gx, gxx := dst.intSums(); gx.Cmp(wx) != 0 || gxx.Cmp(wxx) != 0 {
			t.Fatalf("%s: combined (%s, %s), want (%s, %s)", name, gx, gxx, wx, wxx)
		}
	}
	// Into an empty destination (a worker that saw nothing).
	var dst aggRuntime
	combineVariance(&dst, &b)
	if gx, _ := dst.intSums(); gx == nil || gx.Cmp(bx) != 0 || !dst.intExact {
		t.Fatalf("empty dst combined Σx %v exact=%v, want %s true", gx, dst.intExact, bx)
	}
}
