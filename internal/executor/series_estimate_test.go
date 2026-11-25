package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestEstimateSeriesRows pins the generate_series support-function counts
// measured on PG 18.3 (EXPLAIN rows=).
func TestEstimateSeriesRows(t *testing.T) {
	i := func(v int64) optimizer.Expr { return &optimizer.IntegerConst{Value: v} }
	n := func(s string) optimizer.Expr { return &optimizer.NumericConst{Value: s} }
	ts := func(s string) optimizer.Expr { return &optimizer.TypedStringLit{Type: "timestamp", Value: s} }
	iv := func(s string) optimizer.Expr { return &optimizer.TypedStringLit{Type: "interval", Value: s} }
	cases := []struct {
		name string
		args []optimizer.Expr
		want float64
	}{
		{"int step 3", []optimizer.Expr{i(1), i(100), i(3)}, 34},
		{"int backwards", []optimizer.Expr{i(-5), i(5), i(3)}, 4},
		{"numeric half step", []optimizer.Expr{n("1.0"), n("100.0"), n("0.5")}, 199},
		{"numeric wrong sign", []optimizer.Expr{n("10.0"), n("1.0")}, 0},
		{"numeric equal ends, negative step", []optimizer.Expr{n("5.0"), n("5.0"), i(-1)}, 0},
		{"timestamp days", []optimizer.Expr{ts("2020-01-01"), ts("2020-12-31"), iv("1 day")}, 366},
		{"timestamp month and day", []optimizer.Expr{ts("2020-01-01"), ts("2021-01-01"), iv("1 mon 1 day")}, 12},
	}
	for _, c := range cases {
		got, ok := estimateSeriesRows(c.args)
		if !ok || got != c.want {
			t.Errorf("%s: %v, %v; want %v", c.name, got, ok, c.want)
		}
	}
	if _, ok := estimateSeriesRows([]optimizer.Expr{i(1), i(10), i(0)}); ok {
		t.Error("a zero step has no estimate")
	}
	if got, ok := estimateSeriesRows([]optimizer.Expr{i(1), &optimizer.NullConst{}}); !ok || got != 0 {
		t.Errorf("NULL argument: %v, %v", got, ok)
	}
}
