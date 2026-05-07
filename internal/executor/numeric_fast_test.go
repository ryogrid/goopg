package executor

import "testing"

// TestParseNumericFast_Integers verifies the int64 fast path for the
// canonical TPC-H integer-valued NUMERIC inputs. (M0058-0003.)
func TestParseNumericFast_Integers(t *testing.T) {
	cases := []struct {
		text string
		want int64
	}{
		{"0", 0},
		{"1", 1},
		{"123", 123},
		{"-1", -1},
		{"+5", 5},
		{"-9999999999", -9999999999},
		{"123456789012345678", 123456789012345678}, // 18-digit edge
	}
	for _, c := range cases {
		v, scale, ok := parseNumericFast(c.text)
		if !ok {
			t.Errorf("parseNumericFast(%q): expected fast path, got slow", c.text)
			continue
		}
		if v != c.want {
			t.Errorf("parseNumericFast(%q) = %d; want %d", c.text, v, c.want)
		}
		if scale != 0 {
			t.Errorf("parseNumericFast(%q) scale = %d; want 0", c.text, scale)
		}
	}
}

// TestParseNumericFast_FallsBack verifies the fast path correctly
// declines non-integer inputs so the slow path handles them.
func TestParseNumericFast_FallsBack(t *testing.T) {
	cases := []string{
		"",
		"+",
		"-",
		"123.45",     // decimal
		"1e5",        // exponent
		"1234567890123456789",  // 19-digit, may exceed int64 magnitude
		" 123",       // leading whitespace
		"abc",        // not a number
		"--5",        // double sign
		"+-5",        // mixed sign
	}
	for _, c := range cases {
		if _, _, ok := parseNumericFast(c); ok {
			t.Errorf("parseNumericFast(%q): expected slow-path fallback, got fast path", c)
		}
	}
}
