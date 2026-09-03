package executor

// EX1-01 verification: deform-bound computation over synthetic chains,
// tail-poison runs proving no test reads past the bound, and end-to-end
// seqscan deform-narrowing execution (values identical to full deform).

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// deformCol builds a ColumnRef to input column idx.
func deformCol(idx int) optimizer.Expr {
	return &optimizer.ColumnRef{Index: idx, Name: fmt.Sprintf("c%d", idx), Type: catalog.Type{Name: "int4"}}
}

// deformTable builds a bare table descriptor with n int4 columns. Only the
// column count matters to the bound walk (no storage is touched).
func deformTable(n int) *catalog.Table {
	tbl := &catalog.Table{Name: "w"}
	for i := 0; i < n; i++ {
		tbl.Columns = append(tbl.Columns, catalog.Column{
			Name: fmt.Sprintf("c%d", i), Type: catalog.Type{Name: "int4"}, Ordinal: i,
		})
	}
	return tbl
}

func deformInt(v int64) optimizer.Expr { return &optimizer.IntegerConst{Value: v} }

func deformLt(l, r optimizer.Expr) optimizer.Expr {
	return &optimizer.BinaryOp{Op: parser.OpLt, Left: l, Right: r}
}

// seqLeafBound walks a built operator tree to its SeqScan leaf and reports
// the stamped deform bound (normalised: unset reads as full width) and the
// leaf column count. Gather is followed through the worker buildChild
// closure, which is exactly the EX1-01 capture under test.
func seqLeafBound(t *testing.T, op Operator) (bound, ncols int) {
	t.Helper()
	for {
		switch o := op.(type) {
		case *seqScanOp:
			n := len(o.cols)
			if o.deformBound <= 0 {
				return n, n
			}
			return o.deformBound, n
		case *filterOp:
			op = o.child
		case *projectOp:
			op = o.child
		case *limitOp:
			op = o.child
		case *sortOp:
			op = o.child
		case *aggregateOp:
			op = o.child
		case *distinctOp:
			op = o.child
		case *lockRowsOp:
			op = o.child
		case *resultOp:
			if o.child == nil {
				t.Fatal("resultOp has no child")
			}
			op = o.child
		case *gatherOp:
			wc, err := o.buildChild()
			if err != nil {
				t.Fatalf("gather buildChild: %v", err)
			}
			op = wc
		default:
			t.Fatalf("no SeqScan leaf under %T", op)
		}
	}
}

func mustBuildDeform(t *testing.T, plan optimizer.Node) Operator {
	t.Helper()
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return op
}

// TestScanDeformBoundChains pins the bound computation over synthetic chains:
// narrow shapes narrow, terminators go full, and declined expressions fail
// toward full deform.
func TestScanDeformBoundChains(t *testing.T) {
	scan16 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(16)} }
	scan8 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(8)} }
	filter := func(pred optimizer.Expr, child optimizer.Node) *optimizer.Filter {
		return &optimizer.Filter{Child: child, Predicate: pred}
	}
	identity := func(n int, child optimizer.Node) *optimizer.Project {
		tg := make([]optimizer.Expr, n)
		for i := range tg {
			tg[i] = deformCol(i)
		}
		return &optimizer.Project{Child: child, Targets: tg}
	}

	t.Run("filter-only", func(t *testing.T) {
		op := mustBuildDeform(t, filter(deformLt(deformCol(5), deformInt(1)), scan16()))
		if b, n := seqLeafBound(t, op); b != 6 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want 6/16", b, n)
		}
	})

	t.Run("filter+project-identity", func(t *testing.T) {
		// Project above the filter reads columns 0..2 of the filter output;
		// the filter reads column 4. Bound covers both.
		op := mustBuildDeform(t, identity(3, filter(deformLt(deformCol(4), deformInt(1)), scan16())))
		if b, n := seqLeafBound(t, op); b != 5 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want 5/16", b, n)
		}
	})

	t.Run("project-identity-narrow", func(t *testing.T) {
		// A pruned identity (3 targets over a 16-column child) reads only
		// columns 0..2; the filter above reads column 1. Bound covers both.
		op := mustBuildDeform(t, filter(deformLt(deformCol(1), deformInt(1)), identity(3, scan16())))
		if b, n := seqLeafBound(t, op); b != 3 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want 3/16", b, n)
		}
	})

	t.Run("project-identity-full-width", func(t *testing.T) {
		// A full-width identity genuinely reads every child column, so it
		// re-widens to full even under a narrow filter.
		op := mustBuildDeform(t, filter(deformLt(deformCol(1), deformInt(1)), identity(16, scan16())))
		if b, n := seqLeafBound(t, op); b != 16 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want full 16/16", b, n)
		}
	})

	t.Run("project-reorder-terminator", func(t *testing.T) {
		reorder := &optimizer.Project{Child: filter(deformLt(deformCol(1), deformInt(1)), scan8()),
			Targets: []optimizer.Expr{deformCol(2), deformCol(0)}}
		op := mustBuildDeform(t, reorder)
		if b, n := seqLeafBound(t, op); b != 8 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want full 8/8", b, n)
		}
	})

	t.Run("join-terminator", func(t *testing.T) {
		left := filter(deformLt(deformCol(1), deformInt(1)), scan8())
		plan := &optimizer.Join{
			Type:      optimizer.JoinTypeInner,
			Algo:      optimizer.JoinAlgoNestedLoop,
			Left:      left,
			Right:     scan8(),
			Predicate: deformLt(deformCol(0), deformCol(8)),
			LeftKey:   deformCol(0),
			RightKey:  deformCol(0),
		}
		op := mustBuildDeform(t, plan)
		jo, ok := op.(*joinOp)
		if !ok {
			t.Fatalf("built %T, want *joinOp", op)
		}
		// EX1-02: the merged-space predicate maps per side (col 0 → left 0,
		// col 8 → right 0) and unions with the below-walk refs (the left
		// filter reads column 1): left covers {0,1}, right covers {0}.
		if b, n := seqLeafBound(t, jo.left); b != 2 || n != 8 {
			t.Fatalf("left bound=%d ncols=%d, want 2/8", b, n)
		}
		if b, n := seqLeafBound(t, jo.right); b != 1 || n != 8 {
			t.Fatalf("right bound=%d ncols=%d, want 1/8", b, n)
		}
	})

	t.Run("aggregate-arm-reading", func(t *testing.T) {
		agg := &optimizer.Aggregate{
			Child:      filter(deformLt(deformCol(1), deformInt(1)), scan16()),
			GroupExprs: []optimizer.Expr{deformCol(2)},
			Aggs: []optimizer.AggregateCall{
				{Name: "sum", Arg: deformCol(5), Type: catalog.Type{Name: "int8"}},
			},
		}
		op := mustBuildDeform(t, agg)
		// Arms read {2, 5}, filter reads {1}: bound covers all three.
		if b, n := seqLeafBound(t, op); b != 6 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want 6/16", b, n)
		}
	})

	t.Run("aggregate-drops-ancestors", func(t *testing.T) {
		// The Sort key (column 0 of the aggregate OUTPUT) lives above the
		// reshape and must not constrain the scan below it; only the
		// aggregate arms (column 5 of the child) and the filter apply.
		agg := &optimizer.Aggregate{
			Child: scan16(),
			Aggs: []optimizer.AggregateCall{
				{Name: "sum", Arg: deformCol(5), Type: catalog.Type{Name: "int8"}},
			},
		}
		plan := &optimizer.Sort{Child: agg, Keys: []optimizer.SortKey{{Expr: deformCol(0)}}}
		op := mustBuildDeform(t, plan)
		if b, n := seqLeafBound(t, op); b != 6 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want 6/16", b, n)
		}
	})

	t.Run("aggregate-passthrough-and-filter-arms", func(t *testing.T) {
		agg := &optimizer.Aggregate{
			Child:       scan16(),
			Passthrough: []optimizer.Expr{deformCol(7)},
			Aggs: []optimizer.AggregateCall{
				{Name: "sum", Arg: deformCol(1), Filter: deformLt(deformCol(3), deformInt(9)),
					OrderBy: []optimizer.SortKey{{Expr: deformCol(4)}}, Type: catalog.Type{Name: "int8"}},
			},
		}
		op := mustBuildDeform(t, agg)
		// Refs {7, 1, 3, 4}: bound 8.
		if b, n := seqLeafBound(t, op); b != 8 || n != 16 {
			t.Fatalf("bound=%d ncols=%d, want 8/16", b, n)
		}
	})

	t.Run("wholerow-and-friends-decline", func(t *testing.T) {
		decliners := []struct {
			name string
			expr optimizer.Expr
		}{
			{"MergeWholeRowRef", &optimizer.MergeWholeRowRef{}},
			{"RowExpr", &optimizer.RowExpr{Elems: []optimizer.Expr{deformCol(0)}}},
			{"CTIDExpr", &optimizer.CTIDExpr{}},
			{"TableOidExpr", &optimizer.TableOidExpr{}},
			{"FuncCall", &optimizer.FuncCall{Name: "abs", Args: []optimizer.Expr{deformCol(0)}}},
			{"SubqueryExpr", &optimizer.SubqueryExpr{}},
			{"OuterColumnRef", &optimizer.OuterColumnRef{Level: 1, Index: 0}},
			{"CaseExpr", &optimizer.CaseExpr{Else: deformCol(0)}},
			{"ParamRef", &optimizer.ParamRef{}},
			{"negative-index-col", &optimizer.ColumnRef{Index: -1}},
		}
		for _, tc := range decliners {
			t.Run(tc.name, func(t *testing.T) {
				pred := &optimizer.BinaryOp{Op: parser.OpGt, Left: deformCol(0), Right: tc.expr}
				op := mustBuildDeform(t, filter(pred, scan8()))
				if b, n := seqLeafBound(t, op); b != 8 || n != 8 {
					t.Fatalf("bound=%d ncols=%d, want full 8/8", b, n)
				}
			})
		}
	})

	t.Run("sort-limit-lockrows-fold", func(t *testing.T) {
		// Sort key column 6 narrows to 7.
		op := mustBuildDeform(t, &optimizer.Sort{Child: scan8(), Keys: []optimizer.SortKey{{Expr: deformCol(6)}}})
		if b, n := seqLeafBound(t, op); b != 7 || n != 8 {
			t.Fatalf("sort bound=%d ncols=%d, want 7/8", b, n)
		}
		// A constant LIMIT references nothing: full width.
		op = mustBuildDeform(t, &optimizer.Limit{Child: scan8(), Limit: deformInt(10)})
		if b, n := seqLeafBound(t, op); b != 8 || n != 8 {
			t.Fatalf("limit bound=%d ncols=%d, want full 8/8", b, n)
		}
		// LockRows with no expressions propagates (full here: nothing above).
		op = mustBuildDeform(t, &optimizer.LockRows{Child: scan8()})
		if b, n := seqLeafBound(t, op); b != 8 || n != 8 {
			t.Fatalf("lockrows bound=%d ncols=%d, want full 8/8", b, n)
		}
	})

	t.Run("gather-captures-bound", func(t *testing.T) {
		plan := optimizer.NewGather(0, filter(deformLt(deformCol(2), deformInt(1)), scan8()), 1)
		op := mustBuildDeform(t, plan)
		// seqLeafBound follows the worker buildChild closure.
		if b, n := seqLeafBound(t, op); b != 3 || n != 8 {
			t.Fatalf("gather worker bound=%d ncols=%d, want 3/8", b, n)
		}
	})

	t.Run("distinct-forces-full", func(t *testing.T) {
		// Distinct consumes the whole row (dedup key + full-row sort
		// comparator), so a narrow filter below it must not narrow.
		op := mustBuildDeform(t, &optimizer.Distinct{
			Child: filter(deformLt(deformCol(1), deformInt(1)), scan8())})
		if b, n := seqLeafBound(t, op); b != 8 || n != 8 {
			t.Fatalf("distinct bound=%d ncols=%d, want full 8/8", b, n)
		}
	})
}

// TestEffectiveDeformBoundEdges pins the leaf resolution: None/Full mean full
// width, anything else clamps to [1, ncols], and bound == ncols stays ncols
// (the no-behaviour-change case).
func TestEffectiveDeformBoundEdges(t *testing.T) {
	cases := []struct {
		bound, ncols, want int
	}{
		{deformBoundNone, 16, 16},
		{deformBoundFull, 16, 16},
		{5, 16, 6},
		{0, 16, 1},
		{15, 16, 16},
		{20, 16, 16},
		{3, 8, 4},
	}
	for _, tc := range cases {
		if got := effectiveDeformBound(tc.bound, tc.ncols); got != tc.want {
			t.Errorf("effectiveDeformBound(%d, %d) = %d, want %d",
				tc.bound, tc.ncols, got, tc.want)
		}
	}
}

// TestScanDeformTailPoisonPanics pins the debug mechanism itself: with the
// flag armed, evaluating a poisoned slot panics, while head slots and the
// flag-off path behave exactly as before.
func TestScanDeformTailPoisonPanics(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = true
	defer func() { seqScanDeformPoison = old }()

	row := Row{NewIntDatum(1), NewIntDatum(2)}
	poisonDeformTail(row[1:], 0)
	if !isDeformPoison(row[1]) {
		t.Fatal("tail slot not poisoned")
	}
	if isDeformPoison(row[0]) {
		t.Fatal("head slot wrongly poisoned")
	}

	mustPanic := func(name string, e optimizer.Expr) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: expected panic reading poisoned slot", name)
			}
		}()
		_, _ = evalExprSlot(e, rowSlotView(row), NewContext())
	}
	mustPanic("interpreted", deformCol(1))

	// Head read stays quiet.
	if _, err := evalExprSlot(deformCol(0), rowSlotView(row), NewContext()); err != nil {
		t.Fatalf("head read: %v", err)
	}

	// Flag off: the same poisoned datum reads as an ordinary int.
	seqScanDeformPoison = false
	d, err := evalExprSlot(deformCol(1), rowSlotView(row), NewContext())
	if err != nil || d.Kind != KindInt {
		t.Fatalf("flag-off read = %+v, %v; want plain KindInt", d, err)
	}
}

// deformW8Fixture builds an 8-int-column table with four rows whose values
// encode their origin (10*a + b) so a wrong-column read cannot hide.
func deformW8Fixture(t *testing.T) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	if err := runDDL(t, ctx, "CREATE TABLE w (a int, b int, c int, d int, e int, f int, g int, h int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for a := int64(1); a <= 4; a++ {
		vals := make([]string, 8)
		for i := range vals {
			vals[i] = fmt.Sprintf("%d", 10*a+int64(i))
		}
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO w VALUES (%s)", strings.Join(vals, ","))); err != nil {
			t.Fatalf("INSERT: %v", err)
		}
	}
	return ctx
}

func renderDeformRows(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		parts := make([]string, 0, len(r))
		for _, d := range r {
			parts = append(parts, string(d.AppendValueText(nil)))
		}
		out = append(out, strings.Join(parts, "|"))
	}
	sort.Strings(out)
	return out
}

// TestScanDeformNarrowExecution runs a narrowing filter+project chain against
// a real table with the tail-poison armed (proving no consumer reads past
// the bound) and requires values identical to the full-deform variant of the
// same query.
func TestScanDeformNarrowExecution(t *testing.T) {
	ctx := deformW8Fixture(t)

	narrowSQL := `SELECT a, b FROM w WHERE a > 11 ORDER BY a`
	fullSQL := `SELECT a, b FROM w WHERE a > 11 AND h > 0 ORDER BY a`
	// Fixture rows carry a = 10, 20, 30, 40 (10*k + 0); a > 11 keeps the
	// last three, whose (a, b) pairs are 20|21, 30|31, 40|41.
	want := []string{"20|21", "30|31", "40|41"}

	// The narrow plan must actually engage narrowing (bound < 8).
	narrowPlan, err := testPlanDeform(t, ctx, narrowSQL)
	if err != nil {
		t.Fatalf("plan narrow: %v", err)
	}
	narrowOp := mustBuildDeform(t, narrowPlan)
	if b, n := seqLeafBound(t, narrowOp); b >= n || n != 8 {
		t.Fatalf("narrow leaf bound=%d ncols=%d, want bound < 8", b, n)
	} else {
		t.Logf("narrow chain leaf bound=%d ncols=%d", b, n)
	}

	// The full variant (predicate reaches the last column) must stay full.
	fullPlan, err := testPlanDeform(t, ctx, fullSQL)
	if err != nil {
		t.Fatalf("plan full: %v", err)
	}
	if b, n := seqLeafBound(t, mustBuildDeform(t, fullPlan)); b != 8 || n != 8 {
		t.Fatalf("full leaf bound=%d ncols=%d, want 8/8", b, n)
	}

	// Poison-armed narrow run: any out-of-bound read panics here.
	old := seqScanDeformPoison
	seqScanDeformPoison = true
	defer func() { seqScanDeformPoison = old }()
	narrowRows, err := runQueryWithErr(ctx, narrowSQL)
	if err != nil {
		t.Fatalf("narrow run: %v", err)
	}
	seqScanDeformPoison = false

	fullRows, err := runQueryWithErr(ctx, fullSQL)
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	// And the unpoisoned narrow run, for the record.
	plainRows, err := runQueryWithErr(ctx, narrowSQL)
	if err != nil {
		t.Fatalf("plain narrow run: %v", err)
	}

	for name, rows := range map[string][]Row{"narrow+poison": narrowRows, "full": fullRows, "narrow": plainRows} {
		got := renderDeformRows(rows)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s rows = %v, want %v", name, got, want)
		}
	}
	if fmt.Sprint(renderDeformRows(narrowRows)) != fmt.Sprint(renderDeformRows(fullRows)) {
		t.Errorf("narrow+poison %v != full %v", renderDeformRows(narrowRows), renderDeformRows(fullRows))
	}
}

// TestScanDeformQ6ShapeBound measures the bound a filter+aggregate chain
// produces on a real plan, runs it poison-armed, and requires values
// identical to the full-deform variant.
func TestScanDeformQ6ShapeBound(t *testing.T) {
	ctx := deformW8Fixture(t)

	// Q6 shape: aggregate over a selective filter. Columns are table-ordered
	// (a=0, b=1, c=2, …, h=7), so refs {b=1, a=0, c=2} give bound 3.
	narrowSQL := `SELECT sum(b) FROM w WHERE a > 11 AND c < 33`
	fullSQL := `SELECT sum(b) FROM w WHERE a > 11 AND c < 33 AND h > 0`
	// a > 11 keeps rows 2..4 (a = 20, 30, 40); c < 33 keeps rows 1..3
	// (c = 12, 22, 32). Intersection rows 2..3 with b = 21, 31 → 52.
	want := []string{"52"}

	narrowPlan, err := testPlanDeform(t, ctx, narrowSQL)
	if err != nil {
		t.Fatalf("plan narrow: %v", err)
	}
	narrowOp := mustBuildDeform(t, narrowPlan)
	b, n := seqLeafBound(t, narrowOp)
	t.Logf("Q6-shape (filter+agg) leaf bound=%d ncols=%d", b, n)
	if n != 8 {
		t.Fatalf("ncols=%d, want 8", n)
	}
	if b != 3 {
		t.Fatalf("bound=%d, want 3 (refs a=0, b=1, c=2)", b)
	}

	fullPlan, err := testPlanDeform(t, ctx, fullSQL)
	if err != nil {
		t.Fatalf("plan full: %v", err)
	}
	if fb, fn := seqLeafBound(t, mustBuildDeform(t, fullPlan)); fb != 8 || fn != 8 {
		t.Fatalf("full leaf bound=%d ncols=%d, want 8/8", fb, fn)
	}

	old := seqScanDeformPoison
	seqScanDeformPoison = true
	defer func() { seqScanDeformPoison = old }()
	narrowRows, err := runQueryWithErr(ctx, narrowSQL)
	if err != nil {
		t.Fatalf("narrow run: %v", err)
	}
	seqScanDeformPoison = false

	fullRows, err := runQueryWithErr(ctx, fullSQL)
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	for name, rows := range map[string][]Row{"narrow+poison": narrowRows, "full": fullRows} {
		if got := renderDeformRows(rows); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s rows = %v, want %v", name, got, want)
		}
	}
}

// TestScanDeformBoundEqualsMaxCols covers the degenerate survivor shape where
// the bound equals the prefilter prefix (SELECT a ... WHERE a > ...): the
// second deform phase is an empty range, the tail is poisoned from MaxCols
// on, and values must still match the full-deform variant.
func TestScanDeformBoundEqualsMaxCols(t *testing.T) {
	ctx := deformW8Fixture(t)

	narrowSQL := `SELECT a FROM w WHERE a > 11 ORDER BY a`
	fullSQL := `SELECT a FROM w WHERE a > 11 AND h > 0 ORDER BY a`
	want := []string{"20", "30", "40"}

	narrowPlan, err := testPlanDeform(t, ctx, narrowSQL)
	if err != nil {
		t.Fatalf("plan narrow: %v", err)
	}
	b, n := seqLeafBound(t, mustBuildDeform(t, narrowPlan))
	t.Logf("bound==MaxCols chain leaf bound=%d ncols=%d", b, n)
	if n != 8 || b != 1 {
		t.Fatalf("bound=%d ncols=%d, want 1/8", b, n)
	}

	old := seqScanDeformPoison
	seqScanDeformPoison = true
	defer func() { seqScanDeformPoison = old }()
	narrowRows, err := runQueryWithErr(ctx, narrowSQL)
	if err != nil {
		t.Fatalf("narrow run: %v", err)
	}
	seqScanDeformPoison = false

	fullRows, err := runQueryWithErr(ctx, fullSQL)
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	for name, rows := range map[string][]Row{"narrow+poison": narrowRows, "full": fullRows} {
		if got := renderDeformRows(rows); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s rows = %v, want %v", name, got, want)
		}
	}
}

// TestScanDeformNarrowNoPrefilter covers the narrow survivor path without an
// armed prefilter (no Filter directly above the scan): the scan deforms
// [0, bound) in a single range and poisons the tail.
func TestScanDeformNarrowNoPrefilter(t *testing.T) {
	ctx := deformW8Fixture(t)

	narrowSQL := `SELECT a, b FROM w ORDER BY a LIMIT 2`
	if plan, err := testPlanDeform(t, ctx, narrowSQL); err != nil {
		t.Fatalf("plan: %v", err)
	} else if b, n := seqLeafBound(t, mustBuildDeform(t, plan)); b != 2 || n != 8 {
		t.Fatalf("bound=%d ncols=%d, want 2/8", b, n)
	}

	old := seqScanDeformPoison
	seqScanDeformPoison = true
	defer func() { seqScanDeformPoison = old }()
	rows, err := runQueryWithErr(ctx, narrowSQL)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	seqScanDeformPoison = false

	want := []string{"10|11", "20|21"}
	if got := renderDeformRows(rows); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

// testPlanDeform plans one SQL statement against the fixture catalog.
func testPlanDeform(t *testing.T, ctx *Context, sql string) (optimizer.Node, error) {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	if len(stmts) != 1 {
		return nil, fmt.Errorf("Parse(%q): %d stmts", sql, len(stmts))
	}
	return optimizer.Plan(stmts[0], ctx.Catalog)
}

// TestScanDeformJoinKeyMapping pins the §2 merged-space remap on synthetic
// joins: keys split by the exprSide cutoff (Index < leftWidth → left as-is,
// else right at Index-leftWidth), range-checked per side, unioned with the
// below-walk refs.
func TestScanDeformJoinKeyMapping(t *testing.T) {
	scan8 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(8)} }
	join := func(pred optimizer.Expr) *optimizer.Join {
		return &optimizer.Join{
			Type: optimizer.JoinTypeInner, Algo: optimizer.JoinAlgoNestedLoop,
			Left: scan8(), Right: scan8(), Predicate: pred,
		}
	}
	sides := func(t *testing.T, plan *optimizer.Join) (lb, ln, rb, rn int) {
		t.Helper()
		op := mustBuildDeform(t, plan)
		jo, ok := op.(*joinOp)
		if !ok {
			t.Fatalf("built %T, want *joinOp", op)
		}
		lb, ln = seqLeafBound(t, jo.left)
		rb, rn = seqLeafBound(t, jo.right)
		return lb, ln, rb, rn
	}

	t.Run("both-sides", func(t *testing.T) {
		// Predicate col2 < col10: col2 < leftWidth 8 → left 2; col10 →
		// right 10-8 = 2. No above refs, no below-walk refs.
		lb, ln, rb, rn := sides(t, join(deformLt(deformCol(2), deformCol(10))))
		if lb != 3 || ln != 8 {
			t.Fatalf("left bound=%d ncols=%d, want 3/8", lb, ln)
		}
		if rb != 3 || rn != 8 {
			t.Fatalf("right bound=%d ncols=%d, want 3/8", rb, rn)
		}
	})

	t.Run("width-cap-unmappable", func(t *testing.T) {
		// Predicate col1 < col20: col1 → left 1; col20 → right 12, past
		// the right width 8 → the right side fails to full while the left
		// still narrows.
		lb, ln, rb, rn := sides(t, join(deformLt(deformCol(1), deformCol(20))))
		if lb != 2 || ln != 8 {
			t.Fatalf("left bound=%d ncols=%d, want 2/8", lb, ln)
		}
		if rb != 8 || rn != 8 {
			t.Fatalf("right bound=%d ncols=%d, want full 8/8", rb, rn)
		}
	})

	t.Run("keys-without-predicate-map-positionally", func(t *testing.T) {
		// Keys map by pair position even with no Predicate: LeftKey reads
		// left-row columns, RightKey right-row columns (the executor
		// evaluates them positionally — Q16's unrebased semi keys). The
		// old decline-to-full expectation was the bug.
		p := join(nil)
		p.LeftKey, p.RightKey = deformCol(0), deformCol(0)
		lb, ln, rb, rn := sides(t, p)
		if lb != 1 || ln != 8 {
			t.Fatalf("left bound=%d ncols=%d, want 1/8", lb, ln)
		}
		if rb != 1 || rn != 8 {
			t.Fatalf("right bound=%d ncols=%d, want 1/8", rb, rn)
		}
	})
	t.Run("unrebased-semi-keys-map-positionally", func(t *testing.T) {
		// Q16 regression: decorrelated semi keys unrebased — both
		// operands in left space (`cs1.x = cs1.x`). Index-space split
		// would file the right key on the left and starve the right
		// scan; pair position maps it right. Semi with no above refs.
		p := &optimizer.Join{
			Type: optimizer.JoinTypeSemi, Algo: optimizer.JoinAlgoHash,
			Left: scan8(), Right: scan8(),
			HashKeys: []optimizer.JoinKeyPair{
				{Left: deformCol(2), Right: deformCol(2)},
			},
		}
		lb, ln, rb, rn := sides(t, p)
		if lb != 3 || ln != 8 {
			t.Fatalf("left bound=%d ncols=%d, want 3/8", lb, ln)
		}
		if rb != 3 || rn != 8 {
			t.Fatalf("right bound=%d ncols=%d, want 3/8", rb, rn)
		}
	})
}

// TestScanDeformJoinAbovePayload is the above-ref regression test: a payload
// column read ONLY above the join must still deform below it. Project(col12)
// reads merged column 12 — right-side column 4 — so the right scan must cover
// it; the keys-only bound (right 1/8) would be a wrong answer.
func TestScanDeformJoinAbovePayload(t *testing.T) {
	scan8 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(8)} }
	plan := &optimizer.Join{
		Type: optimizer.JoinTypeInner, Algo: optimizer.JoinAlgoNestedLoop,
		Left: scan8(), Right: scan8(),
		Predicate: deformLt(deformCol(0), deformCol(8)),
	}
	proj := &optimizer.Project{Child: plan, Targets: []optimizer.Expr{deformCol(12)}}
	op := mustBuildDeform(t, proj)
	po, ok := op.(*projectOp)
	if !ok {
		t.Fatalf("built %T, want *projectOp", op)
	}
	jo, ok := po.child.(*joinOp)
	if !ok {
		t.Fatalf("project child is %T, want *joinOp", po.child)
	}
	// Above prefix 12 through left++right (8+8): the left share clamps to 7
	// (full left), the right share is 12-8 = 4; union the keys {0}/{0}.
	if b, n := seqLeafBound(t, jo.left); b != 8 || n != 8 {
		t.Fatalf("left bound=%d ncols=%d, want full 8/8", b, n)
	}
	if b, n := seqLeafBound(t, jo.right); b != 5 || n != 8 {
		t.Fatalf("right bound=%d ncols=%d, want 5/8", b, n)
	}
}

// TestScanDeformSemiJoinLeftOnly pins the semi/anti output layout: the output
// is the left side alone, so above-join refs land on the left only while the
// right narrows by keys+residual alone.
func TestScanDeformSemiJoinLeftOnly(t *testing.T) {
	scan8 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(8)} }
	plan := &optimizer.Join{
		Type: optimizer.JoinTypeSemi, Algo: optimizer.JoinAlgoNestedLoop,
		Left: scan8(), Right: scan8(),
		Predicate: deformLt(deformCol(1), deformCol(9)),
	}
	above := &optimizer.Filter{Child: plan, Predicate: deformLt(deformCol(5), deformInt(1))}
	op := mustBuildDeform(t, above)
	fo, ok := op.(*filterOp)
	if !ok {
		t.Fatalf("built %T, want *filterOp", op)
	}
	jo, ok := fo.child.(*joinOp)
	if !ok {
		t.Fatalf("filter child is %T, want *joinOp", fo.child)
	}
	// Above prefix 5 (< leftWidth 8) lands on the left intact; the right
	// takes no above share. Keys: col1 → left 1; col9 → right 9-8 = 1.
	// Left covers {5, 1} → 6; right covers {1} → 2.
	if b, n := seqLeafBound(t, jo.left); b != 6 || n != 8 {
		t.Fatalf("left bound=%d ncols=%d, want 6/8", b, n)
	}
	if b, n := seqLeafBound(t, jo.right); b != 2 || n != 8 {
		t.Fatalf("right bound=%d ncols=%d, want 2/8", b, n)
	}
}

// TestScanDeformNLIOuterBound pins the NLI left-side rule without storage:
// the outer takes its share of the above prefix plus the outer-side
// predicate refs; inner-side refs are discarded; an unattributable predicate
// fails the outer to full (the inner probes always run full, EX1-02b).
func TestScanDeformNLIOuterBound(t *testing.T) {
	outer := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(8)} }
	nli := func(incoming int, pred optimizer.Expr) int {
		return deformNLIOuterBound(&optimizer.NestedLoopIndexJoin{Outer: outer(), Predicate: pred}, incoming)
	}
	// Predicate col2 < col10: col2 is outer-side (2 < 8), col10 is
	// inner-side (discarded). No above refs → outer bound 2.
	if got := nli(deformBoundNone, deformLt(deformCol(2), deformCol(10))); got != 2 {
		t.Fatalf("outer=%d, want 2", got)
	}
	// Above prefix 5 unions with the outer key ref 2 → 5.
	if got := nli(5, deformLt(deformCol(2), deformCol(10))); got != 5 {
		t.Fatalf("outer=%d, want 5", got)
	}
	// An above prefix past the outer width clamps to the full outer width.
	if got := nli(12, deformLt(deformCol(2), deformCol(10))); got != 7 {
		t.Fatalf("outer=%d, want 7", got)
	}
	// An unattributable predicate fails the outer to full.
	badPred := &optimizer.FuncCall{Name: "abs", Args: []optimizer.Expr{deformCol(0)}}
	if got := nli(deformBoundNone, badPred); got != deformBoundFull {
		t.Fatalf("outer=%d, want Full", got)
	}
	// Full stays full.
	if got := nli(deformBoundFull, deformLt(deformCol(2), deformCol(10))); got != deformBoundFull {
		t.Fatalf("outer=%d, want Full", got)
	}
}

// TestScanDeformProjectThrough pins the EX1-02 project arm: an
// order-preserving all-bare projection maps through by folding its targets
// (bound = max mapped); expressions and consts reset to full. Reordered bare
// targets keep resetting (pinned by project-reorder-terminator, unchanged).
func TestScanDeformProjectThrough(t *testing.T) {
	scan8 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(8)} }
	proj := func(targets ...optimizer.Expr) *optimizer.Project {
		return &optimizer.Project{Child: scan8(), Targets: targets}
	}

	t.Run("ordered-bare-maps", func(t *testing.T) {
		// Targets [0, 2]: the projection materialises child columns {0, 2}.
		op := mustBuildDeform(t, proj(deformCol(0), deformCol(2)))
		if b, n := seqLeafBound(t, op); b != 3 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want 3/8", b, n)
		}
	})

	t.Run("ordered-bare-with-above", func(t *testing.T) {
		// The filter above reads output column 1; the projection itself
		// reads {0, 2}. Bound covers both.
		plan := &optimizer.Filter{
			Child:     proj(deformCol(0), deformCol(2)),
			Predicate: deformLt(deformCol(1), deformInt(1)),
		}
		op := mustBuildDeform(t, plan)
		if b, n := seqLeafBound(t, op); b != 3 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want 3/8", b, n)
		}
	})

	t.Run("expression-resets", func(t *testing.T) {
		p := proj(&optimizer.BinaryOp{Op: parser.OpAdd, Left: deformCol(0), Right: deformInt(1)})
		op := mustBuildDeform(t, p)
		if b, n := seqLeafBound(t, op); b != 8 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want full 8/8", b, n)
		}
	})

	t.Run("const-target-resets", func(t *testing.T) {
		p := proj(deformCol(0), deformInt(1))
		op := mustBuildDeform(t, p)
		if b, n := seqLeafBound(t, op); b != 8 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want full 8/8", b, n)
		}
	})
}

// joinSideBounds builds plan and reports the effective deform bounds of the
// scans under the first Join below the root, following single-child
// pass-throughs. It fails the test when no Join is present.
func joinSideBounds(t *testing.T, plan optimizer.Node) (lb, ln, rb, rn int) {
	t.Helper()
	op := mustBuildDeform(t, plan)
	var find func(o Operator) *joinOp
	find = func(o Operator) *joinOp {
		switch n := o.(type) {
		case *joinOp:
			return n
		case *filterOp:
			return find(n.child)
		case *projectOp:
			return find(n.child)
		case *sortOp:
			return find(n.child)
		case *limitOp:
			return find(n.child)
		case *aggregateOp:
			return find(n.child)
		case *distinctOp:
			return find(n.child)
		case *lockRowsOp:
			return find(n.child)
		case *resultOp:
			if n.child == nil {
				return nil
			}
			return find(n.child)
		default:
			return nil
		}
	}
	jo := find(op)
	if jo == nil {
		t.Fatalf("no joinOp under %T", op)
	}
	lb, ln = seqLeafBound(t, jo.left)
	rb, rn = seqLeafBound(t, jo.right)
	return lb, ln, rb, rn
}

// TestScanDeformQ9ShapeBound pins the Q9 witness shape — an aggregate over a
// filter over a join — in both halves:
//
//   - Synthetic bound half: Aggregate(Arg col1) → Filter(col5) → Join(col0 =
//     col8, left Filter(col3) over scan8, right scan8). Walk: the aggregate
//     folds {1}, the filter folds {5}, the join splits incoming 5 into
//     leftAbove 5 / rightAbove none and unions keys {0}/{0} → left 5,
//     right 0; the left below-walk filter folds {3}. Left covers {5, 3, 0}
//     → 6, right covers {0} → 1: both < ncols under the join.
//   - Execution half: a real self-join runs poison-armed (proving every new
//     arm — join remap, const-pad project reset, pruned ordered-bare
//     projects below the join — excludes nothing any consumer reads) with
//     values identical to the full-deform variant. (The real plan carries a
//     const-padding Project above the join, which declines by design, so
//     narrowing itself is pinned by the synthetic half, not the plan.)
func TestScanDeformQ9ShapeBound(t *testing.T) {
	scan8 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(8)} }
	shape := &optimizer.Aggregate{
		Child: &optimizer.Filter{
			Child: &optimizer.Join{
				Type: optimizer.JoinTypeInner, Algo: optimizer.JoinAlgoNestedLoop,
				Left: &optimizer.Filter{
					Child:     scan8(),
					Predicate: deformLt(deformCol(3), deformInt(1)),
				},
				Right:     scan8(),
				Predicate: deformLt(deformCol(0), deformCol(8)),
			},
			Predicate: deformLt(deformCol(5), deformInt(1)),
		},
		Aggs: []optimizer.AggregateCall{
			{Name: "sum", Arg: deformCol(1), Type: catalog.Type{Name: "int8"}},
		},
	}
	lb, ln, rb, rn := joinSideBounds(t, shape)
	t.Logf("Q9-shape (agg+filter+join) leaf bounds left=%d/%d right=%d/%d", lb, ln, rb, rn)
	if ln != 8 || rn != 8 {
		t.Fatalf("ncols=%d/%d, want 8/8", ln, rn)
	}
	if lb != 6 || rb != 1 {
		t.Fatalf("bounds left=%d right=%d, want 6/8 and 1/8", lb, rb)
	}

	ctx := deformW8Fixture(t)

	// The full variant adds the always-true t2.h > 0 (right column 7) to pin
	// the right side full with identical values.
	narrowSQL := `SELECT sum(t1.b) FROM w t1 JOIN w t2 ON t1.a = t2.a WHERE t1.c < 33`
	fullSQL := `SELECT sum(t1.b) FROM w t1 JOIN w t2 ON t1.a = t2.a WHERE t1.c < 33 AND t2.h > 0`
	// c < 33 keeps fixture rows 1..3 (c = 12, 22, 32); the equi-join pairs
	// each with itself; b = 11, 21, 31 → 63.
	want := []string{"63"}

	fullPlan, err := testPlanDeform(t, ctx, fullSQL)
	if err != nil {
		t.Fatalf("plan full: %v", err)
	}
	if _, _, frb, frn := joinSideBounds(t, fullPlan); frb != 8 || frn != 8 {
		t.Fatalf("full right bound=%d ncols=%d, want 8/8", frb, frn)
	}
	if plan, err := testPlanDeform(t, ctx, narrowSQL); err != nil {
		t.Fatalf("plan narrow: %v", err)
	} else if lb, ln, rb, rn := joinSideBounds(t, plan); ln != 8 || rn != 8 {
		t.Fatalf("narrow ncols=%d/%d, want 8/8", ln, rn)
	} else {
		t.Logf("Q9-shape execution-plan leaf bounds left=%d/%d right=%d/%d", lb, ln, rb, rn)
	}

	old := seqScanDeformPoison
	seqScanDeformPoison = true
	defer func() { seqScanDeformPoison = old }()
	narrowRows, err := runQueryWithErr(ctx, narrowSQL)
	if err != nil {
		t.Fatalf("narrow run: %v", err)
	}
	seqScanDeformPoison = false

	fullRows, err := runQueryWithErr(ctx, fullSQL)
	if err != nil {
		t.Fatalf("full run: %v", err)
	}
	for name, rows := range map[string][]Row{"narrow+poison": narrowRows, "full": fullRows} {
		if got := renderDeformRows(rows); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s rows = %v, want %v", name, got, want)
		}
	}
}
