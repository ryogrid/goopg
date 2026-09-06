package optimizer

// M0127-P5.9-a — the joinlist consumer (relfromjoinlist.go).
//
// The recursion is LIVE in production since M0127-P5.9 (2026-08-06):
// `GOOPG_PGSHAPED_DP` defaults ON and `planSelect` calls the search, so these
// tests are no longer its only observer. Two properties
// carry the file:
//
//  1. whatever the joinlist's shape, the tree handed back publishes the
//     PRE-SEARCH BINDING concatenation — asserted on column NAMES, because an
//     index-only assertion passes for a plan that reads the wrong column; and
//  2. a sub-joinlist is a SEPARATE search problem whose relations are joined to
//     each other before anything outside it — the pin `deconstructJointree`
//     produces for an outer join, and for an explicit JOIN with
//     `GOOPG_PGSHAPED_COLLAPSE` off.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// rfjWidth is the column count every fixture relation has. Two is the smallest
// width that makes a permutation visible: with one column per relation an
// off-by-one map and a correct one are indistinguishable at the boundary.
const rfjWidth = 2

// rfjProblem builds an n-relation problem, one FROM item per name, each a
// `*SeqScan` of `rfjWidth` columns named `<name>0 <name>1`. Column names are
// globally unique, so an assertion on the published schema names is an
// assertion on which relation's column landed where.
//
// `rows[i]` is FROM item i's cardinality: the fixtures make them differ by
// orders of magnitude, because a search that is free to reorder will do so, and
// a test whose relations are all the same size cannot tell a search that ran
// from one that did not.
func rfjProblem(names []string, rows []int64, conjuncts []Expr) *joinlistProblem {
	n := len(names)
	prob := &joinlistProblem{
		bindings:   make([]rangeBinding, n),
		scans:      make([]Node, n),
		relInfos:   make([]baseRelInfo, n),
		conjuncts:  conjuncts,
		cumOffsets: make([]int, n+1),
		cp:         defaultCostParams(),
	}
	for i, name := range names {
		schema := cpjSchema(name, rfjWidth)
		cols := make([]catalog.Column, rfjWidth)
		for c := range cols {
			cols[c] = catalog.Column{Name: schema[c].Name, Type: schema[c].Type, Ordinal: c}
		}
		tbl := &catalog.Table{Name: name, Columns: cols}
		prob.bindings[i] = rangeBinding{table: tbl, alias: name, offset: i * rfjWidth, sourceIdx: int16(i)}
		prob.scans[i] = &SeqScan{Table: tbl, Alias: name, schema: schema}
		prob.relInfos[i] = baseRelInfo{
			bindingIdx:   i,
			sourceIdx:    int16(i),
			table:        tbl,
			baseRows:     rows[i],
			filteredRows: rows[i],
		}
		prob.cumOffsets[i] = i * rfjWidth
	}
	prob.cumOffsets[n] = n * rfjWidth
	return prob
}

// rfjEq is an equality between FROM item `a`'s column 0 and FROM item `b`'s
// column 0, written in the binding coordinates the statement's conjuncts are in
// — the same space `relidsOfExpr` reads.
func rfjEq(names []string, a, b int) Expr {
	return &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Name: names[a] + "0", Index: a * rfjWidth, SourceTableIdx: int16(a)},
		Right: &ColumnRef{Name: names[b] + "0", Index: b * rfjWidth, SourceTableIdx: int16(b)},
	}
}

// rfjBindingOrder is the pre-search concatenation's column names.
func rfjBindingOrder(names []string) []string {
	out := make([]string, 0, len(names)*rfjWidth)
	for _, name := range names {
		for _, c := range cpjSchema(name, rfjWidth) {
			out = append(out, c.Name)
		}
	}
	return out
}

func rfjAssertBindingOrder(t *testing.T, n Node, names []string) {
	t.Helper()
	want := rfjBindingOrder(names)
	got := schemaNames(n.Output())
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("searched tree publishes %v, want the pre-search concatenation %v", got, want)
	}
}

// rfjLeafCount counts the base inputs under a node: every node that is not a
// join or a unary wrapper counts as one, and a nested-loop index join's probed
// inner relation counts as one too. C-04b uses it to say WHICH side of a
// searched outer join holds the preserved leaf.
func rfjLeafCount(n Node) int {
	switch t := n.(type) {
	case nil:
		return 0
	case *Join:
		return rfjLeafCount(t.Left) + rfjLeafCount(t.Right)
	case *NestedLoopIndexJoin:
		return rfjLeafCount(t.Outer) + 1
	case *Project:
		return rfjLeafCount(t.Child)
	case *Filter:
		return rfjLeafCount(t.Child)
	case *Sort:
		return rfjLeafCount(t.Child)
	default:
		return 1
	}
}

// rfjJoins collects every `*Join` in a tree, deepest-last, so a test can talk
// about "the join below the root" without knowing which side it landed on.
func rfjJoins(n Node) []*Join {
	var out []*Join
	var walk func(Node)
	walk = func(x Node) {
		switch t := x.(type) {
		case nil:
			return
		case *Join:
			out = append(out, t)
			walk(t.Left)
			walk(t.Right)
		case *Project:
			walk(t.Child)
		case *Filter:
			walk(t.Child)
		case *Sort:
			walk(t.Child)
		}
	}
	walk(n)
	return out
}

// TestPlanJoinlistSearchFlatProblemIsOneSearch: a flat joinlist — what a comma
// FROM list always produces, at any width (03 §6) — is ONE search problem, and
// its result is published in binding order.
func TestPlanJoinlistSearchFlatProblemIsOneSearch(t *testing.T) {
	names := []string{"a", "b", "c"}
	prob := rfjProblem(names, []int64{1_000_000, 10, 1000},
		[]Expr{rfjEq(names, 0, 1), rfjEq(names, 1, 2)})

	n, err := planJoinlistSearch(deconstructRangeVars(len(names)), prob)
	if err != nil {
		t.Fatalf("planJoinlistSearch: %v", err)
	}
	rfjAssertBindingOrder(t, n, names)

	// Three relations joined pairwise: two joins, and every one of them a real
	// join rather than a cross product, because both clauses were placed.
	joins := rfjJoins(n)
	if len(joins) != 2 {
		t.Fatalf("searched tree has %d joins, want 2 for a 3-relation problem", len(joins))
	}
	for i, j := range joins {
		if j.Predicate == nil && j.LeftKey == nil {
			t.Errorf("join %d has neither a predicate nor a hash key: the clause list was not placed", i)
		}
	}
}

// TestPlanJoinlistSearchPublishesBindingOrderWhateverTheSearchChose is the
// property (1) test, with the sizes arranged so the search cannot leave the
// relations where the FROM clause put them: `a` is a million rows and `c` is
// ten, so no cost model that works picks `a` as the build side.
func TestPlanJoinlistSearchPublishesBindingOrderWhateverTheSearchChose(t *testing.T) {
	names := []string{"a", "b", "c"}
	prob := rfjProblem(names, []int64{1_000_000, 500_000, 10},
		[]Expr{rfjEq(names, 0, 1), rfjEq(names, 1, 2)})

	n, err := planJoinlistSearch(deconstructRangeVars(len(names)), prob)
	if err != nil {
		t.Fatalf("planJoinlistSearch: %v", err)
	}
	rfjAssertBindingOrder(t, n, names)
	if !isSearchedTree(n) {
		t.Fatalf("root %T is not tagged as a searched tree; the legacy posmap family would walk into it", n)
	}
}

// TestPlanJoinlistSearchPinnedSubproblemIsItsOwnSearch is property (2): the
// shape `deconstructJointree` emits for a pinned join — one item whose sub-list
// holds the two sides — must join those two sides to EACH OTHER, even when the
// sizes make that the expensive order.
//
// The fixture is chosen so a flat 3-relation search would NOT produce this: `b`
// and `c` are tiny and `a` is huge, and the only clauses are a-b and a-c, so an
// unpinned search joins `a` to one of them first. Pinning `[b, c]` forces the
// clause-less b×c product to be built first — a plan a free search would refuse,
// which is exactly why it proves the pin was honoured.
func TestPlanJoinlistSearchPinnedSubproblemIsItsOwnSearch(t *testing.T) {
	names := []string{"a", "b", "c"}
	prob := rfjProblem(names, []int64{1_000_000, 10, 20},
		[]Expr{rfjEq(names, 0, 1), rfjEq(names, 0, 2)})

	// `a, (b JOIN c)` with the join pinned: one leaf item, then one sub item
	// whose own list is upstream's `list_make2(l, r)`.
	jl := joinlist{leafItem(0), subItem(joinlist{leafItem(1), leafItem(2)})}

	n, err := planJoinlistSearch(jl, prob)
	if err != nil {
		t.Fatalf("planJoinlistSearch: %v", err)
	}
	rfjAssertBindingOrder(t, n, names)

	joins := rfjJoins(n)
	if len(joins) != 2 {
		t.Fatalf("searched tree has %d joins, want 2", len(joins))
	}
	// The deeper join is the sub-problem's: its own row is b's columns and c's
	// columns and nothing else. Asserting on the SCHEMA rather than on the tree
	// position is what makes this a statement about which relations were joined
	// together, not about which side they landed on.
	sub := joins[len(joins)-1]
	want := rfjBindingOrder([]string{"b", "c"})
	got := schemaNames(sub.Output())
	if len(got) != len(want) {
		t.Fatalf("sub-problem publishes %v, want b and c only (%v)", got, want)
	}
	for _, name := range want {
		if !strings.Contains(strings.Join(got, ","), name) {
			t.Fatalf("sub-problem publishes %v, want it to hold %s", got, name)
		}
	}
	if strings.Contains(strings.Join(got, ","), "a0") {
		t.Fatalf("sub-problem publishes %v: relation `a` crossed the pin", got)
	}

	// The control arm, without which the assertions above would pass for a
	// recursion that ignored the joinlist entirely: the SAME fixture, searched
	// flat, must not choose b×c — it has no clause and both alternatives do.
	free := rfjProblem(names, []int64{1_000_000, 10, 20},
		[]Expr{rfjEq(names, 0, 1), rfjEq(names, 0, 2)})
	freeTree, err := planJoinlistSearch(deconstructRangeVars(len(names)), free)
	if err != nil {
		t.Fatalf("control: planJoinlistSearch: %v", err)
	}
	freeJoins := rfjJoins(freeTree)
	deepest := schemaNames(freeJoins[len(freeJoins)-1].Output())
	if len(deepest) == len(want) && !strings.Contains(strings.Join(deepest, ","), "a0") {
		t.Fatalf("control: the unpinned search also built b×c (%v); the pin assertion proves nothing", deepest)
	}
}

// TestPlanJoinlistSearchSingleItemIsThePreSearchLeaf: upstream's
// `levels_needed == 1` arm (allpaths.c:3399). A one-item joinlist returns the
// item's own rel — no search, no boundary node, and for a leaf item the very
// node the pre-search pipeline built, by identity.
func TestPlanJoinlistSearchSingleItemIsThePreSearchLeaf(t *testing.T) {
	names := []string{"a"}
	prob := rfjProblem(names, []int64{100}, nil)

	n, err := planJoinlistSearch(joinlist{leafItem(0)}, prob)
	if err != nil {
		t.Fatalf("planJoinlistSearch: %v", err)
	}
	if n != prob.scans[0] {
		t.Fatalf("one-item joinlist returned %T, want the pre-search leaf by identity", n)
	}
}

// TestPlanJoinlistSearchNestedPinUnwraps: the pinned shape upstream actually
// emits is `[[l, r]]` — a ONE-item joinlist whose only member is a sub-list of
// two sub-lists (collapse.go's `combineJoinlists`). The extra level is inert in
// PG and must be inert here: the recursion unwraps it to the same 2-relation
// search a flat `[l, r]` would run.
func TestPlanJoinlistSearchNestedPinUnwraps(t *testing.T) {
	names := []string{"a", "b"}
	mk := func() *joinlistProblem {
		return rfjProblem(names, []int64{1000, 10}, []Expr{rfjEq(names, 0, 1)})
	}

	nested, err := planJoinlistSearch(
		joinlist{subItem(joinlist{subItem(joinlist{leafItem(0)}), subItem(joinlist{leafItem(1)})})}, mk())
	if err != nil {
		t.Fatalf("nested: %v", err)
	}
	flat, err := planJoinlistSearch(joinlist{leafItem(0), leafItem(1)}, mk())
	if err != nil {
		t.Fatalf("flat: %v", err)
	}
	if a, b := len(rfjJoins(nested)), len(rfjJoins(flat)); a != b {
		t.Fatalf("nested pin built %d joins, flat built %d: the extra level is not inert", a, b)
	}
	rfjAssertBindingOrder(t, nested, names)
}

// TestPlanJoinlistSearchRejectsMalformedInput. Every case here is a producer
// bug that would otherwise yield a plan that RUNS and returns the wrong
// columns — the class 03 §10's boundary exists to make impossible — so the
// recursion must refuse rather than plan around it.
func TestPlanJoinlistSearchRejectsMalformedInput(t *testing.T) {
	names := []string{"a", "b"}
	flat := joinlist{leafItem(0), leafItem(1)}

	cases := []struct {
		name string
		jl   joinlist
		mut  func(*joinlistProblem)
	}{
		{"nil problem", flat, nil},
		{"empty joinlist", joinlist{}, func(*joinlistProblem) {}},
		{"leaf out of range", joinlist{leafItem(0), leafItem(7)}, func(*joinlistProblem) {}},
		{"joinlist misses a FROM item", joinlist{leafItem(0)}, func(*joinlistProblem) {}},
		{"joinlist repeats a FROM item", joinlist{leafItem(0), leafItem(0)}, func(*joinlistProblem) {}},
		{"leaves out of order", joinlist{leafItem(1), leafItem(0)}, func(*joinlistProblem) {}},
		{"slices disagree", flat, func(p *joinlistProblem) { p.scans = p.scans[:1] }},
		{"offsets not ascending", flat, func(p *joinlistProblem) { p.cumOffsets = []int{0, 0, 4} }},
		{"missing terminating offset", flat, func(p *joinlistProblem) { p.cumOffsets = []int{0, 2} }},
		{"leaf without a node", flat, func(p *joinlistProblem) { p.scans[1] = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var prob *joinlistProblem
			if tc.mut != nil {
				prob = rfjProblem(names, []int64{100, 100}, nil)
				tc.mut(prob)
			}
			if _, err := planJoinlistSearch(tc.jl, prob); err == nil {
				t.Fatal("planJoinlistSearch accepted a malformed problem")
			}
		})
	}
}

// TestJoinlistLeafRange pins the contiguity invariant the coordinate arithmetic
// rests on, on the producer's own output: every sub-joinlist
// `deconstructJointree` can emit covers one unbroken run of FROM items.
func TestJoinlistLeafRange(t *testing.T) {
	// `a, b JOIN c, d` with the JOIN pinned — the production shape while
	// `GOOPG_PGSHAPED_COLLAPSE` is off.
	jl := joinlist{leafItem(0), subItem(joinlist{leafItem(1), leafItem(2)}), leafItem(3)}
	if lo, hi, ok := jl.leafRange(); !ok || lo != 0 || hi != 4 {
		t.Fatalf("leafRange = (%d,%d,%v), want (0,4,true)", lo, hi, ok)
	}
	if lo, hi, ok := jl[1].sub.leafRange(); !ok || lo != 1 || hi != 3 {
		t.Fatalf("sub leafRange = (%d,%d,%v), want (1,3,true)", lo, hi, ok)
	}
	// An interleaved item is not describable to an enclosing problem at all.
	if _, _, ok := (joinlist{leafItem(0), leafItem(2)}).leafRange(); ok {
		t.Fatal("leafRange accepted a joinlist with a hole")
	}
	if _, _, ok := (joinlist{}).leafRange(); ok {
		t.Fatal("leafRange accepted an empty joinlist")
	}
}

// TestDeconstructedJointreeFeedsTheRecursion is the end-to-end seam between
// P5.8 and this task: the joinlist the PRODUCER emits for a real FROM clause
// plans without any adjustment, in both collapse regimes, and publishes binding
// order either way. That the two regimes may pick DIFFERENT trees is the point
// of the flag; that both publish the same row is the contract.
func TestDeconstructedJointreeFeedsTheRecursion(t *testing.T) {
	names := []string{"a", "b", "c"}
	// `FROM a, b JOIN c ON b.b0 = c.c0` with a second clause reaching out of
	// the explicit JOIN, so both regimes have work to place.
	from := parseFrom(t, "a, b JOIN c ON b.b0 = c.c0")
	for _, collapse := range []bool{false, true} {
		jl := deconstructJointree(from, defaultCollapseLimits(), collapse)
		prob := rfjProblem(names, []int64{1_000_000, 10, 1000},
			[]Expr{rfjEq(names, 0, 1), rfjEq(names, 1, 2)})
		n, err := planJoinlistSearch(jl, prob)
		if err != nil {
			t.Fatalf("collapse=%v: planJoinlistSearch: %v", collapse, err)
		}
		rfjAssertBindingOrder(t, n, names)
	}
}
