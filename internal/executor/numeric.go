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

// parseNumeric parses a SQL NUMERIC literal — `123`, `123.45`,
// `-1.5`, `1.5e3`, `1.5e-2` — into mantissa+scale form. The
// returned scale is non-negative; positive exponents shift digits
// out of the fractional part (raising or lowering scale), negative
// exponents shift them in. Returns an error for malformed input.
//
// v0's NUMERIC carrier is int64-bounded; values whose magnitude
// exceeds int64 surface as `numeric out of int64 range`. This
// matches the deferred big-int support documented in
// docs/design/0003-0012-numeric-arithmetic.md.
func parseNumeric(text string) (int64, int8, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return 0, 0, fmt.Errorf("empty numeric literal")
	}
	neg := false
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		s = s[1:]
	}
	// Split off the exponent.
	mantissaStr := s
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissaStr = s[:i]
		expStr := s[i+1:]
		v, err := strconv.Atoi(expStr)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid numeric exponent %q", expStr)
		}
		exp = v
	}
	// Split mantissa into integer and fractional parts.
	intPart, fracPart := mantissaStr, ""
	if i := strings.IndexByte(mantissaStr, '.'); i >= 0 {
		intPart = mantissaStr[:i]
		fracPart = mantissaStr[i+1:]
	}
	if intPart == "" && fracPart == "" {
		return 0, 0, fmt.Errorf("invalid numeric literal %q", text)
	}
	digits := intPart + fracPart
	if digits == "" {
		return 0, 0, fmt.Errorf("invalid numeric literal %q", text)
	}
	mantissa, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("numeric out of int64 range: %q", text)
	}
	if neg {
		mantissa = -mantissa
	}
	scale := len(fracPart) - exp
	if scale < 0 {
		// Pad mantissa with trailing zeros so scale >= 0.
		for ; scale < 0; scale++ {
			if mantissa > maxInt64Div10 || mantissa < -maxInt64Div10 {
				return 0, 0, fmt.Errorf("numeric overflow scaling %q", text)
			}
			mantissa *= 10
		}
	}
	if scale > 127 {
		return 0, 0, fmt.Errorf("numeric scale %d exceeds int8", scale)
	}
	return mantissa, int8(scale), nil
}

const maxInt64Div10 = 922337203685477580 // (1<<63 - 1) / 10

// numericFromInt promotes an int64 to KindNumeric form (scale=0).
func numericFromInt(n int64) Datum {
	return Datum{Kind: KindNumeric, NumericMantissa: n, NumericScale: 0}
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
		if m, s, err := parseNumeric(d.String); err == nil {
			return Datum{Kind: KindNumeric, NumericMantissa: m, NumericScale: s}, nil
		}
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires numeric operands", op)}
}

// alignNumeric upscales whichever of the two has the smaller scale
// so both have the larger scale. Returns the aligned mantissas and
// the common scale; reports overflow as an error.
func alignNumeric(am int64, as int8, bm int64, bs int8) (int64, int64, int8, error) {
	common := as
	if bs > common {
		common = bs
	}
	for as < common {
		if am > maxInt64Div10 || am < -maxInt64Div10 {
			return 0, 0, 0, fmt.Errorf("numeric overflow aligning scale")
		}
		am *= 10
		as++
	}
	for bs < common {
		if bm > maxInt64Div10 || bm < -maxInt64Div10 {
			return 0, 0, 0, fmt.Errorf("numeric overflow aligning scale")
		}
		bm *= 10
		bs++
	}
	return am, bm, common, nil
}

// numericAdd / numericSub / numericMul / numericDiv implement the
// four arithmetic operators on KindNumeric values. NULL handling
// is the caller's responsibility (evalBinary short-circuits before
// reaching these).
func numericAdd(a, b Datum) (Datum, error) {
	am, bm, scale, err := alignNumeric(a.NumericMantissa, a.NumericScale, b.NumericMantissa, b.NumericScale)
	if err != nil {
		return Datum{}, err
	}
	return Datum{Kind: KindNumeric, NumericMantissa: am + bm, NumericScale: scale}, nil
}

func numericSub(a, b Datum) (Datum, error) {
	am, bm, scale, err := alignNumeric(a.NumericMantissa, a.NumericScale, b.NumericMantissa, b.NumericScale)
	if err != nil {
		return Datum{}, err
	}
	return Datum{Kind: KindNumeric, NumericMantissa: am - bm, NumericScale: scale}, nil
}

func numericMul(a, b Datum) (Datum, error) {
	scale := int(a.NumericScale) + int(b.NumericScale)
	if scale > 127 {
		return Datum{}, fmt.Errorf("numeric scale overflow in multiply")
	}
	// int64 multiply: the product can overflow when both inputs
	// are near max. Detect by computing in 128-bit space (just
	// check magnitude before).
	if !canMulInt64(a.NumericMantissa, b.NumericMantissa) {
		return Datum{}, fmt.Errorf("numeric overflow in multiply")
	}
	return Datum{Kind: KindNumeric, NumericMantissa: a.NumericMantissa * b.NumericMantissa, NumericScale: int8(scale)}, nil
}

func canMulInt64(a, b int64) bool {
	if a == 0 || b == 0 {
		return true
	}
	r := a * b
	return r/b == a
}

// numericDiv produces an approximate quotient at a target scale
// (caller-supplied). v0 picks max(scale_a, scale_b, 6) — matches
// upstream's NUMERIC division for unscaled inputs and is enough
// for TPC-H's `1 - l_discount` shape where both sides have small
// scales. Division by zero raises 22012.
func numericDiv(a, b Datum, pos int) (Datum, error) {
	if b.NumericMantissa == 0 {
		return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
	}
	target := int(a.NumericScale)
	if int(b.NumericScale) > target {
		target = int(b.NumericScale)
	}
	if target < 6 {
		target = 6
	}
	if target > 127 {
		return Datum{}, fmt.Errorf("numeric scale overflow in divide")
	}
	// Compute mantissaA * 10^(target + scaleB - scaleA) / mantissaB
	// so the quotient has `target` fractional digits.
	//
	// Use big.Int for the intermediate scale-up: for TPC-H Q14 the
	// aggregate numerator can be large enough that multiplying by 10^6
	// overflows int64 even though the final quotient still fits int64.
	shift := target + int(b.NumericScale) - int(a.NumericScale)
	am := big.NewInt(a.NumericMantissa)
	if shift > 0 {
		scale := new(big.Int).Exp(bigNumericTen, big.NewInt(int64(shift)), nil)
		am.Mul(am, scale)
	}
	q := new(big.Int).Quo(am, big.NewInt(b.NumericMantissa))
	if q.Cmp(bigNumericMinInt64) < 0 || q.Cmp(bigNumericMaxInt64) > 0 {
		return Datum{}, fmt.Errorf("numeric overflow in divide")
	}
	return Datum{Kind: KindNumeric, NumericMantissa: q.Int64(), NumericScale: int8(target)}, nil
}

// numericCmp compares two numeric values, returning -1/0/+1.
// Aligning scales in advance avoids cross-scale precision loss.
func numericCmp(a, b Datum) (int, error) {
	am, bm, _, err := alignNumeric(a.NumericMantissa, a.NumericScale, b.NumericMantissa, b.NumericScale)
	if err != nil {
		return 0, err
	}
	switch {
	case am < bm:
		return -1, nil
	case am > bm:
		return 1, nil
	}
	return 0, nil
}
