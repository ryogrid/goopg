package executor

// R95 (plan-parity-fix-take2): lateral-probe nested-loop worker semantics.
//
// Q96's shape — R25's decomposed probe (lateral Join over a bare
// parameterized index probe, re-opened per outer tuple) — partitioned over
// workers on the outer side. The serial-vs-parallel identity gate is what
// catches the N-copies failure mode. The trees below mirror Q96's measured
// shape (Join Lateral + IndexScan probe with OuterColumnRef keys); the
// planner does not emit that shape in a unit fixture (the lateral SQL plans
// a bitmap/seq inner, EXISTS unnests to SEMI), so they are hand-built and
// every test asserts the shape it exercises.

import (
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// latProbeFixture adds the probe index Q96's shape needs: entries keyed by
// the inner join column. Created via SQL, not the catalog API — only the
// DDL path backs the index with storage the executor can open (a
// catalog-only index dies at Open with "short read at block").
func latProbeFixture(t *testing.T, ctx *Context) {
	t.Helper()
	if err := runDDL(t, ctx, "CREATE INDEX pq_dim_dk_idx ON pq_dim (dk)"); err != nil {
		t.Fatal(err)
	}
	setLatStats(t, ctx)
}

func setLatStats(t *testing.T, ctx *Context) {
	t.Helper()
	set := func(name string, rows int64, cols ...catalog.ColumnStats) {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
		if !ok || tbl == nil {
			t.Fatalf("table %s not found", name)
		}
		ctx.Catalog.SetTableStats(tbl, &catalog.TableStats{
			RowCount: rows, Pages: 4096, Analyzed: true, Columns: cols,
		})
	}
	set("pq_fact", 400,
		catalog.ColumnStats{NDistinct: 400},
		catalog.ColumnStats{NDistinct: 40},
		catalog.ColumnStats{NDistinct: 400},
	)
	set("pq_dim", 20,
		catalog.ColumnStats{NDistinct: 19},
		catalog.ColumnStats{NDistinct: 20},
	)
}

// latProbeIndex finds the probe index by its leading column.
func latProbeIndex(t *testing.T, ctx *Context) (*catalog.Table, *catalog.Index) {
	t.Helper()
	dim, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "pq_dim"})
	if !ok {
		t.Fatal("pq_dim missing")
	}
	for _, ci := range ctx.Catalog.IndexesOnTable(dim) {
		if len(ci.Columns) > 0 && ci.Columns[0] == "dk" {
			return dim, ci
		}
	}
	t.Fatal("probe index on pq_dim(dk) missing")
	return nil, nil
}

// latProbeTree hand-builds the decomposed probe shape: lateral INNER NL
// over a bare parameterized index probe. outerWhere, when non-nil, wraps
// the outer in a Filter (also the vehicle for the subqCache pin below).
func latProbeTree(t *testing.T, ctx *Context, outerWhere optimizer.Expr) optimizer.Node {
	t.Helper()
	fact, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "pq_fact"})
	if !ok {
		t.Fatal("pq_fact missing")
	}
	dim, idx := latProbeIndex(t, ctx)
	var outer optimizer.Node = &optimizer.SeqScan{Table: fact}
	if outerWhere != nil {
		outer = &optimizer.Filter{Child: outer, Predicate: outerWhere}
	}
	return &optimizer.Join{
		Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeInner,
		Lateral: true, Left: outer,
		Right: &optimizer.IndexScan{Table: dim, Index: idx,
			Key: &optimizer.OuterColumnRef{Level: 1, Index: 1, Name: "fk"}},
	}
}

// latExistsWhere plans a passing uncorrelated EXISTS for the subquery pin.
func latExistsWhere(t *testing.T, ctx *Context) optimizer.Expr {
	t.Helper()
	stmts, err := parser.Parse("SELECT 1 FROM pq_dim WHERE dk > 0")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("subquery plan: %v", err)
	}
	return &optimizer.ExistsExpr{Plan: sub, IsNonCorrelated: true}
}

// fakeBindableOp is a probe-shaped operator that implements lateralBindable.
// It pins the double-binding refusal: the BuildFast bridge forwards the
// slot binding unconditionally, so a wrapped probe would bind twice (slot
// plus ctx.OuterRows) where serial execution binds once.
type fakeBindableOp struct {
	*indexScanOp
}

func (o *fakeBindableOp) BindLateralOuter(slot SlotView) {}

// TestParallelLateralWalkerRefusals pins the admission matrix on all three
// walks plus the inner-no-claim rule.
func TestParallelLateralWalkerRefusals(t *testing.T) {
	probePlan := func() *optimizer.IndexScan {
		return &optimizer.IndexScan{Index: &catalog.Index{},
			Key: &optimizer.OuterColumnRef{Level: 1, Index: 1}}
	}
	goodPlan := func() *optimizer.Join {
		return &optimizer.Join{Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeInner,
			Lateral: true, Left: &optimizer.SeqScan{}, Right: probePlan()}
	}
	// Approval on all three walks, inner takes no claim state.
	op := &joinOp{plan: goodPlan(), left: &seqScanOp{}, right: &indexScanOp{}}
	if !attachParallelScan(op, newParallelScanState(0)) {
		t.Fatal("approved lateral probe must attach (sequential)")
	}
	if op.left.(*seqScanOp).pscan == nil {
		t.Error("outer scan must take the claim")
	}
	if op.right.(*indexScanOp).pidx != nil {
		t.Error("probe must never take index claim state")
	}
	// Each walk attaches only its own scan kind: bitmap/index walks on a
	// sequential outer correctly report nothing to attach.
	op2 := &joinOp{plan: goodPlan(), left: &bitmapHeapScanOp{}, right: &indexScanOp{}}
	if !attachParallelBitmapScan(op2, newParallelBitmapState()) {
		t.Fatal("approved lateral probe must attach (bitmap walk reaches outer)")
	}
	if op2.left.(*bitmapHeapScanOp).pbm == nil {
		t.Error("outer bitmap must take the claim")
	}
	op3 := &joinOp{plan: goodPlan(), left: &indexOnlyScanOp{}, right: &indexScanOp{}}
	if !attachParallelIndexScan(op3, newParallelIndexScanState()) {
		t.Fatal("approved lateral probe must attach (index walk reaches outer)")
	}
	// Poisoned inners are refused on the matching walk too (not just vacuously).
	badBitmapOuter := func(right Operator) *joinOp {
		return &joinOp{plan: goodPlan(), left: &bitmapHeapScanOp{}, right: right}
	}
	if attachParallelBitmapScan(badBitmapOuter(&fakeBindableOp{indexScanOp: &indexScanOp{}}), newParallelBitmapState()) {
		t.Error("bitmap walk must refuse a bindable inner")
	}
	badIndexOuter := func(right Operator) *joinOp {
		return &joinOp{plan: goodPlan(), left: &indexOnlyScanOp{}, right: right}
	}
	if attachParallelIndexScan(badIndexOuter(&fakeBindableOp{indexScanOp: &indexScanOp{}}), newParallelIndexScanState()) {
		t.Error("index walk must refuse a bindable inner")
	}

	refusals := map[string]*joinOp{
		"semi":        {plan: &optimizer.Join{Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeSemi, Lateral: true, Left: &optimizer.SeqScan{}, Right: probePlan()}, left: &seqScanOp{}, right: &indexScanOp{}},
		"cross":       {plan: &optimizer.Join{Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeCross, Lateral: true, Left: &optimizer.SeqScan{}, Right: probePlan()}, left: &seqScanOp{}, right: &indexScanOp{}},
		// NOTE: non-lateral INNER over a probe is NOT refused — R94's
		// ordinary rule admits it (same agreement lesson as the planner
		// test); the shape is worker-sound either way.
		"seq-inner": {plan: &optimizer.Join{Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeInner, Lateral: true, Left: &optimizer.SeqScan{}, Right: &optimizer.SeqScan{}}, left: &seqScanOp{}, right: &seqScanOp{}},
		"bindable-inner": {plan: goodPlan(), left: &seqScanOp{}, right: &fakeBindableOp{indexScanOp: &indexScanOp{}}},
		"bitmap-inner": {plan: &optimizer.Join{Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeInner, Lateral: true, Left: &optimizer.SeqScan{}, Right: &optimizer.BitmapHeapScan{}}, left: &seqScanOp{}, right: &bitmapHeapScanOp{}},
		"nil-plan":    {left: &seqScanOp{}, right: &indexScanOp{}},
	}
	for name, o := range refusals {
		if attachParallelScan(o, newParallelScanState(0)) {
			t.Errorf("%s: sequential walk must refuse", name)
		}
		if attachParallelBitmapScan(o, newParallelBitmapState()) {
			t.Errorf("%s: bitmap walk must refuse", name)
		}
		if attachParallelIndexScan(o, newParallelIndexScanState()) {
			t.Errorf("%s: index walk must refuse", name)
		}
	}
	if attachParallelScan(&joinOp{plan: goodPlan(), left: &seqScanOp{}, right: &indexScanOp{}}, nil) {
		t.Error("nil state must refuse")
	}
}

// TestCollectShareableJoinsDescendsLateralNL pins the prebuild agreement:
// hashes below an approved lateral (or ordinary) NL outer are collected;
// anything else is untouched.
func TestCollectShareableJoinsDescendsLateralNL(t *testing.T) {
	hashOp := func() *joinOp {
		return &joinOp{plan: &optimizer.Join{Algo: optimizer.JoinAlgoHash, Type: optimizer.JoinTypeInner},
			left: &seqScanOp{}, right: &seqScanOp{}}
	}
	probeOp := &indexScanOp{}
	lateralPlan := &optimizer.Join{Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeInner,
		Lateral: true, Left: &optimizer.SeqScan{},
		Right: &optimizer.IndexScan{Key: &optimizer.OuterColumnRef{Level: 1, Index: 1}}}
	lateral := &joinOp{plan: lateralPlan, left: hashOp(), right: probeOp}
	var out []*joinOp
	collectShareableJoins(lateral, &out)
	if len(out) != 1 {
		t.Fatalf("hash below lateral-NL outer must be collected, got %d", len(out))
	}
	ordinaryPlan := &optimizer.Join{Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeInner,
		Left: &optimizer.SeqScan{}, Right: &optimizer.SeqScan{}}
	ordinary := &joinOp{plan: ordinaryPlan, left: hashOp(), right: &seqScanOp{}}
	out = nil
	collectShareableJoins(ordinary, &out)
	if len(out) != 1 {
		t.Fatalf("hash below ordinary-NL outer must be collected, got %d", len(out))
	}
	refusedPlan := &optimizer.Join{Algo: optimizer.JoinAlgoNestedLoop, Type: optimizer.JoinTypeRight,
		Lateral: true, Left: &optimizer.SeqScan{}, Right: &optimizer.SeqScan{}}
	refused := &joinOp{plan: refusedPlan, left: hashOp(), right: &seqScanOp{}}
	out = nil
	collectShareableJoins(refused, &out)
	if len(out) != 0 {
		t.Fatalf("refused NL must collect nothing, got %d", len(out))
	}
}

// drainSerial runs a hand-built tree serially (the identity baseline).
func drainSerial(t *testing.T, ctx *Context, node optimizer.Node) []string {
	t.Helper()
	advanceStmtCounter(ctx)
	op, err := Build(node)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out
}

// TestParallelLateralProbeIdentity is the gate: the hand-Gathered lateral
// probe returns the exact serial multiset at 1/2/4 workers.
func TestParallelLateralProbeIdentity(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()
	latProbeFixture(t, ctx)

	factTrue := &optimizer.BinaryOp{Op: parser.OpGt,
		Left:  &optimizer.ColumnRef{Index: 0, Name: "fid"},
		Right: &optimizer.IntegerConst{Value: -1}}
	factSelective := &optimizer.BinaryOp{Op: parser.OpGt,
		Left:  &optimizer.ColumnRef{Index: 2, Name: "amt"},
		Right: &optimizer.IntegerConst{Value: 200}}
	factEmpty := &optimizer.BinaryOp{Op: parser.OpGt,
		Left:  &optimizer.ColumnRef{Index: 0, Name: "fid"},
		Right: &optimizer.IntegerConst{Value: 100000}}
	// Subquery outer: the subqCache depth-scoping pin. The lateral
	// push/pop churns OuterRows depth per row per worker; a subquery in
	// the outer exercises the depth-keyed cache under that churn
	// (subq_cache.go). Uncorrelated and passing, so the oracle is the
	// plain case's 186 rows. Planned fresh per run: plan nodes must not
	// be shared across executions.
	buildWhere := map[string]func() optimizer.Expr{
		"plain":    func() optimizer.Expr { return factTrue },
		"filtered": func() optimizer.Expr { return factSelective },
		"empty":    func() optimizer.Expr { return factEmpty },
		"subquery": func() optimizer.Expr { return latExistsWhere(t, ctx) },
	}
	for name, build := range buildWhere {
		where := build()
		node := latProbeTree(t, ctx, where)
		want := drainSerial(t, ctx, node)
		sort.Strings(want)
		if name != "empty" && len(want) == 0 {
			t.Fatalf("%s: fixture produced no rows; the comparison is vacuous", name)
		}
		for _, workers := range []int{1, 2, 4} {
			got := runNLGathered(t, ctx, latProbeTree(t, ctx, build()), workers)
			sort.Strings(got)
			if len(got) != len(want) {
				t.Fatalf("%s workers=%d: got %d rows, want %d", name, workers, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s workers=%d: row %d differs", name, workers, i)
				}
			}
		}
	}
}
