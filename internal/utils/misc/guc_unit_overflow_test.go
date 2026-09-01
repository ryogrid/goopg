package misc

import "testing"

// TestParseIntWithUnitRejectsOverflow is the review/260831-2 UT-3 guard.
// convertUnit computed `n * mul / div` unchecked, so a huge unit-suffixed GUC
// value wrapped silently instead of being rejected. PG 18.3 answers both of
// these with `invalid value for parameter … HINT: Value exceeds integer range.`
// (verified against the oracle on port 65438).
func TestParseIntWithUnitRejectsOverflow(t *testing.T) {
	cases := []struct {
		in     string
		native Unit
	}{
		{"9000000TB", UnitKB},             // work_mem
		{"9223372036854775d", UnitMs},     // statement_timeout
		{"9223372036854775807TB", UnitKB}, //
	}
	for _, c := range cases {
		got, err := parseIntWithUnit(c.in, c.native)
		if err == nil {
			t.Errorf("parseIntWithUnit(%q) = %d, want an out-of-range error", c.in, got)
		}
	}

	// Values that do fit must still convert exactly.
	if got, err := parseIntWithUnit("8MB", UnitKB); err != nil || got != 8*1024 {
		t.Errorf("parseIntWithUnit(\"8MB\", UnitKB) = %d, %v; want 8192, nil", got, err)
	}
	if got, err := parseIntWithUnit("1500ms", UnitMs); err != nil || got != 1500 {
		t.Errorf("parseIntWithUnit(\"1500ms\", UnitMs) = %d, %v; want 1500, nil", got, err)
	}
}
