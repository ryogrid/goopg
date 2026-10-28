package executor

import (
	"math"
	"math/bits"
)

// intVariance is var_pop / var_samp / stddev_pop / stddev_samp for an integer
// variance state (st.intExact). It is exactIntVariance — PG's
// numeric_stddev_internal over int2/int4's int128 sums (Int128AggState) —
// computed in fixed width when the state lives entirely in the int128 lane
// and every intermediate fits: the numerator N·Σx² − (Σx)² and the
// denominator in 64 bits, the variance mantissa q = round(num·10^rscale/den)
// in 64 bits, and sqrt_var's argument q·10^(8f−rscale) in 128 bits. Anything
// else takes exactIntVariance unchanged. Each step mirrors that function, so
// the digits and the display scale are the same (TestIntVarianceMatchesBig).
//
// It exists because exactIntVariance built several big.Ints per group and took
// a Newton big.Int square root: 8 % of TPC-DS Q39, whose stddev_samp
// finalises ~90 000 groups (M0122-0012 C31).
func (st *aggRuntime) intVariance(isSample, isSqrt bool) Datum {
	if d, ok := st.intVarianceFixed(isSample, isSqrt); ok {
		return d
	}
	sx, sxx := st.intSums()
	return exactIntVariance(sx, sxx, st.count, isSample, isSqrt)
}

// intVarianceDisabled sends every integer variance through exactIntVariance.
// Tests set it to compare the two.
var intVarianceDisabled = false

func (st *aggRuntime) intVarianceFixed(isSample, isSqrt bool) (Datum, bool) {
	if intVarianceDisabled || !st.int128On || st.intSx != nil || st.intSxx != nil {
		return Datum{}, false
	}
	n := st.count
	if n == 0 || (isSample && n <= 1) {
		return NullDatum, true
	}
	if n < 0 {
		return Datum{}, false
	}
	// Σx as a signed 64-bit value, Σx² as an unsigned one.
	if st.sx128.hi != int64(st.sx128.lo)>>63 || st.sxx128.hi != 0 {
		return Datum{}, false
	}
	sx := int64(st.sx128.lo)
	sxx := st.sxx128.lo
	absX := uint64(sx)
	if sx < 0 {
		absX = uint64(-sx)
		if sx == math.MinInt64 {
			return Datum{}, false
		}
	}
	// num = N*sxx - sx^2, in 128 bits.
	aHi, aLo := bits.Mul64(uint64(n), sxx)
	bHi, bLo := bits.Mul64(absX, absX)
	if aHi < bHi || (aHi == bHi && aLo <= bLo) {
		// exactIntVariance's num.Sign() <= 0: make_result(&const_zero).
		return Datum{Kind: KindNumeric}, true
	}
	numLo, borrow := bits.Sub64(aLo, bLo, 0)
	numHi, _ := bits.Sub64(aHi, bHi, borrow)
	if numHi != 0 {
		return Datum{}, false
	}
	num := numLo
	var den uint64
	if isSample {
		h, l := bits.Mul64(uint64(n), uint64(n-1))
		if h != 0 {
			return Datum{}, false
		}
		den = l
	} else {
		h, l := bits.Mul64(uint64(n), uint64(n))
		if h != 0 {
			return Datum{}, false
		}
		den = l
	}
	if den == 0 {
		return NullDatum, true
	}
	// pgSelectDivScaleInt.
	w1, f1 := nbaseWeightFirstUint64(num)
	w2, f2 := nbaseWeightFirstUint64(den)
	qweight := w1 - w2
	if f1 <= f2 {
		qweight--
	}
	rscale := max(16-qweight*4, 0)
	rscale = min(rscale, 1000)
	// div_var(num, den, rscale, round): round half away from zero.
	mHi, mLo, ok := mulPow10To128(0, num, rscale)
	if !ok || mHi >= den {
		return Datum{}, false
	}
	q, r := bits.Div64(mHi, mLo, den)
	if r >= den-r {
		q++
		if q == 0 {
			return Datum{}, false
		}
	}
	if q > math.MaxInt64 {
		return Datum{}, false
	}
	if !isSqrt {
		return Datum{Kind: KindNumeric, Int: int64(q), Scale: int16(rscale)}, true
	}
	return sqrtScaledFixed(q, rscale)
}

// sqrtScaledFixed is pgSqrtScaled for a q that fits uint64, in fixed width.
func sqrtScaledFixed(q uint64, rscale int) (Datum, bool) {
	if q == 0 {
		return Datum{Kind: KindNumeric, Scale: int16(min(rscale, numericMaxDisplayScale))}, true
	}
	// pgNBaseWeightFirst(q, rscale): q * 10^(s4-rscale), s4 = rscale rounded up
	// to a multiple of 4.
	s4 := (rscale + 3) / 4 * 4
	vHi, vLo, ok := mulPow10To128(0, q, s4-rscale)
	if !ok || vHi != 0 {
		return Datum{}, false
	}
	nd := (decimalDigitsUint64(vLo) + 3) / 4
	weight := nd - 1 - s4/4
	resWeight := weight / 2
	if weight < 0 {
		resWeight = -((-weight-1)/2 + 1)
	}
	resNdigits := max(resWeight+1+(rscale+4)/4, 1)
	f := resNdigits - resWeight - 1
	e := 8*f - rscale
	if e < 0 {
		return Datum{}, false
	}
	aHi, aLo, ok := mulPow10To128(0, q, e)
	if !ok {
		return Datum{}, false
	}
	root, ok := isqrt128(aHi, aLo)
	if !ok {
		return Datum{}, false
	}
	// round_var(root / 10^(4f), rscale): half away from zero.
	de := 4*f - rscale
	if de < 0 || de >= len(pow10Uint64) {
		return Datum{}, false
	}
	drop := pow10Uint64[de]
	res, rem := root/drop, root%drop
	if rem >= drop-rem {
		res++
	}
	if res > math.MaxInt64 {
		return Datum{}, false
	}
	return Datum{Kind: KindNumeric, Int: int64(res), Scale: int16(rscale)}, true
}

// nbaseWeightFirstUint64 is pgNBaseWeightFirst(u, 0) for u > 0: the NBASE
// weight of u and its leading base-10^4 digit.
func nbaseWeightFirstUint64(u uint64) (int, uint64) {
	w := (decimalDigitsUint64(u) - 1) / 4
	return w, u / pow10Uint64[4*w]
}

// mulPow10To128 returns (hi, lo) * 10^k as a 128-bit value, ok=false on
// overflow.
func mulPow10To128(hi, lo uint64, k int) (uint64, uint64, bool) {
	for k > 0 {
		step := min(k, len(pow10Uint64)-1)
		m := pow10Uint64[step]
		pHi, pLo := bits.Mul64(lo, m)
		tHi, tLo := bits.Mul64(hi, m)
		if tHi != 0 {
			return 0, 0, false
		}
		var c uint64
		hi, c = bits.Add64(pHi, tLo, 0)
		if c != 0 {
			return 0, 0, false
		}
		lo = pLo
		k -= step
	}
	return hi, lo, true
}

// isqrt128 is floor(sqrt(hi·2^64 + lo)): a float64 estimate refined by
// Newton steps on 128/64-bit divisions and corrected to the exact floor.
func isqrt128(hi, lo uint64) (uint64, bool) {
	x := float64(hi)*0x1p64 + float64(lo)
	s := uint64(math.MaxUint64)
	if f := math.Sqrt(x); f < 0x1p64 {
		s = uint64(f)
	}
	if s == 0 {
		s = 1
	}
	for range 4 {
		if hi >= s {
			return 0, false
		}
		qd, _ := bits.Div64(hi, lo, s)
		ns := s/2 + qd/2 + (s&qd&1)
		if ns == s {
			break
		}
		s = ns
	}
	// Exact floor: s*s <= x < (s+1)*(s+1).
	for sqGreater(s, hi, lo) {
		s--
	}
	for s < math.MaxUint64 && !sqGreater(s+1, hi, lo) {
		s++
	}
	return s, true
}

// sqGreater reports whether s*s > hi·2^64 + lo.
func sqGreater(s, hi, lo uint64) bool {
	sh, sl := bits.Mul64(s, s)
	return sh > hi || (sh == hi && sl > lo)
}
