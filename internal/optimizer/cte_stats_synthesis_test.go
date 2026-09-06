package optimizer

// B-06 step-2 (part 1) gate: synthesis rules incl. miss→nil; identity
// collisions are a registry concern (next slice), not asserted here.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func synthTestTable(t *testing.T) *catalog.Table {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"},
		[]catalog.Column{
			{Name: "g1", Type: catalog.Type{Name: "int4"}},
			{Name: "g2", Type: catalog.Type{Name: "int4"}},
			{Name: "v", Type: catalog.Type{Name: "int4"}},
		})
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func synthAggEntry(t *testing.T) *plannedCTE {
	t.Helper()
	tbl := synthTestTable(t)
	scan := &SeqScan{Table: tbl, EstRelRows: 10000,
		schema: Schema{{Name: "g1"}, {Name: "g2"}, {Name: "v"}}}
	agg := &Aggregate{
		Child:      scan,
		GroupExprs: []Expr{&ColumnRef{Index: 0, Name: "g1"}, &ColumnRef{Index: 1, Name: "g2"}},
		Aggs:       []AggregateCall{{Name: "count"}},
		schema:     Schema{{Name: "g1"}, {Name: "g2"}, {Name: "c"}},
	}
	return &plannedCTE{name: "w", body: agg, schema: agg.Output()}
}

func TestSynthesizeAggregateOutputs(t *testing.T) {
	entry := synthAggEntry(t)
	got := synthesizeCTEStats(entry)
	if len(got.cols) != 3 {
		t.Fatalf("cols = %d, want 3", len(got.cols))
	}
	if got.cols[0].kind != cteColGroupKey || got.cols[1].kind != cteColGroupKey {
		t.Errorf("group keys kinds = %v %v, want groupkey groupkey",
			got.cols[0].kind, got.cols[1].kind)
	}
	if got.cols[0].ndistinct != -1 {
		t.Errorf("group key ndistinct = %v, want unknown (-1) until step 3 restriction rules",
			got.cols[0].ndistinct)
	}
	if got.cols[2].kind != cteColAggOut {
		t.Fatalf("agg output kind = %v, want aggout", got.cols[2].kind)
	}
	wantGroups := float64(estimateNumGroups(entry.body.(*Aggregate).GroupExprs,
		entry.body.(*Aggregate).Child, EstimateRows(entry.body.(*Aggregate).Child)))
	if got.cols[2].ndistinct != wantGroups {
		t.Errorf("aggout ndistinct = %v, want FD bound %v", got.cols[2].ndistinct, wantGroups)
	}
	// Non-tautological bound sanity (independent of the estimator's own
	// arithmetic): an FD bound over a 10k-row child is positive and can
	// never exceed the input rows.
	if got.cols[2].ndistinct < 1 || got.cols[2].ndistinct > 10000 {
		t.Errorf("aggout ndistinct = %v, want within [1, 10000]", got.cols[2].ndistinct)
	}
	if got.rows <= 0 {
		t.Errorf("rows = %v, want body estimate > 0", got.rows)
	}
}

func TestSynthesizeProjectWrappedAggregate(t *testing.T) {
	// Production shape: planSelectWithSettings always wraps the body in
	// Project. A bare-*Aggregate matcher would be a dead rule.
	tbl := synthTestTable(t)
	scan := &SeqScan{Table: tbl, EstRelRows: 10000,
		schema: Schema{{Name: "g1"}, {Name: "g2"}, {Name: "v"}}}
	agg := &Aggregate{
		Child:      scan,
		GroupExprs: []Expr{&ColumnRef{Index: 0, Name: "g1"}},
		Aggs:       []AggregateCall{{Name: "count"}},
	}
	proj := &Project{Child: agg,
		Targets: []Expr{&ColumnRef{Index: 0, Name: "g1"}, &ColumnRef{Index: 1, Name: "c"}},
		schema:  Schema{{Name: "g1"}, {Name: "c"}}}
	entry := &plannedCTE{name: "w", body: proj, schema: proj.Output()}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColGroupKey {
		t.Errorf("wrapped group key kind = %v, want groupkey", got.cols[0].kind)
	}
	if got.cols[1].kind != cteColAggOut || got.cols[1].ndistinct < 1 {
		t.Errorf("wrapped aggout = %+v, want aggout with FD bound >= 1", got.cols[1])
	}
}

func TestSynthesizeReorderedTargets(t *testing.T) {
	// SELECT count(*), g — positional mapping must follow Targets,
	// not output order.
	tbl := synthTestTable(t)
	scan := &SeqScan{Table: tbl, EstRelRows: 10000,
		schema: Schema{{Name: "g1"}, {Name: "v"}}}
	agg := &Aggregate{
		Child:      scan,
		GroupExprs: []Expr{&ColumnRef{Index: 0, Name: "g1"}},
		Aggs:       []AggregateCall{{Name: "count"}},
	}
	proj := &Project{Child: agg,
		Targets: []Expr{&ColumnRef{Index: 1, Name: "c"}, &ColumnRef{Index: 0, Name: "g1"}},
		schema:  Schema{{Name: "c"}, {Name: "g1"}}}
	entry := &plannedCTE{name: "w", body: proj, schema: proj.Output()}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColAggOut {
		t.Errorf("reordered pos 0 kind = %v, want aggout", got.cols[0].kind)
	}
	if got.cols[1].kind != cteColGroupKey {
		t.Errorf("reordered pos 1 kind = %v, want groupkey", got.cols[1].kind)
	}
}

func TestSynthesizeGrandTotal(t *testing.T) {
	tbl := synthTestTable(t)
	scan := &SeqScan{Table: tbl, EstRelRows: 10000,
		schema: Schema{{Name: "v"}}}
	agg := &Aggregate{
		Child: scan,
		Aggs:  []AggregateCall{{Name: "count"}},
	}
	entry := &plannedCTE{name: "w", body: agg, schema: Schema{{Name: "c"}}}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColAggOut || got.cols[0].ndistinct != 1 {
		t.Errorf("grand total = %+v, want {aggout 1}", got.cols[0])
	}
}

func TestSynthesizeComputedTargetUnknown(t *testing.T) {
	// A computed (non-ColumnRef) Project target above an Aggregate is
	// neither a group key nor an agg output — unknown, never misread.
	tbl := synthTestTable(t)
	scan := &SeqScan{Table: tbl, EstRelRows: 100,
		schema: Schema{{Name: "g1"}, {Name: "v"}}}
	agg := &Aggregate{
		Child:      scan,
		GroupExprs: []Expr{&ColumnRef{Index: 0, Name: "g1"}},
		Aggs:       []AggregateCall{{Name: "count"}},
	}
	proj := &Project{Child: agg,
		Targets: []Expr{
			&BinaryOp{Op: parser.OpAdd, Left: &ColumnRef{Index: 0, Name: "g1"}, Right: &IntegerConst{Value: 1}},
			&ColumnRef{Index: 1, Name: "c"},
		},
		schema: Schema{{Name: "g1p1"}, {Name: "c"}}}
	entry := &plannedCTE{name: "w", body: proj, schema: proj.Output()}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColUnknown {
		t.Errorf("computed target kind = %v, want unknown", got.cols[0].kind)
	}
	if got.cols[1].kind != cteColAggOut {
		t.Errorf("plain target kind = %v, want aggout", got.cols[1].kind)
	}
}

func TestSynthesizeGroupingSetsUnknown(t *testing.T) {
	entry := synthAggEntry(t)
	entry.body.(*Aggregate).GroupingSets = [][]int{{0, 1}, {0}}
	got := synthesizeCTEStats(entry)
	for i, c := range got.cols {
		if c.kind != cteColUnknown || c.ndistinct != -1 {
			t.Errorf("col %d = %+v, want unknown with grouping sets", i, c)
		}
	}
}

func litProject(t *testing.T, lits ...string) *Project {
	t.Helper()
	child := &SeqScan{Table: synthTestTable(t), EstRelRows: 10,
		schema: Schema{{Name: "x"}}}
	targets := make([]Expr, len(lits))
	for i, lit := range lits {
		if lit == "" {
			targets[i] = &ColumnRef{Index: 0, Name: "x"}
		} else {
			targets[i] = &StringConst{Value: lit}
		}
	}
	return &Project{Child: child, Targets: targets,
		schema: Schema{{Name: "a"}, {Name: "b"}}}
}

func TestSynthesizeUnionLiterals(t *testing.T) {
	left := litProject(t, "s", "")
	right := litProject(t, "w", "")
	body := &SetOp{Left: left, Right: right, Op: parser.SetOpUnion, All: true}
	entry := &plannedCTE{name: "u", body: body, schema: Schema{{Name: "a"}, {Name: "b"}}}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColLiteral || got.cols[0].ndistinct != 2 {
		t.Errorf("literal col = %+v, want {literal 2}", got.cols[0])
	}
	if got.cols[1].kind != cteColUnknown {
		t.Errorf("non-literal col = %+v, want unknown", got.cols[1])
	}
}

func TestSynthesizeUnionNonLiteralVetoes(t *testing.T) {
	left := litProject(t, "s")
	right := litProject(t, "")
	body := &SetOp{Left: left, Right: right, Op: parser.SetOpUnion, All: true}
	entry := &plannedCTE{name: "u", body: body, schema: Schema{{Name: "a"}}}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColUnknown {
		t.Errorf("mixed literal/non-literal col = %+v, want unknown", got.cols[0])
	}
}

func TestSynthesizeNonUnionSetOpUnknown(t *testing.T) {
	left := litProject(t, "s")
	right := litProject(t, "s")
	body := &SetOp{Left: left, Right: right, Op: parser.SetOpIntersect, All: false}
	entry := &plannedCTE{name: "u", body: body, schema: Schema{{Name: "a"}}}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColUnknown {
		t.Errorf("non-UNION-ALL col = %+v, want unknown", got.cols[0])
	}
}

func TestSynthesizeIntersectAllVetoes(t *testing.T) {
	// INTERSECT ALL of 's' vs 'w' is EMPTY (nd 0), yet the literals
	// match — claiming the across-branch count (nd 2) would overstate
	// ndistinct and understate selectivity (the inverse of B-06).
	left := litProject(t, "s")
	right := litProject(t, "w")
	for _, op := range []parser.SetOpType{parser.SetOpIntersect, parser.SetOpExcept} {
		body := &SetOp{Left: left, Right: right, Op: op, All: true}
		entry := &plannedCTE{name: "u", body: body, schema: Schema{{Name: "a"}}}
		if got := synthesizeCTEStats(entry); got.cols[0].kind != cteColUnknown {
			t.Errorf("op %v col = %+v, want unknown", op, got.cols[0])
		}
	}
}

func TestSynthesizeNestedUnionAll(t *testing.T) {
	// Left-deep 3-branch fold: {'s'} ∪ {'w'} ∪ {'s'} = nd 2.
	mid := &SetOp{Left: litProject(t, "s"), Right: litProject(t, "w"), Op: parser.SetOpUnion, All: true}
	body := &SetOp{Left: mid, Right: litProject(t, "s"), Op: parser.SetOpUnion, All: true}
	entry := &plannedCTE{name: "u", body: body, schema: Schema{{Name: "a"}}}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColLiteral || got.cols[0].ndistinct != 2 {
		t.Errorf("nested union col = %+v, want {literal 2}", got.cols[0])
	}
}

func TestSynthesizeTypedStringLit(t *testing.T) {
	mkTyped := func(lit string) *Project {
		child := &SeqScan{Table: synthTestTable(t), EstRelRows: 10,
			schema: Schema{{Name: "x"}}}
		return &Project{Child: child,
			Targets: []Expr{&TypedStringLit{Value: lit}},
			schema:  Schema{{Name: "a"}}}
	}
	body := &SetOp{Left: mkTyped("s"), Right: mkTyped("w"), Op: parser.SetOpUnion, All: true}
	entry := &plannedCTE{name: "u", body: body, schema: Schema{{Name: "a"}}}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColLiteral || got.cols[0].ndistinct != 2 {
		t.Errorf("typed-literal col = %+v, want {literal 2}", got.cols[0])
	}
}

func TestSynthesizeBareAggregateBranchVetoes(t *testing.T) {
	// A UNION branch that is a bare Aggregate (no Project wrapper)
	// carries no per-position literal — veto, even though the planner
	// always wraps branches in production (the wrap is what the
	// positive tests pin).
	tbl := synthTestTable(t)
	scan := &SeqScan{Table: tbl, EstRelRows: 10, schema: Schema{{Name: "x"}}}
	agg := &Aggregate{Child: scan, Aggs: []AggregateCall{{Name: "count"}}}
	body := &SetOp{Left: litProject(t, "s"), Right: agg, Op: parser.SetOpUnion, All: true}
	entry := &plannedCTE{name: "u", body: body, schema: Schema{{Name: "a"}}}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColUnknown {
		t.Errorf("aggregate-branch col = %+v, want unknown", got.cols[0])
	}
}

func TestSynthesizeCastWrappedBranchMisses(t *testing.T) {
	// A cast-wrapper Project (wrapSetOpBranchWithCasts shape) hides
	// every literal: miss (nil) rather than misread. Pins the
	// documented fragility, not the behavior.
	casted := litProject(t, "s")
	casted.Targets[0] = &CastExpr{Operand: casted.Targets[0]}
	body := &SetOp{Left: casted, Right: litProject(t, "w"), Op: parser.SetOpUnion, All: true}
	entry := &plannedCTE{name: "u", body: body, schema: Schema{{Name: "a"}}}
	got := synthesizeCTEStats(entry)
	if got.cols[0].kind != cteColUnknown {
		t.Errorf("cast-wrapped col = %+v, want unknown (miss, documented)", got.cols[0])
	}
}

func TestSynthesizeNilAndDML(t *testing.T) {
	if got := synthesizeCTEStats(nil); len(got.cols) != 0 {
		t.Errorf("nil entry cols = %d, want 0", len(got.cols))
	}
	entry := synthAggEntry(t)
	entry.isDML = true
	got := synthesizeCTEStats(entry)
	for i, c := range got.cols {
		if c.kind != cteColUnknown {
			t.Errorf("DML col %d = %+v, want unknown", i, c)
		}
	}
}

func TestPeelCTEBody(t *testing.T) {
	leaf := &SeqScan{}
	f := &Filter{Child: leaf}
	if got := peelCTEBody(&Sort{Child: &Limit{Child: f}}); got != Node(leaf) {
		t.Errorf("peel through Sort/Limit/Filter = %T, want *SeqScan", got)
	}
	join := &Join{Left: leaf, Right: leaf}
	if got := peelCTEBody(join); got != Node(join) {
		t.Error("peel must stop at Join")
	}
}
