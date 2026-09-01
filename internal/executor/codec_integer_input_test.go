package executor

import "testing"

// TestParseIntegerInputLeadingZeroIsDecimal is the review/260831-2 EC-1 guard.
// parseIntegerInput handed the string to strconv.ParseInt with base 0, where a
// bare leading zero means OCTAL — so '0123'::int came out as 83 and '09'::int
// was a syntax error. PG's integer input function reads a leading zero as an
// ordinary decimal digit ('0123' = 123, '09' = 9, verified against the PG 18.3
// oracle); only the explicit 0b/0o/0x prefixes change the base, with an
// optional sign in front.
func TestParseIntegerInputLeadingZeroIsDecimal(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0123", 123},
		{"09", 9},
		{"-0123", -123},
		{" 010 ", 10},
		{"00", 0},
		{"0", 0},
		{"0_1", 1},
		// Prefixed forms keep their base, sign included.
		{"0x10", 16},
		{"0X1f", 31},
		{"-0x10", -16},
		{"+0x10", 16},
		{"0o17", 15},
		{"0O17", 15},
		{"0b101", 5},
		{"-0b11", -3},
	}
	for _, c := range cases {
		got, err := parseIntegerInput(c.in, "integer", 32)
		if err != nil {
			t.Errorf("parseIntegerInput(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseIntegerInput(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"0xg", "0_", "_0", "", "0b"} {
		if got, err := parseIntegerInput(bad, "integer", 32); err == nil {
			t.Errorf("parseIntegerInput(%q) = %d, want a syntax error", bad, got)
		}
	}
	if _, err := parseIntegerInput("2147483648", "integer", 32); err == nil {
		t.Error("parseIntegerInput(2147483648, int4) accepted an out-of-range value")
	}
}
