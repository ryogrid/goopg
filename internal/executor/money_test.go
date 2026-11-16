package executor

import (
	"math"
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

// TestCashBuiltins pins the cash.c operator functions the planner rewrites
// money arithmetic into (moneyArithCall): integer division truncates
// (cash_div_int4: $12.35 / 2 = $6.17, not the rounded $6.18), float operands
// round with rint, money / money is float8, a zero divisor is 22012, and
// overflow is 22003 — values from regress money.out.
func TestCashBuiltins(t *testing.T) {
	m := func(s string) Datum { c, _ := moneyCents(NewStringDatum(s)); return moneyDatum(c) }
	for _, tc := range []struct {
		name string
		args []Datum
		want string
	}{
		{"cash_pl", []Datum{m("123"), m("123")}, "$246.00"},
		{"cash_mi", []Datum{m("123"), m("123.45")}, "-$0.45"},
		{"cash_mul_int4", []Datum{m("123"), NewIntDatum(2)}, "$246.00"},
		{"int4_mul_cash", []Datum{NewIntDatum(2), m("123")}, "$246.00"},
		{"cash_div_int4", []Datum{m("12.35"), NewIntDatum(2)}, "$6.17"},
		{"cash_div_flt8", []Datum{m("12.35"), floatTextDatum("2")}, "$6.18"},
		{"cash_mul_flt8", []Datum{m("123.45"), floatTextDatum("2.5")}, "$308.62"},
		{"cashlarger", []Datum{m("1"), m("2")}, "$2.00"},
		{"cashsmaller", []Datum{m("1"), m("2")}, "$1.00"},
		{"cash_words", []Datum{m("123")}, "One hundred twenty three dollars and zero cents"},
	} {
		d, err := evalCashBuiltin(tc.name, tc.args, 0)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		got := d.StringValue()
		if tc.name != "cash_words" {
			got = MoneyText(d)
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
	if d, err := evalCashBuiltin("cash_div_cash", []Datum{m("123"), m("2")}, 0); err != nil || d.Format() != "61.5" {
		t.Errorf("cash_div_cash = %v, %v; want float8 61.5", d.Format(), err)
	}
	for _, tc := range []struct {
		name string
		args []Datum
		code string
	}{
		{"cash_div_int2", []Datum{m("1"), NewIntDatum(0)}, "22012"},
		{"cash_pl", []Datum{m("92233720368547758.07"), m("0.01")}, "22003"},
		{"cash_mul_int4", []Datum{m("92233720368547758.07"), NewIntDatum(2)}, "22003"},
	} {
		if _, err := evalCashBuiltin(tc.name, tc.args, 0); err == nil || err.(*ExecError).Code != tc.code {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.code)
		}
	}
	if got := MoneyText(moneyDatum(math.MinInt64)); got != "-$92,233,720,368,547,758.08" {
		t.Errorf("minimum money renders %q", got)
	}
	if got := moneyDatum(math.MinInt64).Format(); got != "-92233720368547758.08" {
		t.Errorf("minimum money as numeric = %q", got)
	}
}

// TestMoneyBTreeKey pins M0149-0001k5's btree key: the int64 Cash count in
// int8 order (cash_cmp), probes resolved through cash_in, and the scalar
// decode twin returning the money Datum.
func TestMoneyBTreeKey(t *testing.T) {
	col := &catalog.Column{Name: "m", Type: catalog.Type{Name: "money"}}
	var prev []byte
	for _, s := range []string{"-$750.00", "-0.01", "0", "12", "$1,000,000.00"} {
		k, err := encodeBTreeKeyForColumn(nil, NewStringDatum(s), col, 0)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if prev != nil && string(prev) >= string(k) {
			t.Errorf("key for %s does not sort after its predecessor", s)
		}
		prev = k
		d, n, handled, derr := decodeScalarBTreeKey(k, "money")
		want, _ := moneyCents(NewStringDatum(s))
		if !handled || derr != nil || n != 8 || MoneyText(d) != MoneyText(moneyDatum(want)) {
			t.Errorf("%s: decoded %q", s, MoneyText(d))
		}
	}
	if !isSupportedBTreeKeyType("money") {
		t.Error("money must be a supported btree key type")
	}
}

// TestMoneyArrayElements pins M0149-0001k5's money[] elements: in a
// fixed_length_money cluster each element is the 8-byte Cash (elemtype 790;
// a 3-element array is 24 + 3×8 = 48 bytes, as PG 18.3 measures) and the
// array renders through cash_out with array_out's quoting; without the
// capability the older text elements round-trip.
func TestMoneyArrayElements(t *testing.T) {
	defer catalog.SetFixedLengthMoney(catalog.FixedLengthMoney())
	typ := catalog.Type{Name: "money", IsArray: true}
	for _, on := range []bool{true, false} {
		catalog.SetFixedLengthMoney(on)
		enc, err := encodeValuePG(typ, NewStringDatum(`{12.34,"$1,000.00",-5}`))
		if err != nil {
			t.Fatalf("capability=%v: %v", on, err)
		}
		d, _, err := decodePhysicalPGValueMctx(typ, enc, nil)
		if err != nil {
			t.Fatalf("capability=%v: decode: %v", on, err)
		}
		if on {
			if len(enc) != 48 || d.StringValue() != `{$12.34,"$1,000.00",-$5.00}` {
				t.Errorf("capable cluster: %d bytes, %q", len(enc), d.StringValue())
			}
		} else if d.StringValue() != `{12.34,"$1,000.00",-5}` {
			t.Errorf("legacy cluster: %q", d.StringValue())
		}
	}
}
