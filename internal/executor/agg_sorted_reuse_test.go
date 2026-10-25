package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestAggSortedReusedRuntimeKeepsEmittedRows pins M0122-0012 slice 87: the
// sorted aggregate clears a finalized group's runtime and carries the next
// group in it, and reuses its key row, passthrough row and key buffer. Every
// row it emitted must keep its values after later groups (and a second scan
// through the same operator) reuse that runtime — each row is retained as
// returned, without a copy, and rendered only at the end — and equal the hash
// strategy's rows. The aggregates hold their state in every kind of field the
// clear drops: a numeric sum, text max, a DISTINCT count's set, array_agg's
// elements, avg's count and sum.
func TestAggSortedReusedRuntimeKeepsEmittedRows(t *testing.T) {
	var rows [][]optimizer.Expr
	for k := 1; k <= 40; k++ {
		for j := 0; j < 1+k%3; j++ {
			rows = append(rows, []optimizer.Expr{
				&optimizer.IntegerConst{Value: int64(k)},
				&optimizer.NumericConst{Value: fmt.Sprintf("%d123456789012345678901234567890.%02d", k, j)},
				&optimizer.StringConst{Value: fmt.Sprintf("s%02d-%d", k, j)},
				&optimizer.IntegerConst{Value: int64(j % 2)},
			})
		}
	}
	col := func(i int, typ string) *optimizer.ColumnRef {
		return &optimizer.ColumnRef{Index: i, Name: fmt.Sprintf("c%d", i), Type: catalog.Type{Name: typ}}
	}
	plan := func(strategy optimizer.AggStrategy) *optimizer.Aggregate {
		return &optimizer.Aggregate{
			Strategy:   strategy,
			Child:      &optimizer.Values{Rows: rows},
			GroupExprs: []optimizer.Expr{col(0, "int4")},
			Aggs: []optimizer.AggregateCall{
				{Name: "sum", Arg: col(1, "numeric"), Type: catalog.Type{Name: "numeric"}},
				{Name: "max", Arg: col(2, "text"), Type: catalog.Type{Name: "text"}},
				{Name: "count", Arg: col(3, "int4"), Distinct: true, Type: catalog.Type{Name: "int8"}},
				{Name: "array_agg", Arg: col(3, "int4"), Type: catalog.Type{Name: "int4", IsArray: true}},
				{Name: "avg", Arg: col(1, "numeric"), Type: catalog.Type{Name: "numeric"}},
			},
		}
	}
	render := func(rs []Row) string {
		var b strings.Builder
		for _, r := range rs {
			for _, d := range r {
				if d.IsNull() {
					b.WriteString("NULL")
				} else {
					b.Write(d.AppendValueText(nil))
				}
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		return b.String()
	}
	want := render(runAgg(t, plan(optimizer.AggStrategyHashed)))

	op, err := Build(plan(optimizer.AggStrategySorted))
	if err != nil {
		t.Fatal(err)
	}
	ctx := NewContext()
	var scans [2][]Row
	for pass := range scans {
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			scans[pass] = append(scans[pass], slot.Row())
		}
		_ = op.Close()
	}
	if agg := unwrapAggregateOp(op); agg == nil || agg.sFree == nil {
		t.Fatal("the sorted path kept no runtime to reuse; nothing is exercised")
	}
	for pass, got := range scans {
		if g := render(got); g != want {
			t.Fatalf("scan %d: sorted rows differ from the hash strategy's\nsorted:\n%.400s\nhashed:\n%.400s", pass, g, want)
		}
	}
}

func unwrapAggregateOp(op Operator) *aggregateOp {
	for op != nil {
		switch o := op.(type) {
		case *aggregateOp:
			return o
		case *instrumentedOp:
			op = o.inner
		default:
			return nil
		}
	}
	return nil
}
