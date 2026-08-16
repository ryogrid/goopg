package optimizer

// M0127-P5.9-b — the `planSelect` seam (joinsearchseam.go).
//
// This is the first task in P5 whose subject is REACHABLE from production, so
// the tests split in two: the flag-off arm must be provably inert (the S5
// rollback story of 08 §2 is worth nothing if "off" is only approximately off),
// and the flag-on arm must show the three things the seam decides — which
// statements enter the search, which conjuncts survive it, and what the legacy
// passes may still do to what it built.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// withPGShapedDP forces the flag on for one test and restores it. The flag is
// read once at process start in production (joinsearch.go), which is exactly
// why a test may not rely on the environment for it.
func withPGShapedDP(t *testing.T) {
	t.Helper()
	prev := pgShapedDP
	pgShapedDP = true
	t.Cleanup(func() { pgShapedDP = prev })
}

// seamFixture builds what `planSelect` hands the seam for `FROM n0, n1, …`: a
// left-deep CROSS chain of `*SeqScan` leaves, the bindings in FROM order, and
// the joinlist a comma FROM list produces.
//
// Column names are globally unique (`a0 a1 b0 b1 …`), so every assertion below
// about a published schema is an assertion about WHICH relation's column landed
// where — the only form of that assertion that a wrong-column plan fails.
func seamFixture(names []string, rows []int64) (Node, *resolveContext) {
	var root Node
	bindings := make([]rangeBinding, len(names))
	for i, name := range names {
		schema := cpjSchema(name, rfjWidth)
		cols := make([]catalog.Column, rfjWidth)
		for c := range cols {
			cols[c] = catalog.Column{Name: schema[c].Name, Type: schema[c].Type, Ordinal: c}
		}
		tbl := &catalog.Table{Name: name, Columns: cols, Stats: &catalog.TableStats{RowCount: rows[i]}}
		leaf := &SeqScan{Table: tbl, Alias: name, schema: schema}
		bindings[i] = rangeBinding{table: tbl, alias: name, offset: i * rfjWidth, sourceIdx: int16(i)}
		if root == nil {
			root = leaf
			continue
		}
		root = &Join{Type: JoinTypeCross, Left: root, Right: leaf,
			schema: appendSchema(root.Output(), leaf.Output())}
	}
	ctx := newResolveContext(bindings, root.Output())
	ctx.joinlist = deconstructRangeVars(len(names))
	return root, ctx
}

// seamLocal is a single-relation restriction on FROM item `rel`, in the
// statement's binding coordinates.
func seamLocal(names []string, rel int) Expr {
	return &BinaryOp{
		Op:    parser.OpGt,
		Left:  &ColumnRef{Name: names[rel] + "1", Index: rel*rfjWidth + 1, SourceTableIdx: int16(rel)},
		Right: &IntegerConst{Value: 5},
	}
}

// seamOrOfAnds is the Q19 shape: an OR whose branches share one equality. The
// clause list takes the SHARED equality; the OR itself has to stay in the
// residual `Filter`, because it is not implied by the equality it implies.
func seamOrOfAnds(names []string, a, b int) Expr {
	branch := func(lo bool) Expr {
		op := parser.OpGt
		if !lo {
			op = parser.OpLt
		}
		return &BinaryOp{Op: parser.OpAnd,
			Left: rfjEq(names, a, b),
			Right: &BinaryOp{Op: op,
				Left:  &ColumnRef{Name: names[b] + "1", Index: b*rfjWidth + 1, SourceTableIdx: int16(b)},
				Right: &IntegerConst{Value: 3}}}
	}
	return &BinaryOp{Op: parser.OpOr, Left: branch(true), Right: branch(false)}
}

// TestPGShapedSeamIsInertWithTheFlagOff is the rollback guarantee of 08 §2: with
// `GOOPG_PGSHAPED_DP` off the seam must not merely produce the same plan, it
// must not run at all — the node and the predicate come back by identity.
//
// M0127-P5.9 flipped the default ON, so this test now forces the flag off
// itself instead of asserting the process default. That is not a weakening —
// the guarantee under test was always about the OFF arm, and before the flip
// the process default happened to be the arm it needed. After the flip it is
// the kill-switch arm, and the kill-switch is exactly what must keep working:
// it is S5's whole rollback story until S7 deletes the old DP.
func TestPGShapedSeamIsInertWithTheFlagOff(t *testing.T) {
	prev := pgShapedDP
	pgShapedDP = false
	t.Cleanup(func() { pgShapedDP = prev })
	names := []string{"a", "b", "c"}
	node, ctx := seamFixture(names, []int64{1_000_000, 10, 1000})
	pred := combineAnd([]Expr{rfjEq(names, 0, 1), rfjEq(names, 1, 2)})

	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if used {
		t.Fatal("the seam ran with the flag off")
	}
	if out != node || residual != pred {
		t.Fatal("the seam altered its inputs while declining")
	}
}

// TestPGShapedSeamSearchesAndPublishesBindingOrder: the flag-on path plans the
// whole FROM list as one problem, tags the root so the legacy layout family
// leaves it alone, and republishes the pre-search concatenation (03 §10) — the
// property every expression above the join was resolved against.
func TestPGShapedSeamSearchesAndPublishesBindingOrder(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := seamFixture(names, []int64{1_000_000, 500_000, 10})
	pred := combineAnd([]Expr{rfjEq(names, 0, 1), rfjEq(names, 1, 2)})

	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if !used {
		t.Fatal("the seam declined a plain 3-relation comma FROM list")
	}
	if residual != nil {
		t.Fatalf("residual = %T, want nil: both conjuncts are join clauses the search placed", residual)
	}
	if !isSearchedTree(out) {
		t.Fatalf("root %T is untagged; the legacy posmap passes would walk into it", out)
	}
	rfjAssertBindingOrder(t, out, names)
	if n := len(rfjJoins(out)); n != 2 {
		t.Fatalf("searched tree has %d joins, want 2 for 3 relations", n)
	}
}

// TestPGShapedSeamResidualIsWhatTheSearchDidNotPlace is the task's own
// question. Three conjunct classes, three different destinations, and the test
// asserts all three at once because the failure mode is a conjunct that reaches
// NONE of them — a qual silently dropped is a wrong answer, not a slow plan.
func TestPGShapedSeamResidualIsWhatTheSearchDidNotPlace(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := seamFixture(names, []int64{1_000_000, 500_000, 10})
	local := seamLocal(names, 0)
	or := seamOrOfAnds(names, 0, 2)
	pred := combineAnd([]Expr{rfjEq(names, 0, 1), rfjEq(names, 1, 2), local, or})

	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if !used {
		t.Fatal("the seam declined")
	}
	// (1) The two join equalities were placed by the search and are gone from
	// the residual; (2) the OR survives it, because the clause list took the
	// equality COMMON to its branches and not the OR.
	got := splitAnd(residual)
	if len(got) != 1 || got[0] != or {
		t.Fatalf("residual = %v, want exactly the OR-of-ANDs", got)
	}
	// (3) The single-relation restriction went INTO the leaf, in leaf-local
	// coordinates, and is therefore neither residual nor lost.
	leafLocals := 0
	for _, f := range seamLeafLocalFilters(out) {
		leafLocals++
		if cr, isCol := f.Predicate.(*BinaryOp).Left.(*ColumnRef); isCol && cr.Index != 1 {
			t.Errorf("leaf-local predicate holds index %d, want the leaf-local 1", cr.Index)
		}
	}
	if leafLocals != 1 {
		t.Fatalf("found %d leaf-local filters, want 1 (the `a1 > 5` restriction)", leafLocals)
	}
	rfjAssertBindingOrder(t, out, names)
}

// seamLeaves takes the three `*SeqScan` leaves back out of `seamFixture`'s CROSS
// chain, so a test can rebuild them into a different chain shape without
// duplicating the fixture's catalog/statistics setup.
func seamLeaves(t *testing.T, node Node) (Node, Node, Node) {
	t.Helper()
	top, ok := node.(*Join)
	if !ok {
		t.Fatalf("fixture root is %T, want the CROSS chain", node)
	}
	left, ok := top.Left.(*Join)
	if !ok {
		t.Fatalf("fixture root's left is %T, want the CROSS chain's lower link", top.Left)
	}
	return left.Left, left.Right, top.Right
}

// seamInnerChain builds what `planFromItem` produces for ONE comma-separated
// FROM item written `n0 JOIN n1 ON n0.x = n1.x JOIN n2 ON n1.x = n2.x`: a
// left-deep chain of INNER links, each carrying its own `ON` qual, with the
// joinlist the real producer computes for that FROM clause.
//
// The quals are in the statement's coordinates because the item is the FIRST
// one — which is exactly the case the seam admits, and the reason the fixture
// does not have to model a shift the seam refuses to perform.
func seamInnerChain(t *testing.T, names []string, rows []int64) (Node, *resolveContext) {
	t.Helper()
	node, ctx := seamFixture(names, rows)
	a, b, c := seamLeaves(t, node)
	lower := &Join{Type: JoinTypeInner, Left: a, Right: b,
		schema: appendSchema(a.Output(), b.Output()), Predicate: rfjEq(names, 0, 1)}
	root := &Join{Type: JoinTypeInner, Left: lower, Right: c,
		schema: appendSchema(lower.Output(), c.Output()), Predicate: rfjEq(names, 1, 2)}
	ctx.joinlist = deconstructJointree(
		parseFrom(t, "a JOIN b ON a.a0 = b.b0 JOIN c ON b.b0 = c.c0"),
		defaultCollapseLimits(), pgShapedCollapseEnabled())
	return root, ctx
}

// seamEqualities is the set of equi-join clauses a plan tree actually enforces,
// keyed by COLUMN NAME because the fixture's names are globally unique and a
// searched tree's internal joins are written in the search's own per-joinrel
// layouts, where an index means nothing to a caller.
//
// Both spellings are collected: the residual `Predicate` and the `LeftKey` /
// `RightKey` pair a hash path decomposes the same equality into.
func seamEqualities(n Node) map[string]bool {
	out := map[string]bool{}
	add := func(l, r Expr) {
		lc, lok := l.(*ColumnRef)
		rc, rok := r.(*ColumnRef)
		if !lok || !rok {
			return
		}
		lo, hi := lc.Name, rc.Name
		if lo > hi {
			lo, hi = hi, lo
		}
		out[lo+"="+hi] = true
	}
	for _, j := range rfjJoins(n) {
		if j.LeftKey != nil && j.RightKey != nil {
			add(j.LeftKey, j.RightKey)
		}
		for _, c := range splitAnd(j.Predicate) {
			if bo, isBin := c.(*BinaryOp); isBin && bo.Op == parser.OpEq {
				add(bo.Left, bo.Right)
			}
		}
	}
	return out
}

// TestPGShapedSeamSearchesAnExplicitInnerChain is M0127-P5.9-r's subject: the
// seam admits a FROM clause written `JOIN … ON` and routes the `ON` quals into
// the search's clause list.
//
// The assertion that matters is not "used == true" — it is that every qual is
// still enforced afterwards. The pre-search chain is DISCARDED by the seam (only
// its leaves are carried into the search), so an `ON` qual the walk failed to
// hand over would not be demoted to a slower plan, it would vanish and the
// statement would return the cross product. Hence: both equalities are found in
// the searched tree, the WHERE restriction is found on its leaf, and the
// residual is empty — the three destinations, all three checked at once.
func TestPGShapedSeamSearchesAnExplicitInnerChain(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := seamInnerChain(t, names, []int64{1_000_000, 500_000, 10})

	out, residual, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
	if !used {
		t.Fatal("the seam declined an explicit 3-relation INNER JOIN chain")
	}
	if residual != nil {
		t.Fatalf("residual = %v, want nil: the ON quals are join clauses and the WHERE restriction is leaf-local", residual)
	}
	if !isSearchedTree(out) {
		t.Fatalf("root %T is untagged; the legacy posmap passes would walk into it", out)
	}
	got := seamEqualities(out)
	for _, want := range []string{"a0=b0", "b0=c0"} {
		if !got[want] {
			t.Fatalf("the searched tree does not enforce %s (enforces %v) — an ON qual was dropped, "+
				"which is a cross product, not a slow plan", want, got)
		}
	}
	if n := len(seamLeafLocalFilters(out)); n != 1 {
		t.Fatalf("found %d leaf-local filters, want 1 (the WHERE `a1 > 5` restriction)", n)
	}
	rfjAssertBindingOrder(t, out, names)
	if n := len(rfjJoins(out)); n != 2 {
		t.Fatalf("searched tree has %d joins, want 2 for 3 relations", n)
	}
}

// TestSearchConsumesAsksTheProducer pins the residual rule against
// `buildRestrictInfos` itself, one conjunct class per case. The OR row is the
// one that would be wrong under any re-derived "does it reach two relations"
// test — which is why the rule is a question put to the producer.
func TestSearchConsumesAsksTheProducer(t *testing.T) {
	names := []string{"a", "b", "c"}
	cum := []int{0, rfjWidth, 2 * rfjWidth, 3 * rfjWidth}
	threeRel := &BinaryOp{Op: parser.OpLt,
		Left:  &ColumnRef{Name: "a0", Index: 0, SourceTableIdx: 0},
		Right: &BinaryOp{Op: parser.OpAdd, Left: &ColumnRef{Name: "b0", Index: rfjWidth}, Right: &ColumnRef{Name: "c0", Index: 2 * rfjWidth}}}
	cases := []struct {
		name string
		expr Expr
		want bool
	}{
		{"cross-relation equality", rfjEq(names, 0, 1), true},
		{"non-equality join qual", threeRel, true},
		{"single-relation restriction", seamLocal(names, 0), false},
		{"OR of ANDs", seamOrOfAnds(names, 0, 2), false},
		{"constant", &BooleanConst{Value: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchConsumes(tc.expr, cum); got != tc.want {
				t.Fatalf("searchConsumes = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPGShapedSeamDeclines covers the three shapes the seam refuses, each for a
// correctness reason rather than a tuning one (see the file header of
// joinsearchseam.go).
func TestPGShapedSeamDeclines(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}

	t.Run("lateral item", func(t *testing.T) {
		node, ctx := seamFixture(names, []int64{100, 100, 100})
		// The marker `planFromClause` sets on the CROSS join it builds for a
		// LATERAL FROM item. `extractScans` flattens the chain and cannot see
		// it, which is the whole reason the seam walks for it separately.
		node.(*Join).Lateral = true
		if _, _, used := tryPGShapedJoinSearch(node, rfjEq(names, 0, 1), ctx, nil); used {
			t.Fatal("the seam reordered a FROM list containing a LATERAL item")
		}
	})

	t.Run("leaf count disagrees with binding count", func(t *testing.T) {
		node, ctx := seamFixture(names, []int64{100, 100, 100})
		// `a LEFT JOIN b ON … JOIN c ON …`. M0127-P5.9-r taught the walk to
		// descend INNER links, but an OUTER one still stops it: its qual is
		// not a WHERE qual and reordering across it changes the rows. So the
		// LEFT node comes back as ONE leaf for TWO bindings, the leaf count
		// disagrees with the binding count, and the statement is declined
		// whole — the joinlist's leaf indices would otherwise subscript
		// bindings the leaves do not correspond to.
		chain := node.(*Join)
		chain.Left.(*Join).Type = JoinTypeLeft
		chain.Left.(*Join).Predicate = rfjEq(names, 0, 1)
		chain.Type = JoinTypeInner
		chain.Predicate = rfjEq(names, 1, 2)
		if _, _, used := tryPGShapedJoinSearch(chain, seamLocal(names, 0), ctx, nil); used {
			t.Fatal("the seam searched a FROM list whose leaves it cannot enumerate")
		}
	})

	t.Run("lateral on an explicit JOIN link", func(t *testing.T) {
		node, ctx := seamInnerChain(t, names, []int64{100, 100, 100})
		// `planFromItem` marks the INNER node it builds when the right side
		// references the left (planner.go:2261-2270). The walk flattens that
		// node away, so the marker has to be read from the chain before it is
		// flattened or the search would be free to reorder across it.
		node.(*Join).Lateral = true
		if _, _, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil); used {
			t.Fatal("the seam reordered an explicit JOIN chain with a LATERAL right side")
		}
	})

	t.Run("ON qual on a non-first FROM item", func(t *testing.T) {
		// `FROM a, b JOIN c ON b.b0 = c.c0`: the ON qual was resolved in the
		// SECOND item's own coordinates, where `b0` is index 0, while the
		// statement's space puts it at `rfjWidth`. Re-basing it needs a
		// rewriter that answers "unchanged" for an expression kind it does not
		// know, so the seam declines instead of shifting — see the file header.
		node, ctx := seamFixture(names, []int64{100, 100, 100})
		a, b, c := seamLeaves(t, node)
		item := &Join{Type: JoinTypeInner, Left: b, Right: c,
			schema: appendSchema(b.Output(), c.Output()),
			Predicate: &BinaryOp{Op: parser.OpEq,
				Left:  &ColumnRef{Name: "b0", Index: 0, SourceTableIdx: 1},
				Right: &ColumnRef{Name: "c0", Index: rfjWidth, SourceTableIdx: 2}}}
		root := &Join{Type: JoinTypeCross, Left: a, Right: item,
			schema: appendSchema(a.Output(), item.Output())}
		ctx.joinlist = deconstructJointree(
			parseFrom(t, "a, b JOIN c ON b.b0 = c.c0"), defaultCollapseLimits(), pgShapedCollapseEnabled())
		if _, _, used := tryPGShapedJoinSearch(root, seamLocal(names, 0), ctx, nil); used {
			t.Fatal("the seam searched a chain whose ON qual it cannot re-base")
		}
	})

	t.Run("binding offsets disagree with the leaf widths", func(t *testing.T) {
		node, ctx := seamFixture(names, []int64{100, 100, 100})
		ctx.bindings[2].offset++
		if _, _, used := tryPGShapedJoinSearch(node, rfjEq(names, 0, 1), ctx, nil); used {
			t.Fatal("the seam searched with a coordinate map that is not the leaves' own")
		}
	})

	t.Run("single relation", func(t *testing.T) {
		node, ctx := seamFixture([]string{"a"}, []int64{100})
		if _, _, used := tryPGShapedJoinSearch(node, seamLocal([]string{"a"}, 0), ctx, nil); used {
			t.Fatal("the seam ran a search for one relation")
		}
	})
}

// TestSearchedTreeIsOpaqueToTheLegacyRewrites is 08 §3's coexistence rule for
// the four passes that REWRITE a join tree (the three that renumber one were
// covered by P5.5-f-ii-a). Each of them addresses a tree in the statement's
// FROM-cumulative space; a searched tree's internal joins are not in that
// space, so "leaves it alone" is a correctness property, not a performance one.
func TestSearchedTreeIsOpaqueToTheLegacyRewrites(t *testing.T) {
	names := []string{"a", "b"}
	build := func() Node {
		left := &SeqScan{Table: &catalog.Table{Name: "a"}, Alias: "a", schema: cpjSchema("a", rfjWidth)}
		right := &SeqScan{Table: &catalog.Table{Name: "b"}, Alias: "b", schema: cpjSchema("b", rfjWidth)}
		j := &Join{Type: JoinTypeCross, Left: left, Right: right,
			schema: appendSchema(left.Output(), right.Output())}
		return markSearchedTree(j)
	}

	t.Run("pushPredicatesIntoCrossJoins", func(t *testing.T) {
		searched := build()
		c := rfjEq(names, 0, 1)
		f := &Filter{Child: searched, Predicate: c}
		out := pushPredicatesIntoCrossJoins(f)
		if out != Node(f) || f.Predicate != c {
			t.Fatal("the pass rehomed a conjunct onto a searched join")
		}
		if searched.(*Join).Predicate != nil {
			t.Fatal("the searched join acquired a predicate in FROM-cumulative coordinates")
		}
	})

	// The `rewriteMultiWayChain` subtest was removed with the packer at
	// M0127-P6.2. It asserted the seam's original motivating case: the packer
	// must return a searched tree untouched rather than re-sorting the leaf
	// layout the search had already costed.

	t.Run("rewriteScanInputsWithSingleTablePredicates", func(t *testing.T) {
		searched := build()
		if out := rewriteScanInputsWithSingleTablePredicates(searched, stubCatalogForSeam{}); out != searched {
			t.Fatalf("the scan-input pass rebuilt a searched tree as %T", out)
		}
	})

	t.Run("rewriteJoinsToNLI", func(t *testing.T) {
		searched := build()
		if out := rewriteJoinsToNLI(searched, stubCatalogForSeam{}); out != searched {
			t.Fatalf("the NLI pass re-decided the method on a searched tree (%T)", out)
		}
	})
}

// TestSearchTupleFractionReadsTheParseTree pins `preprocess_limit`'s three
// states as they arrive at the seam: absent, a literal, and present-but-unknown
// (the 10 % punt). The last is the one that must NOT read as absent — a
// `LIMIT $1` plan that assumes all rows are wanted is the regression this
// encoding exists to prevent.
func TestSearchTupleFractionReadsTheParseTree(t *testing.T) {
	parseLimit := func(sql string) (parser.Expr, parser.Expr) {
		t.Helper()
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("parse %q: %v", sql, err)
		}
		sel := stmts[0].(*parser.SelectStmt)
		return sel.Limit, sel.Offset
	}
	cases := []struct {
		sql  string
		want float64
	}{
		{"SELECT * FROM a", 0},
		// `LIMIT NULL` is upstream's spelling of LIMIT ALL once parsed, and it
		// must read as ABSENT rather than as the punt.
		{"SELECT * FROM a LIMIT NULL", 0},
		{"SELECT * FROM a LIMIT 10", 10},
		{"SELECT * FROM a LIMIT 10 OFFSET 5", 15},
		{"SELECT * FROM a OFFSET 5", 0},
		{"SELECT * FROM a LIMIT $1", unestimatableLimitFraction},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			lim, off := parseLimit(tc.sql)
			if got := searchTupleFraction(lim, off); got != tc.want {
				t.Fatalf("searchTupleFraction = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPGShapedSeamSearchesTwoRelationProblems covers the population change the
// seam brings on its own: the old DP declines below three bindings
// (bushy.go:86), PG searches a two-relation problem like any other, and so does
// this. The fraction is non-zero so the run goes through the `ConsiderStartup`
// regime (relnode.c:211) rather than only the fraction == 0 one; what is
// asserted is the boundary contract, because that is what a caller can see —
// the fraction's own arithmetic is pinned by
// `TestSearchTupleFractionReadsTheParseTree` and `tuplefraction_test.go`.
func TestPGShapedSeamSearchesTwoRelationProblems(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b"}
	node, ctx := seamFixture(names, []int64{1_000, 10})
	ctx.tupleFraction = 10
	out, _, used := tryPGShapedJoinSearch(node, rfjEq(names, 0, 1), ctx, nil)
	if !used {
		t.Fatal("the seam declined a 2-relation problem")
	}
	rfjAssertBindingOrder(t, out, names)
	if !strings.Contains(strings.Join(schemaNames(out.Output()), ","), "a0") {
		t.Fatal("the searched tree lost a relation")
	}
}

// stubCatalogForSeam is a catalog that answers nothing, which is all the two
// catalog-taking passes above need: both must decline BEFORE consulting it.
type stubCatalogForSeam struct{ catalog.Catalog }

// seamLeafLocalFilters collects the `LeafLocal` wrappers in a tree — the shape
// a relation's own restrictions take once they are pushed into the leaf.
func seamLeafLocalFilters(n Node) []*Filter {
	var out []*Filter
	var walk func(Node)
	walk = func(x Node) {
		switch t := x.(type) {
		case nil:
			return
		case *Filter:
			if t.LeafLocal {
				out = append(out, t)
			}
			walk(t.Child)
		case *Join:
			walk(t.Left)
			walk(t.Right)
		case *NestedLoopIndexJoin:
			walk(t.Outer)
		case *Project:
			walk(t.Child)
		case *Sort:
			walk(t.Child)
		}
	}
	walk(n)
	return out
}
