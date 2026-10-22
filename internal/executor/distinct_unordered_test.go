package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// M0122-0012 slice 69 guards: a hashed DISTINCT skips its output sort only
// when its consumer is a hashed aggregate whose result cannot depend on the
// order of its input rows; every other DISTINCT keeps sorting.

func TestAggregateIgnoresInputOrder(t *testing.T) {
	agg := func(a ...optimizer.AggregateCall) *optimizer.Aggregate {
		return &optimizer.Aggregate{Aggs: a, GroupExprs: []optimizer.Expr{deformCol(0)}}
	}
	call := func(name, in string) optimizer.AggregateCall {
		return optimizer.AggregateCall{Name: name, Arg: deformCol(1), InputType: catalog.Type{Name: in}}
	}
	for name, c := range map[string]struct {
		p    *optimizer.Aggregate
		want bool
	}{
		"sum int + count + min/max": {agg(call("sum", "int4"), call("count", "int4"), call("min", "text"), call("max", "numeric")), true},
		"avg numeric":               {agg(call("avg", "numeric")), true},
		"sum float8":                {agg(call("sum", "float8")), false},
		"avg float4":                {agg(call("avg", "float4")), false},
		"array_agg":                 {agg(call("array_agg", "int4")), false},
		"string_agg":                {agg(call("string_agg", "text")), false},
		"user aggregate":            {agg(call("my_agg", "int4")), false},
		"ordered sum": {agg(optimizer.AggregateCall{Name: "sum", Arg: deformCol(1), InputType: catalog.Type{Name: "int4"},
			OrderBy: []optimizer.SortKey{{Expr: deformCol(1)}}}), false},
		"sorted strategy": {func() *optimizer.Aggregate {
			a := agg(call("sum", "int4"))
			a.Strategy = optimizer.AggStrategySorted
			return a
		}(), false},
		"finalize": {func() *optimizer.Aggregate { a := agg(call("sum", "int4")); a.Mode = optimizer.AggModeFinal; return a }(), false},
		"grouping sets": {func() *optimizer.Aggregate {
			a := agg(call("sum", "int4"))
			a.GroupingSets = [][]int{{0}, {}}
			return a
		}(), false},
	} {
		if got := aggregateIgnoresInputOrder(c.p); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestDistinctUnderOrderFreeAggregateSkipsSort(t *testing.T) {
	plan := func(aggName, in string) *optimizer.Aggregate {
		return &optimizer.Aggregate{
			Child: &optimizer.Project{
				Child:   &optimizer.Distinct{Child: &optimizer.SeqScan{Table: deformTable(3)}},
				Targets: []optimizer.Expr{deformCol(0), deformCol(1)},
			},
			GroupExprs: []optimizer.Expr{deformCol(0)},
			Aggs:       []optimizer.AggregateCall{{Name: aggName, Arg: deformCol(1), InputType: catalog.Type{Name: in}}},
		}
	}
	distinctUnder := func(t *testing.T, op Operator) *distinctOp {
		t.Helper()
		p, ok := op.(*aggregateOp).child.(*projectOp)
		if !ok {
			t.Fatalf("aggregate child is %T", op.(*aggregateOp).child)
		}
		d, ok := p.child.(*distinctOp)
		if !ok {
			t.Fatalf("project child is %T", p.child)
		}
		return d
	}
	if d := distinctUnder(t, mustBuildDeform(t, plan("sum", "int4"))); !d.unordered {
		t.Fatal("DISTINCT under sum(int4) still sorts")
	}
	if d := distinctUnder(t, mustBuildDeform(t, plan("array_agg", "int4"))); d.unordered {
		t.Fatal("DISTINCT under array_agg skipped its sort: array_agg keeps input order")
	}
	if op := mustBuildDeform(t, &optimizer.Distinct{Child: &optimizer.SeqScan{Table: deformTable(3)}}); op.(*distinctOp).unordered {
		t.Fatal("a top-level DISTINCT skipped its sort")
	}
}

func TestDistinctUnorderedEmitsFirstSeenOrder(t *testing.T) {
	rows := []Row{
		{NewIntDatum(3)}, {NewIntDatum(1)}, {NewIntDatum(3)}, {NullDatum}, {NewIntDatum(2)}, {NewIntDatum(1)},
	}
	run := func(unordered bool) []string {
		d := newDistinctOp(&optimizer.Distinct{}, &rowsOp{rows: rows})
		d.unordered = unordered
		if err := d.Open(&Context{}); err != nil {
			t.Fatal(err)
		}
		var out []string
		for {
			s, err := d.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, datumKey(s.Row()[0]))
		}
		return out
	}
	sorted, firstSeen := run(false), run(true)
	want := []string{datumKey(NewIntDatum(1)), datumKey(NewIntDatum(2)), datumKey(NewIntDatum(3)), datumKey(NullDatum)}
	if len(sorted) != 4 || sorted[0] != want[0] || sorted[1] != want[1] || sorted[2] != want[2] || sorted[3] != want[3] {
		t.Fatalf("sorted DISTINCT = %v, want %v (NULLs last)", sorted, want)
	}
	wantSeen := []string{want[2], want[0], want[3], want[1]}
	for i := range wantSeen {
		if i >= len(firstSeen) || firstSeen[i] != wantSeen[i] {
			t.Fatalf("unordered DISTINCT = %v, want first-seen %v", firstSeen, wantSeen)
		}
	}
}
