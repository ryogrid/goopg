package executor

// tid_cast_test.go — unit coverage for tidin-equivalent parsing used by the
// `::tid` cast, pg_input_is_valid('…','tid'), and pg_input_error_info('…','tid').
//
// Mirrors PostgreSQL's tidin (src/backend/utils/adt/tid.c): block is an
// unsigned 32-bit BlockNumber accepted via strtoul with the wider-than-32-bit
// round-trip guard (so '(-1,0)' normalises to '(4294967295,0)' but
// '(4294967296,1)' is rejected), and offset is a uint16 OffsetNumber bounded by
// USHRT_MAX. Backs the M0097-0036 `tid` regress gap.

import (
	"testing"
)

func TestParseTidInput(t *testing.T) {
	cases := []struct {
		in        string
		wantBlock uint32
		wantOff   uint16
		wantOK    bool
	}{
		{"(0,0)", 0, 0, true},
		{"(0,1)", 0, 1, true},
		// -1 wraps to the max BlockNumber (strtoul + sign-extended round-trip).
		{"(-1,0)", 4294967295, 0, true},
		{"(4294967295,65535)", 4294967295, 65535, true},
		// block one past uint32 max → rejected (round-trip guard fails).
		{"(4294967296,1)", 0, 0, false},
		// offset past uint16 max → rejected.
		{"(1,65536)", 0, 0, false},
		// negative offset → strtoul wraps huge, exceeds USHRT_MAX → rejected.
		{"(0,-1)", 0, 0, false},
		// missing offset component → rejected.
		{"(0)", 0, 0, false},
		// block at int32 min wraps to 2^31 and round-trips.
		{"(-2147483648,2)", 2147483648, 2, true},
		// below int32 min no longer round-trips → rejected.
		{"(-2147483649,0)", 0, 0, false},
		// trailing junk before the delimiter → rejected.
		{"(0x,1)", 0, 0, false},
		{"(1,2x)", 0, 0, false},
		// trailing text after ')' is ignored, matching tidin's scan loop.
		{"(7,3)junk", 7, 3, true},
	}
	for _, c := range cases {
		block, off, ok := parseTidInput(c.in)
		if ok != c.wantOK {
			t.Errorf("parseTidInput(%q) ok=%v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if ok && (block != c.wantBlock || off != c.wantOff) {
			t.Errorf("parseTidInput(%q) = (%d,%d), want (%d,%d)", c.in, block, off, c.wantBlock, c.wantOff)
		}
	}
}

// TestEvalCastTidNormalizesAndValidates checks the `::tid` cast path: valid
// inputs are re-emitted in canonical unsigned form, invalid ones raise 22P02.
func TestEvalCastTidNormalizesAndValidates(t *testing.T) {
	// Valid: '(-1,0)' must normalise to the unsigned block representation.
	got, err := evalCast(NewStringDatum("(-1,0)"), "tid", 0)
	if err != nil {
		t.Fatalf("evalCast('(-1,0)'::tid) unexpected error: %v", err)
	}
	if got.Kind != KindString || got.StringValue() != "(4294967295,0)" {
		t.Errorf("evalCast('(-1,0)'::tid) = %q, want (4294967295,0)", got.StringValue())
	}

	// Invalid: block out of range must error with 22P02.
	if _, err := evalCast(NewStringDatum("(4294967296,1)"), "tid", 0); err == nil {
		t.Errorf("evalCast('(4294967296,1)'::tid) expected error, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22P02" {
		t.Errorf("evalCast('(4294967296,1)'::tid) error = %v, want 22P02 ExecError", err)
	}
}
