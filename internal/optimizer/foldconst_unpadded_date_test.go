package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPadISODateLiteral covers the normalization M0141-S2b-17 added: PG's
// date_in accepts `2002-5-01`, the planner's fixed Go layouts did not.
func TestPadISODateLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2002-5-01", "2002-05-01"},
		{"2002-05-1", "2002-05-01"},
		{" 2002-5-1 ", "2002-05-01"},
		{"2002-05-01", "2002-05-01"},
		{"2002-5-01 13:45:00", "2002-05-01 13:45:00"},
		{"02-5-01", "02-5-01"},       // not a 4-digit year: unchanged
		{"May 1 2002", "May 1 2002"}, // other date_in spellings: unchanged
		{"2002/5/01", "2002/5/01"},
		{"2002-5-0x", "2002-5-0x"},
	}
	for _, c := range cases {
		if got := padISODateLiteral(c.in); got != c.want {
			t.Errorf("padISODateLiteral(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFoldUnpaddedDatePlusInterval: TPC-DS Q94's upper bound,
// `'2002-5-01'::date + 60 days`, folds to the same timestamp PG's EXPLAIN
// prints (`'2002-06-30 00:00:00'::timestamp`). Before M0141-S2b-17 the
// unpadded month made parseTemporalLiteral decline, so the bound stayed an
// expression and got no histogram estimate.
func TestFoldUnpaddedDatePlusInterval(t *testing.T) {
	e := &BinaryOp{Op: parser.OpAdd,
		Left:  &TypedStringLit{Type: "date", Value: "2002-5-01"},
		Right: &IntervalLit{Value: "60 day"},
	}
	lit, ok := FoldConstants(e).(*TypedStringLit)
	if !ok {
		t.Fatalf("FoldConstants = %T, want a folded *TypedStringLit", FoldConstants(e))
	}
	if lit.Type != "timestamp" || lit.Value != "2002-06-30 00:00:00" {
		t.Errorf("folded to %s %q, want timestamp %q", lit.Type, lit.Value, "2002-06-30 00:00:00")
	}
}

// TestNumericValueUnpaddedDate: the histogram side reads the same spelling,
// so both bounds of Q94's range land in their buckets.
func TestNumericValueUnpaddedDate(t *testing.T) {
	a, okA := numericValue("2002-5-01", "date")
	b, okB := numericValue("2002-05-01", "date")
	if !okA || !okB || a != b {
		t.Errorf("numericValue unpadded=(%v,%v) padded=(%v,%v), want equal and ok", a, okA, b, okB)
	}
	ts, okT := numericValue("2002-5-01 00:00:00", "timestamp")
	tp, okP := numericValue("2002-05-01 00:00:00", "timestamp")
	if !okT || !okP || ts != tp {
		t.Errorf("timestamp numericValue unpadded=(%v,%v) padded=(%v,%v), want equal and ok", ts, okT, tp, okP)
	}
}
