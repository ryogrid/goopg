package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestStatLiteralEqualComparesNumbersByValue pins M0146-0198. var_eq_const
// (selfuncs.c) tests each MCV with the type's equality operator, so a
// numeric(5,2) MCV stamped "-5.00" matches the literal -5. goopg compared the
// two renderings byte for byte, missed the MCV, and TPC-DS Q43's
// `s_gmt_offset = -5` estimated 1 row of store instead of PG's 12.
func TestStatLiteralEqualComparesNumbersByValue(t *testing.T) {
	for _, tc := range []struct {
		stamped, literal, typ string
		want                  bool
	}{
		{"-5.00", "-5", "numeric", true},
		{"-5.00", "-5.0", "numeric", true},
		{"-5.00", "-6", "numeric", false},
		{"1.50", "1.5", "numeric", true},
		{"NaN", "NaN", "numeric", true},
		{"NaN", "0", "numeric", false},
		{"7", "07", "int4", true},
		{"2.5", "2.50", "float8", true},
		{"-5.00", "-5", "text", false}, // text compares as text
	} {
		if got := statLiteralEqual(tc.stamped, tc.literal, tc.typ); got != tc.want {
			t.Errorf("statLiteralEqual(%q, %q, %s) = %v, want %v", tc.stamped, tc.literal, tc.typ, got, tc.want)
		}
	}
	stats := &catalog.ColumnStats{NDistinct: 1, MCV: []catalog.MCVEntry{{Value: "-5.00", Frequency: 1}}}
	if sel := eqSelectivityForColumn(stats, &IntegerConst{Value: -5}, 12, "numeric"); sel != 1 {
		t.Errorf("s_gmt_offset = -5 against MCV {-5.00} freq 1: selectivity %v, want 1 (PG's 12 of 12 rows)", sel)
	}
}
