package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestMoneyDatumCastAndStorage pins M0149-0001k1:
//   - `::money` gives a scale-2 numeric of the Cash value (cash_in, numeric
//     rounding half away from zero, int × 100), so money orders as a number
//     and renders through cash_out;
//   - a capable cluster stores the 8-byte Cash, a legacy cluster the
//     cash_out text, and both decode to the same Datum with the following
//     column intact.
func TestMoneyDatumCastAndStorage(t *testing.T) {
	for _, tc := range []struct {
		in   Datum
		want int64
	}{
		{NewStringDatum("$1,234.56"), 123456},
		{NewStringDatum("(1)"), -100},
		{NewNumericInt64Datum(12345, 3), 1235},
		{NewNumericInt64Datum(-12345, 3), -1235},
		{NewIntDatum(7), 700},
	} {
		d, err := evalCast(tc.in, "money", 0, nil)
		if err != nil || d.Kind != KindNumeric || d.NumericMantissaValue() != tc.want || d.NumericScaleValue() != 2 {
			t.Errorf("evalCast(%v, money) = %v, %v; want cents %d", tc.in, d, err, tc.want)
		}
	}
	if _, err := evalCast(NewStringDatum("x12"), "money", 0, nil); err == nil || err.(*ExecError).Code != "22P02" {
		t.Errorf("x12: err = %v, want 22P02", err)
	}
	if got := MoneyText(moneyDatum(-123456)); got != "-$1,234.56" {
		t.Errorf("MoneyText = %q", got)
	}
	defer catalog.SetFixedLengthMoney(catalog.FixedLengthMoney())
	cols := []catalog.Column{{Name: "v", Type: catalog.Type{Name: "money"}}, {Name: "after", Type: catalog.Type{Name: "int4"}}}
	for _, on := range []bool{true, false} {
		catalog.SetFixedLengthMoney(on)
		data, err := EncodeRowPG(cols, Row{NewStringDatum("12.34"), NewIntDatum(99)})
		if err != nil {
			t.Fatalf("capability=%v: %v", on, err)
		}
		if on && len(data) != 12 {
			t.Errorf("capable cluster: %d bytes, want 12 (8-byte Cash + int4)", len(data))
		}
		got := make(Row, 2)
		if err := DecodeRowIntoMctxPGTuple(got, cols, data, nil, 2, nil); err != nil {
			t.Fatalf("capability=%v: decode: %v", on, err)
		}
		if MoneyText(got[0]) != "$12.34" || got[1].Int != 99 {
			t.Errorf("capability=%v: decoded %q, %d", on, MoneyText(got[0]), got[1].Int)
		}
	}
}
