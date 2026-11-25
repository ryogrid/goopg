package executor

import "testing"

// TestBitStringOps pins M0149-0001h1a1's varbit.c ports: the bitwise
// operators' size check (22026), shifts that keep the length and clamp, the
// bit → int4 / int8 casts (unsigned digits into two's complement, length
// check 22003, no bit → int2 and no bit varying → integer: 42846) and the
// explicit length coercion of bit(n) / bit varying(n).
func TestBitStringOps(t *testing.T) {
	s := NewStringDatum
	for _, tc := range []struct {
		name string
		args []Datum
		want string
	}{
		{"bitor", []Datum{s("101"), s("011")}, "111"},
		{"bitand", []Datum{s("101"), s("011")}, "001"},
		{"bitxor", []Datum{s("101"), s("011")}, "110"},
		{"bitnot", []Datum{s("101")}, "010"},
		{"bitshiftleft", []Datum{s("10110"), NewIntDatum(2)}, "11000"},
		{"bitshiftleft", []Datum{s("10110"), NewIntDatum(-1)}, "01011"},
		{"bitshiftright", []Datum{s("10110"), NewIntDatum(7)}, "00000"},
		{"bitcat", []Datum{s("10"), s("01")}, "1001"},
		{"bitsetbit", []Datum{s("101"), NewIntDatum(2), NewIntDatum(0)}, "100"},
	} {
		d, err := evalBitStringFunc(tc.name, tc.args, 0)
		if err != nil || d.StringValue() != tc.want {
			t.Errorf("%s%v = %q, %v; want %q", tc.name, tc.args, d.StringValue(), err, tc.want)
		}
	}
	if _, err := evalBitStringFunc("bitor", []Datum{s("101"), s("0110")}, 0); err == nil || err.(*ExecError).Code != "22026" {
		t.Errorf("bitor size mismatch: %v, want 22026", err)
	}
	if _, err := evalBitStringFunc("bitgetbit", []Datum{s("101"), NewIntDatum(3)}, 0); err == nil || err.(*ExecError).Code != "2202E" {
		t.Errorf("bitgetbit out of range: %v, want 2202E", err)
	}
	for _, tc := range []struct {
		digits, src, target string
		want                int64
		code                string
	}{
		{"100000000000000000", "bit", "int4", 131072, ""},
		{"10000000000000000000000000000000", "bit", "integer", -2147483648, ""},
		{"", "bit", "int4", 0, ""},
		{"1" + "00000000000000000000000000000000", "bit", "int4", 0, "22003"},
		{"11111111", "bit", "int8", 255, ""},
		{"1", "bit", "int2", 0, "42846"},
		{"1", "varbit", "int4", 0, "42846"},
	} {
		d, ok, err := bitStringCast(s(tc.digits), tc.src, tc.target, 0)
		if !ok {
			t.Errorf("%s::%s from %s: not handled", tc.digits, tc.target, tc.src)
			continue
		}
		if tc.code != "" {
			if err == nil || err.(*ExecError).Code != tc.code {
				t.Errorf("%s::%s from %s: err %v, want %s", tc.digits, tc.target, tc.src, err, tc.code)
			}
			continue
		}
		if err != nil || d.Int != tc.want {
			t.Errorf("%s::%s from %s = %d, %v; want %d", tc.digits, tc.target, tc.src, d.Int, err, tc.want)
		}
	}
	if _, ok, _ := bitStringCast(s("101"), "text", "int4", 0); ok {
		t.Errorf("a text source must not take the bit cast")
	}
	if got := bitExplicitLength("101", 5, true); got != "10100" {
		t.Errorf("bit(5) explicit = %q", got)
	}
	if got := bitExplicitLength("10101", 3, false); got != "101" {
		t.Errorf("varbit(3) explicit = %q", got)
	}
}
