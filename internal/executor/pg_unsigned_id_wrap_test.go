package executor

import "testing"

// pgUnsignedIDFromDatum must mirror uint32in_subr/uint64in_subr's handling of
// negative input (postgres/src/backend/utils/adt/numutils.c): strtoul/strtoull
// parse a leading '-' and wrap, so a negative value whose magnitude fits the
// signed width is NOT a 22003 — it stores its two's-complement image.
//
// This pins M-NIGHTLY AI-20260814-011711-002 (regress/oid): INSERT '-1040'
// must store 4294966256, exactly as PG does. a30ab155 (M0119-0006 54th slice)
// introduced the `v < 0` rejection that broke this, on the theory that
// uint32in_subr "raises 22003 outside the type's range, never a silent wrap" —
// true for magnitude overflow, false for a leading sign, which strtoul wraps
// and the PG_UINT32_MAX != ULONG_MAX block then admits after signed extension.
func TestPgUnsignedIDFromDatumNegativeWrap(t *testing.T) {
	cases := []struct {
		name     string
		typeName string
		bits     int
		datum    Datum
		want     uint64
		wantErr  bool
	}{
		{"oid wraps -1040 to 4294966256", "oid", 32, NewStringDatum("-1040"), 4294966256, false},
		{"oid positive fits", "oid", 32, NewIntDatum(4294966256), 4294966256, false},
		{"oid 2^32 is out of range", "oid", 32, NewIntDatum(4294967296), 0, true},
		{"oid below int32 is out of range", "oid", 32, NewIntDatum(-2147483649), 0, true},
		{"oid min int32 wraps to 2^31", "oid", 32, NewIntDatum(-2147483648), 2147483648, false},
		{"xid8 wraps -1040", "xid8", 64, NewIntDatum(-1040), 18446744073709550576, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pgUnsignedIDFromDatum(tc.datum, tc.typeName, tc.bits)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("pgUnsignedIDFromDatum(%q, %q, %d) = %d, want error", tc.datum.Format(), tc.typeName, tc.bits, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("pgUnsignedIDFromDatum(%q, %q, %d): %v", tc.datum.Format(), tc.typeName, tc.bits, err)
			}
			if got != tc.want {
				t.Fatalf("pgUnsignedIDFromDatum(%q, %q, %d) = %d, want %d", tc.datum.Format(), tc.typeName, tc.bits, got, tc.want)
			}
		})
	}
}
