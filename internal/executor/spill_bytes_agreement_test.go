package executor

import (
	"math"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/executor/hashsize"
)

// Agreement tests for hashsize.SpillBytes against spill.go's actual encoder.
//
// SpillBytes is the planner's model of what a spilled row COSTS on disk;
// encodeDatum/appendRowPayload/writeFrame is what actually writes it. They are
// a sibling pair in the sense this codebase keeps paying for — encode/decode,
// fast-path/interpreted evaluator, column-lookup/star-expansion — where a unit
// test on one side passes while the other is silently wrong. What follows is
// the mechanical comparison that keeps them from drifting.
//
// Design: docs/design/planner-spill-cost-calibration/DESIGN.md §6.2.

// encodedRowBytes is what the writer would emit for one row through
// WriteRowHashed: the 4-byte frame length, the 4-byte hash, and the payload.
func encodedRowBytes(row Row) int {
	payload := appendRowPayload(nil, row)
	return 4 + 4 + len(payload)
}

// TestSpillBytesAgreesWithEncoderPerKind pins the per-column error budget the
// SpillColumnBytes comment claims. The planner sees only (ncols, avgVarBytes)
// and cannot know the kind mix, so one number stands in for encodeDatum's
// whole switch; what has to hold is that the substitution is bounded, and that
// the bound is far smaller than the 39 B/column of over-statement that using
// EntryBytes for file I/O introduced.
func TestSpillBytesAgreesWithEncoderPerKind(t *testing.T) {
	// Per-column slack the model is permitted, in bytes, either direction.
	// KindInterval is the widest fixed arm at 17 against the model's 9.
	const tolerance = 8.0

	cases := []struct {
		name string
		d    Datum
		// varBytes is the variable-width payload the planner's avgVarBytes
		// statistic would carry for this column; fixed-width kinds have none.
		varBytes float64
	}{
		{"int", NewIntDatum(42), 0},
		{"bool", NewBoolDatum(true), 0},
		{"null", Datum{Kind: KindNull}, 0},
		{"time", NewTimeDatum(time.Unix(1_700_000_000, 0).UTC()), 0},
		{"interval", NewIntervalDatumFull(3, 4, 500), 0},
		{"string empty", NewStringDatum(""), 0},
		{"string short", NewStringDatum("abcdefgh"), 8},
		{"string long", NewStringDatum(string(make([]byte, 256))), 256},
		{"bytes", NewBytesDatum(make([]byte, 64)), 64},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := float64(encodedRowBytes(Row{tc.d}))
			want := hashsize.SpillBytes(1, tc.varBytes)
			if math.Abs(got-want) > tolerance {
				t.Errorf("one %s column: encoder writes %.0f B, SpillBytes models "+
					"%.0f B — off by %.0f, tolerance %.0f. The encoder and the "+
					"model have drifted; fix whichever is wrong and update the "+
					"table in SpillColumnBytes' comment, not this tolerance",
					tc.name, got, want, got-want, tolerance)
			}
		})
	}
}

// TestSpillBytesAgreesWithEncoderOnRealisticRows checks the model where it is
// actually used — whole rows of several columns — rather than one column at a
// time, because the frame and count overheads are per ROW and would be
// invisible in a per-column check.
func TestSpillBytesAgreesWithEncoderOnRealisticRows(t *testing.T) {
	cases := []struct {
		name     string
		row      Row
		varBytes float64
	}{
		{
			// The Q9 `orders` build side, the shape the whole D-05 chain was
			// measured on: two fixed-width columns, no variable payload.
			name: "Q9 orders build (2 fixed cols)",
			row:  Row{NewIntDatum(1), NewIntDatum(2)},
		},
		{
			name: "5 fixed cols",
			row: Row{NewIntDatum(1), NewIntDatum(2), NewIntDatum(3),
				NewIntDatum(4), NewIntDatum(5)},
		},
		{
			name: "3 fixed + 2 text",
			row: Row{NewIntDatum(1), NewIntDatum(2), NewIntDatum(3),
				NewStringDatum("0123456789"), NewStringDatum("0123456789abcdefghij")},
			varBytes: 30,
		},
		{
			name:     "narrow row, wide text",
			row:      Row{NewIntDatum(1), NewStringDatum(string(make([]byte, 400)))},
			varBytes: 400,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ncols := len(tc.row)
			got := float64(encodedRowBytes(tc.row))
			want := hashsize.SpillBytes(ncols, tc.varBytes)
			// Whole-row budget is the per-column budget times the column
			// count: the frame and count terms are exact, so all the slack
			// is per column.
			budget := 8.0 * float64(ncols)
			if math.Abs(got-want) > budget {
				t.Errorf("%s: encoder writes %.0f B, SpillBytes models %.0f B "+
					"(off by %.0f, budget %.0f for %d columns)",
					tc.name, got, want, got-want, budget, ncols)
			}
		})
	}
}

// TestSpillBytesIsFarBelowEntryBytesOnNarrowRows is the reason the model
// exists, stated as an assertion rather than a comment.
//
// spillPages sized batch files with EntryBytes, which measures the IN-MEMORY
// entry — a 48-byte Datum struct per column plus a 24-byte slice header. On a
// narrow fixed-width row almost all of that is overhead the file never
// carries, so the charge ran several times the real I/O; the deferral ledger
// row M0127-P5.7-a recorded the direction but not the size. This pins the size,
// and pins that it is NOT a constant — which is what rules out correcting it
// with a single multiplier.
func TestSpillBytesIsFarBelowEntryBytesOnNarrowRows(t *testing.T) {
	narrow := hashsize.EntryBytes(2, 0) / hashsize.SpillBytes(2, 0)
	wide := hashsize.EntryBytes(2, 400) / hashsize.SpillBytes(2, 400)

	if narrow < 4.0 {
		t.Errorf("narrow fixed-width row: EntryBytes/SpillBytes = %.2f, expected "+
			">= 4 — if this fell, either the Datum shrank or the encoder grew, "+
			"and spillPages' correction should be re-derived", narrow)
	}
	if wide > 1.5 {
		t.Errorf("wide text row: EntryBytes/SpillBytes = %.2f, expected <= 1.5 — "+
			"variable-width payload is carried at par on both sides, so the two "+
			"models must converge as it dominates", wide)
	}
	if narrow <= wide {
		t.Fatalf("EntryBytes/SpillBytes must FALL as variable-width payload grows "+
			"(narrow %.2f, wide %.2f). That it varies at all is the point: a "+
			"single scalar multiplier cannot correct a ratio that moves with "+
			"the column mix", narrow, wide)
	}
}
