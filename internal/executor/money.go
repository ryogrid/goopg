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
func moneyDatum(c int64) Datum { return NewNumericInt64Datum(c, 2) }

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
