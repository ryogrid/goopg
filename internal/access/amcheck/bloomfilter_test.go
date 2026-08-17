package amcheck

import (
	"encoding/binary"
	"math"
	"testing"
)

// elemBytes renders an integer as 8 little-endian bytes — a stable, collision-
// free way to derive distinct fingerprint elements for the property tests.
func elemBytes(i uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], i)
	return b[:]
}

// isPowerOfTwo reports whether v is a power of two (and non-zero).
func isPowerOfTwo(v uint64) bool {
	return v != 0 && v&(v-1) == 0
}

// TestBloomCreateSizing verifies the invariants bloom_create must hold: the
// bitset is a power-of-two number of bits, at least 1MB (2^23 bits) and at most
// 512MB (2^32 bits), the byte slice matches m, and kHashFuncs is in [1, 10].
func TestBloomCreateSizing(t *testing.T) {
	cases := []struct {
		name       string
		totalElems int64
		workMemKB  int
	}{
		{"tiny estimate floors to 1MB", 1, 64},
		{"small set", 1000, 1024},
		{"workmem caps below 2 bytes/elem", 10_000_000, 64}, // 64KB workmem < 2 bytes/elem
		{"large estimate, generous workmem", 5_000_000, 512 * 1024},
		{"zero estimate guarded", 0, 1024},
		{"negative estimate guarded", -5, 1024},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := bloomCreate(c.totalElems, c.workMemKB, 0)
			if !isPowerOfTwo(f.m) {
				t.Fatalf("m=%d is not a power of two", f.m)
			}
			const minBits = 1024 * 1024 * bitsPerByte // 1MB floor
			const maxBits = uint64(1) << 32           // 512MB cap
			if f.m < minBits {
				t.Errorf("m=%d below 1MB floor (%d bits)", f.m, minBits)
			}
			if f.m > maxBits {
				t.Errorf("m=%d above 512MB cap (%d bits)", f.m, maxBits)
			}
			if got, want := uint64(len(f.bitset)), f.m/bitsPerByte; got != want {
				t.Errorf("bitset len=%d, want m/8=%d", got, want)
			}
			if f.kHashFuncs < 1 || f.kHashFuncs > maxHashFuncs {
				t.Errorf("kHashFuncs=%d out of [1,%d]", f.kHashFuncs, maxHashFuncs)
			}
			// A freshly created filter has every bit unset.
			if p := f.bloomPropBitsSet(); p != 0 {
				t.Errorf("fresh filter prop bits set=%v, want 0", p)
			}
		})
	}
}

// TestBloomNoFalseNegatives is the load-bearing property: every element added
// to the filter must subsequently test as present (bloomLacksElement == false).
// A single false negative would be a spurious amcheck corruption report.
func TestBloomNoFalseNegatives(t *testing.T) {
	const n = 50_000
	f := bloomCreate(n, 1024, 0)
	for i := uint64(0); i < n; i++ {
		f.bloomAddElement(elemBytes(i))
	}
	for i := uint64(0); i < n; i++ {
		if f.bloomLacksElement(elemBytes(i)) {
			t.Fatalf("false negative: element %d reported absent after add", i)
		}
	}
}

// TestBloomFalsePositiveRate checks that the false positive rate for elements
// never added stays within the design target. The filter only reaches its
// intended density when the bitset is well-matched to the element count (the
// 1MB floor over-provisions for small sets, driving saturation and FP rate to
// ~0). We therefore size n large enough to land in the realistic regime — here
// m settles at 2^23 bits with k=6, giving ~0.5 saturation — and assert the
// observed FP rate stays comfortably under 5% (a loose bound that still catches
// a broken hash or sizing regression).
func TestBloomFalsePositiveRate(t *testing.T) {
	const n = 1_000_000
	f := bloomCreate(n, 2048, 0)
	for i := uint64(0); i < n; i++ {
		f.bloomAddElement(elemBytes(i))
	}
	// Probe a disjoint range [n, 2n): none of these were added.
	var falsePositives int
	for i := uint64(n); i < 2*n; i++ {
		if !f.bloomLacksElement(elemBytes(i)) {
			falsePositives++
		}
	}
	rate := float64(falsePositives) / float64(n)
	if rate > 0.05 {
		t.Errorf("false positive rate=%.4f exceeds 5%% bound (fp=%d/%d); "+
			"suggests a hash or sizing regression", rate, falsePositives, n)
	}
	// Sanity: with the bitset matched to the element count, bits set should be
	// near 0.5 (more hash functions are used as more memory is available per
	// element, keeping density centered there).
	if p := f.bloomPropBitsSet(); p < 0.2 || p > 0.8 {
		t.Errorf("prop bits set=%.4f outside sane [0.2,0.8] band", p)
	}
}

// TestBloomSeedDistinguishesFalsePositives verifies that distinct seeds produce
// distinct false-positive sets — the property bloom_create's seed exists for
// (so re-fingerprinting the same set is unlikely to reproduce the same false
// positives). Both filters must still have zero false negatives.
func TestBloomSeedDistinguishesFalsePositives(t *testing.T) {
	// n is sized so the filter actually carries some false positives (a sparse,
	// over-provisioned filter has none, leaving the seeds nothing to disagree
	// on). At this density the FP rate is ~2%, so the two seeds disagree on
	// thousands of absent probes.
	const n = 1_000_000
	f1 := bloomCreate(n, 1024, 0)
	f2 := bloomCreate(n, 1024, 0x9E3779B97F4A7C15)
	for i := uint64(0); i < n; i++ {
		f1.bloomAddElement(elemBytes(i))
		f2.bloomAddElement(elemBytes(i))
	}
	// No false negatives under either seed.
	for i := uint64(0); i < n; i++ {
		if f1.bloomLacksElement(elemBytes(i)) || f2.bloomLacksElement(elemBytes(i)) {
			t.Fatalf("false negative for element %d", i)
		}
	}
	// The two seeds should disagree on at least some absent probes.
	var disagreements int
	for i := uint64(n); i < 2*n; i++ {
		e := elemBytes(i)
		if f1.bloomLacksElement(e) != f2.bloomLacksElement(e) {
			disagreements++
		}
	}
	if disagreements == 0 {
		t.Error("distinct seeds produced identical false-positive behavior")
	}
}

// TestBloomEmptyAndVariableLengthElements exercises edge-case element shapes:
// the empty slice and elements of varied lengths must round-trip without panics
// and without false negatives.
func TestBloomEmptyAndVariableLengthElements(t *testing.T) {
	f := bloomCreate(100, 1024, 42)
	elems := [][]byte{
		{},
		{0x00},
		[]byte("a"),
		[]byte("the quick brown fox"),
		make([]byte, 257), // spans more than one hash mix round in any impl
	}
	for _, e := range elems {
		f.bloomAddElement(e)
	}
	for i, e := range elems {
		if f.bloomLacksElement(e) {
			t.Errorf("false negative for element %d (len %d)", i, len(e))
		}
	}
}

// TestMyBloomPower checks the power-of-two flooring helper against hand-computed
// values, including the 2^32 cap.
func TestMyBloomPower(t *testing.T) {
	cases := []struct {
		in   uint64
		want int
	}{
		{0, -1},
		{1, 0},
		{2, 1},
		{3, 1},
		{4, 2},
		{7, 2},
		{8, 3},
		{1 << 23, 23},
		{(1 << 23) + 1, 23},
		{1 << 32, 32},
		{(1 << 33), 32}, // capped at 32
		{^uint64(0), 32},
	}
	for _, c := range cases {
		if got := myBloomPower(c.in); got != c.want {
			t.Errorf("myBloomPower(%d)=%d, want %d", c.in, got, c.want)
		}
	}
}

// TestOptimalK checks the hash-function-count formula and its [1, maxHashFuncs]
// clamping. The optimal k for an m-bit, n-element filter is round(ln2 * m / n).
func TestOptimalK(t *testing.T) {
	cases := []struct {
		bitsetBits uint64
		totalElems int64
		want       int
	}{
		{1 << 23, 1, maxHashFuncs}, // huge m/n ⇒ clamp high
		{1 << 23, 1 << 30, 1},      // tiny m/n ⇒ clamp low to 1
		{16 * 1000, 1000, 11 - 1},  // round(ln2*16)=round(11.09)=11 ⇒ clamp to 10
		{1000, 1000, 1},            // round(ln2)=round(0.69)=1
	}
	for _, c := range cases {
		got := optimalK(c.bitsetBits, c.totalElems)
		if got != c.want {
			t.Errorf("optimalK(%d,%d)=%d, want %d", c.bitsetBits, c.totalElems, got, c.want)
		}
		if got < 1 || got > maxHashFuncs {
			t.Errorf("optimalK(%d,%d)=%d out of range", c.bitsetBits, c.totalElems, got)
		}
	}
	// Cross-check against the raw formula for a mid-range case.
	m := uint64(1) << 20
	n := int64(50_000)
	want := int(math.Round(math.Ln2 * float64(m) / float64(n)))
	if want < 1 {
		want = 1
	} else if want > maxHashFuncs {
		want = maxHashFuncs
	}
	if got := optimalK(m, n); got != want {
		t.Errorf("optimalK(%d,%d)=%d, want %d", m, n, got, want)
	}
}

// TestModM verifies mod_m behaves as a power-of-two mask.
func TestModM(t *testing.T) {
	for _, m := range []uint64{1, 2, 8, 1024, 1 << 20, 1 << 32} {
		mask := uint32(m - 1)
		for _, v := range []uint32{0, 1, 7, 1023, 0x1234_5678, ^uint32(0)} {
			got := modM(v, m)
			if got != v&mask {
				t.Errorf("modM(%d,%d)=%d, want %d", v, m, got, v&mask)
			}
			if uint64(got) >= m {
				t.Errorf("modM(%d,%d)=%d not < m", v, m, got)
			}
		}
	}
}
