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
