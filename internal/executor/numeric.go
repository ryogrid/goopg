package executor

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/parser"
)

var (
	bigNumericTen      = big.NewInt(10)
	bigNumericMinInt64 = big.NewInt(-9223372036854775808)
	bigNumericMaxInt64 = big.NewInt(9223372036854775807)
)

// int64Pow10[i] = 10^i for i in [0, 18]. 10^19 overflows
// int64. Used by mulInt64Pow10 for scale alignment in the
// M0074-0006 int64 fast-path.
var int64Pow10 = [19]int64{
	1, 10, 100, 1000, 10000, 100000, 1000000,
	10000000, 100000000, 1000000000,
	10000000000, 100000000000, 1000000000000,
	10000000000000, 100000000000000,
	1000000000000000, 10000000000000000,
	100000000000000000, 1000000000000000000,
}

// mulInt64Pow10 returns v * 10^exp, with ok=false on
// int64 overflow. exp must be ≥ 0. The bound check uses
// division (math.MaxInt64 / p) to avoid the multiply
// itself wrapping. (M0074-0006.)
func mulInt64Pow10(v int64, exp int) (int64, bool) {
	if exp < 0 {
		// Negative exponent is a caller bug.
		return 0, false
	}
	if v == 0 {
		// Zero never overflows, regardless of exp.
		return 0, true
	}
	if exp > 18 {
		// 10^exp > MaxInt64; non-zero v always overflows.
		return 0, false
	}
	if exp == 0 {
		return v, true
	}
	p := int64Pow10[exp]
	if v > 0 {
		if v > math.MaxInt64/p {
			return 0, false
		}
	} else {
		if v < math.MinInt64/p {
			return 0, false
		}
	}
	return v * p, true
}

// alignNumericInt64 brings two int64 mantissas to a
// common scale via mulInt64Pow10. ok=false on overflow.
// (M0074-0006.)
func alignNumericInt64(am int64, ascale int16, bm int64, bscale int16) (int64, int64, bool) {
	diff := int(ascale) - int(bscale)
	switch {
	case diff == 0:
		return am, bm, true
	case diff > 0:
		// a has more decimals; scale b up by 10^diff
		scaled, ok := mulInt64Pow10(bm, diff)
		if !ok {
			return 0, 0, false
		}
		return am, scaled, true
	default:
		scaled, ok := mulInt64Pow10(am, -diff)
		if !ok {
			return 0, 0, false
		}
		return scaled, bm, true
	}
}

// numericCmpInt64Fast compares two int64-mantissa NUMERIC
// values after scale alignment. Returns (cmp, ok) where
// ok=false signals overflow during alignment — caller
// should fall through to the big.Int slow path.
// (M0074-0006.)
func numericCmpInt64Fast(am int64, ascale int16, bm int64, bscale int16) (int, bool) {
	am2, bm2, ok := alignNumericInt64(am, ascale, bm, bscale)
	if !ok {
		return 0, false
	}
	switch {
	case am2 < bm2:
		return -1, true
	case am2 > bm2:
		return 1, true
	}
	return 0, true
}

// addInt64Overflow returns a + b, with ok=false on int64
// overflow. (M0074-0006.)
func addInt64Overflow(a, b int64) (int64, bool) {
	r := a + b
	// Overflow iff sign(a) == sign(b) && sign(r) != sign(a).
	if (a >= 0) == (b >= 0) && (r >= 0) != (a >= 0) {
		return 0, false
	}
	return r, true
}

// subInt64Overflow returns a - b, with ok=false on int64
// overflow. (M0074-0006.)
func subInt64Overflow(a, b int64) (int64, bool) {
	r := a - b
	// Overflow iff sign(a) != sign(b) && sign(r) != sign(a).
	if (a >= 0) != (b >= 0) && (r >= 0) != (a >= 0) {
		return 0, false
	}
	return r, true
}

// mulInt64Overflow returns a * b, with ok=false on int64
// overflow. (M0074-0006.)
func mulInt64Overflow(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	// MinInt64 * -1 overflows.
	if a == math.MinInt64 || b == math.MinInt64 {
		if a == -1 || b == -1 {
			return 0, false
		}
	}
	r := a * b
	if r/b != a {
		return 0, false
	}
	return r, true
}

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
	return newBigNumericInCtx(mmgr.Perm(), bb, int16(scale))
}

// parseNumericFast attempts an int64-only fast path for the
// overwhelmingly common case where the textual NUMERIC literal is a
// plain decimal integer with no fraction, no exponent, and a value
// fitting in 18 decimal digits (always int64-safe). Returns
// (mantissa, scale=0, true) on success; (0, 0, false) when the input
// requires the *big.Int slow path. (M0058-0003.)
//
// HammerDB's TPC-H schema declares all integer columns as NUMERIC,
// parseNumericFastScale parses a NUMERIC text value with support for
// decimal points. On success, returns the mantissa as int64 and the
// actual scale (number of fractional digits).
//
// The mantissa represents the value × 10^scale. For example:
//
//	"123.45" → (12345, 2, true)     — 12345 × 10⁻² = 123.45
//	"-0.01"  → (-1, 2, true)        — -1 × 10⁻² = -0.01
//	"42"     → (42, 0, true)        — 42 × 10⁰ = 42
//
// Falls back to (0, 0, false) when:
//   - The value exceeds 18 decimal digits (int64 overflow risk)
//   - An exponent notation (e/E) is present
//   - Any character other than [+-]?[0-9]*[.]?[0-9]* digit is present
//
// expectedScale, when >= 0, is the column's declared scale (e.g. 2 for
// NUMERIC(15,2)). When expectedScale >= 0 and the actual scale differs, the
// function returns false — the caller should fall back to the big.Int path.
//
// See docs/design/tpch-round5-fixes/06.
func parseNumericFastScale(text string, expectedScale int16) (int64, int16, bool) {
	if len(text) == 0 {
		return 0, 0, false
	}
	// Strip leading sign.
	neg := false
	s := text
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	if len(s) == 0 {
		return 0, 0, false
	}
	// Reject exponent notation.
	for i := 0; i < len(s); i++ {
		if s[i] == 'e' || s[i] == 'E' {
			return 0, 0, false
		}
	}
	// Find decimal point.
	dot := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			dot = i
			break
		}
	}
	var intPart, fracPart string
	if dot >= 0 {
		intPart = s[:dot]
		fracPart = s[dot+1:]
	} else {
		intPart = s
	}
	digits := intPart + fracPart
	if len(digits) == 0 || len(digits) > 18 {
		return 0, 0, false
	}
	// Parse digits as int64.
	var v int64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, 0, false
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	actualScale := int16(len(fracPart))
	// When the column has a declared scale, verify it matches.
	if expectedScale >= 0 && actualScale != expectedScale {
		return 0, 0, false
	}
	return v, actualScale, true
}

// parseNumericFastInt parses a NUMERIC text value that is an integer
// (no decimal point, no fractional part). Returns (value, scale=0, true)
// on success.  This is the renamed original parseNumericFast.
func parseNumericFastInt(text string) (int64, int16, bool) {
	s := text
	if len(s) == 0 {
		return 0, 0, false
	}
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	if len(s) == 0 || len(s) > 18 {
		return 0, 0, false
	}
	var v int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, 0, false
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	return v, 0, true
}

// parseNumericFast is the legacy wrapper; prefers the integer-only fast path.
// New callers should use parseNumericFastInt or parseNumericFastScale directly.
func parseNumericFast(text string) (int64, int16, bool) {
	return parseNumericFastInt(text)
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
	// Strip numeric separator underscores (PostgreSQL 16+ syntax). M0097-0003.
	if strings.Contains(s, "_") {
		s = strings.ReplaceAll(s, "_", "")
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
func promoteToNumeric(a, b Datum, op parser.OpCode, pos int) (Datum, Datum, error) {
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

func toNumeric(d Datum, op parser.OpCode, pos int) (Datum, error) {
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
	// M0074-0006: int64 fast-path. Skip big.Int when both
	// operands are in the int64 lane and the aligned sum
	// doesn't overflow.
	if a.NumericBigValue() == nil && b.NumericBigValue() == nil {
		am, bm, ok := alignNumericInt64(a.NumericMantissaValue(), a.Scale,
			b.NumericMantissaValue(), b.Scale)
		if ok {
			scale := a.Scale
			if b.Scale > scale {
				scale = b.Scale
			}
			if r, ok := addInt64Overflow(am, bm); ok {
				return Datum{Kind: KindNumeric, Int: r, Scale: scale}, nil
			}
		}
	}
	am, bm, scale := alignNumericBig(numericMant(a), a.Scale, numericMant(b), b.Scale)
	return newNumeric(new(big.Int).Add(am, bm), int(scale)), nil
}

func numericSub(a, b Datum) (Datum, error) {
	// M0074-0006: int64 fast-path. Skip big.Int when both
	// operands are in the int64 lane and the aligned diff
	// doesn't overflow.
	if a.NumericBigValue() == nil && b.NumericBigValue() == nil {
		am, bm, ok := alignNumericInt64(a.NumericMantissaValue(), a.Scale,
			b.NumericMantissaValue(), b.Scale)
		if ok {
			scale := a.Scale
			if b.Scale > scale {
				scale = b.Scale
			}
			if r, ok := subInt64Overflow(am, bm); ok {
				return Datum{Kind: KindNumeric, Int: r, Scale: scale}, nil
			}
		}
	}
	am, bm, scale := alignNumericBig(numericMant(a), a.Scale, numericMant(b), b.Scale)
	return newNumeric(new(big.Int).Sub(am, bm), int(scale)), nil
}

func numericMul(a, b Datum) (Datum, error) {
	scale := int(a.Scale) + int(b.Scale)
	if scale > numericMaxDisplayScale {
		return Datum{}, fmt.Errorf("numeric scale %d exceeds %d in multiply", scale, numericMaxDisplayScale)
	}
	// M0074-0006: int64 fast-path. Skip big.Int when both
	// operands are in the int64 lane and the product
	// doesn't overflow.
	if a.NumericBigValue() == nil && b.NumericBigValue() == nil {
		if r, ok := mulInt64Overflow(a.NumericMantissaValue(), b.NumericMantissaValue()); ok {
			return Datum{Kind: KindNumeric, Int: r, Scale: int16(scale)}, nil
		}
	}
	prod := new(big.Int).Mul(numericMant(a), numericMant(b))
	return newNumeric(prod, scale), nil
}

// decimalDigitCount returns the number of decimal digits
// in |v| (returns 1 for v == 0). Uses bits.Len64 + log10
// table approximation to avoid floating-point math.
// (M0075-0005.)
//
// Algorithm: bits.Len64(v) = floor(log2(v)) + 1, so
// floor(log2(v)) = bitsLen - 1. Then floor(log10(v)) ≈
// floor(log2(v)) * log10(2). log10(2) ≈ 0.30103, scaled
// integer 1233 / 4096 = 0.30103515. The result n is an
// approximation of floor(log10(v)); refine via direct
// pow-table comparison.
func decimalDigitCount(v int64) int {
	if v == 0 {
		return 1
	}
	if v < 0 {
		v = -v
	}
	bitsLen := bits.Len64(uint64(v))
	// Approximate floor(log10(v)) — guaranteed an UPPER
	// bound, then walk down if v < 10^n.
	n := ((bitsLen - 1) * 1233) >> 12
	// Refine boundaries: if v >= 10^(n+1), bump up.
	if n+1 <= 18 && v >= int64Pow10[n+1] {
		n++
	}
	return n + 1
}

// floorDiv4 returns floor(x / 4). Go's integer division
// rounds toward zero; this adjusts for negative inputs.
// Mirrors upstream's NBASE weight floor semantics.
// (M0075-0005.)
func floorDiv4(x int) int {
	if x >= 0 {
		return x / 4
	}
	return -((-x + 3) / 4)
}

// firstNBaseDigitInt64 mirrors nbaseWeightAndFirstDigit's
// NBASE first-digit extraction, but operates on the int64
// mantissa directly without decimal-string conversion.
// |absV| is the absolute mantissa, nd its decimal digit
// count, dscale the decimal scale, w the NBASE weight.
// (M0075-0005.)
func firstNBaseDigitInt64(absV int64, nd, dscale, w int) int {
	msbDecPos := nd - 1 - dscale
	posInNBASE := msbDecPos - 4*w // 0..3
	firstdigit := 0
	for i := 0; i <= posInNBASE; i++ {
		var d int
		if i < nd {
			// Extract i-th decimal digit from absV (MSB first).
			power := nd - 1 - i
			if power >= 0 && power < len(int64Pow10) {
				divisor := int64Pow10[power]
				d = int((absV / divisor) % 10)
			}
		}
		firstdigit = firstdigit*10 + d
	}
	return firstdigit
}

// numericDivScaleInt64 is the int64 mirror of numericDivScale.
// Computes the result scale via upstream's NBASE=10000 formula
// without decimal-string conversion. (M0075-0005.)
func numericDivScaleInt64(am, bm int64, da, db int) int {
	absA := am
	if absA < 0 {
		absA = -absA
	}
	absB := bm
	if absB < 0 {
		absB = -absB
	}
	if absA == 0 {
		// Caller short-circuits zero numerator before this; defensive.
		return 0
	}
	ndA := decimalDigitCount(absA)
	ndB := decimalDigitCount(absB)
	msbA := ndA - 1 - da
	msbB := ndB - 1 - db
	wA := floorDiv4(msbA)
	wB := floorDiv4(msbB)
	fdA := firstNBaseDigitInt64(absA, ndA, da, wA)
	fdB := firstNBaseDigitInt64(absB, ndB, db, wB)
	qweight := wA - wB
	if fdA <= fdB {
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

// numericDivInt64Fast computes a / b using int64 arithmetic
// when both operands are in the int64 lane and the shifted
// numerator fits in int64. Returns (result, ok); ok=false
// signals overflow — caller falls through to big.Int slow
// path. (M0075-0005.)
//
// Result-correctness invariant: when ok=true, the output
// must match numericDiv's big.Int slow path bit-for-bit.
// Pinned by TestNumericDivInt64FastVsBigPath fuzz.
func numericDivInt64Fast(a, b Datum) (Datum, bool) {
	am := a.NumericMantissaValue()
	bm := b.NumericMantissaValue()
	da, db := int(a.Scale), int(b.Scale)

	// Zero numerator → result scale = max(da, db, 0).
	if am == 0 {
		zScale := da
		if db > zScale {
			zScale = db
		}
		if zScale < 0 {
			zScale = 0
		}
		return Datum{Kind: KindNumeric, Int: 0, Scale: int16(zScale)}, true
	}

	rscale := numericDivScaleInt64(am, bm, da, db)
	shift := rscale + db - da

	// Numerator scaling: am * 10^shift (or am / 10^|shift|).
	var num int64
	if shift > 0 {
		v, ok := mulInt64Pow10(am, shift)
		if !ok {
			return Datum{}, false
		}
		num = v
	} else if shift < 0 {
		s := -shift
		if s > 18 {
			return Datum{}, false
		}
		num = am / int64Pow10[s]
	} else {
		num = am
	}

	// Integer divide.
	q := num / bm
	rem := num % bm

	// Round half-away-from-zero (matches big.Int slow path).
	absRem := rem
	if absRem < 0 {
		absRem = -absRem
	}
	absB := bm
	if absB < 0 {
		absB = -absB
	}

	if absRem != 0 && 2*absRem >= absB {
		// 2*absRem could in theory overflow if absRem is near MaxInt64/2;
		// guard against it.
		if absRem > math.MaxInt64/2 {
			// Overflow boundary; defer to big.Int.
			return Datum{}, false
		}
		if q == 0 {
			// Sign of unrepresentable quotient comes from num*bm.
			if (num < 0) != (bm < 0) {
				q = -1
			} else {
				q = 1
			}
		} else if q > 0 {
			q++
		} else {
			q--
		}
	}

	return Datum{Kind: KindNumeric, Int: q, Scale: int16(rscale)}, true
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
//
// M0075-0005: int64 fast-path skips big.Int allocation when
// both operands are in the int64 lane and the shifted
// numerator fits in int64. Covers the dominant TPC-H avg/sum
// hot path (Q1, Q3, Q5, Q14).
func numericDiv(a, b Datum, pos int) (Datum, error) {
	if a.NumericBigValue() == nil && b.NumericBigValue() == nil {
		bmFast := b.NumericMantissaValue()
		if bmFast == 0 {
			return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
		}
		if d, ok := numericDivInt64Fast(a, b); ok {
			return d, nil
		}
	}
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
//
// M0074-0006: int64 fast-path skips big.Int allocation when
// both operands are in the int64 lane (Big == nil) and the
// aligned mantissas don't overflow int64. TPC-H NUMERIC(15,2)
// columns all fit easily; on Q5 this eliminates ~5.86 % flat
// CPU previously consumed by numericMant's per-call big.Int
// allocation.
func numericCmp(a, b Datum) (int, error) {
	if a.NumericBigValue() == nil && b.NumericBigValue() == nil {
		if cmp, ok := numericCmpInt64Fast(a.NumericMantissaValue(), a.Scale,
			b.NumericMantissaValue(), b.Scale); ok {
			return cmp, nil
		}
		// Fall through to big.Int slow path on overflow.
	}
	am, bm, _ := alignNumericBig(numericMant(a), a.Scale, numericMant(b), b.Scale)
	switch am.Cmp(bm) {
	case -1:
		return -1, nil
	case 1:
		return 1, nil
	}
	return 0, nil
}
