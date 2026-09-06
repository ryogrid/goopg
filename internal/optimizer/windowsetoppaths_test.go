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

	got := costWindow(cp, inputTotal, rows, numPart, numOrd, numFuncs, inNcols, inAvgVarBytes)
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
	narrow := costWindow(cp, 100, 5e6, 1, 1, 1, 2, 0)
	wide := costWindow(cp, 100, 5e6, 1, 1, 1, 40, 400)
	if !(wide.Total > narrow.Total) {
		t.Fatalf("wide-row window total %v not above narrow-row %v; costSortRun is being fed the key count, not the row width", wide.Total, narrow.Total)
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
	addWindowPaths(rel, seed, []*WindowAgg{w1, w2}, in, cp)

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
	got, err := createWindowPaths(newUpperRels(), []*WindowAgg{w1, w2}, in, DefaultPlannerSettings(), 0)
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
	if inner.Child != Node(in) {
		t.Fatalf("bottom child is %T/%p, want the pre-producer input %p — the producer introduced a node", inner.Child, inner.Child, in)
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
	if _, err := createWindowPaths(u, []*WindowAgg{windowTestNode(in, 1)}, in, ps, 0); err != nil {
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
