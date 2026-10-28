package executor

import (
	"strings"
	"testing"
)

// TestEarlyScanQualInPlaceMatchesLate pins M0122-0012 C58: the seq scan's
// EARLY qual runs over rowSlotView(scanRow), and fastOperandPtr now reads its
// columns in place, so fastComparisonBool's typed and plain-string branches
// serve it. Every qual must keep exactly the rows the LATE position (the
// finished row, the oracle scanPrefilterForceLate selects) keeps: ints,
// numerics, dates, bpchar with padding, varchar and text, NULLs, all six
// comparison operators, the literal on either side, and column-to-column.
func TestEarlyScanQualInPlaceMatchesLate(t *testing.T) {
	t.Cleanup(func() { scanPrefilterForceLate.Store(false) })
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE sqi (d date, n numeric(12,2), i int, f char(3), v varchar(10), s text, j int)`)
	runSQL(t, ctx, `INSERT INTO sqi SELECT
		CASE WHEN x % 31 = 0 THEN NULL ELSE DATE '2021-01-01' + (x % 90) END,
		CASE WHEN x % 29 = 0 THEN NULL ELSE (x % 50) * 1.5 END,
		CASE WHEN x % 23 = 0 THEN NULL ELSE x % 40 END,
		CASE WHEN x % 19 = 0 THEN NULL WHEN x % 3 = 0 THEN 'R' WHEN x % 3 = 1 THEN 'AB ' ELSE 'N' END,
		CASE WHEN x % 17 = 0 THEN NULL ELSE 'v' || (x % 7) END,
		CASE WHEN x % 13 = 0 THEN NULL WHEN x % 5 = 0 THEN 'R ' ELSE 's' || (x % 11) END,
		x % 41
		FROM generate_series(1, 3000) x`)
	quals := []string{
		"f = 'R'", "f <> 'R'", "f = 'AB'", "f < 'N'", "f >= 'AB '", "'R' = f",
		"v = 'v3'", "v > 'v4'", "s = 'R'", "s = 'R '", "s <= 's5'", "s <> 's1'",
		"i = 7", "i < 20", "i >= 39", "20 > i", "i <> j", "i = j",
		"n = 15", "n > 30.5", "n <= 1.5",
		"d = DATE '2021-02-01'", "d < DATE '2021-01-15'", "d >= DATE '2021-03-01'",
	}
	for _, q := range quals {
		sql := "SELECT d, n, i, f, v, s, j FROM sqi WHERE " + q + " ORDER BY j, i, s, d, n, f, v"
		got := renderRows(runSQL(t, ctx, sql))
		scanPrefilterForceLate.Store(true)
		want := renderRows(runSQL(t, ctx, sql))
		scanPrefilterForceLate.Store(false)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: early in-place qual keeps %d rows, late keeps %d", q, len(got), len(want))
		}
		// text does not strip padding: 'R ' <> 'R', so this one is empty in PG.
		if wantEmpty := q == "s = 'R'"; (len(got) == 0) != wantEmpty {
			t.Fatalf("%s: %d rows (want empty: %v)", q, len(got), wantEmpty)
		}
	}
}
