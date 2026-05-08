package executor

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

var (
	bigNumericTen      = big.NewInt(10)
	bigNumericMinInt64 = big.NewInt(-9223372036854775808)
	bigNumericMaxInt64 = big.NewInt(9223372036854775807)
)

// numericMinSigDigits mirrors upstream's NUMERIC_MIN_SIG_DIGITS
// (postgres/src/include/utils/numeric.h:50). Division ensures the
// result carries at least this many significant decimal digits,
// which is enough for float8-equivalent precision and matches
// upstream's TPC-H output byte-for-byte.
const numericMinSigDigits = 16

// numericMaxDisplayScale mirrors upstream's NUMERIC_MAX_DISPLAY_SCALE
// (NUMERIC_MAX_PRECISION = 1000) — the upper bound on a result's
// fractional-digit count.
const numericMaxDisplayScale = 1000

// numericMant returns d's mantissa as a *big.Int, regardless of
// whether d is in the int64 fast-path lane (NumericBig == nil) or
// the big-int overflow lane (NumericBig != nil). Always returns a
// fresh *big.Int so callers can mutate without aliasing the Datum.
func numericMant(d Datum) *big.Int {
	if d.NumericBigValue() != nil {
		return new(big.Int).Set(d.NumericBigValue())
	}
	return big.NewInt(d.NumericMantissaValue())
}

// newNumeric constructs a KindNumeric Datum from a *big.Int
// mantissa and an int (scale). When the mantissa fits in int64 the
// result lands on the fast path (NumericMantissa); otherwise it
// stays in the *big.Int lane (NumericBig). Scale ≤ 0 is rejected
// only for negative values (the caller should already have clamped
// to the upstream-compatible 0..1000 range); we tolerate scale=0
// because that's the common case for integer-shaped NUMERICs.
func newNumeric(b *big.Int, scale int) Datum {
	if scale > numericMaxDisplayScale {
		// Defensive: callers should have clamped already, but
		// arithmetic at the int16 boundary is still well-defined
		// — just clip rather than corrupt.
		scale = numericMaxDisplayScale
	}
	if scale < 0 {
		scale = 0
	}
	if b.Cmp(bigNumericMinInt64) >= 0 && b.Cmp(bigNumericMaxInt64) <= 0 {
		return Datum{Kind: KindNumeric, Int: b.Int64(), Scale: int16(scale)}
	}
	bb := new(big.Int).Set(b)
	return Datum{Kind: KindNumeric, Big: bb, Scale: int16(scale)}
}

// parseNumericFast attempts an int64-only fast path for the
// overwhelmingly common case where the textual NUMERIC literal is a
// plain decimal integer with no fraction, no exponent, and a value
// fitting in 18 decimal digits (always int64-safe). Returns
// (mantissa, scale=0, true) on success; (0, 0, false) when the input
// requires the *big.Int slow path. (M0058-0003.)
//
// HammerDB's TPC-H schema declares all integer columns as NUMERIC,
// so the typical lineitem row has 16 NUMERIC columns whose textual
// payload is a small integer. The slow-path big.Int allocation
// (~100 bytes + GC pressure) used to dominate SeqScan cost (~400 ns
// per column).  This helper avoids the allocation entirely when the
// digit count is in the int64 range.
func parseNumericFast(text string) (int64, int16, bool) {
	s := text
	if len(s) == 0 {
		return 0, 0, false
	}
	// Strip a single leading sign.
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	if len(s) == 0 || len(s) > 18 {
		// Empty after sign, or more than 18 digits — uncertain int64
		// fit. Fall back to slow path. (math.MinInt64 is 19 digits
		// but we only accept 18-digit positive magnitudes; this loses
		// a sliver of fast-path coverage in exchange for a branch-free
		// loop.)
		return 0, 0, false
	}
	var v int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			// Decimal point, exponent, leading whitespace etc. all
			// fail the fast path.
			return 0, 0, false
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	return v, 0, true
}

// parseNumeric parses a SQL NUMERIC literal — `123`, `123.45`,
// `-1.5`, `1.5e3`, `1.5e-2` — into a (mantissa *big.Int, scale)
// pair. Scale is non-negative; positive exponents shift digits out
// of the fractional part (raising or lowering scale), negative
// exponents shift them in. Returns an error for malformed input.
//
// Unlike the original int64-bounded parser, this one accepts
// arbitrary precision — the carrier is *big.Int. The returned
// mantissa fits in int64 for the common case (callers go via
// newNumeric to land on the fast path).
//
// Hot callers should try parseNumericFast first (M0058-0003).
func parseNumeric(text string) (*big.Int, int16, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return nil, 0, fmt.Errorf("empty numeric literal")
	}
	neg := false
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		s = s[1:]
	}
	mantissaStr := s
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissaStr = s[:i]
		expStr := s[i+1:]
		v, err := strconv.Atoi(expStr)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid numeric exponent %q", expStr)
		}
		exp = v
	}
	intPart, fracPart := mantissaStr, ""
	if i := strings.IndexByte(mantissaStr, '.'); i >= 0 {
		intPart = mantissaStr[:i]
		fracPart = mantissaStr[i+1:]
	}
	if intPart == "" && fracPart == "" {
		return nil, 0, fmt.Errorf("invalid numeric literal %q", text)
	}
	digits := intPart + fracPart
	if digits == "" {
		return nil, 0, fmt.Errorf("invalid numeric literal %q", text)
	}
	mantissa := new(big.Int)
	if _, ok := mantissa.SetString(digits, 10); !ok {
		return nil, 0, fmt.Errorf("invalid numeric literal %q", text)
	}
	if neg {
		mantissa.Neg(mantissa)
	}
	scale := len(fracPart) - exp
	if scale < 0 {
		shift := new(big.Int).Exp(bigNumericTen, big.NewInt(int64(-scale)), nil)
		mantissa.Mul(mantissa, shift)
		scale = 0
	}
	if scale > numericMaxDisplayScale {
		return nil, 0, fmt.Errorf("numeric scale %d exceeds %d", scale, numericMaxDisplayScale)
	}
	return mantissa, int16(scale), nil
}

// numericFromInt promotes an int64 to KindNumeric form (scale=0).
func numericFromInt(n int64) Datum {
	return Datum{Kind: KindNumeric, Int: n, Scale: 0}
}

// promoteToNumeric brings two operands to KindNumeric so the
// arithmetic helpers can run uniformly. KindInt promotes to
// scale=0; KindString rejects with 42883 (the analyzer should
// have caught it, but evaluator-side checks keep mistakes
// localised). Both operands are returned as KindNumeric on success.
func promoteToNumeric(a, b Datum, op string, pos int) (Datum, Datum, error) {
	an, err := toNumeric(a, op, pos)
	if err != nil {
		return Datum{}, Datum{}, err
	}
	bn, err := toNumeric(b, op, pos)
	if err != nil {
		return Datum{}, Datum{}, err
	}
	return an, bn, nil
}

func toNumeric(d Datum, op string, pos int) (Datum, error) {
	switch d.Kind {
	case KindNumeric:
		return d, nil
	case KindInt:
		return numericFromInt(d.Int), nil
	case KindString:
		if m, s, err := parseNumeric(d.StringValue()); err == nil {
			return newNumeric(m, int(s)), nil
		}
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires numeric operands", op)}
}

// alignNumericBig upscales whichever of the two operands has the
// smaller scale so both reach a common scale. Returns the aligned
// mantissas (as fresh *big.Int) and the common scale.
func alignNumericBig(am *big.Int, as int16, bm *big.Int, bs int16) (*big.Int, *big.Int, int16) {
	common := as
	if bs > common {
		common = bs
	}
	if as < common {
		shift := new(big.Int).Exp(bigNumericTen, big.NewInt(int64(common-as)), nil)
		am = new(big.Int).Mul(am, shift)
	}
	if bs < common {
		shift := new(big.Int).Exp(bigNumericTen, big.NewInt(int64(common-bs)), nil)
		bm = new(big.Int).Mul(bm, shift)
	}
	return am, bm, common
}

// numericAdd / numericSub / numericMul / numericDiv implement the
// four arithmetic operators on KindNumeric values. NULL handling
// is the caller's responsibility (evalBinary short-circuits before
// reaching these). All operate on *big.Int internally, so int64
// overflow is no longer a concern; the result lands on the int64
// fast path automatically when it fits via newNumeric.
func numericAdd(a, b Datum) (Datum, error) {
	am, bm, scale := alignNumericBig(numericMant(a), a.Scale, numericMant(b), b.Scale)
	return newNumeric(new(big.Int).Add(am, bm), int(scale)), nil
}

func numericSub(a, b Datum) (Datum, error) {
	am, bm, scale := alignNumericBig(numericMant(a), a.Scale, numericMant(b), b.Scale)
	return newNumeric(new(big.Int).Sub(am, bm), int(scale)), nil
}

func numericMul(a, b Datum) (Datum, error) {
	scale := int(a.Scale) + int(b.Scale)
	if scale > numericMaxDisplayScale {
		return Datum{}, fmt.Errorf("numeric scale %d exceeds %d in multiply", scale, numericMaxDisplayScale)
	}
	prod := new(big.Int).Mul(numericMant(a), numericMant(b))
	return newNumeric(prod, scale), nil
}

// numericDiv computes a / b with PostgreSQL-compatible result
// scale. Mirrors `select_div_scale` (postgres/src/backend/utils/adt/
// numeric.c:10135-10195):
//
//	rscale = max(NUMERIC_MIN_SIG_DIGITS - qweight, dscale_a, dscale_b, 0)
//	rscale = min(rscale, NUMERIC_MAX_DISPLAY_SCALE)
//
// where qweight = (weight_a - weight_b) - (firstdigit_a <=
// firstdigit_b ? 1 : 0). With our DEC_DIGITS = 1 (decimal model),
// upstream's `qweight*DEC_DIGITS` collapses to qweight.
//
// Quotient is rounded half-away-from-zero, matching upstream's
// `div_var` with rounding=true. Division by zero raises 22012.
func numericDiv(a, b Datum, pos int) (Datum, error) {
	bm := numericMant(b)
	if bm.Sign() == 0 {
		return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
	}
	am := numericMant(a)
	da, db := int(a.Scale), int(b.Scale)
	if am.Sign() == 0 {
		// Zero numerator — short-circuit at max(da, db, 0).
		zScale := da
		if db > zScale {
			zScale = db
		}
		if zScale < 0 {
			zScale = 0
		}
		return newNumeric(new(big.Int).SetInt64(0), zScale), nil
	}

	rscale := numericDivScale(am, bm, da, db)

	// shift = rscale + db - da; multiply numerator by 10^shift so
	// integer division produces a quotient with `rscale` fractional
	// digits.
	shift := rscale + db - da
	num := new(big.Int).Set(am)
	if shift > 0 {
		scl := new(big.Int).Exp(bigNumericTen, big.NewInt(int64(shift)), nil)
		num.Mul(num, scl)
	} else if shift < 0 {
		scl := new(big.Int).Exp(bigNumericTen, big.NewInt(int64(-shift)), nil)
		num.Quo(num, scl)
	}

	q, rem := new(big.Int).QuoRem(num, bm, new(big.Int))
	// Round half-away-from-zero. With Go's Quo/QuoRem semantics
	// (truncated division), the sign of `rem` matches the sign of
	// `num`. We compare 2*|rem| against |bm|.
	twoAbsRem := new(big.Int).Abs(rem)
	twoAbsRem.Lsh(twoAbsRem, 1)
	absB := new(big.Int).Abs(bm)
	if twoAbsRem.Cmp(absB) >= 0 {
		// Increment magnitude by 1, preserving sign of q. When q
		// is zero, the resulting sign comes from num*bm.
		var bump *big.Int
		if q.Sign() == 0 {
			// q is zero — sign comes from the numerator times
			// the denominator's sign.
			if (num.Sign() < 0) != (bm.Sign() < 0) {
				bump = big.NewInt(-1)
			} else {
				bump = big.NewInt(1)
			}
		} else if q.Sign() > 0 {
			bump = big.NewInt(1)
		} else {
			bump = big.NewInt(-1)
		}
		q.Add(q, bump)
	}

	return newNumeric(q, rscale), nil
}

// numericDivScale implements upstream's select_div_scale rule
// (postgres/src/backend/utils/adt/numeric.c:10135-10195). Upstream
// works in NBASE=10000 (DEC_DIGITS=4 decimal digits per NBASE
// digit), so we mirror that conversion exactly: convert each
// operand's decimal mantissa+dscale to its NBASE weight and first
// NBASE digit, then apply the upstream formula
//
//	rscale = max(NUMERIC_MIN_SIG_DIGITS(16) - qweight*4, da, db, 0)
//
// where qweight = (weight1_NBASE - weight2_NBASE) - (firstdigit1_NBASE
// <= firstdigit2_NBASE ? 1 : 0). Using NBASE=10000 (not our internal
// decimal model with DEC_DIGITS=1) is necessary because the formula
// counts NBASE-aligned significant chunks, not single decimal digits
// — a 70/6 division (Q1's avg) has weight1_NBASE=weight2_NBASE=0 in
// NBASE because both fit in one NBASE digit, giving rscale=16; in a
// pure decimal model the weights differ by 1 and rscale would land
// at 15.
func numericDivScale(am, bm *big.Int, da, db int) int {
	absA := new(big.Int).Abs(am).Text(10)
	absB := new(big.Int).Abs(bm).Text(10)
	w1, fd1 := nbaseWeightAndFirstDigit(absA, da)
	w2, fd2 := nbaseWeightAndFirstDigit(absB, db)
	qweight := w1 - w2
	if fd1 <= fd2 {
		qweight--
	}
	rscale := numericMinSigDigits - qweight*4
	if da > rscale {
		rscale = da
	}
	if db > rscale {
		rscale = db
	}
	if rscale < 0 {
		rscale = 0
	}
	if rscale > numericMaxDisplayScale {
		rscale = numericMaxDisplayScale
	}
	return rscale
}

// nbaseWeightAndFirstDigit converts a value `|mantissa| * 10^-dscale`
// (with mantissa given as the decimal digit string of its absolute
// value) into upstream's NumericVar weight + first NBASE digit. NBASE
// is 10000 — each NBASE digit packs 4 consecutive decimal digits.
//
// The MSB of the value sits at decimal position `nd - 1 - dscale`
// (where nd = number of decimal digits). That MSB falls into NBASE
// digit at position floor(MSBPos / 4). The first NBASE digit's value
// is the 4-decimal-digit chunk aligned to that NBASE position,
// padded on the right with zeros if the mantissa has fewer trailing
// digits.
func nbaseWeightAndFirstDigit(absStr string, dscale int) (weight, firstdigit int) {
	if len(absStr) == 1 && absStr[0] == '0' {
		return 0, 0
	}
	nd := len(absStr)
	msbDecPos := nd - 1 - dscale
	weight = msbDecPos / 4
	if msbDecPos < 0 && msbDecPos%4 != 0 {
		// Floor-div: Go's integer division rounds toward zero,
		// but upstream's weight uses floor for negative exponents.
		weight--
	}
	posInNBASE := msbDecPos - 4*weight // 0..3
	firstdigit = 0
	for i := 0; i <= posInNBASE; i++ {
		d := byte(0)
		if i < nd {
			d = absStr[i] - '0'
		}
		firstdigit = firstdigit*10 + int(d)
	}
	return weight, firstdigit
}

// numericCmp compares two numeric values, returning -1/0/+1.
// Aligning scales in advance avoids cross-scale precision loss.
func numericCmp(a, b Datum) (int, error) {
	am, bm, _ := alignNumericBig(numericMant(a), a.Scale, numericMant(b), b.Scale)
	switch am.Cmp(bm) {
	case -1:
		return -1, nil
	case 1:
		return 1, nil
	}
	return 0, nil
}
