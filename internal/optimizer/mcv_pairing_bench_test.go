package optimizer

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func benchMCV(n int, offset int) []catalog.MCVEntry {
	out := make([]catalog.MCVEntry, n)
	for i := range out {
		out[i] = catalog.MCVEntry{Value: fmt.Sprintf("v%05d", i+offset), Frequency: 1.0 / float64(n)}
	}
	return out
}

// pairMCVsNested is the pre-OP1-2 pairing, kept so the differential test can
// assert the indexed form pairs identically.
func pairMCVsNested(mcv1, mcv2 []catalog.MCVEntry, clamped2 int) (float64, int) {
	matched2 := make([]bool, clamped2)
	freq, n := 0.0, 0
	for i := range mcv1 {
		for k := 0; k < clamped2; k++ {
			if matched2[k] || mcv1[i].Value != mcv2[k].Value {
				continue
			}
			matched2[k] = true
			n++
			freq += mcv1[i].Frequency
			break
		}
	}
	return freq, n
}

// pairMCVsIndexed mirrors the shipped pairing in semiPairMatchFraction.
func pairMCVsIndexed(mcv1, mcv2 []catalog.MCVEntry, clamped2 int) (float64, int) {
	first := make(map[string]int, clamped2)
	for k := clamped2 - 1; k >= 0; k-- {
		first[mcv2[k].Value] = k
	}
	matched2 := make([]bool, clamped2)
	freq, n := 0.0, 0
	for i := range mcv1 {
		k, ok := first[mcv1[i].Value]
		if !ok {
			continue
		}
		for k < clamped2 && matched2[k] && mcv2[k].Value == mcv1[i].Value {
			k++
		}
		if k >= clamped2 || matched2[k] || mcv2[k].Value != mcv1[i].Value {
			continue
		}
		matched2[k] = true
		n++
		freq += mcv1[i].Frequency
	}
	return freq, n
}

// TestMCVPairingMatchesNested pins review/260831 OP1-2: indexing the second MCV
// list must pair exactly what the nested scan paired, including partial
// overlaps, duplicates and a clamped second list.
func TestMCVPairingMatchesNested(t *testing.T) {
	dup := append(benchMCV(5, 0), benchMCV(5, 0)...)
	cases := []struct{ a, b []catalog.MCVEntry }{
		{benchMCV(10, 0), benchMCV(10, 0)},
		{benchMCV(10, 0), benchMCV(10, 5)},
		{benchMCV(10, 0), benchMCV(10, 100)},
		{dup, benchMCV(10, 0)},
		{benchMCV(10, 0), dup},
	}
	for i, c := range cases {
		for _, clamp := range []int{0, 3, len(c.b)} {
			wantF, wantN := pairMCVsNested(c.a, c.b, clamp)
			gotF, gotN := pairMCVsIndexed(c.a, c.b, clamp)
			if gotN != wantN || gotF != wantF {
				t.Errorf("case %d clamp %d: indexed = (%v, %d), nested = (%v, %d)", i, clamp, gotF, gotN, wantF, wantN)
			}
		}
	}
}

// BenchmarkMCVPairing measures the MCV pairing at the default statistics
// target (review/260831 OP1-2).
func BenchmarkMCVPairing(b *testing.B) {
	a := benchMCV(100, 0)
	c := benchMCV(100, 50)
	b.Run("nested", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			pairMCVsNested(a, c, len(c))
		}
	})
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			pairMCVsIndexed(a, c, len(c))
		}
	})
}
