package executor

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// numericColumnTypmod returns a numeric column's declared precision and
// scale. ok is false for a bare `numeric`, which carries no typmod and
// stores values as given. `numeric(p)` means scale 0 (gram.y / numerictypmodin).
func numericColumnTypmod(t catalog.Type) (precision, scale int, ok bool) {
	switch len(t.Args) {
	case 0:
		return 0, 0, false
	case 1:
		return int(t.Args[0]), 0, true
	default:
		return int(t.Args[0]), int(t.Args[1]), true
	}
}

var bigTen = big.NewInt(10)

// isTypmodNumericColumn reports whether c is a numeric(p[,s]) column.
func isTypmodNumericColumn(c catalog.Column) bool {
	if c.Type.IsArray {
		return false
	}
	n := strings.ToLower(c.Type.Name)
	return (n == "numeric" || n == "decimal") && len(c.Type.Args) > 0
}

// applyNumericTypmod ports numeric.c apply_typmod: it rounds the value to the
// declared scale (round_var, half away from zero; a negative scale rounds to
// tens, hundreds, …), sets the display scale to max(scale, 0), and raises
// 22003 `numeric field overflow` when the rounded value has more integer
// digits than precision − scale. A zero value never overflows. PG applies it
// whenever a value is stored into a numeric(p,s) column — INSERT, UPDATE,
// DEFAULT, COPY FROM, MERGE — so SELECT sees `2.50` for 2.5 and `1.3` for
// 1.26 in numeric(4,1) (M0146-0087). Non-numeric datums pass through.
func applyNumericTypmod(d Datum, precision, scale int) (Datum, error) {
	switch d.Kind {
	case KindNumeric:
	case KindInt:
		// An integer literal ('3', 7) reaches here as an int.
		d = NewNumericInt64Datum(d.Int, 0)
	case KindString:
		bi, sc, err := parseNumeric(d.StringValue())
		if err != nil || sc < 0 {
			return d, nil // NaN / Infinity / unparsable: left to the codec
		}
		d = NewNumericBigDatum(bi, sc)
	default:
		return d, nil
	}
	// Fast path: an int64 mantissa already at the declared scale and inside
	// the precision is stored as it is (the common COPY / INSERT case).
	if d.Flags&flagBigNumeric == 0 && int(d.NumericScaleValue()) == scale && scale >= 0 && precision <= 18 {
		lim := int64(1)
		for i := 0; i < precision; i++ {
			lim *= 10
		}
		if mant := d.NumericMantissaValue(); mant > -lim && mant < lim {
			return d, nil
		}
	}
	m, dscale := numericRoundedMantissa(d, scale)
	if m.Sign() != 0 {
		maxdigits := precision - scale
		digits := len(new(big.Int).Abs(m).String())
		if digits-dscale > maxdigits {
			lim := fmt.Sprintf("10^%d", maxdigits)
			if maxdigits == 0 {
				lim = "1"
			}
			return Datum{}, &ExecError{
				Code:    "22003",
				Message: "numeric field overflow",
				Detail: fmt.Sprintf("A field with precision %d, scale %d must round to an absolute value less than %s.",
					precision, scale, lim),
			}
		}
	}
	if m.IsInt64() {
		return NewNumericInt64Datum(m.Int64(), int16(dscale)), nil
	}
	return NewNumericBigDatum(m, int16(dscale)), nil
}

// numericRoundedMantissa rounds a KindNumeric datum to `scale` decimal places
// (round_var: half away from zero; a negative scale rounds to tens,
// hundreds, …) and returns the mantissa at the display scale max(scale, 0).
func numericRoundedMantissa(d Datum, scale int) (*big.Int, int) {
	var m *big.Int
	if d.Flags&flagBigNumeric != 0 {
		m = new(big.Int).Set(d.NumericBigValue())
	} else {
		m = big.NewInt(d.NumericMantissaValue())
	}
	cur := int(d.NumericScaleValue())

	// Round to `scale` digits after the decimal point.
	if cur > scale {
		div := new(big.Int).Exp(bigTen, big.NewInt(int64(cur-scale)), nil)
		neg := m.Sign() < 0
		q, r := new(big.Int).QuoRem(new(big.Int).Abs(m), div, new(big.Int))
		if r.Lsh(r, 1).Cmp(div) >= 0 {
			q.Add(q, big.NewInt(1))
		}
		if neg {
			q.Neg(q)
		}
		m = q
	} else if cur < scale {
		m.Mul(m, new(big.Int).Exp(bigTen, big.NewInt(int64(scale-cur)), nil))
	}
	// m is now value × 10^scale. The display scale is never negative: a
	// negative scale keeps the rounded value as an integer.
	dscale := scale
	if scale < 0 {
		m.Mul(m, new(big.Int).Exp(bigTen, big.NewInt(int64(-scale)), nil))
		dscale = 0
	}

	return m, dscale
}

// roundNumericExact is numeric_round(value, scale): exact decimal rounding,
// half away from zero, display scale max(scale, 0). round() on a numeric
// used float64 arithmetic, so round(x, 40) printed binary artefacts such as
// -4.3099999999999996092014953319448977708817 (M0146-0087).
func roundNumericExact(d Datum, scale int) Datum {
	m, dscale := numericRoundedMantissa(d, scale)
	if m.IsInt64() {
		return NewNumericInt64Datum(m.Int64(), int16(dscale))
	}
	return NewNumericBigDatum(m, int16(dscale))
}
