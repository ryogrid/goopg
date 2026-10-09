package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestHistogramOpSelectivityEqSel pins ineq_histogram_selectivity's eq_selec
// arithmetic (postgres/src/backend/utils/adt/selfuncs.c, M0146-0005s): the
// histogram estimates `x <= c`, `<` and `>=` subtract eq_selec, and the first
// bin is rescaled so that `x <= first bound` is eq_selec, not zero.
func TestHistogramOpSelectivityEqSel(t *testing.T) {
	bounds := []string{"1", "100", "200", "300", "400", "500"}
	const eq = 0.002
	first := 49.0 / 99.0 // binfrac of 50 in [1, 100]
	cases := []struct {
		op   parser.OpCode
		lit  string
		want float64
	}{
		{parser.OpLt, "200", 0.4 - eq},
		{parser.OpLe, "200", 0.4},
		{parser.OpGt, "200", 0.6},
		{parser.OpGe, "200", 0.6 + eq},
		{parser.OpLt, "250", 0.5 - eq},
		{parser.OpLe, "250", 0.5},
		// Below or at the first bound.
		{parser.OpLt, "0", 0},
		{parser.OpLt, "1", 0},
		{parser.OpLe, "1", eq},
		{parser.OpGe, "1", 1},
		{parser.OpGt, "1", 1 - eq},
		// Inside the first bin: rescaled by eq_selec * (1 - binfrac).
		{parser.OpLe, "50", first/5 + eq*(1-first)},
		{parser.OpLt, "50", first/5 + eq*(1-first) - eq},
		// At and above the last bound.
		{parser.OpLt, "500", 1 - eq},
		{parser.OpLe, "500", 1},
		{parser.OpGe, "500", eq},
		{parser.OpLt, "600", 1},
	}
	for _, c := range cases {
		got := histogramOpSelectivity(c.op, bounds, c.lit, "int4", eq)
		if math.Abs(got-c.want) > 1e-12 {
			t.Errorf("op %v %s: got %v want %v", c.op, c.lit, got, c.want)
		}
	}
}

// TestHistogramEqSel: 1/(ndistinct - #MCV), the relative ndistinct resolved
// against the tuple count, and 0 when no more than one non-MCV value remains.
func TestHistogramEqSel(t *testing.T) {
	mcv := []catalog.MCVEntry{{Value: "1", Frequency: 0.1}, {Value: "2", Frequency: 0.1}}
	if got := histogramEqSel(&catalog.ColumnStats{NDistinct: 202, MCV: mcv}, 0); got != 1.0/200 {
		t.Errorf("absolute ndistinct: got %v want 1/200", got)
	}
	if got := histogramEqSel(&catalog.ColumnStats{NDistinctFrac: 0.5, MCV: mcv}, 1004); got != 1.0/500 {
		t.Errorf("relative ndistinct: got %v want 1/500", got)
	}
	if got := histogramEqSel(&catalog.ColumnStats{NDistinct: 3, MCV: mcv}, 0); got != 0 {
		t.Errorf("one non-MCV value: got %v want 0", got)
	}
	if got := histogramEqSel(nil, 0); got != 0 {
		t.Errorf("nil stats: got %v want 0", got)
	}
}
