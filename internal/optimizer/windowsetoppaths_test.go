package optimizer

// C-18 (P4-09) — the WINDOW and SETOP upper rels.
//
// What is pinned here, and why each pin exists:
//
//   - the §4.3 sizing duty on both rels (an `NCols == 0` rel silently prices a
//     spilling sort as an in-memory quicksort);
//   - the single-candidate invariant on both, and — for windows — that a
//     multi-group chain contributes exactly ONE path to the rel, not one per
//     group (`create_one_window_path` add_paths once, planner.c:4620);
//   - the SETOP rel being allocated PER NODE, which is the regression pin for
//     the wrong-answer defect a shared relids-0 rel caused: with one rel per
//     chain, `getCheapestFractionalPath` answered the outer node's question
//     with the inner node's candidate and the executor's set-op precedence
//     suite went red;
//   - the C-10c pointer walk on both producers: the emitted subtree contains
//     EXACTLY the pre-producer input node(s), no Sort/Filter/Join introduced;
//   - both cost functions against their NAMED constants, never literals.

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// windowTestNode builds one spec group over `child`: two partition columns,
// one order key, one window function appended to the output.
func windowTestNode(child Node, nfuncs int) *WindowAgg {
	out := append(Schema(nil), child.Output()...)
	funcs := make([]WindowFunc, 0, nfuncs)
	for i := 0; i < nfuncs; i++ {
		funcs = append(funcs, WindowFunc{Name: "row_number", Type: catalog.Type{Name: "int8"}})
		out = append(out, SchemaColumn{Name: "rn", Type: catalog.Type{Name: "int8"}})
	}
	return &WindowAgg{
		pos:         7,
		Child:       child,
		PartitionBy: []Expr{&ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}},
		OrderBy: []SortKey{
			{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}},
		},
		Funcs:  funcs,
		schema: out,
	}
}

// TestSizeWindowRelFromNode pins the DESIGN §4.3 duty: Rows is the INPUT's
// count (a WindowAgg emits one row per input row, costsize.c:3165) and
// Width/NCols/AvgVarBytes describe the rel's OUTPUT. A zero NCols here is the
// silent bug this test exists to catch.
func TestSizeWindowRelFromNode(t *testing.T) {
	in := upperOrderedInput(1000)
	win := windowTestNode(in, 1)
	rel := &RelOptInfo{}
	sizeWindowRelFromNode(rel, win, in)
	if rel.Rows != 1000 {
		t.Fatalf("WINDOW rel Rows = %v, want the input's 1000 (window preserves cardinality)", rel.Rows)
	}
	if rel.NCols != len(win.Output()) {
		t.Fatalf("WINDOW rel NCols = %d, want %d (output width)", rel.NCols, len(win.Output()))
	}
	if rel.NCols == 0 {
		t.Fatal("WINDOW rel NCols = 0 suppresses costSortRun's external-merge arm")
	}
	if rel.AvgVarBytes <= 0 {
		t.Fatalf("WINDOW rel AvgVarBytes = %v, want > 0 (text + numeric columns present)", rel.AvgVarBytes)
	}
	if rel.Width <= 0 {
		t.Fatalf("WINDOW rel Width = %d, want > 0", rel.Width)
	}
}

// TestCostWindowIsCostWindowaggTermByTerm prices the node through the NAMED
// cost constants — `costSortRun` for the executor's internal sort plus
// upstream's three per-input-row terms (costsize.c:3151/3161/3162). Pinning a
// literal here would hide a calibration change in any of them.
func TestCostWindowIsCostWindowaggTermByTerm(t *testing.T) {
	cp := defaultCostParams()
	const (
		rows            = 1000.0
		inputTotal      = 100.0
		numPart, numOrd = 2, 3
		numFuncs        = 2
		inNcols         = 4
		inAvgVarBytes   = 32.0
	)
	sortRun := costSortRun(cp, rows, inNcols, inAvgVarBytes, -1)
	wantStartup := inputTotal + sortRun.Startup
	wantTotal := wantStartup + (sortRun.Total - sortRun.Startup) +
		cp.cpuOperatorCost*numFuncs*rows +
		cp.cpuOperatorCost*(numPart+numOrd)*rows +
		cp.cpuTupleCost*rows

	got := costWindow(cp, inputTotal, rows, numPart, numOrd, numFuncs, inNcols, inAvgVarBytes, 64, 0, 0)
	if math.Abs(got.Startup-wantStartup) > 1e-9 {
		t.Fatalf("costWindow startup = %v, want %v", got.Startup, wantStartup)
	}
	if math.Abs(got.Total-wantTotal) > 1e-9 {
		t.Fatalf("costWindow total = %v, want %v", got.Total, wantTotal)
	}
	// The node BLOCKS: startup is never the input's startup.
	if got.Startup <= inputTotal-1e-9 {
		t.Fatalf("costWindow startup %v does not cover the input's total %v; the executor sorts before emitting", got.Startup, inputTotal)
	}
}

// TestCostWindowSortTermUsesRowWidthNotKeyCount is the trap this cut was
// warned about: `costSortRun`'s `ncols` sizes one ROW for the spill arm, so
// passing the window's KEY count there would model a narrow row and suppress
// the disk charge. A wide row must price strictly higher than a narrow one at
// the same key count.
func TestCostWindowSortTermUsesRowWidthNotKeyCount(t *testing.T) {
	cp := defaultCostParams()
	narrow := costWindow(cp, 100, 5e6, 1, 1, 1, 2, 0, 16, 0, 0)
	wide := costWindow(cp, 100, 5e6, 1, 1, 1, 40, 400, 1024, 0, 0)
	if !(wide.Total > narrow.Total) {
		t.Fatalf("wide-row window total %v not above narrow-row %v; costSortRun is being fed the key count, not the row width", wide.Total, narrow.Total)
	}
}

// TestCostWindowPresortedCreditIsInertAtZero pins M0141-S2b-3a's contract:
// `presortedCount=0` (every caller today — `addWindowPaths` always passes it)
// must reproduce the pre-3a price EXACTLY. This is the "predicted
// byte-identical" gate the task was filed under: the credit exists but has no
// caller yet, so a regression here would be silent everywhere else.
func TestCostWindowPresortedCreditIsInertAtZero(t *testing.T) {
	cp := defaultCostParams()
	got := costWindow(cp, 100, 5e6, 2, 3, 2, 4, 32, 64, 0, 0)
	sortRun := costSortRun(cp, 5e6, 4, 32, -1)
	wantStartup := 100 + sortRun.Startup
	wantTotal := wantStartup + (sortRun.Total - sortRun.Startup) +
		cp.cpuOperatorCost*2*5e6 + cp.cpuOperatorCost*5*5e6 + cp.cpuTupleCost*5e6
	if math.Abs(got.Startup-wantStartup) > 1e-6 || math.Abs(got.Total-wantTotal) > 1e-6 {
		t.Fatalf("costWindow(presortedCount=0) = %+v, want {%v %v} (no-credit price unchanged)", got, wantStartup, wantTotal)
	}
}

// TestCostWindowFullPresortedMatchChargesNoSort pins the FULL-match credit:
// when the input already delivers every required sort key
// (`presortedCount >= numPartCols+numOrderCols`), `createWindowPlan` stacks no
// Sort node at all (`childDeliversSortKeys`), so the price must drop to
// exactly the input's own total plus the per-row window overhead — no sort
// term of any kind.
func TestCostWindowFullPresortedMatchChargesNoSort(t *testing.T) {
	cp := defaultCostParams()
	const (
		inputTotal      = 100.0
		rows            = 5e6
		numPart, numOrd = 2, 3
		numFuncs        = 2
	)
	got := costWindow(cp, inputTotal, rows, numPart, numOrd, numFuncs, 4, 32, 64, numPart+numOrd, rows)
	wantTotal := inputTotal +
		cp.cpuOperatorCost*numFuncs*rows +
		cp.cpuOperatorCost*(numPart+numOrd)*rows +
		cp.cpuTupleCost*rows
	if math.Abs(got.Startup-inputTotal) > 1e-6 {
		t.Fatalf("costWindow(full match) startup = %v, want exactly the input total %v (no sort charged)", got.Startup, inputTotal)
	}
	if math.Abs(got.Total-wantTotal) > 1e-6 {
		t.Fatalf("costWindow(full match) total = %v, want %v", got.Total, wantTotal)
	}
	noCredit := costWindow(cp, inputTotal, rows, numPart, numOrd, numFuncs, 4, 32, 64, 0, 0)
	if !(got.Total < noCredit.Total) {
		t.Fatalf("full-match price %v not below no-credit price %v", got.Total, noCredit.Total)
	}
}

// TestCostWindowPartialPresortedMatchIsCheaperThanFullSort pins the PARTIAL
// credit's shape: a candidate presorted on a genuine prefix must price
// strictly below the flat full-sort price, and the saving must GROW as the
// prefix's group count grows — more groups means a smaller residual sort
// within each one (`costIncrementalSort`'s own `groupTuples = inputTuples /
// inputGroups` division), the same direction
// `TestCostIncrementalSort_NeverExceedsSingleGroupCost` pins for the ORDERED
// rel's own arm.
func TestCostWindowPartialPresortedMatchIsCheaperThanFullSort(t *testing.T) {
	cp := defaultCostParams()
	const (
		inputTotal      = 100.0
		rows            = 5e6
		numPart, numOrd = 2, 3
	)
	noCredit := costWindow(cp, inputTotal, rows, numPart, numOrd, 1, 4, 32, 64, 0, 0)
	fewGroups := costWindow(cp, inputTotal, rows, numPart, numOrd, 1, 4, 32, 64, 1, 10)
	manyGroups := costWindow(cp, inputTotal, rows, numPart, numOrd, 1, 4, 32, 64, 1, 1000)
	if !(fewGroups.Total < noCredit.Total) {
		t.Fatalf("partial-prefix price %v not below no-credit price %v", fewGroups.Total, noCredit.Total)
	}
	if !(manyGroups.Total < fewGroups.Total) {
		t.Fatalf("more groups (%v) should cost less than fewer groups (%v)", manyGroups.Total, fewGroups.Total)
	}
}

func TestAddWindowPathsUsesPreWindowEmittedWidth(t *testing.T) {
	restore := setPGSortRelationBytesCostForTest(true)
	defer restore()
	cp := defaultCostParams()
	cp.workMem = 100
	in := upperOrderedInput(1000000)
	win := windowTestNode(in, 1)
	winRel := &RelOptInfo{}
	sizeWindowRelFromNode(winRel, win, in)
	seed := &Path{Rel: winRel, Rows: winRel.Rows, Cost: Cost{Total: 100}}
	addWindowPaths(winRel, seed, []*WindowAgg{win}, in, cp, nil)
	if len(winRel.Pathlist) != 1 {
		t.Fatalf("window paths = %d, want one", len(winRel.Pathlist))
	}
	want := costWindow(cp, seed.Cost.Total, seed.Rows,
		len(win.PartitionBy), len(win.OrderBy), len(win.Funcs), len(in.Output()),
		nodeAvgVarBytes(in.Output()), nodeTupleWidth(in), 0, 0)
	postWindow := costWindow(cp, seed.Cost.Total, seed.Rows,
		len(win.PartitionBy), len(win.OrderBy), len(win.Funcs), len(in.Output()),
		nodeAvgVarBytes(in.Output()), nodeTupleWidth(win), 0, 0)
	if want == postWindow {
		t.Fatalf("fixture does not distinguish pre-window width %d from output width %d", nodeTupleWidth(in), nodeTupleWidth(win))
	}
	if got := winRel.Pathlist[0].Cost; got != want {
		t.Fatalf("Window path cost %+v, want pre-window-width cost %+v, not post-window %+v", got, want, postWindow)
	}
}

// TestAddWindowPathsAddsOnePathPerChain pins `create_one_window_path`'s
// shape: a chain of N spec groups stacks N `PathWindow`s and add_paths the
// TOPMOST once. One path per group on the rel would let `set_cheapest` answer
// group 2's question with group 1's candidate — the relids-0 sharing bug.
func TestAddWindowPathsAddsOnePathPerChain(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(1000)
	w1 := windowTestNode(in, 1)
	w2 := windowTestNode(w1, 1)
	u := newUpperRels()
	rel := fetchUpperRel(u, UpperWindow, 0, 0)
	sizeWindowRelFromNode(rel, w2, in)
	seed := seedPathForNode(rel, in)
	addWindowPaths(rel, seed, []*WindowAgg{w1, w2}, in, cp, nil)

	if len(rel.Pathlist) != 1 {
		t.Fatalf("WINDOW pathlist holds %d paths, want exactly 1 (the topmost of the chain)", len(rel.Pathlist))
	}
	top := rel.Pathlist[0]
	if top.Kind != PathWindow || top.Window != w2 {
		t.Fatalf("top path is not the outer window group: kind=%d window=%p want %p", top.Kind, top.Window, w2)
	}
	if len(top.Children) != 1 || top.Children[0].Kind != PathWindow || top.Children[0].Window != w1 {
		t.Fatal("the inner window group is not the top path's child; the chain was not stacked")
	}
	if top.Children[0].Children[0] != seed {
		t.Fatal("the seed is not the bottom of the stack")
	}
	// Stacking must cost monotonically: two window evaluations cannot be
	// cheaper than one over the same input.
	if !(top.Cost.Total > top.Children[0].Cost.Total) {
		t.Fatalf("two-group chain total %v not above one-group %v", top.Cost.Total, top.Children[0].Cost.Total)
	}
}

// TestCreateWindowPathsEmitsTheSameChainOverTheSameInput is the C-10c pointer
// walk for the window producer: the emitted subtree holds EXACTLY the
// pre-producer input node, with no node introduced between it and the first
// WindowAgg, and every spec field carried across.
func TestCreateWindowPathsEmitsTheSameChainOverTheSameInput(t *testing.T) {
	in := upperOrderedInput(1000)
	w1 := windowTestNode(in, 1)
	w2 := windowTestNode(w1, 2)
	got, err := createWindowPaths(newUpperRels(), []*WindowAgg{w1, w2}, in, DefaultPlannerSettings(), 0, nil, nil)
	if err != nil {
		t.Fatalf("createWindowPaths: %v", err)
	}
	outer, ok := got.(*WindowAgg)
	if !ok {
		t.Fatalf("emitted %T, want *WindowAgg", got)
	}
	if outer == w2 {
		t.Fatal("producer returned the input spec itself; the arm must emit a fresh copy")
	}
	if len(outer.Funcs) != len(w2.Funcs) || outer.pos != w2.pos || len(outer.PartitionBy) != len(w2.PartitionBy) {
		t.Fatal("outer window spec was not carried across")
	}
	inner, ok := outer.Child.(*WindowAgg)
	if !ok {
		t.Fatalf("outer child is %T, want the inner *WindowAgg — a node was introduced between them", outer.Child)
	}
	if len(inner.Funcs) != len(w1.Funcs) {
		t.Fatal("inner window spec was not carried across")
	}
	// R6 (plan-parity-fix-take2): the producer now stacks the Sort PG's
	// `create_one_window_path` stacks, so the bottom window's child is that
	// Sort rather than the input directly. The invariant this walk exists to
	// protect is unchanged and still checked one level down — the producer
	// must carry the pre-producer input through by IDENTITY, never substitute
	// or rebuild it. Only the depth moved.
	bottomSort, ok := inner.Child.(*Sort)
	if !ok {
		t.Fatalf("bottom child is %T, want the *Sort the window producer stacks", inner.Child)
	}
	if bottomSort.Child != Node(in) {
		t.Fatalf("under the Sort the child is %T/%p, want the pre-producer input %p — the producer substituted a node",
			bottomSort.Child, bottomSort.Child, in)
	}
	// The Sort must be the window's OWN ordering, not an arbitrary one.
	if want := windowSortKeys(w1); !sortKeysEqual(bottomSort.Keys, want) {
		t.Fatalf("stacked Sort keys %v do not match the window's PARTITION BY ++ ORDER BY %v",
			bottomSort.Keys, want)
	}
	// And exactly ONE sort for the chain: the upper window shares the
	// ordering, so it must not re-sort (PG's pathkeys-already-satisfied
	// rule). Asserted by the `inner` type check above plus this flag.
	if !outer.Presorted || !inner.Presorted {
		t.Fatal("both windows must be marked Presorted; the executor would sort a second time")
	}
}

// setOpTestNode builds a set-op over two priced branches.
func setOpTestNode(op parser.SetOpType, all bool, l, r Node) *SetOp {
	return &SetOp{pos: 11, Left: l, Right: r, Op: op, All: all}
}

// TestSizeSetOpRelFromNode pins the §4.3 duty on the SETOP rel, with Rows
// single-sourced from `estimateSetOp` so the rel and every legacy reader
// agree.
func TestSizeSetOpRelFromNode(t *testing.T) {
	l := upperOrderedInput(1000)
	r := upperOrderedInput(400)
	node := setOpTestNode(parser.SetOpUnion, true, l, r)
	rel := &RelOptInfo{}
	sizeSetOpRelFromNode(rel, node)
	if rel.Rows < 1 {
		t.Fatalf("SETOP rel Rows = %v, want >= 1", rel.Rows)
	}
	if rel.NCols != len(node.Output()) || rel.NCols == 0 {
		t.Fatalf("SETOP rel NCols = %d, want %d", rel.NCols, len(node.Output()))
	}
	if rel.Width <= 0 {
		t.Fatalf("SETOP rel Width = %d, want > 0", rel.Width)
	}
}

// TestSetOpStreamsMatchesTheExecutorsPredicate is the sibling-paths pin
// (rule #2): `costSetOp` charges the streaming arm exactly when
// `newSetOp` runs the streaming form (`operators_setop.go`:
// `p.All && p.Op == parser.SetOpUnion`). If the executor's predicate is
// widened and this one is not, a blocking node is priced as a streaming one.
func TestSetOpStreamsMatchesTheExecutorsPredicate(t *testing.T) {
	cases := []struct {
		op   parser.SetOpType
		all  bool
		want bool
	}{
		{parser.SetOpUnion, true, true},
		{parser.SetOpUnion, false, false},
		{parser.SetOpIntersect, true, false},
		{parser.SetOpIntersect, false, false},
		{parser.SetOpExcept, true, false},
		{parser.SetOpExcept, false, false},
	}
	for _, c := range cases {
		got := setOpStreams(&SetOp{Op: c.op, All: c.all})
		if got != c.want {
			t.Fatalf("setOpStreams(op=%v all=%v) = %v, want %v", c.op, c.all, got, c.want)
		}
	}
}

// TestCostSetOpIsCostAppendAndCreateSetopPath prices both arms through the
// NAMED constants: `cost_append` (costsize.c:2250) for the streaming form,
// `create_setop_path`'s hashed arm (pathnode.c:3849) for the buffered one.
func TestCostSetOpIsCostAppendAndCreateSetopPath(t *testing.T) {
	cp := defaultCostParams()
	const (
		lStart, lTotal, lRows = 5.0, 100.0, 1000.0
		rStart, rTotal, rRows = 3.0, 40.0, 400.0
		outRows               = 1400.0
		ncols                 = 3
	)

	stream := costSetOp(cp, true, lStart, lTotal, lRows, rStart, rTotal, rRows, outRows, ncols)
	if stream.Startup != lStart {
		t.Fatalf("streaming startup = %v, want the left branch's %v (cost_append)", stream.Startup, lStart)
	}
	if stream.Total != lTotal+rTotal {
		t.Fatalf("streaming total = %v, want %v (sum of subpath totals, no per-row term)", stream.Total, lTotal+rTotal)
	}

	buffered := costSetOp(cp, false, lStart, lTotal, lRows, rStart, rTotal, rRows, outRows, ncols)
	wantStartup := lTotal + rTotal + cp.cpuOperatorCost*(lRows+rRows)*ncols
	wantTotal := wantStartup + cp.cpuOperatorCost*outRows
	if math.Abs(buffered.Startup-wantStartup) > 1e-9 {
		t.Fatalf("buffered startup = %v, want %v", buffered.Startup, wantStartup)
	}
	if math.Abs(buffered.Total-wantTotal) > 1e-9 {
		t.Fatalf("buffered total = %v, want %v", buffered.Total, wantTotal)
	}
	// The buffered form charges cpu_operator_cost per output row, NOT
	// cpu_tuple_cost: "SetOp does no qual-checking or projection"
	// (pathnode.c:3862). Guard the constant that was picked.
	if cp.cpuOperatorCost == cp.cpuTupleCost {
		t.Skip("cpu_operator_cost == cpu_tuple_cost; the arms are indistinguishable")
	}
	if math.Abs(buffered.Total-(wantStartup+cp.cpuTupleCost*outRows)) < 1e-9 {
		t.Fatal("buffered total used cpu_tuple_cost per output row; upstream charges cpu_operator_cost")
	}
}

// TestAddSetOpPathsSingleCandidate: goopg's `setOp` has one executor form per
// node, so the rel gets exactly one candidate — and it is the one matching
// the node's own form.
func TestAddSetOpPathsSingleCandidate(t *testing.T) {
	cp := defaultCostParams()
	l := upperOrderedInput(1000)
	r := upperOrderedInput(400)
	for _, c := range []struct {
		name string
		node *SetOp
	}{
		{"union all", setOpTestNode(parser.SetOpUnion, true, l, r)},
		{"union distinct", setOpTestNode(parser.SetOpUnion, false, l, r)},
		{"except", setOpTestNode(parser.SetOpExcept, false, l, r)},
	} {
		u := newUpperRels()
		rel := newUpperRelForNode(u, UpperSetOp, 0)
		sizeSetOpRelFromNode(rel, c.node)
		lseed := seedPathForNode(rel, l)
		rseed := seedPathForNode(rel, r)
		addSetOpPaths(rel, lseed, rseed, c.node, cp)
		if len(rel.Pathlist) != 1 {
			t.Fatalf("%s: pathlist holds %d paths, want exactly 1", c.name, len(rel.Pathlist))
		}
		p := rel.Pathlist[0]
		if p.Kind != PathSetOp || p.SetOp != c.node {
			t.Fatalf("%s: path is not the node's own PathSetOp", c.name)
		}
		if len(p.Children) != 2 || p.Children[0] != lseed || p.Children[1] != rseed {
			t.Fatalf("%s: children are not [left, right] in that order", c.name)
		}
	}
}

// TestSetOpRelIsAllocatedPerNode is the regression pin for the wrong-answer
// defect: `A INTERSECT B EXCEPT C` folds two `*SetOp` nodes, and each must get
// its OWN rel. Sharing one relids-0 rel let the second fold's
// `getCheapestFractionalPath` return the FIRST node's path — the executor's
// set-op precedence suite went red on exactly this.
func TestSetOpRelIsAllocatedPerNode(t *testing.T) {
	a := upperOrderedInput(1000)
	b := upperOrderedInput(400)
	c := upperOrderedInput(50)
	u := newUpperRels()
	ps := DefaultPlannerSettings()

	inner, err := createSetOpPaths(u, setOpTestNode(parser.SetOpIntersect, false, a, b), ps, 0)
	if err != nil {
		t.Fatalf("inner: %v", err)
	}
	outer, err := createSetOpPaths(u, setOpTestNode(parser.SetOpExcept, false, inner, c), ps, 0)
	if err != nil {
		t.Fatalf("outer: %v", err)
	}
	if len(u.rels[UpperSetOp]) != 2 {
		t.Fatalf("registry holds %d SETOP rels, want 2 (one per node)", len(u.rels[UpperSetOp]))
	}
	if u.rels[UpperSetOp][0].Relids == u.rels[UpperSetOp][1].Relids {
		t.Fatal("the two SETOP rels share a key; fetchUpperRel would collapse them")
	}
	got, ok := outer.(*SetOp)
	if !ok {
		t.Fatalf("emitted %T, want *SetOp", outer)
	}
	if got.Op != parser.SetOpExcept {
		t.Fatalf("outer emitted Op = %v, want EXCEPT — the inner node's candidate won the outer node's rel", got.Op)
	}
	if got.Left != inner || got.Right != Node(c) {
		t.Fatal("outer branches are not the pre-producer nodes")
	}
}

// TestCreateSetOpPathsEmitsTheSameNodeOverTheSameBranches is the C-10c
// pointer walk for the set-op producer: same Op/All/pos, and the emitted node
// holds EXACTLY the two pre-producer branch nodes with nothing introduced
// between.
func TestCreateSetOpPathsEmitsTheSameNodeOverTheSameBranches(t *testing.T) {
	l := upperOrderedInput(1000)
	r := upperOrderedInput(400)
	in := setOpTestNode(parser.SetOpExcept, true, l, r)
	got, err := createSetOpPaths(newUpperRels(), in, DefaultPlannerSettings(), 0)
	if err != nil {
		t.Fatalf("createSetOpPaths: %v", err)
	}
	out, ok := got.(*SetOp)
	if !ok {
		t.Fatalf("emitted %T, want *SetOp", got)
	}
	if out == in {
		t.Fatal("producer returned the input spec itself; the arm must emit a fresh copy")
	}
	if out.Op != in.Op || out.All != in.All || out.pos != in.pos {
		t.Fatalf("spec not carried across: got op=%v all=%v pos=%d", out.Op, out.All, out.pos)
	}
	if out.Left != Node(l) || out.Right != Node(r) {
		t.Fatal("emitted branches are not the pre-producer nodes; the producer introduced a node")
	}
}

// TestUpperRelRegistryHasEveryKindWiredAfterC18 is the census C-17 turns into
// its verification: a statement shape reaching each producer files a rel of
// that kind. Here it is asserted at the producer level (the planner-level
// census is C-17's).
func TestUpperRelRegistryHasEveryKindWiredAfterC18(t *testing.T) {
	u := newUpperRels()
	in := upperOrderedInput(100)
	ps := DefaultPlannerSettings()
	if _, err := createWindowPaths(u, []*WindowAgg{windowTestNode(in, 1)}, in, ps, 0, nil, nil); err != nil {
		t.Fatalf("window: %v", err)
	}
	if _, err := createSetOpPaths(u, setOpTestNode(parser.SetOpUnion, true, in, in), ps, 0); err != nil {
		t.Fatalf("setop: %v", err)
	}
	if len(u.rels[UpperWindow]) != 1 {
		t.Fatalf("WINDOW rels = %d, want 1", len(u.rels[UpperWindow]))
	}
	if len(u.rels[UpperSetOp]) != 1 {
		t.Fatalf("SETOP rels = %d, want 1", len(u.rels[UpperSetOp]))
	}
}

// TestCreateSetOpPathsThreadsBranchRelsOntoSetOpRel is M0140-0006a's gate:
// each branch's own searched RelOptInfo (`searchedRelOf(setOpNode.Left/
// .Right)`) becomes reachable off the SETOP rel
// (`RelOptInfo.LeftBranchRel`/`RightBranchRel`) once that branch is a
// searched-tree root — and reaching it changes NOTHING about the elected
// plan yet: `addSetOpPaths` still offers the one candidate it always has, so
// `rel.Pathlist` stays exactly what a non-searched pair of branches produces
// (TestAddSetOpPathsSingleCandidate).
func TestCreateSetOpPathsThreadsBranchRelsOntoSetOpRel(t *testing.T) {
	u := newUpperRels()

	leftRel := &RelOptInfo{}
	leftRel.PartialPathlist = []*Path{{Kind: PathSeqScan, Rows: 5, Cost: Cost{Total: 10}}}
	l := &searchedPricedNode{pricedNode: *upperOrderedInput(1000)}
	l.markFromJoinSearch()
	l.setSearchRel(leftRel)

	rightRel := &RelOptInfo{}
	rightRel.PartialPathlist = []*Path{{Kind: PathSeqScan, Rows: 3, Cost: Cost{Total: 7}}}
	r := &searchedPricedNode{pricedNode: *upperOrderedInput(400)}
	r.markFromJoinSearch()
	r.setSearchRel(rightRel)

	node := setOpTestNode(parser.SetOpUnion, true, l, r)
	if _, err := createSetOpPaths(u, node, DefaultPlannerSettings(), 0); err != nil {
		t.Fatalf("createSetOpPaths: %v", err)
	}

	if len(u.rels[UpperSetOp]) != 1 {
		t.Fatalf("SETOP rels = %d, want 1", len(u.rels[UpperSetOp]))
	}
	rel := u.rels[UpperSetOp][0]
	if rel.LeftBranchRel != leftRel {
		t.Fatalf("rel.LeftBranchRel = %p, want the left branch's own search rel %p", rel.LeftBranchRel, leftRel)
	}
	if rel.RightBranchRel != rightRel {
		t.Fatalf("rel.RightBranchRel = %p, want the right branch's own search rel %p", rel.RightBranchRel, rightRel)
	}
	// Plumbing only: the tournament still offers exactly the one candidate.
	if len(rel.Pathlist) != 1 {
		t.Fatalf("rel.Pathlist = %d entries, want 1 — LeftBranchRel/RightBranchRel must not be offered to the tournament yet", len(rel.Pathlist))
	}
}

// TestCreateSetOpPathsLeavesBranchRelsNilForNonSearchedBranches pins the
// negative case: plain Nodes with no searched-tree tag leave the new fields
// at their zero value, exactly like today (no field, no behavior).
func TestCreateSetOpPathsLeavesBranchRelsNilForNonSearchedBranches(t *testing.T) {
	u := newUpperRels()
	l := upperOrderedInput(1000)
	r := upperOrderedInput(400)
	node := setOpTestNode(parser.SetOpUnion, true, l, r)
	if _, err := createSetOpPaths(u, node, DefaultPlannerSettings(), 0); err != nil {
		t.Fatalf("createSetOpPaths: %v", err)
	}
	rel := u.rels[UpperSetOp][0]
	if rel.LeftBranchRel != nil {
		t.Fatalf("rel.LeftBranchRel = %v, want nil for a non-searched branch", rel.LeftBranchRel)
	}
	if rel.RightBranchRel != nil {
		t.Fatalf("rel.RightBranchRel = %v, want nil for a non-searched branch", rel.RightBranchRel)
	}
}

// TestAddPartialSetOpPathPricesLikeCostAppendParallelArm is M0140-0006b's
// cost pin: cost_append's parallel-aware arm (costsize.c:2330-2394),
// specialised to goopg's fixed two-child SetOp shape. Both branches offer a
// partial path with DIFFERENT worker counts, so the rescale term
// (subpath_parallel_divisor / parallel_divisor) is exercised, not just the
// same-worker-count degenerate case.
func TestAddPartialSetOpPathPricesLikeCostAppendParallelArm(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	left := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 500, Cost: Cost{Startup: 1, Total: 50},
		ParallelSafe: true, ParallelWorkers: 2,
	}}}
	right := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 200, Cost: Cost{Startup: 2, Total: 20},
		ParallelSafe: true, ParallelWorkers: 3,
	}}}
	setOpRel.LeftBranchRel = left
	setOpRel.RightBranchRel = right
	// Searched marks make each branch's boundary chain admissible to a
	// partial pick (setOpBranchPartialChainOK); with both partials cheaper
	// than the seed's TotalCost=100 the mixed arm's pick is partial on both
	// sides, so only the pure arm files — the arm this test prices.
	l := &searchedPricedNode{pricedNode: *upperOrderedInput(1000)}
	l.markFromJoinSearch()
	r := &searchedPricedNode{pricedNode: *upperOrderedInput(400)}
	r.markFromJoinSearch()
	node := setOpTestNode(parser.SetOpUnion, true, l, r)

	addPartialSetOpPath(setOpRel, node, cp, false)

	if !setOpRel.ConsiderParallel {
		t.Fatal("setOpRel.ConsiderParallel = false, want true (both branches consider parallel)")
	}
	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1", len(setOpRel.PartialPathlist))
	}
	p := setOpRel.PartialPathlist[0]
	if p.Kind != PathSetOp || p.SetOp != node {
		t.Fatal("partial path is not the node's own PathSetOp")
	}
	const wantWorkers = 3 // max(2,3), already >= the fixed two-child floor
	if p.ParallelWorkers != wantWorkers {
		t.Fatalf("ParallelWorkers = %d, want %d", p.ParallelWorkers, wantWorkers)
	}
	if p.Cost.Startup != 1 {
		t.Fatalf("Startup = %v, want 1 (min of the two branches' startup)", p.Cost.Startup)
	}
	divisor := getParallelDivisor(wantWorkers, cp.parallelLeaderParticipation)
	lDivisor := getParallelDivisor(2, cp.parallelLeaderParticipation)
	rDivisor := getParallelDivisor(3, cp.parallelLeaderParticipation)
	wantRows := clampRowEst(500*(lDivisor/divisor) + 200*(rDivisor/divisor))
	if math.Abs(p.Rows-wantRows) > 1e-9 {
		t.Fatalf("Rows = %v, want %v", p.Rows, wantRows)
	}
	wantTotal := 50 + 20 + cp.cpuTupleCost*appendCPUCostMultiplier*wantRows
	if math.Abs(p.Cost.Total-wantTotal) > 1e-9 {
		t.Fatalf("Total = %v, want %v", p.Cost.Total, wantTotal)
	}
	if !p.ParallelSafe {
		t.Fatal("ParallelSafe = false, want true")
	}
	if len(p.Children) != 2 || p.Children[0] != left.PartialPathlist[0] || p.Children[1] != right.PartialPathlist[0] {
		t.Fatal("children are not [left partial, right partial] in that order")
	}
}

// TestAddPartialSetOpPathAtLeastTwoWorkersForTwoChildren pins PG's
// enable_parallel_append floor (allpaths.c:1560-1566,
// `pg_leftmost_one_pos32(numChildren=2)+1 == 2`), applied unconditionally
// since goopg has no enable_parallel_append GUC to gate the bump behind
// (matches costSetOp's single-candidate posture).
func TestAddPartialSetOpPathAtLeastTwoWorkersForTwoChildren(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	mk := func(rows float64) *RelOptInfo {
		return &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
			Kind: PathSeqScan, Rows: rows, Cost: Cost{Total: rows / 4},
			ParallelSafe: true, ParallelWorkers: 1,
		}}}
	}
	setOpRel.LeftBranchRel = mk(100)
	setOpRel.RightBranchRel = mk(50)
	// Searched marks admit each branch's partial pick; Total=rows/4<100 keeps
	// both picks partial so the floor is exercised on the pure arm itself.
	l := &searchedPricedNode{pricedNode: *upperOrderedInput(100)}
	l.markFromJoinSearch()
	r := &searchedPricedNode{pricedNode: *upperOrderedInput(50)}
	r.markFromJoinSearch()
	node := setOpTestNode(parser.SetOpUnion, true, l, r)

	addPartialSetOpPath(setOpRel, node, cp, false)

	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1", len(setOpRel.PartialPathlist))
	}
	if got := setOpRel.PartialPathlist[0].ParallelWorkers; got != 2 {
		t.Fatalf("ParallelWorkers = %d, want 2 (both branches request 1, floor is 2)", got)
	}
}

// TestAddPartialSetOpPathCapsAtMaxParallelWorkersPerGather.
func TestAddPartialSetOpPathCapsAtMaxParallelWorkersPerGather(t *testing.T) {
	cp := defaultCostParams()
	cp.maxParallelWorkersPerGather = 1
	setOpRel := &RelOptInfo{}
	mk := func(rows float64, workers int) *RelOptInfo {
		return &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
			Kind: PathSeqScan, Rows: rows, Cost: Cost{Total: rows / 4},
			ParallelSafe: true, ParallelWorkers: workers,
		}}}
	}
	setOpRel.LeftBranchRel = mk(100, 3)
	setOpRel.RightBranchRel = mk(50, 2)
	// Searched marks + strictly-cheaper partials keep both picks partial.
	l := &searchedPricedNode{pricedNode: *upperOrderedInput(100)}
	l.markFromJoinSearch()
	r := &searchedPricedNode{pricedNode: *upperOrderedInput(50)}
	r.markFromJoinSearch()
	node := setOpTestNode(parser.SetOpUnion, true, l, r)

	addPartialSetOpPath(setOpRel, node, cp, false)

	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1", len(setOpRel.PartialPathlist))
	}
	if got := setOpRel.PartialPathlist[0].ParallelWorkers; got != 1 {
		t.Fatalf("ParallelWorkers = %d, want 1 (capped by maxParallelWorkersPerGather)", got)
	}
}

// TestAddPartialSetOpPathRefusesNonStreaming: only UNION ALL is Append-shaped
// (setOpStreams); every other set-op form is the buffered/hashed arm, which
// has no partial-safe executor shape and gets no partial path regardless of
// what the branches offer.
func TestAddPartialSetOpPathRefusesNonStreaming(t *testing.T) {
	cp := defaultCostParams()
	mk := func() *RelOptInfo {
		return &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
			Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 100},
			ParallelSafe: true, ParallelWorkers: 2,
		}}}
	}
	for _, c := range []struct {
		name string
		op   parser.SetOpType
		all  bool
	}{
		{"union distinct", parser.SetOpUnion, false},
		{"intersect all", parser.SetOpIntersect, true},
		{"except all", parser.SetOpExcept, true},
	} {
		setOpRel := &RelOptInfo{}
		setOpRel.LeftBranchRel = mk()
		setOpRel.RightBranchRel = mk()
		node := setOpTestNode(c.op, c.all, upperOrderedInput(100), upperOrderedInput(100))

		addPartialSetOpPath(setOpRel, node, cp, false)

		if len(setOpRel.PartialPathlist) != 0 {
			t.Fatalf("%s: PartialPathlist = %d entries, want 0 (not a streaming UNION ALL)", c.name, len(setOpRel.PartialPathlist))
		}
	}
}

// TestAddPartialSetOpPathRefusesUnderGatherPathsOff.
func TestAddPartialSetOpPathRefusesUnderGatherPathsOff(t *testing.T) {
	restore := setGatherPathsModeForTest(gatherPathsOff)
	defer restore()
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	mk := func() *RelOptInfo {
		return &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
			Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 100},
			ParallelSafe: true, ParallelWorkers: 2,
		}}}
	}
	setOpRel.LeftBranchRel = mk()
	setOpRel.RightBranchRel = mk()
	node := setOpTestNode(parser.SetOpUnion, true, upperOrderedInput(100), upperOrderedInput(100))

	addPartialSetOpPath(setOpRel, node, cp, false)

	if len(setOpRel.PartialPathlist) != 0 {
		t.Fatalf("PartialPathlist = %d entries, want 0 under GOOPG_GATHER_PATHS=off", len(setOpRel.PartialPathlist))
	}
}

// TestAddPartialSetOpPathRefusesWhenABranchDoesNotConsiderParallel mirrors
// the join-rel rule (build_join_rel, relnode.c:842): the conjunction of both
// inputs, not just one.
func TestAddPartialSetOpPathRefusesWhenABranchDoesNotConsiderParallel(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	setOpRel.LeftBranchRel = &RelOptInfo{ConsiderParallel: false, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 100}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	setOpRel.RightBranchRel = &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 100}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	node := setOpTestNode(parser.SetOpUnion, true, upperOrderedInput(100), upperOrderedInput(100))

	addPartialSetOpPath(setOpRel, node, cp, false)

	if setOpRel.ConsiderParallel {
		t.Fatal("setOpRel.ConsiderParallel = true, want false (left branch refuses)")
	}
	if len(setOpRel.PartialPathlist) != 0 {
		t.Fatalf("PartialPathlist = %d entries, want 0", len(setOpRel.PartialPathlist))
	}
}

// TestAddPartialSetOpPathRefusesWhenABranchHasNoPartialPath: a branch can
// consider parallel yet still offer no partial path at all (e.g. its own
// search never built one). The PURE arm still refuses that shape — it needs
// BOTH branches to have one — but since M0140-0006c-3 the MIXED arm accepts
// it: the partial-less branch is claimed whole by one participant (PG's
// pa_nonpartial_subpaths), exactly the shape this arm exists for. The
// branches carry searched marks so the right branch's cheaper partial is a
// real pick, not a chain refusal.
func TestAddPartialSetOpPathRefusesWhenABranchHasNoPartialPath(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	setOpRel.LeftBranchRel = &RelOptInfo{ConsiderParallel: true}
	right := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 50}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	setOpRel.RightBranchRel = right
	l := &searchedPricedNode{pricedNode: *upperOrderedInput(100)}
	l.markFromJoinSearch()
	r := &searchedPricedNode{pricedNode: *upperOrderedInput(100)}
	r.markFromJoinSearch()
	node := setOpTestNode(parser.SetOpUnion, true, l, r)

	addPartialSetOpPath(setOpRel, node, cp, false)

	if !setOpRel.ConsiderParallel {
		t.Fatal("setOpRel.ConsiderParallel = false, want true (both branches consider parallel, regardless of partial paths)")
	}
	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1 (the mixed arm: left claimed whole, right partial)", len(setOpRel.PartialPathlist))
	}
	p := setOpRel.PartialPathlist[0]
	if !p.SetOpLeftNonPartial || p.SetOpRightNonPartial {
		t.Fatalf("markers = (L=%v, R=%v), want (true, false): the partial-less left branch is claimed whole", p.SetOpLeftNonPartial, p.SetOpRightNonPartial)
	}
	if len(p.Children) != 2 || p.Children[1] != right.PartialPathlist[0] {
		t.Fatal("right child is not the branch's partial path")
	}
	if p.Children[0].Kind != PathPrebuilt {
		t.Fatalf("left child kind = %v, want PathPrebuilt (the claimed-whole seed over the branch's serial plan)", p.Children[0].Kind)
	}
}

// TestAddPartialSetOpPathMixedArmSuppressedAtTopLevel pins the placement
// rule (prepunion.c vs allpaths.c): `generate_union_paths` — the path
// builder for a statement-level set operation — files ONLY the pure arm;
// the mixed `pa_subpaths` arm is `add_paths_to_append_rel`'s alone,
// reached for appendrels (flattened FROM-clause/CTE union-alls), never a
// top-level `a UNION ALL b`. goopg's proxy is ps.ParallelStatementOK: the
// SAME shape that files the mixed arm in a nested scope (topLevel=false,
// the test above) must file NOTHING at top level — otherwise a top-level
// setop could emit `Gather > Append` PG's setop pipeline cannot produce
// (the TPC-DS Q66 regression: an all-claimed Parallel Append where PG
// plans a serial Append).
func TestAddPartialSetOpPathMixedArmSuppressedAtTopLevel(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	setOpRel.LeftBranchRel = &RelOptInfo{ConsiderParallel: true}
	right := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 50}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	setOpRel.RightBranchRel = right
	l := searchedSetOpBranch(100)
	r := searchedSetOpBranch(100)
	node := setOpTestNode(parser.SetOpUnion, true, l, r)

	addPartialSetOpPath(setOpRel, node, cp, true)

	if !setOpRel.ConsiderParallel {
		t.Fatal("setOpRel.ConsiderParallel = false, want true — the flag stamps independently of the arms")
	}
	if len(setOpRel.PartialPathlist) != 0 {
		p := setOpRel.PartialPathlist[0]
		t.Fatalf("PartialPathlist = %d entries at topLevel, want 0 — got markers (L=%v, R=%v); "+
			"the mixed arm must not file for a statement-level set operation "+
			"(generate_union_paths has no pa_subpaths arm)", len(setOpRel.PartialPathlist),
			p.SetOpLeftNonPartial, p.SetOpRightNonPartial)
	}
}

// TestAddPartialSetOpPathPureArmStillFilesAtTopLevel is the other half of
// the placement rule: generate_union_paths DOES file the pure arm at top
// level — a statement-level `a UNION ALL b` where every child has a
// partial path legitimately earns `Gather > Append` in PG. topLevel=true
// must therefore suppress only the mixed arm, never the pure one.
func TestAddPartialSetOpPathPureArmStillFilesAtTopLevel(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	pp := func() *Path {
		return &Path{Kind: PathSeqScan, Rows: 50, Cost: Cost{Total: 50}, ParallelSafe: true, ParallelWorkers: 2}
	}
	setOpRel.LeftBranchRel = &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{pp()}}
	setOpRel.RightBranchRel = &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{pp()}}
	l := searchedSetOpBranch(100)
	r := searchedSetOpBranch(100)
	node := setOpTestNode(parser.SetOpUnion, true, l, r)

	addPartialSetOpPath(setOpRel, node, cp, true)

	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries at topLevel with both branches partial, want 1 (the pure arm)", len(setOpRel.PartialPathlist))
	}
	p := setOpRel.PartialPathlist[0]
	if p.SetOpLeftNonPartial || p.SetOpRightNonPartial {
		t.Fatalf("markers = (L=%v, R=%v), want (false, false): a top-level pure-arm path claims no branch whole", p.SetOpLeftNonPartial, p.SetOpRightNonPartial)
	}
}

// searchedSetOpBranch is a searched-marked test branch: the boundary chain
// is empty (the node IS the searched root), so setOpBranchPartialChainOK
// admits its rel's partial path as a pick.
func searchedSetOpBranch(rows float64) *searchedPricedNode {
	n := &searchedPricedNode{pricedNode: *upperOrderedInput(rows)}
	n.markFromJoinSearch()
	return n
}

// TestSetOpBranchPartialChainOK pins the partial-pick admissibility walk:
// a searched rel's partial path can stand in for the branch's own partial
// subpath only when every boundary wrapper above the emission is either
// per-worker-safe and stamp-descendable (*Project, *Filter, *Sort) or
// dropped by the splice with no row effect (*Gather, *GatherMerge).
// Everything else — row-capping *Limit, row-transforming *Distinct/
// *Aggregate, typed-child *Memoize, unsearched nodes — leaves the branch
// with no partial to offer, exactly PG's NULL pick for a wrapped child.
func TestSetOpBranchPartialChainOK(t *testing.T) {
	sr := func() *searchedPricedNode { return searchedSetOpBranch(100) }
	plain := func() Node { return upperOrderedInput(100) }
	cases := []struct {
		name string
		node Node
		want bool
	}{
		{"bare-searched-root", sr(), true},
		{"project-over-searched", &Project{Child: sr()}, true},
		{"filter-over-searched", &Filter{Child: sr()}, true},
		{"sort-over-searched", &Sort{Child: sr()}, true},
		{"gather-over-searched", &Gather{Child: sr()}, true},
		{"gathermerge-over-searched", &GatherMerge{Child: sr()}, true},
		{"nested-wrappers", &Project{Child: &Filter{Child: &Gather{Child: &Sort{Child: sr()}}}}, true},
		{"limit-over-searched", &Limit{Child: sr()}, false},
		{"distinct-over-searched", &Distinct{Child: sr()}, false},
		{"aggregate-over-searched", &Aggregate{Child: sr()}, false},
		{"lockrows-over-searched", &LockRows{Child: sr()}, false},
		{"project-over-limit", &Project{Child: &Limit{Child: sr()}}, false},
		{"memoize-over-searched", &Memoize{Child: &IndexScan{}}, false},
		{"unsearched-leaf", plain(), false},
		{"project-over-unsearched", &Project{Child: plain()}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := setOpBranchPartialChainOK(tc.node); got != tc.want {
				t.Errorf("setOpBranchPartialChainOK = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAddPartialSetOpPathMixedArmPricesLikeCostAppend is the mixed arm's
// cost pin: one branch offers a partial path (strictly cheaper than its
// serial seed), the other offers none and is claimed WHOLE. Cost terms
// from cost_append's parallel-aware arm specialised to this shape
// (costsize.c:2330-2403): partial totals undivided, claimed-whole totals
// through append_nonpartial_cost's makespan, rows = partial-rescaled +
// whole/divisor, workers = max over PARTIAL children's counts floored at 2.
func TestAddPartialSetOpPathMixedArmPricesLikeCostAppend(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	left := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 600, Cost: Cost{Startup: 1, Total: 60},
		ParallelSafe: true, ParallelWorkers: 4,
	}}}
	setOpRel.LeftBranchRel = left
	setOpRel.RightBranchRel = &RelOptInfo{ConsiderParallel: true} // no partial path
	node := setOpTestNode(parser.SetOpUnion, true, searchedSetOpBranch(600), searchedSetOpBranch(300))

	addPartialSetOpPath(setOpRel, node, cp, false)

	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1 (pure arm cannot file — right has no partial)", len(setOpRel.PartialPathlist))
	}
	p := setOpRel.PartialPathlist[0]
	if p.Kind != PathSetOp || p.SetOp != node {
		t.Fatal("filed path is not the node's own PathSetOp")
	}
	if p.SetOpLeftNonPartial || !p.SetOpRightNonPartial {
		t.Fatalf("markers = (L=%v, R=%v), want (false, true)", p.SetOpLeftNonPartial, p.SetOpRightNonPartial)
	}
	const wantWorkers = 4 // max over PARTIAL children (4); claimed-whole contributes nothing
	if p.ParallelWorkers != wantWorkers {
		t.Fatalf("ParallelWorkers = %d, want %d", p.ParallelWorkers, wantWorkers)
	}
	if len(p.Children) != 2 || p.Children[0] != left.PartialPathlist[0] {
		t.Fatal("left child is not the branch's partial path")
	}
	if p.Children[1].Kind != PathPrebuilt {
		t.Fatalf("right child kind = %v, want PathPrebuilt (claimed-whole seed)", p.Children[1].Kind)
	}
	// Startup: min over the first parallel_workers subpaths = both.
	if p.Cost.Startup != 1 {
		t.Fatalf("Startup = %v, want 1 (min of partial's 1 and seed's 10)", p.Cost.Startup)
	}
	divisor := getParallelDivisor(wantWorkers, cp.parallelLeaderParticipation)
	lDivisor := getParallelDivisor(4, cp.parallelLeaderParticipation)
	// left rescales from its own divisor; right's whole-branch rows divide
	// by the Append divisor (one participant emits it all).
	wantRows := clampRowEst(600*(lDivisor/divisor) + 300/divisor)
	if math.Abs(p.Rows-wantRows) > 1e-9 {
		t.Fatalf("Rows = %v, want %v", p.Rows, wantRows)
	}
	// Total: partial's 60 + makespan over {seed's 100} + per-tuple overhead.
	wantTotal := 60 + 100 + cp.cpuTupleCost*appendCPUCostMultiplier*wantRows
	if math.Abs(p.Cost.Total-wantTotal) > 1e-9 {
		t.Fatalf("Total = %v, want %v", p.Cost.Total, wantTotal)
	}
	if !p.ParallelSafe {
		t.Fatal("ParallelSafe = false, want true")
	}
}

// TestAddPartialSetOpPathMixedArmTieLandsInNonPartial pins PG's strict `<`:
// a partial path whose total TIES the branch's serial plan does NOT win —
// the branch joins pa_nonpartial_subpaths (allpaths.c:1424-1426 compares
// `partial->total_cost < nppath->total_cost`, strictly).
func TestAddPartialSetOpPathMixedArmTieLandsInNonPartial(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	// upperOrderedInput seeds carry TotalCost=100, so a partial at exactly
	// 100 ties the claimed-whole candidate.
	setOpRel.LeftBranchRel = &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 100}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	setOpRel.RightBranchRel = &RelOptInfo{ConsiderParallel: true}
	node := setOpTestNode(parser.SetOpUnion, true, searchedSetOpBranch(100), searchedSetOpBranch(100))

	addPartialSetOpPath(setOpRel, node, cp, false)

	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1", len(setOpRel.PartialPathlist))
	}
	p := setOpRel.PartialPathlist[0]
	if !p.SetOpLeftNonPartial || !p.SetOpRightNonPartial {
		t.Fatalf("markers = (L=%v, R=%v), want (true, true): a tied partial loses to the serial plan", p.SetOpLeftNonPartial, p.SetOpRightNonPartial)
	}
	if p.Children[0].Kind != PathPrebuilt || p.Children[1].Kind != PathPrebuilt {
		t.Fatal("both children should be claimed-whole seeds on a tie")
	}
}

// TestAddPartialSetOpPathMixedArmAllClaimedWorkersFloor: an all-claimed
// two-child Append still plans 2 workers — the log2(numChildren)+1 bump
// exists precisely for non-partial children (allpaths.c:1596-1603: workers
// come only from partial subpaths, then the two-child floor lifts 0 to 2).
func TestAddPartialSetOpPathMixedArmAllClaimedWorkersFloor(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	setOpRel.LeftBranchRel = &RelOptInfo{ConsiderParallel: true}
	setOpRel.RightBranchRel = &RelOptInfo{ConsiderParallel: true}
	node := setOpTestNode(parser.SetOpUnion, true, searchedSetOpBranch(100), searchedSetOpBranch(100))

	addPartialSetOpPath(setOpRel, node, cp, false)

	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1", len(setOpRel.PartialPathlist))
	}
	p := setOpRel.PartialPathlist[0]
	if got := p.ParallelWorkers; got != 2 {
		t.Fatalf("ParallelWorkers = %d, want 2 (no partial children; the two-child floor)", got)
	}
	// Cost: makespan over two equal seeds (workers >= children) = max, and
	// rows divide both by the append divisor.
	divisor := getParallelDivisor(2, cp.parallelLeaderParticipation)
	wantRows := clampRowEst(100/divisor + 100/divisor)
	if math.Abs(p.Rows-wantRows) > 1e-9 {
		t.Fatalf("Rows = %v, want %v", p.Rows, wantRows)
	}
	wantTotal := 100.0 + cp.cpuTupleCost*appendCPUCostMultiplier*wantRows // max(100,100)
	if math.Abs(p.Cost.Total-wantTotal) > 1e-9 {
		t.Fatalf("Total = %v, want %v", p.Cost.Total, wantTotal)
	}
}

// TestAddPartialSetOpPathMixedArmKillsWhenBranchOffersNeither mirrors PG's
// `pa_subpaths_valid = false`: a branch whose serial plan is not
// parallel-safe (LockRows — workers may not stamp row marks) and which has
// no partial path offers neither pick, so the whole arm dies.
func TestAddPartialSetOpPathMixedArmKillsWhenBranchOffersNeither(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	setOpRel.LeftBranchRel = &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 50}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	setOpRel.RightBranchRel = &RelOptInfo{ConsiderParallel: true}
	node := setOpTestNode(parser.SetOpUnion, true,
		searchedSetOpBranch(100), &LockRows{Child: searchedSetOpBranch(100)})

	addPartialSetOpPath(setOpRel, node, cp, false)

	if len(setOpRel.PartialPathlist) != 0 {
		t.Fatalf("PartialPathlist = %d entries, want 0 (right branch offers neither pick)", len(setOpRel.PartialPathlist))
	}
}

// TestAddPartialSetOpPathMixedArmInheritsPureRows pins the `partial_rows`
// override (allpaths.c:1594-1627 → pathnode.c:1417-1419): when the pure arm
// also filed, the mixed path takes ITS row estimate — the Append emits the
// same multiset either way.
func TestAddPartialSetOpPathMixedArmInheritsPureRows(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	left := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 500, Cost: Cost{Total: 50}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	// Right's partial is DEARER than its serial seed (200 > 100): the pure
	// arm files on it, but the mixed arm's pick lands claimed-whole.
	right := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 300, Cost: Cost{Total: 200}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	setOpRel.LeftBranchRel = left
	setOpRel.RightBranchRel = right
	node := setOpTestNode(parser.SetOpUnion, true, searchedSetOpBranch(500), searchedSetOpBranch(300))

	addPartialSetOpPath(setOpRel, node, cp, false)

	// The mixed path is cheaper than the pure one (50+makespan vs 50+200),
	// so pruning leaves exactly the mixed entry — carrying the pure arm's
	// rows as its own estimate.
	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1 (mixed pruned the dearer pure arm)", len(setOpRel.PartialPathlist))
	}
	p := setOpRel.PartialPathlist[0]
	if p.SetOpLeftNonPartial || !p.SetOpRightNonPartial {
		t.Fatalf("markers = (L=%v, R=%v), want (false, true)", p.SetOpLeftNonPartial, p.SetOpRightNonPartial)
	}
	pureRows := clampRowEst(500 + 300) // same workers both sides: rescale is identity
	if math.Abs(p.Rows-pureRows) > 1e-9 {
		t.Fatalf("Rows = %v, want the pure arm's %v (partial_rows override)", p.Rows, pureRows)
	}
}

// TestAddPartialSetOpPathMixedArmStripsGatherFromClaimedBranch pins the
// nppath rule PG encodes in `get_cheapest_parallel_safe_total_inner` +
// `create_gather_path` (`parallel_safe = false`): a branch whose serial
// winner is a Gather still offers a claimed-whole plan — its serial form,
// with the gather stripped — never the Gather itself.
func TestAddPartialSetOpPathMixedArmStripsGatherFromClaimedBranch(t *testing.T) {
	cp := defaultCostParams()
	setOpRel := &RelOptInfo{}
	setOpRel.LeftBranchRel = &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 100, Cost: Cost{Total: 50}, ParallelSafe: true, ParallelWorkers: 2,
	}}}
	setOpRel.RightBranchRel = &RelOptInfo{ConsiderParallel: true}
	// Right's serial plan won an inner Gather: the searched root still sits
	// under it, so the branch's own partial path remains admissible — but
	// with no partial on the rel, the pick must be the gather-free serial
	// seed, not a claimed-whole copy of the Gather itself.
	gathered := &Gather{Child: searchedSetOpBranch(100)}
	node := setOpTestNode(parser.SetOpUnion, true, searchedSetOpBranch(100), gathered)

	addPartialSetOpPath(setOpRel, node, cp, false)

	if len(setOpRel.PartialPathlist) != 1 {
		t.Fatalf("PartialPathlist = %d entries, want 1", len(setOpRel.PartialPathlist))
	}
	p := setOpRel.PartialPathlist[0]
	if p.SetOpLeftNonPartial || !p.SetOpRightNonPartial {
		t.Fatalf("markers = (L=%v, R=%v), want (false, true)", p.SetOpLeftNonPartial, p.SetOpRightNonPartial)
	}
	seed := p.Children[1]
	if seed.Kind != PathPrebuilt {
		t.Fatalf("right child kind = %v, want PathPrebuilt", seed.Kind)
	}
	built, _ := createPlanNode(seed)
	if _, isGather := built.(*Gather); isGather {
		t.Fatal("claimed-whole child built the Gather itself — a worker cannot run a nested gather")
	}
	if built != Node(gathered.Child) {
		t.Fatal("claimed-whole child is not the branch's gather-free serial subtree")
	}
}

// TestCreateSetOpPathsPartialPathDoesNotMoveThePlan is the end-to-end
// acceptance createSetOpPaths itself must satisfy: even with both branches
// offering a cheap partial path, the emitted node and the serial tournament
// winner are byte-identical. Since M0140-0006b-2 the PartialPathlist HAS a
// reader (generateUpperRelGatherPaths, called below addSetOpPaths), so a
// Gather IS now generated over the partial SetOp — but the serial seed costs
// (100+100) dominate the Gather price (partial ~17 + parallel_setup_cost),
// so add_path prunes it and the winner is unchanged. If this Pathlist count
// ever rises to 2, a Gather survived pruning: check values (the M0140-0006c
// claim-set) and categories before celebrating, per R3.
func TestCreateSetOpPathsPartialPathDoesNotMoveThePlan(t *testing.T) {
	u := newUpperRels()

	leftRel := &RelOptInfo{ConsiderParallel: true}
	leftRel.PartialPathlist = []*Path{{Kind: PathSeqScan, Rows: 5, Cost: Cost{Total: 10}, ParallelSafe: true, ParallelWorkers: 2}}
	l := &searchedPricedNode{pricedNode: *upperOrderedInput(1000)}
	l.markFromJoinSearch()
	l.setSearchRel(leftRel)

	rightRel := &RelOptInfo{ConsiderParallel: true}
	rightRel.PartialPathlist = []*Path{{Kind: PathSeqScan, Rows: 3, Cost: Cost{Total: 7}, ParallelSafe: true, ParallelWorkers: 2}}
	r := &searchedPricedNode{pricedNode: *upperOrderedInput(400)}
	r.markFromJoinSearch()
	r.setSearchRel(rightRel)

	node := setOpTestNode(parser.SetOpUnion, true, l, r)
	got, err := createSetOpPaths(u, node, DefaultPlannerSettings(), 0)
	if err != nil {
		t.Fatalf("createSetOpPaths: %v", err)
	}

	rel := u.rels[UpperSetOp][0]
	if len(rel.PartialPathlist) != 1 {
		t.Fatalf("rel.PartialPathlist = %d entries, want 1 (the producer under test)", len(rel.PartialPathlist))
	}
	if len(rel.Pathlist) != 1 {
		t.Fatalf("rel.Pathlist = %d entries, want 1 — a partial path must not be offered to the serial tournament", len(rel.Pathlist))
	}
	out, ok := got.(*SetOp)
	if !ok || out.Op != parser.SetOpUnion || !out.All {
		t.Fatalf("emitted node changed shape: %#v", got)
	}
	if out.Left != Node(l) || out.Right != Node(r) {
		t.Fatal("emitted branches are not the pre-producer nodes; the partial candidate must not have won")
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithTwoScanBranches pins M0140-0006c's
// admission test: a PathSetOp whose two branches are both bare scans is
// gatherable, and the two branches' Path pointers are exactly Children[0]/[1]
// (addPartialSetOpPath's own shape), not re-derived.
func TestPartialPathDrivingKindAcceptsSetOpWithTwoScanBranches(t *testing.T) {
	left := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	right := &Path{Kind: PathIndexScan, ParallelSafe: true, ParallelWorkers: 2}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{left, right}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp", got)
	}
	if !partialPathShapeIsGatherable(setOp) {
		t.Error("a two-scan-branch partial SetOp is not gatherable; makeGatherPath can never place a Gather over it")
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithBitmapBranch is M0140-0006c-2
// slice C's base case: a bitmap-driven branch is admitted, mirroring the
// top-level arm's unconditional admit — the branch's bitmap op attaches
// through its own leaf claim set, which prebuildBitmap now publishes per
// branch (cs.setOpLeft/setOpRight.pbm). Dormant until a producer files a
// partial bitmap path — none does today, same as the top-level arm.
func TestPartialPathDrivingKindAcceptsSetOpWithBitmapBranch(t *testing.T) {
	left := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	right := &Path{Kind: PathBitmapHeapScan, ParallelSafe: true, ParallelWorkers: 2}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{left, right}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (bitmap branch admitted)", got)
	}
	if !partialPathShapeIsGatherable(setOp) {
		t.Error("a bitmap-branch partial SetOp is not gatherable; makeGatherPath can never place a Gather over it")
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithBitmapSpine covers the bitmap
// cases the join arms' refusal maps carried before slice C: a bitmap
// probe under a hash join, and a bitmap outer under a merge join and a
// nested loop — each spine admits through whichever arm claims each link.
func TestPartialPathDrivingKindAcceptsSetOpWithBitmapSpine(t *testing.T) {
	scan := func() *Path { return &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2} }
	bm := func() *Path { return &Path{Kind: PathBitmapHeapScan, ParallelSafe: true, ParallelWorkers: 2} }
	branches := map[string]*Path{
		"hash-probe": {
			Kind: PathHashJoin, ParallelSafe: true, ParallelWorkers: 2,
			Children: []*Path{bm(), scan()},
		},
		"merge-outer": {
			Kind: PathMergeJoin, ParallelSafe: true, ParallelWorkers: 2,
			Children: []*Path{bm(), scan()},
		},
		"nl-outer": {
			Kind: PathNestLoop, Jointype: parser.JoinInner,
			ParallelSafe: true, ParallelWorkers: 2,
			Children: []*Path{bm(), {Kind: PathSeqScan}},
		},
		// The recursion propagates admission UP the spine: a merge whose
		// outer is a merge whose own outer is a bitmap admits end to end.
		"nested-merge-outer": {
			Kind: PathMergeJoin, ParallelSafe: true, ParallelWorkers: 2,
			Children: []*Path{
				{Kind: PathMergeJoin, ParallelSafe: true, ParallelWorkers: 2,
					Children: []*Path{bm(), scan()}},
				scan(),
			},
		},
	}
	for name, branch := range branches {
		setOp := &Path{Kind: PathSetOp, Children: []*Path{scan(), branch}}
		if got := partialPathDrivingKind(setOp); got != PathSetOp {
			t.Errorf("%s: partialPathDrivingKind = %v, want PathSetOp (bitmap spine admitted)", name, got)
		}
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithHashJoinBranch pins M0140-0006c-2's
// widening: a hash-join-driven branch is admitted when its probe side is
// itself admittable. The build side needs no admission of its own — the
// leader drains it once via prebuildSharedHashJoins, which now sees through
// a *setOp (collectShareableJoins + HasShareableHashJoin both descend).
func TestPartialPathDrivingKindAcceptsSetOpWithHashJoinBranch(t *testing.T) {
	scanProbe := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	left := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	right := &Path{
		Kind: PathHashJoin, ParallelSafe: true, ParallelWorkers: 2,
		Children: []*Path{scanProbe, {Kind: PathSeqScan}},
	}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{left, right}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (hash branch over scan probe admitted)", got)
	}
	if !partialPathShapeIsGatherable(setOp) {
		t.Error("a hash-branch partial SetOp is not gatherable; makeGatherPath can never place a Gather over it")
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithNestedHashJoinBranch pins the
// recursion: a hash join whose probe side is itself a hash join over scans
// is admitted — the prebuild collects every hash on the probe spine, and a
// hash on a build side is drained serially as part of that build.
func TestPartialPathDrivingKindAcceptsSetOpWithNestedHashJoinBranch(t *testing.T) {
	inner := &Path{
		Kind: PathHashJoin, ParallelSafe: true, ParallelWorkers: 2,
		Children: []*Path{{Kind: PathSeqScan}, {Kind: PathSeqScan}},
	}
	left := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	right := &Path{
		Kind: PathHashJoin, ParallelSafe: true, ParallelWorkers: 2,
		Children: []*Path{inner, {Kind: PathSeqScan}},
	}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{left, right}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (nested hash probe admitted)", got)
	}
}

// TestPartialPathDrivingKindRefusesSetOpWithBadHashJoinBranch pins the
// fail-closed edges of the hash arm: a parameterised or malformed hash
// join. Join-driven branches that ARE admitted (merge since slice B,
// nested loop since slice A, bitmap-driven probes since slice C) live in
// their own acceptance/refusal pairs — the only join kind still refused
// here outright is one no arm claims.
func TestPartialPathDrivingKindRefusesSetOpWithBadHashJoinBranch(t *testing.T) {
	scan := func() *Path { return &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2} }
	cases := map[string]*Path{
		"parameterised-hash": {
			Kind: PathHashJoin, ParallelSafe: true, ParallelWorkers: 2, RequiredOuter: 1,
			Children: []*Path{scan(), scan()},
		},
		"one-child-hash": {
			Kind: PathHashJoin, ParallelSafe: true, ParallelWorkers: 2,
			Children: []*Path{scan()},
		},
		// No "bitmap-probe-hash" case: a bitmap-driven probe is ADMITTED by
		// M0140-0006c-2 slice C — see the bitmap spine acceptance test.
		// No plain "merge-branch" case: a merge over two bare scans is
		// ADMITTED by M0140-0006c-2 slice B (Jointype's zero value is
		// parser.JoinInner, a partial-capable merge jointype) — see the
		// acceptance/refusal pair below. Only guard-violating merge
		// shapes still refuse.
		// No plain "nestloop-branch" case either, for the same reason
		// (slice A).
	}
	for name, branch := range cases {
		setOp := &Path{Kind: PathSetOp, Children: []*Path{scan(), branch}}
		if got := partialPathDrivingKind(setOp); got != PathPrebuilt {
			t.Errorf("%s: partialPathDrivingKind = %v, want PathPrebuilt (refused)", name, got)
		}
	}
}

// TestPartialPathDrivingKindRefusesSetOpWithParameterizedIndexBranch mirrors
// the top-level PathIndexScan arm's own RequiredOuter guard for a SetOp
// branch: no worker can supply an outer parameter.
func TestPartialPathDrivingKindRefusesSetOpWithParameterizedIndexBranch(t *testing.T) {
	left := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	right := &Path{Kind: PathIndexScan, ParallelSafe: true, ParallelWorkers: 2, RequiredOuter: 1}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{left, right}}
	if got := partialPathDrivingKind(setOp); got != PathPrebuilt {
		t.Fatalf("partialPathDrivingKind = %v, want PathPrebuilt (parameterized index branch refused)", got)
	}
}

// TestPartialPathDrivingKindRefusesSetOpWithoutTwoChildren guards the
// Children-length assumption addPartialSetOpPath's own shape guarantees —
// a malformed PathSetOp must fail closed, not index out of range.
func TestPartialPathDrivingKindRefusesSetOpWithoutTwoChildren(t *testing.T) {
	setOp := &Path{Kind: PathSetOp, Children: []*Path{{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}}}
	if got := partialPathDrivingKind(setOp); got != PathPrebuilt {
		t.Fatalf("partialPathDrivingKind = %v, want PathPrebuilt (only 1 child)", got)
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithNestLoopBranch is M0140-0006c-2
// slice A's whole-inner case: a nested-loop-driven branch is admitted when
// the NL carries the same guards partialPathDrivingKind's own PathNestLoop
// arm requires (JoinInner, unparameterized, a V5 outer, and a whole or
// probe inner). nlClassifyFixture/nlClassifyPath build exactly R60's filed
// shape, so this exercises the arm on a producer-realistic path rather than
// a hand-minimal one.
func TestPartialPathDrivingKindAcceptsSetOpWithNestLoopBranch(t *testing.T) {
	joinrel, outer, inner := nlClassifyFixture()
	nlBranch := nlClassifyPath(joinrel, outer, inner, parser.JoinInner)
	left := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{left, nlBranch}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (whole-inner NL branch admitted)", got)
	}
	if !partialPathShapeIsGatherable(setOp) {
		t.Error("an NL-branch partial SetOp is not gatherable; makeGatherPath can never place a Gather over it")
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithNestedNestLoopBranch pins the
// recursion: an NL branch whose OUTER is itself an admitted NL is partial
// through the whole spine — Q76's nested-NL shape (minus its Memoize
// inner, which is M0142-0005a's separate gate).
func TestPartialPathDrivingKindAcceptsSetOpWithNestedNestLoopBranch(t *testing.T) {
	joinrel, outer, inner := nlClassifyFixture()
	innerNL := nlClassifyPath(joinrel, outer, inner, parser.JoinInner)
	nlBranch := &Path{
		Kind: PathNestLoop, Jointype: parser.JoinInner,
		ParallelSafe: true, ParallelWorkers: 2,
		Children: []*Path{innerNL, {Kind: PathSeqScan}},
	}
	left := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{left, nlBranch}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (nested-NL spine admitted)", got)
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithNestLoopIndexProbeBranch is the
// R95 inner shape on a branch: a lateral index probe re-opened per
// worker-local outer row takes no claim of its own, so the branch admits it
// under the same satisfiable-subset re-check the general arm runs.
func TestPartialPathDrivingKindAcceptsSetOpWithNestLoopIndexProbeBranch(t *testing.T) {
	joinrel, outer, inner, a, _ := latClassifyFixture()
	nlBranch := latClassifyPath(joinrel, outer, inner, parser.JoinInner, latParamInner(inner, a))
	left := &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{left, nlBranch}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (index-probe NL branch admitted)", got)
	}
}

// TestPartialPathDrivingKindRefusesSetOpWithBadNestLoopBranch pins every
// guard the new PathNestLoop arm inherits from the general one — the
// fail-closed direction on a branch is serial, same as everywhere else.
func TestPartialPathDrivingKindRefusesSetOpWithBadNestLoopBranch(t *testing.T) {
	scan := func() *Path { return &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2} }
	nl := func(children ...*Path) *Path {
		return &Path{
			Kind: PathNestLoop, Jointype: parser.JoinInner,
			ParallelSafe: true, ParallelWorkers: 2, Children: children,
		}
	}
	cases := map[string]*Path{
		"left-join": {
			Kind: PathNestLoop, Jointype: parser.JoinLeft,
			ParallelSafe: true, ParallelWorkers: 2,
			Children: []*Path{scan(), {Kind: PathSeqScan}},
		},
		"parameterised-nl": {
			Kind: PathNestLoop, Jointype: parser.JoinInner,
			ParallelSafe: true, ParallelWorkers: 2, RequiredOuter: 1,
			Children: []*Path{scan(), {Kind: PathSeqScan}},
		},
		"one-child": nl(scan()),
		"zero-worker-outer": nl(
			&Path{Kind: PathSeqScan, ParallelSafe: true}, &Path{Kind: PathSeqScan}),
		"unsafe-outer": nl(
			&Path{Kind: PathSeqScan, ParallelWorkers: 2}, &Path{Kind: PathSeqScan}),
		"parameterised-outer": nl(
			&Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2, RequiredOuter: 1},
			&Path{Kind: PathSeqScan}),
		"memoize-inner": nl(scan(), &Path{Kind: PathMemoize}),
		"parameterised-seq-inner": nl(scan(),
			&Path{Kind: PathSeqScan, RequiredOuter: 1}),
		"clauseless-probe": nl(scan(),
			&Path{Kind: PathIndexScan, RequiredOuter: 1}),
		// No "bitmap-outer" case: a bitmap-driven outer is ADMITTED by
		// M0140-0006c-2 slice C — see the bitmap spine acceptance test.
		// (Merge-driven outers likewise admit since slice B.)
	}
	for name, branch := range cases {
		setOp := &Path{Kind: PathSetOp, Children: []*Path{scan(), branch}}
		if got := partialPathDrivingKind(setOp); got != PathPrebuilt {
			t.Errorf("%s: partialPathDrivingKind = %v, want PathPrebuilt (refused)", name, got)
		}
	}

	// The R95 probe-subset refusals need the filed-shape fixture (an index
	// probe path carrying IndexClauses plus the join's relid sets): a probe
	// whose requirement no outer rel supplies, and a join whose relid sets
	// are unrecorded, both refuse.
	for name, build := range map[string]func() *Path{
		"unpartitioned": func() *Path {
			jr, o, i, a, _ := latClassifyFixture()
			p := latClassifyPath(jr, o, i, parser.JoinInner, latParamInner(i, a))
			p.OuterRelids = 0
			return p
		},
		"unsatisfiable-probe-req": func() *Path {
			jr, o, i, _, _ := latClassifyFixture()
			return latClassifyPath(jr, o, i, parser.JoinInner, latParamInner(i, relsetOf(2)))
		},
	} {
		setOp := &Path{Kind: PathSetOp, Children: []*Path{scan(), build()}}
		if got := partialPathDrivingKind(setOp); got != PathPrebuilt {
			t.Errorf("%s: partialPathDrivingKind = %v, want PathPrebuilt (refused)", name, got)
		}
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithMergeJoinBranch is M0140-0006c-2
// slice B's base case: a merge-join-driven branch is admitted when the merge
// carries the same guards partialPathDrivingKind's own PathMergeJoin arm
// requires (unparameterized, exactly two children) — partial through the
// OUTER side, so the recursion descends Children[0] only.
func TestPartialPathDrivingKindAcceptsSetOpWithMergeJoinBranch(t *testing.T) {
	scan := func() *Path { return &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2} }
	merge := &Path{
		Kind: PathMergeJoin, ParallelSafe: true, ParallelWorkers: 2,
		Children: []*Path{scan(), scan()},
	}
	setOp := &Path{Kind: PathSetOp, Children: []*Path{scan(), merge}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (merge branch admitted)", got)
	}
}

// TestPartialPathDrivingKindAcceptsSetOpWithNestedMergeJoinBranch proves the
// recursion stays branch-local end to end: a merge whose outer is itself a
// merge (a merge-of-merge spine) is admitted, and a nested loop whose outer
// is a merge rides slice B's arm through slice A's — the spine admits
// through whichever arm claims each link, exactly like
// partialPathDrivingKind's general recursion does at top level.
func TestPartialPathDrivingKindAcceptsSetOpWithNestedMergeJoinBranch(t *testing.T) {
	scan := func() *Path { return &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2} }
	merge := func(children ...*Path) *Path {
		return &Path{Kind: PathMergeJoin, ParallelSafe: true, ParallelWorkers: 2, Children: children}
	}
	innerMerge := merge(scan(), scan())
	outerMerge := merge(innerMerge, scan())
	setOp := &Path{Kind: PathSetOp, Children: []*Path{scan(), outerMerge}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (merge-of-merge spine admitted)", got)
	}

	// Cross-arm spine: an NL whose outer is a merge admits through slice
	// A's arm recursing into slice B's (the case the bad-NL refusal map
	// carried before B landed — the spine is now valid end to end).
	nlOverMerge := &Path{
		Kind: PathNestLoop, Jointype: parser.JoinInner,
		ParallelSafe: true, ParallelWorkers: 2,
		Children: []*Path{merge(scan(), scan()), {Kind: PathSeqScan}},
	}
	setOp = &Path{Kind: PathSetOp, Children: []*Path{scan(), nlOverMerge}}
	if got := partialPathDrivingKind(setOp); got != PathSetOp {
		t.Fatalf("partialPathDrivingKind = %v, want PathSetOp (NL-over-merge spine admitted)", got)
	}
}

// TestPartialPathDrivingKindRefusesSetOpWithBadMergeJoinBranch pins the
// fail-closed edges of the merge arm: a parameterised or malformed merge
// refuses outright, and a nil outer propagates the refusal up the spine.
// (A bitmap-driven outer is ADMITTED since slice C — leaf claim sets now
// carry their own pbm via per-branch prebuildBitmap publication; the
// spine cases live in the bitmap acceptance test.)
func TestPartialPathDrivingKindRefusesSetOpWithBadMergeJoinBranch(t *testing.T) {
	scan := func() *Path { return &Path{Kind: PathSeqScan, ParallelSafe: true, ParallelWorkers: 2} }
	merge := func(children ...*Path) *Path {
		return &Path{Kind: PathMergeJoin, ParallelSafe: true, ParallelWorkers: 2, Children: children}
	}
	cases := map[string]*Path{
		"parameterised-merge": {
			Kind: PathMergeJoin, ParallelSafe: true, ParallelWorkers: 2, RequiredOuter: 1,
			Children: []*Path{scan(), scan()},
		},
		"one-child-merge":  merge(scan()),
		"zero-child-merge": merge(),
		"nil-outer":        merge(nil, scan()),
	}
	for name, branch := range cases {
		setOp := &Path{Kind: PathSetOp, Children: []*Path{scan(), branch}}
		if got := partialPathDrivingKind(setOp); got != PathPrebuilt {
			t.Errorf("%s: partialPathDrivingKind = %v, want PathPrebuilt (refused)", name, got)
		}
	}
}
