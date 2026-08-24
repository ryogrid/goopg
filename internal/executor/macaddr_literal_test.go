package executor

import "testing"

// TestMacaddrLiteralParseValidation pins parseMacaddrLiteral (M0134-0138)
// against the macaddr_in accept/reject verdicts exercised by
// postgres/src/test/regress/sql/macaddr.sql's macaddr_data fixture: all 7
// sscanf-format spellings of the same address are accepted and canonicalize
// identically, an unbounded colon-run out of octet range is rejected with a
// distinct SQLSTATE, and malformed input is rejected outright.
func TestMacaddrLiteralParseValidation(t *testing.T) {
	cases := []struct {
		in      string
		wantOK  bool
		wantErr string // SQLSTATE, only checked when !wantOK
		canon   string // expected macaddrCanonicalText, only checked when wantOK
	}{
		{"08:00:2b:01:02:03", true, "", "08:00:2b:01:02:03"},
		{"08-00-2b-01-02-03", true, "", "08:00:2b:01:02:03"},
		{"08002b:010203", true, "", "08:00:2b:01:02:03"},
		{"08002b-010203", true, "", "08:00:2b:01:02:03"},
		{"0800.2b01.0203", true, "", "08:00:2b:01:02:03"},
		{"0800-2b01-0203", true, "", "08:00:2b:01:02:03"},
		{"08002b010203", true, "", "08:00:2b:01:02:03"},
		{"0800:2b01:0203", false, "22P02", ""}, // only 3 groups, not 6
		{"not even close", false, "22P02", ""},
		{"08:00:2b:01:02:ZZ", false, "22P02", ""}, // ZZ not hex
		{"08:00:2b:01:02:", false, "22P02", ""},   // trailing empty field
		// Unbounded colon form can overrun 0xff -> distinct SQLSTATE.
		{"100:00:2b:01:02:03", false, "22003", ""},
	}
	for _, c := range cases {
		a, b, cc, d, e, f, err := parseMacaddrLiteral(c.in)
		if c.wantOK {
			if err != nil {
				t.Errorf("parseMacaddrLiteral(%q) unexpected error: %v", c.in, err)
				continue
			}
			got := macaddrCanonicalText(a, b, cc, d, e, f)
			if got != c.canon {
				t.Errorf("parseMacaddrLiteral(%q) canonical = %q, want %q", c.in, got, c.canon)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseMacaddrLiteral(%q) = ok, want error %s", c.in, c.wantErr)
			continue
		}
		if err.Code != c.wantErr {
			t.Errorf("parseMacaddrLiteral(%q) code = %s, want %s", c.in, err.Code, c.wantErr)
		}
	}
}

// TestMacaddrBitwiseOperators pins macaddr_not/macaddr_and/macaddr_or's
// text-shape-sniffed detour through evalUnary/evalBinary's shared bitwise
// arms (M0134-0138), reusing the exact values macaddr.sql's `SELECT ~b`
// / `b & '...'` / `b | '...'` rows exercise.
func TestMacaddrBitwiseOperators(t *testing.T) {
	a, b, c, d, e, f, err := parseMacaddrLiteral("08:00:2b:01:02:03")
	if err != nil {
		t.Fatalf("parseMacaddrLiteral: %v", err)
	}
	not := macaddrCanonicalText(^a&0xff, ^b&0xff, ^c&0xff, ^d&0xff, ^e&0xff, ^f&0xff)
	if want := "f7:ff:d4:fe:fd:fc"; not != want {
		t.Errorf("~08:00:2b:01:02:03 = %q, want %q", not, want)
	}

	ma, mb, mc, md, me, mf, err := parseMacaddrLiteral("00:00:00:ff:ff:ff")
	if err != nil {
		t.Fatalf("parseMacaddrLiteral: %v", err)
	}
	and := macaddrCanonicalText(a&ma, b&mb, c&mc, d&md, e&me, f&mf)
	if want := "00:00:00:01:02:03"; and != want {
		t.Errorf("08:00:2b:01:02:03 & 00:00:00:ff:ff:ff = %q, want %q", and, want)
	}

	oa, ob, oc, od, oe, of, err := parseMacaddrLiteral("01:02:03:04:05:06")
	if err != nil {
		t.Fatalf("parseMacaddrLiteral: %v", err)
	}
	or := macaddrCanonicalText(a|oa, b|ob, c|oc, d|od, e|oe, f|of)
	if want := "09:02:2b:05:07:07"; or != want {
		t.Errorf("08:00:2b:01:02:03 | 01:02:03:04:05:06 = %q, want %q", or, want)
	}
}

// TestMacaddrTrunc pins macaddr_trunc's octet-zeroing (M0134-0138).
func TestMacaddrTrunc(t *testing.T) {
	a, b, c, d, e, f, err := parseMacaddrLiteral("08:00:2a:01:02:03")
	if err != nil {
		t.Fatalf("parseMacaddrLiteral: %v", err)
	}
	a, b, c, d, e, f = macaddrTruncOctets(a, b, c, d, e, f)
	got := macaddrCanonicalText(a, b, c, d, e, f)
	if want := "08:00:2a:00:00:00"; got != want {
		t.Errorf("trunc(08:00:2a:01:02:03) = %q, want %q", got, want)
	}
}
