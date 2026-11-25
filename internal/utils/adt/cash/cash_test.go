package cash

import (
	"errors"
	"testing"
)

// TestInOut pins cash_in / cash_out against PG 18.3 under lc_monetary=C
// (values from postgres/src/test/regress/expected/money.out).
func TestInOut(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		out  string
	}{
		{"12.34", 1234, "$12.34"},
		{"$1,234.56", 123456, "$1,234.56"},
		{"-12.34", -1234, "-$12.34"},
		{"(1.00)", -100, "-$1.00"},
		{"12.345", 1235, "$12.35"},
		{"12.344", 1234, "$12.34"},
		{"1000000", 100000000, "$1,000,000.00"},
		{"0", 0, "$0.00"},
		{"  $ 7 ", 700, "$7.00"},
		{"-92233720368547758.08", -9223372036854775808, "-$92,233,720,368,547,758.08"},
		{"92233720368547758.07", 9223372036854775807, "$92,233,720,368,547,758.07"},
		{"1-", -100, "-$1.00"},
	} {
		got, err := In(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("In(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
			continue
		}
		if o := Out(got); o != tc.out {
			t.Errorf("Out(%d) = %q, want %q", got, o, tc.out)
		}
	}
	var re *RangeError
	if _, err := In("92233720368547758.08"); !errors.As(err, &re) {
		t.Errorf("overflow: err = %v, want RangeError", err)
	}
	var se *SyntaxError
	if _, err := In("12x"); !errors.As(err, &se) {
		t.Errorf("12x: err = %v, want SyntaxError", err)
	}
}

// TestWords pins cash_words against money.out.
func TestWords(t *testing.T) {
	for _, tc := range []struct {
		v    int64
		want string
	}{
		{12300, "One hundred twenty three dollars and zero cents"},
		{12423, "One hundred twenty four dollars and twenty three cents"},
		{101, "One dollar and one cent"},
		{-100, "Minus one dollar and zero cents"},
	} {
		if got := Words(tc.v); got != tc.want {
			t.Errorf("Words(%d) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

// TestLocaleMoney pins cash_out and cash_in under de_DE.UTF-8's and
// ja_JP.UTF-8's monetary data, as PG 18.3 prints and reads them.
func TestLocaleMoney(t *testing.T) {
	de := Conv{CurrencySymbol: "€", MonDecimalPoint: ",", MonThousandsSep: ".", MonGrouping: 3,
		NegativeSign: "-", FracDigits: 2, PCsPrecedes: 0, PSepBySpace: 1, NCsPrecedes: 0,
		NSepBySpace: 1, PSignPosn: 1, NSignPosn: 1}
	ja := Conv{CurrencySymbol: "￥", MonDecimalPoint: ".", MonThousandsSep: ",", MonGrouping: 3,
		NegativeSign: "-", FracDigits: 0, PCsPrecedes: 1, PSepBySpace: 0, NCsPrecedes: 1,
		NSepBySpace: 0, PSignPosn: 1, NSignPosn: 4}
	cases := []struct {
		conv  Conv
		value int64
		want  string
	}{
		{de, 123456, "1.234,56 €"},
		{de, -123456, "-1.234,56 €"},
		{ja, 1235, "￥1,235"},
		{ja, -1235, "￥-1,235"},
		{CLocale, -123456, "-$1,234.56"},
	}
	for _, c := range cases {
		if got := OutLocale(c.value, c.conv); got != c.want {
			t.Errorf("OutLocale(%d) = %q, want %q", c.value, got, c.want)
		}
	}
	if v, err := InLocale("1.234,56 €", de); err != nil || v != 123456 {
		t.Errorf("InLocale de = %d, %v", v, err)
	}
	if v, err := InLocale("￥1,235", ja); err != nil || v != 1235 {
		t.Errorf("InLocale ja = %d, %v", v, err)
	}
}
