package executor

import "testing"

// TestMacaddr8LiteralParseValidation pins parseMacaddr8Literal (M0134-0139)
// against the macaddr8_in accept/reject verdicts exercised by
// postgres/src/test/regress/sql/macaddr8.sql: every 6-byte spelling of an
// address auto-widens to EUI-64 (FF/FE inserted as the 4th/5th octets), an
// 8-byte spelling passes through unchanged, mixing separator characters or
// supplying a wrong byte count is rejected, and trailing/leading junk with
// intervening whitespace is rejected too.
func TestMacaddr8LiteralParseValidation(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
		canon  string // expected macaddr8CanonicalText, only checked when wantOK
	}{
		{"08:00:2b:01:02:03     ", true, "08:00:2b:ff:fe:01:02:03"},
		{"    08:00:2b:01:02:03     ", true, "08:00:2b:ff:fe:01:02:03"},
		{"    08:00:2b:01:02:03", true, "08:00:2b:ff:fe:01:02:03"},
		{"08:00:2b:01:02:03:04:05     ", true, "08:00:2b:01:02:03:04:05"},
		{"    08:00:2b:01:02:03:04:05     ", true, "08:00:2b:01:02:03:04:05"},
		{"    08:00:2b:01:02:03:04:05", true, "08:00:2b:01:02:03:04:05"},
		{"08-00-2b-01-02-03", true, "08:00:2b:ff:fe:01:02:03"},
		{"08002b:010203", true, "08:00:2b:ff:fe:01:02:03"},
		{"08002b-010203", true, "08:00:2b:ff:fe:01:02:03"},
		{"0800.2b01.0203", true, "08:00:2b:ff:fe:01:02:03"},
		{"0800-2b01-0203", true, "08:00:2b:ff:fe:01:02:03"},
		{"08002b010203", true, "08:00:2b:ff:fe:01:02:03"},
		{"0800:2b01:0203", true, "08:00:2b:ff:fe:01:02:03"},
		{"123    08:00:2b:01:02:03", false, ""},         // leading junk before whitespace
		{"08:00:2b:01:02:03  123", false, ""},            // trailing junk after whitespace
		{"08:00:2b:01:02:03:04:05:06:07", false, ""},     // 9 bytes
		{"08-00-2b-01-02-03-04-05-06-07", false, ""},     // 10 bytes
		{"0z002b0102030405", false, ""},                  // non-hex digit
		{"08002b010203xyza", false, ""},                  // non-hex tail
		{"08:00-2b:01:02:03:04:05", false, ""},            // mixed separators
		{"08:00:2b:01.02:03:04:05", false, ""},            // mixed separators
		{"not even close", false, ""},
	}
	for _, c := range cases {
		a, b, cc, d, e, f, g, h, err := parseMacaddr8Literal(c.in)
		if c.wantOK {
			if err != nil {
				t.Errorf("parseMacaddr8Literal(%q) unexpected error: %v", c.in, err)
				continue
			}
			got := macaddr8CanonicalText(a, b, cc, d, e, f, g, h)
			if got != c.canon {
				t.Errorf("parseMacaddr8Literal(%q) canonical = %q, want %q", c.in, got, c.canon)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseMacaddr8Literal(%q) = ok, want error", c.in)
		}
	}
}

// TestMacaddr8SetBitAndTrunc pins macaddr8_set7bit and macaddr8_trunc
// (mac8.c) against macaddr8.sql's `SELECT macaddr8_set7bit(...)` row and the
// macaddr8_data.trunc column. M0134-0139.
func TestMacaddr8SetBitAndTrunc(t *testing.T) {
	a, b, c, d, e, f, g, h, err := parseMacaddr8Literal("00:08:2b:01:02:03")
	if err != nil {
		t.Fatalf("parseMacaddr8Literal: %v", err)
	}
	a, b, c, d, e, f, g, h = macaddr8Set7BitOctets(a, b, c, d, e, f, g, h)
	if got, want := macaddr8CanonicalText(a, b, c, d, e, f, g, h), "02:08:2b:ff:fe:01:02:03"; got != want {
		t.Errorf("macaddr8_set7bit(00:08:2b:01:02:03) = %q, want %q", got, want)
	}

	ta, tb, tc, td, te, tf, tg, th, terr := parseMacaddr8Literal("08:00:2a:01:02:03")
	if terr != nil {
		t.Fatalf("parseMacaddr8Literal: %v", terr)
	}
	ta, tb, tc, td, te, tf, tg, th = macaddr8TruncOctets(ta, tb, tc, td, te, tf, tg, th)
	if got, want := macaddr8CanonicalText(ta, tb, tc, td, te, tf, tg, th), "08:00:2a:00:00:00:00:00"; got != want {
		t.Errorf("trunc(08:00:2a:01:02:03) = %q, want %q", got, want)
	}
}

// TestMacaddr8Macaddr8ToMacaddrConversion pins macaddr8tomacaddr's FF/FE
// range check (mac8.c:544-566), used by ::macaddr casts on a macaddr8
// column. M0134-0139.
func TestMacaddr8ToMacaddrConversion(t *testing.T) {
	a, b, c, d, e, f, g, h, err := parseMacaddr8Literal("08:00:2b:01:02:03")
	if err != nil {
		t.Fatalf("parseMacaddr8Literal: %v", err)
	}
	ra, rb, rc, rd, re, rf, cerr := macaddr8ToMacaddrOctets(a, b, c, d, e, f, g, h)
	if cerr != nil {
		t.Fatalf("macaddr8ToMacaddrOctets: unexpected error %v", cerr)
	}
	if got, want := macaddrCanonicalText(ra, rb, rc, rd, re, rf), "08:00:2b:01:02:03"; got != want {
		t.Errorf("macaddr8tomacaddr(08:00:2b:01:02:03) = %q, want %q", got, want)
	}

	// A genuine 8-byte address whose 4th/5th octets are NOT ff/fe cannot
	// convert.
	a2, b2, c2, d2, e2, f2, g2, h2, err2 := parseMacaddr8Literal("08:00:2b:01:02:03:04:05")
	if err2 != nil {
		t.Fatalf("parseMacaddr8Literal: %v", err2)
	}
	if _, _, _, _, _, _, cerr2 := macaddr8ToMacaddrOctets(a2, b2, c2, d2, e2, f2, g2, h2); cerr2 == nil {
		t.Errorf("macaddr8ToMacaddrOctets(08:00:2b:01:02:03:04:05) = ok, want error")
	} else if cerr2.Code != "22003" {
		t.Errorf("macaddr8ToMacaddrOctets error code = %s, want 22003", cerr2.Code)
	}
}
