package nodes

import "testing"

// TestScanBitInput pins the bit_in/varbit_in text-input scan
// (postgres/src/backend/utils/adt/varbit.c:147-276, 452-577) that the M0148-0001
// fix wired into the coercion chokepoint. The quoted B'…' spelling is a SCANNER
// production (scan.l xbstart), so the text bit_in sees is the UNQUOTED "B101" —
// exactly what aggregates.sql COPYs (`B0101` into a bit column, row "1 1 1 1 1
// B0101") and what text→bit casts carry.
func TestScanBitInput(t *testing.T) {
	ok := []struct {
		in, want string
	}{
		{"B0101", "0101"},   // varbit.c:166-169 leading B → binary
		{"b11", "11"},       // lowercase
		{"X1F", "00011111"}, // varbit.c:170-173 leading X → hex, 4 bits/digit MSB-first
		{"xF4", "11110100"},
		{"1001", "1001"}, // varbit.c:176-184 no prefix → binary from 0
		{"", ""},         // B'' / empty → zero-length bit string
		{"B", ""},        // prefix alone → empty body
	}
	for _, tc := range ok {
		got, code, msg, good := ScanBitInput(tc.in)
		if !good {
			t.Errorf("ScanBitInput(%q) rejected: %s %s", tc.in, code, msg)
			continue
		}
		if got != tc.want {
			t.Errorf("ScanBitInput(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	bad := []struct {
		in, code, msg string
	}{
		{"B2", "22P02", `"2" is not a valid binary digit`},
		{"B'101'", "22P02", `"'" is not a valid binary digit`}, // quoted form is INVALID text in PG too
		{"0xb", "22P02", `"x" is not a valid binary digit`},
		{"xZZ", "22P02", `"Z" is not a valid hexadecimal digit`},
		{"x1 1", "22P02", `" " is not a valid hexadecimal digit`},
	}
	for _, tc := range bad {
		_, code, msg, good := ScanBitInput(tc.in)
		if good {
			t.Errorf("ScanBitInput(%q) accepted, want %s", tc.in, tc.msg)
			continue
		}
		if code != tc.code {
			t.Errorf("ScanBitInput(%q) code=%q want %q", tc.in, code, tc.code)
		}
		if msg != tc.msg {
			t.Errorf("ScanBitInput(%q) msg=%q want %q", tc.in, msg, tc.msg)
		}
	}

	// The VARBITMAXLEN guard (varbit.c:195-199) fires on hex bodies whose 4×
	// bit length exceeds varBitMaxLen. Building a just-past-boundary body
	// would allocate ~0.5 GB per probe (the memory guard's business), so pin
	// the boundary arithmetic the guard uses; the 54000 return path itself is
	// three lines above the scan. Message wording per varbit.c:197-198:
	// "bit string length exceeds the maximum allowed (2147483640)".
	if varBitMaxLen != 2147483640 || varBitMaxLen/4 != 536870910 {
		t.Errorf("varBitMaxLen=%d: want 2147483640 (INT_MAX-8+1, varbit.h:83), /4=536870910", varBitMaxLen)
	}
}

// TestParseBitFromStringPrefixes pins that the pgwire/pgnodes text→bit path
// (NewBitConst/NewVarBitConst via parseBitFromString) applies the same prefix
// scan, so a bound 'B0101' text arrives as the identical Const.
func TestParseBitFromStringPrefixes(t *testing.T) {
	for _, tc := range []struct {
		in     string
		bitLen int32
	}{
		{"B0101", 4},
		{"X1F", 8},
		{"1010", 4},
	} {
		bitLen, _, ok := parseBitFromString(tc.in)
		if !ok || bitLen != tc.bitLen {
			t.Errorf("parseBitFromString(%q) = (%d, %v), want (%d, true)", tc.in, bitLen, ok, tc.bitLen)
		}
	}
}
