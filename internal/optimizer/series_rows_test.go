package optimizer

import "testing"

// TestGenerateSeriesRowsDefault: without an estimate, generate_series is sized
// by prorows (1000), and arguments a volatile call or a column feed are not
// evaluated.
func TestGenerateSeriesRowsDefault(t *testing.T) {
	saved := EstimateSeriesRows
	defer func() { EstimateSeriesRows = saved }()
	calls := 0
	EstimateSeriesRows = func([]Expr) (float64, bool) { calls++; return 0.4, true }
	gs := &GenerateSeries{Start: &IntegerConst{Value: 1}, Stop: &IntegerConst{Value: 0}}
	if got := generateSeriesRows(gs); got != 1 {
		t.Errorf("clamped estimate = %d, want 1", got)
	}
	gs.Stop = &FuncCall{Name: "random"}
	if got := generateSeriesRows(gs); got != 1000 || calls != 1 {
		t.Errorf("volatile argument: rows %d, calls %d", got, calls)
	}
	gs.Stop = &ColumnRef{Name: "n"}
	if got := generateSeriesRows(gs); got != 1000 || calls != 1 {
		t.Errorf("column argument: rows %d, calls %d", got, calls)
	}
}
