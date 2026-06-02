package executor

import (
	"testing"
)

func TestToCharNumericFormat(t *testing.T) {
	cases := []struct {
		val    Datum
		fmt    string
		expect string
	}{
		// FM0000 (zero-padded, fill mode)
		{NewIntDatum(0), "FM0000", "0000"},
		{NewIntDatum(150), "FM0000", "0150"},
		{NewIntDatum(1234), "FM0000", "1234"},
		{NewIntDatum(12345), "FM0000", "12345"},
		{NewIntDatum(6), "FM0000", "0006"},
		// FM suffix
		{NewIntDatum(150), "0000FM", "0150"},
		{NewIntDatum(1), "000000000000FM", "000000000001"},
		{NewIntDatum(19999), "000000000000FM", "000000019999"},
		// Non-FM 0000: adds sign space
		{NewIntDatum(150), "0000", " 0150"},
		{NewIntDatum(-150), "0000", "-0150"},
		// FM9999: no sign space, space-fill
		{NewIntDatum(150), "FM9999", "150"},
		{NewIntDatum(0), "FM9990", "0"},
		// Non-FM 9999: leading space from format + sign space
		{NewIntDatum(150), "9999", "  150"},
		{NewIntDatum(-150), "9999", " -150"},
		// FM with negative
		{NewIntDatum(-150), "FM0000", "-0150"},
		// PR format: negative places <> around number with fill spaces before <
		// Positive adds leading sign-space and trailing space for > position.
		{NewIntDatum(-123), "9999999999999999PR", "             <123>"},
		{NewIntDatum(-4567890123456789), "9999999999999999PR", "<4567890123456789>"},
		// Quoted text: literal separator space before the quote moves after the text
		// for fill-area entries, matching PostgreSQL's to_char behavior.
		{NewIntDatum(456), `99999 "text" 9999`, `      text   456`},
		// Quoted text: the literal text is inserted at its position in the format.
		// The space before a quoted segment appears BEFORE the literal text in output.
		{NewIntDatum(123), `99999 "text" 9999 "9999" 999 "\"text between quote marks\"" 9999`,
			`      text      9999     "text between quote marks"   123`},
		// SG in the middle: sign at that position, no extra leading space. M0097-0147.
		{NewIntDatum(123), "999999SG9999999999", "      +       123"},
		{NewIntDatum(456), "999999SG9999999999", "      +       456"},
		{NewIntDatum(4567890123456789), "999999SG9999999999", "456789+0123456789"},
		{NewIntDatum(-4567890123456789), "999999SG9999999999", "456789-0123456789"},
	}
	for _, tc := range cases {
		got := toCharNumericFormat(tc.val, tc.fmt)
		if got != tc.expect {
			t.Errorf("toCharNumericFormat(%v, %q) = %q, want %q", tc.val.Format(), tc.fmt, got, tc.expect)
		}
	}
}
