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
		// Below or at the first bound. With no index to read the actual
		// endpoints from, the estimate is clamped to a hundredth of the
		// histogram resolution: 0.01 / 5 = 0.002 (M0146-0233).
		{parser.OpLt, "0", 0.002},
		{parser.OpLt, "1", 0.002},
		{parser.OpLe, "1", eq},
		{parser.OpGe, "1", 0.998},
		{parser.OpGt, "1", 1 - eq},
		// Inside the first bin: rescaled by eq_selec * (1 - binfrac).
		{parser.OpLe, "50", first/5 + eq*(1-first)},
		{parser.OpLt, "50", first/5 + eq*(1-first) - eq},
		// At and above the last bound.
		{parser.OpLt, "500", 1 - eq},
		{parser.OpLe, "500", 0.998},
		{parser.OpGe, "500", eq},
		{parser.OpLt, "600", 0.998},
	}
	for _, c := range cases {
		got := histogramOpSelectivity(c.op, bounds, c.lit, "int4", eq, nil)
		if math.Abs(got-c.want) > 1e-12 {
			t.Errorf("op %v %s: got %v want %v", c.op, c.lit, got, c.want)
		}
	}
}

// TestHistogramOpSelectivityActualEndpoints pins get_actual_variable_range's
// part in ineq_histogram_selectivity (M0146-0233): when the binary search
// reaches the first or last bound, that bound is replaced by the column's
// actual minimum or maximum, and a successful refresh (have_end) lifts the
// cutoff clamp. A probe that finds nothing leaves the histogram as it is,
// and the clamp applies.
func TestHistogramOpSelectivityActualEndpoints(t *testing.T) {
	bounds := []string{"1", "100", "200", "300", "400", "500"}
	const eq = 0.002
	probe := func(wantMax bool) (string, bool) {
		if wantMax {
			return "900", true
		}
		return "-100", true
	}
	none := func(bool) (string, bool) { return "", false }
	cases := []struct {
		op    parser.OpCode
		lit   string
		probe endpointProbe
		want  float64
	}{
		// Last bin is [400, 900]: 500 is a fifth of the way in.
		{parser.OpLe, "500", probe, (4 + 0.2) / 5},
		// First bin is [-100, 100]: 0 is halfway, rescaled by eq_selec, and `<`
		// subtracts it; no clamp.
		{parser.OpLt, "0", probe, 0.5/5 + eq*0.5 - eq},
		// Above the actual maximum: exactly 1, not 1 - cutoff.
		{parser.OpLt, "1000", probe, 1},
		// The bin search never reaches an end: the probe is not consulted.
		{parser.OpLe, "250", probe, 0.5},
		// No usable index entry: the histogram stands and the clamp applies.
		{parser.OpLe, "500", none, 0.998},
		{parser.OpLt, "1000", none, 0.998},
	}
	for _, c := range cases {
		got := histogramOpSelectivity(c.op, bounds, c.lit, "int4", eq, c.probe)
		if math.Abs(got-c.want) > 1e-12 {
			t.Errorf("op %v %s: got %v want %v", c.op, c.lit, got, c.want)
		}
	}
	// Two bounds: both ends are refreshed before the search.
	if got := histogramOpSelectivity(parser.OpLe, []string{"10", "20"}, "15", "int4", 0, probe); math.Abs(got-115.0/1000) > 1e-12 {
		t.Errorf("two-bound histogram over [-100, 900]: got %v want 0.115", got)
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
