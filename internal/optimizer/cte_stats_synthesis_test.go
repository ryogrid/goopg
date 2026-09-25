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
	// Step 3 (the group-combo rule, M0145-0009 slice 2) landed, but it applies
	// only when the INPUT ndistinct is known: min(input, group count). This
	// fixture's table carries no stats, so the input is unknown and the rule
	// declines — the group count alone is not a usable fallback (it regressed
	// TPC-DS Q59; see groupKeyNDistinct's comment). Unknown here is the rule
	// working, not the rule missing.
	if got.cols[0].ndistinct != -1 {
		t.Errorf("group key ndistinct = %v, want unknown (-1): this fixture has no column stats",
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

// --- M0145-0009 slice 3: the WindowAgg pass-through arm -----------------
//
// `Project(WindowAgg)` was the largest unclassified population the slice-1
// census found — 366 of 648 unknown asks per TPC-DS SF0.25 run. The rule is
// stronger than the group-key one because a WindowAgg is row-preserving: a
// column it hands through HAS its input's distinctness rather than merely
// being bounded by it.

func synthWindowTestTable() *catalog.Table {
	return &catalog.Table{
		Name: "win_src",
		Columns: []catalog.Column{
			{Name: "cust", Type: catalog.Type{Name: "int4"}},
			{Name: "yr", Type: catalog.Type{Name: "int4"}},
		},
		Stats: &catalog.TableStats{
			RowCount: 100000,
			Columns:  []catalog.ColumnStats{{NDistinct: 40000}, {NDistinct: 5}},
		},
	}
}

func TestSynthesizeWindowPassthrough(t *testing.T) {
	tbl := synthWindowTestTable()
	scan := &SeqScan{Table: tbl, EstRelRows: 100000, schema: tableSchema(tbl)}
	win := &WindowAgg{
		Child:       scan,
		PartitionBy: []Expr{&ColumnRef{Index: 0, Name: "cust"}},
		Funcs:       []WindowFunc{{Name: "rank"}},
		// child row ++ func outputs
		schema: Schema{{Name: "cust"}, {Name: "yr"}, {Name: "rnk"}},
	}
	proj := &Project{Child: win,
		Targets: []Expr{
			&ColumnRef{Index: 1, Name: "yr"},   // pass-through, low cardinality
			&ColumnRef{Index: 0, Name: "cust"}, // pass-through, high cardinality
			&ColumnRef{Index: 2, Name: "rnk"},  // the window function output
		},
		schema: Schema{{Name: "yr"}, {Name: "cust"}, {Name: "rnk"}}}
	entry := &plannedCTE{name: "w", body: proj, schema: proj.Output()}

	got := synthesizeCTEStats(entry)
	// Reordered targets must map correctly, and the pass-through columns take
	// the INPUT's ndistinct exactly — no clamp, because the row set is
	// unchanged.
	if got.cols[0].kind != cteColPassthrough || got.cols[0].ndistinct != 5 {
		t.Errorf("yr = kind %v nd %v, want passthrough 5", got.cols[0].kind, got.cols[0].ndistinct)
	}
	if got.cols[1].kind != cteColPassthrough || got.cols[1].ndistinct != 40000 {
		t.Errorf("cust = kind %v nd %v, want passthrough 40000", got.cols[1].kind, got.cols[1].ndistinct)
	}
	// The window function's own output is a computed value — unknown.
	if got.cols[2].kind != cteColUnknown || got.cols[2].ndistinct != -1 {
		t.Errorf("rank output = kind %v nd %v, want unknown", got.cols[2].kind, got.cols[2].ndistinct)
	}
}

func TestSynthesizeWindowFailsClosed(t *testing.T) {
	tbl := synthWindowTestTable()
	scan := &SeqScan{Table: tbl, EstRelRows: 100000, schema: tableSchema(tbl)}
	win := &WindowAgg{Child: scan, Funcs: []WindowFunc{{Name: "rank"}},
		schema: Schema{{Name: "cust"}, {Name: "yr"}, {Name: "rnk"}}}

	// A computed target reads no single input column — unknown, never guessed.
	computed := &Project{Child: win,
		Targets: []Expr{&BinaryOp{Op: parser.OpAdd,
			Left: &ColumnRef{Index: 1}, Right: &IntegerConst{Value: 1}}},
		schema: Schema{{Name: "yrp1"}}}
	if got := synthesizeCTEStats(&plannedCTE{name: "w", body: computed, schema: computed.Output()}); got.cols[0].kind != cteColUnknown {
		t.Errorf("computed target = %v, want unknown", got.cols[0].kind)
	}

	// A body whose input carries no stats leaves the pass-through unknown
	// rather than inventing a number.
	nostats := &SeqScan{Table: &catalog.Table{Name: "win_nostats",
		Columns: []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}}},
		EstRelRows: 1000, schema: Schema{{Name: "a"}}}
	bare := &WindowAgg{Child: nostats, Funcs: []WindowFunc{{Name: "rank"}},
		schema: Schema{{Name: "a"}, {Name: "rnk"}}}
	ent := &plannedCTE{name: "w", body: bare, schema: bare.Output()}
	if got := synthesizeCTEStats(ent); got.cols[0].kind != cteColUnknown {
		t.Errorf("unanalyzed pass-through = %v, want unknown", got.cols[0].kind)
	}
}

// TestSynthesizeWindowDoesNotOverwriteAggregate pins the dispatch order
// guarantee: the window arm only fills records still `unknown`, so a body that
// somehow matched an earlier rule keeps that rule's answer.
func TestSynthesizeWindowDoesNotOverwriteAggregate(t *testing.T) {
	entry := synthAggEntry(t)
	got := synthesizeCTEStats(entry)
	for i, c := range got.cols {
		if c.kind == cteColPassthrough {
			t.Errorf("col %d became passthrough on an Aggregate body", i)
		}
	}
}
