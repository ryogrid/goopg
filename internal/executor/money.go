package executor

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/goopg/goopg/internal/utils/adt/cash"
)

// money (M0149-0001k1). PG's Cash is an int64 count of cents
// (postgres/src/include/utils/cash.h). goopg carries a money value as a
// scale-2 KindNumeric Datum whose mantissa is that count: comparison,
// ordering and numeric arithmetic are then exact with no money-specific
// arms, and only input (cash_in), output (cash_out) and storage need to know
// the type.

// moneyDatum wraps a Cash value as goopg's money Datum.
func moneyDatum(c int64) Datum {
	if c == math.MinInt64 {
		// The int64 numeric lane cannot negate its own minimum when it
		// formats, so the most negative Cash rides the big lane.
		return NewNumericBigDatum(big.NewInt(c), 2)
	}
	return NewNumericInt64Datum(c, 2)
}

// moneyCents converts a Datum to Cash the way PG's casts into money do:
//   - text / unknown: cash_in;
//   - numeric: numeric_cash, value × 100 rounded half away from zero;
//   - int2 / int4 / int8: int4_cash / int8_cash, value × 100.
func moneyCents(d Datum) (int64, error) {
	switch d.Kind {
	case KindString:
		c, err := cash.In(d.StringValue())
		if err != nil {
			return 0, moneyInputError(err)
		}
		return c, nil
	case KindInt:
		if d.Int > math.MaxInt64/100 || d.Int < math.MinInt64/100 {
			return 0, &ExecError{Code: "22003", Message: "money out of range"}
		}
		return d.Int * 100, nil
	case KindNumeric:
		mant := d.NumericBigValue()
		if mant == nil {
			mant = big.NewInt(d.NumericMantissaValue())
		}
		scale := int(d.NumericScaleValue())
		v := new(big.Int).Set(mant)
		if scale <= 2 {
			v.Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(2-scale)), nil))
		} else {
			div := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale-2)), nil)
			q, r := new(big.Int).QuoRem(v, div, new(big.Int))
			// round half away from zero, as numeric_int8 (round_var) does
			if new(big.Int).Mul(new(big.Int).Abs(r), big.NewInt(2)).Cmp(div) >= 0 {
				if v.Sign() < 0 {
					q.Sub(q, big.NewInt(1))
				} else {
					q.Add(q, big.NewInt(1))
				}
			}
			v = q
		}
		if !v.IsInt64() {
			return 0, &ExecError{Code: "22003", Message: "money out of range"}
		}
		return v.Int64(), nil
	}
	return 0, &ExecError{Code: "42846", Message: fmt.Sprintf("cannot cast value of kind %d to money", d.Kind)}
}

// moneyInputError maps cash.In's errors to PG's SQLSTATEs.
func moneyInputError(err error) error {
	var re *cash.RangeError
	if errors.As(err, &re) {
		return &ExecError{Code: "22003", Message: err.Error()}
	}
	return &ExecError{Code: "22P02", Message: err.Error()}
}

// MoneyText renders a money Datum as cash_out does ("$1,234.56"); a value
// that is not a money Datum (legacy text) is rendered from its cash_in parse,
// or verbatim when even that fails.
func MoneyText(d Datum) string {
	if d.Kind == KindNumeric && d.NumericBigValue() == nil && d.NumericScaleValue() == 2 {
		return cash.Out(d.NumericMantissaValue())
	}
	c, err := moneyCents(d)
	if err != nil {
		return d.Format()
	}
	return cash.Out(c)
}

// isMoneyTypeName reports whether a lowered type name is money.
func isMoneyTypeName(name string) bool { return strings.EqualFold(name, "money") }

// isCashBuiltin reports whether name is one of cash.c's money operator /
// aggregate-support functions implemented by evalCashBuiltin.
func isCashBuiltin(name string) bool {
	switch name {
	case "cash_words",
		"cash_pl", "cash_mi", "cashlarger", "cashsmaller", "cash_div_cash",
		"cash_mul_flt8", "cash_mul_flt4", "cash_mul_int8", "cash_mul_int4", "cash_mul_int2",
		"flt8_mul_cash", "flt4_mul_cash", "int8_mul_cash", "int4_mul_cash", "int2_mul_cash",
		"cash_div_flt8", "cash_div_flt4", "cash_div_int8", "cash_div_int4", "cash_div_int2":
		return true
	}
	return false
}

// moneyOutOfRange / moneyDivByZero are raised while executing, where PG's
// ereport carries no cursor position, so neither sets Pos.
func moneyOutOfRange(int) error {
	return &ExecError{Code: "22003", Message: "money out of range"}
}

func moneyDivByZero(int) error {
	return &ExecError{Code: "22012", Message: "division by zero"}
}

// cashFromFloat is cash_mul_float8 / cash_div_float8's tail: rint, then the
// FLOAT8_FITS_IN_INT64 range check.
func cashFromFloat(f float64, pos int) (Datum, error) {
	r := math.RoundToEven(f)
	if math.IsNaN(r) || r < -9223372036854775808.0 || r >= 9223372036854775808.0 {
		return Datum{}, moneyOutOfRange(pos)
	}
	return moneyDatum(int64(r)), nil
}

// evalCashBuiltin implements cash.c's operator functions (PG 18):
// cash_pl / cash_mi with overflow checks, cash_mul_flt* = rint(c * f),
// cash_div_flt* = rint(c / f), cash_mul_int* / cash_div_int* in integer
// arithmetic (C division truncates toward zero), cash_div_cash = c1 / c2 in
// float8, and cashlarger / cashsmaller. Strict: a NULL argument gives NULL.
func evalCashBuiltin(name string, args []Datum, pos int) (Datum, error) {
	if name == "cash_words" && len(args) == 1 {
		if args[0].IsNull() {
			return NullDatum, nil
		}
		c, err := moneyCents(args[0])
		if err != nil {
			return Datum{}, err
		}
		return NewStringDatum(cash.Words(c)), nil
	}
	if len(args) != 2 {
		return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("function %s requires 2 arguments", name)}
	}
	for _, a := range args {
		if a.IsNull() {
			return NullDatum, nil
		}
	}
	cents := func(d Datum) (int64, error) {
		c, err := moneyCents(d)
		if ee, ok := err.(*ExecError); ok {
			ee.Pos = pos
		}
		return c, err
	}
	num := func(d Datum) (float64, error) {
		f, ok := datumToFloat64(d)
		if !ok {
			return 0, &ExecError{Code: "22P02", Pos: pos, Message: fmt.Sprintf("invalid numeric operand for %s", name)}
		}
		return f, nil
	}
	ival := func(d Datum) (int64, error) {
		if d.Kind == KindInt {
			return d.Int, nil
		}
		f, err := num(d)
		return int64(f), err
	}
	// money is the first argument except for the *_mul_cash forms.
	mi, oi := 0, 1
	if strings.HasSuffix(name, "_mul_cash") {
		mi, oi = 1, 0
	}
	c, err := cents(args[mi])
	if err != nil {
		return Datum{}, err
	}
	switch name {
	case "cash_pl", "cash_mi", "cashlarger", "cashsmaller", "cash_div_cash":
		c2, err := cents(args[1])
		if err != nil {
			return Datum{}, err
		}
		switch name {
		case "cash_pl":
			r := c + c2
			if (c2 > 0 && r < c) || (c2 < 0 && r > c) {
				return Datum{}, moneyOutOfRange(pos)
			}
			return moneyDatum(r), nil
		case "cash_mi":
			r := c - c2
			if (c2 < 0 && r < c) || (c2 > 0 && r > c) {
				return Datum{}, moneyOutOfRange(pos)
			}
			return moneyDatum(r), nil
		case "cashlarger":
			return moneyDatum(max(c, c2)), nil
		case "cashsmaller":
			return moneyDatum(min(c, c2)), nil
		default: // cash_div_cash
			if c2 == 0 {
				return Datum{}, moneyDivByZero(pos)
			}
			return floatTextDatum(PGFloatOut(float64(c)/float64(c2), 64)), nil
		}
	}
	isFloat := strings.Contains(name, "flt")
	isDiv := strings.HasPrefix(name, "cash_div_")
	if isFloat {
		f, err := num(args[oi])
		if err != nil {
			return Datum{}, err
		}
		if isDiv {
			if f == 0 {
				return Datum{}, moneyDivByZero(pos)
			}
			return cashFromFloat(float64(c)/f, pos)
		}
		return cashFromFloat(float64(c)*f, pos)
	}
	i, err := ival(args[oi])
	if err != nil {
		return Datum{}, err
	}
	if isDiv {
		if i == 0 {
			return Datum{}, moneyDivByZero(pos)
		}
		return moneyDatum(c / i), nil
	}
	r := c * i
	if c != 0 && (r/c != i || (c == -1 && i == math.MinInt64) || (i == -1 && c == math.MinInt64)) {
		return Datum{}, moneyOutOfRange(pos)
	}
	return moneyDatum(r), nil
}
