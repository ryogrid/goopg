package optimizer

// M0127-P5.9-s — the pinned OUTER spine and the inner prefix below it
// (`splitOuterSpine`, joinsearchseam.go; `pinnedItem` / `pinnedOuter` /
// `innerPrefixBelowOuterSpine`, collapse.go).
//
// Two claims are under test and they fail in opposite directions, so both are
// needed:
//
//   - the peel must WORK, because every explicit-JOIN query in both corpora is
//     topped by an outer link and P5.9-r's INNER walk therefore never fired on a
//     real statement (09 §3.19). Failure here is a search that still does
//     nothing;
//   - the peel must not admit a link it cannot honour, because a searched prefix
//     is a prefix the seam pushes conjuncts INTO. Failure there is a wrong
//     answer: a LEFT JOIN planned as an INNER JOIN, or a `WHERE` qual on a
//     nullable side evaluated before the null-extension that makes it true.
//
// The fixtures therefore assert where each qual ENDED UP, never just `used`.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// seamChainFromSQL builds the pre-search chain AND the joinlist for one
// comma-separated FROM item, both from the SAME parsed FROM clause.
//
// Deriving both from one source is not tidiness here, it is what keeps the test
// non-vacuous: `splitOuterSpine` DECLINES when the plan tree and the joinlist
// disagree about a link, so a hand-built fixture that disagreed with its
// joinlist would not fail these tests — it would make every one of them pass by
// declining.
//
// The SQL text is read for its JOIN TYPES and its shape only; each link gets the
// fixture's own `<names[i]>0 = <names[i+1]>0` equality as its `ON` qual, in the
// statement's binding coordinates, which is what `planFromItem` resolves for a
// FIRST comma FROM item (the only case the seam admits).
func seamChainFromSQL(t *testing.T, names []string, rows []int64, from string) (Node, *resolveContext) {
	t.Helper()
	return seamChainFromSQLWrapped(t, names, rows, from, nil)
}

// seamChainFromSQLWrapped is seamChainFromSQL with each leaf passed through
// `wrap` (nil = identity) before the chain is built over it — C-04b's firewall
// fixture builds Filter-wrapped CTE leaves this way, over the SAME bindings
// and joinlist a base-table chain gets, so the only thing that differs is what
// the leaf classifier sees.
func seamChainFromSQLWrapped(t *testing.T, names []string, rows []int64, from string, wrap func(i int, leaf Node) Node) (Node, *resolveContext) {
	t.Helper()
	base, ctx := seamFixture(names, rows)
	leaves := seamChainLeaves(t, base, len(names))
	if wrap != nil {
		for i := range leaves {
			leaves[i] = wrap(i, leaves[i])
		}
	}
	fromExprs := parseFrom(t, from)
	if len(fromExprs) != 1 {
		t.Fatalf("fixture FROM %q has %d comma items, want 1", from, len(fromExprs))
	}
	if got := len(fromExprs[0].Joins); got != len(names)-1 {
		t.Fatalf("fixture FROM %q has %d JOIN links for %d relations, want %d",
			from, got, len(names), len(names)-1)
	}
	root := leaves[0]
	for i, j := range fromExprs[0].Joins {
		right := leaves[i+1]
		root = &Join{
			Type:      spinePlanJoinType(t, j.Type),
			Left:      root,
			Right:     right,
			schema:    appendSchema(root.Output(), right.Output()),
			Predicate: rfjEq(names, i, i+1),
		}
	}
	// C-04a: the SpecialJoinInfos travel WITH the joinlist, exactly as
	// `planFromClause` publishes them — an admitted outer link whose
	// SpecialJoinInfo is missing is declined by the seam's fail-closed guard,
	// so a fixture that built only the joinlist would make every LEFT test
	// pass by declining.
	ctx.joinlist, ctx.joinInfoList = deconstructJointreeScopedSJI(
		fromExprs, defaultCollapseLimits(), pgShapedCollapseEnabled(), nil)
	return root, ctx
}

// seamChainLeaves peels `seamFixture`'s left-deep CROSS chain back into its
// leaves, in binding order, so a fixture can rebuild the chain with other join
// types over the SAME leaf nodes (and therefore the same bindings and stats).
func seamChainLeaves(t *testing.T, node Node, n int) []Node {
	t.Helper()
	out := make([]Node, n)
	cur := node
	for i := n - 1; i > 0; i-- {
		j, ok := cur.(*Join)
		if !ok {
			t.Fatalf("fixture chain has %T where link %d should be", cur, i)
		}
		out[i] = j.Right
		cur = j.Left
	}
	out[0] = cur
	return out
}

// spinePlanJoinType maps the parser's join type onto the planner's. The two
// enumerations are separate types with different members (the planner has
// Semi/Anti, which no `JOIN` keyword spells), and `spineLinkSearchable` compares
// one of each — so a fixture that needs both spellings of one link has to cross
// the same boundary the production check does.
func spinePlanJoinType(t *testing.T, pt parser.JoinType) JoinType {
	t.Helper()
	switch pt {
	case parser.JoinInner:
		return JoinTypeInner
	case parser.JoinCross:
		return JoinTypeCross
	case parser.JoinLeft:
		return JoinTypeLeft
	case parser.JoinRight:
		return JoinTypeRight
	case parser.JoinFull:
		return JoinTypeFull
	default:
		t.Fatalf("fixture uses parser join type %d, which has no planner spelling", pt)
		return JoinTypeInner
	}
}

// TestJoinlistTagsAPinnedOuterJoinWithItsType is the producer half: the joinlist
// now records WHICH join pinned an item, which is the fact everything else in
// this file rests on.
//
// Before P5.9-s a pinned item was indistinguishable from a
// `from_collapse_limit` sub-list, so `makeRelFromJoinlist` had no way to tell a
// forced order from an outer join and searched both alike.
// C-04a retargeted it from LEFT to RIGHT, and C-04b from RIGHT to FULL: LEFT
// and RIGHT no longer pin and no longer start a spine (they enter the search
// themselves); FULL still does, and the tag is still what tells
// `makeRelFromJoinlist` a forced order from an outer join. The LEFT/RIGHT
// half of this claim lives in TestSeamPlansALeftLinkInsideOneSearchProblem
// and TestSeamPlansARightLinkInsideOneSearchProblem.
func TestJoinlistTagsAPinnedOuterJoinWithItsType(t *testing.T) {
	jl := deconstructJointree(
		parseFrom(t, "a JOIN b ON a.x = b.x FULL JOIN c ON b.x = c.x"),
		defaultCollapseLimits(), true)

	if len(jl) != 1 {
		t.Fatalf("joinlist has %d items, want 1 (the FULL pin absorbs the chain)", len(jl))
	}
	if !jl[0].pinnedOuter() {
		t.Fatalf("the top item is not marked as a pinned outer join (jointype=%s)",
			joinTypeName(jl[0].jointype))
	}
	if jl[0].jointype != parser.JoinFull {
		t.Fatalf("pinned item jointype = %s, want FULL", joinTypeName(jl[0].jointype))
	}

	prefix, spine := jl.innerPrefixBelowOuterSpine()
	if len(spine) != 1 || spine[0] != parser.JoinFull {
		t.Fatalf("spine = %v, want one FULL link", spine)
	}
	if got := prefix.leaves(nil); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("inner prefix leaves = %v, want [0 1] — the two INNER-joined relations", got)
	}

	// C-04a/b: a LEFT or RIGHT link in the same position does NOT pin, does
	// NOT start a spine, and leaves one flat three-relation problem behind.
	for _, spelling := range []string{"LEFT JOIN", "RIGHT JOIN"} {
		flat := deconstructJointree(
			parseFrom(t, "a JOIN b ON a.x = b.x "+spelling+" c ON b.x = c.x"),
			defaultCollapseLimits(), true)
		if len(flat) != 3 {
			t.Fatalf("%s joinlist has %d items, want 3 — the link must flatten (C-04a/b)", spelling, len(flat))
		}
		if _, spine := flat.innerPrefixBelowOuterSpine(); len(spine) != 0 {
			t.Fatalf("%s spine = %v, want none: the link belongs in the search now", spelling, spine)
		}
	}
}

// TestInnerPrefixIsTheIdentityWithoutAnOuterPin: a joinlist with no outer pin
// must come back unchanged, because that is the path every shape P5.9-r already
// searched still takes. An `innerPrefixBelowOuterSpine` that "helpfully" unwrapped
// an INNER pin would silently change which orders those statements consider.
func TestInnerPrefixIsTheIdentityWithoutAnOuterPin(t *testing.T) {
	for _, from := range []string{
		"a, b, c",
		"a JOIN b ON a.x = b.x JOIN c ON b.x = c.x",
	} {
		t.Run(from, func(t *testing.T) {
			jl := deconstructJointree(parseFrom(t, from), defaultCollapseLimits(), true)
			prefix, spine := jl.innerPrefixBelowOuterSpine()
			if len(spine) != 0 {
				t.Fatalf("spine = %v, want none: %q has no outer link", spine, from)
			}
			if len(prefix) != len(jl) {
				t.Fatalf("prefix has %d items, want the joinlist's own %d", len(prefix), len(jl))
			}
		})
	}
}

// TestSearchRefusesToPlanAPinnedOuterJoin is the guard that replaces an
// accident. Handing `makeRelFromJoinlist` a pinned outer join it cannot rebuild
// asks it to build an outer join, and the only tree it can build is an inner
// one — so the answer must be an ERROR (the seam then falls back to the
// syntactic tree, which still carries the outer join on its own node), never a
// plan.
//
// P5.9-r left this shape unreachable rather than refused: the seam's leaf-count
// decline was the only thing between a `LEFT JOIN` and a plan that dropped its
// unmatched left rows. This test is that decline turned into an invariant, so a
// future widening of the seam cannot reintroduce it silently.
func TestSearchRefusesToPlanAPinnedOuterJoin(t *testing.T) {
	// C-04a retargeted this from LEFT, and C-04b from RIGHT, to the one type
	// that still cannot be rebuilt: FULL. LEFT and RIGHT can — `join_is_legal`
	// matches the SpecialJoinInfo (for RIGHT, the reduced LEFT one),
	// `jointypeForDirection` builds paths in the one legal orientation and
	// `createPlanNode` emits a LEFT join — so refusing them would be
	// withholding a correct plan.
	names := []string{"a", "b"}
	prob := rfjProblem(names, []int64{1000, 10}, nil)
	jl := joinlist{pinnedItem(parser.JoinFull, joinlist{leafItem(0)}, joinlist{leafItem(1)})}
	_, err := planJoinlistSearch(jl, prob)
	if err == nil {
		t.Fatal("the search planned a pinned FULL join — it can only have built an INNER join, " +
			"which drops the unmatched rows the statement asked for")
	}
	if !strings.Contains(err.Error(), "pinned FULL join") {
		t.Fatalf("error %q does not name the shape it refused", err)
	}

	// C-04b: a pinned LEFT or RIGHT item is searchable ONLY through its
	// SpecialJoinInfo — that is the whole of what tells the search it is an
	// outer join. A pinned item whose SJI is not in `root->join_info_list`
	// (a hand-built joinlist; the production producer attaches the same
	// pointer it accumulates) must be refused, not searched as an inner
	// join. Fail-closed, and mutation-checked: the same item WITH its SJI
	// in the list plans as an outer join.
	for _, jt := range []parser.JoinType{parser.JoinLeft, parser.JoinRight} {
		prob := rfjProblem(names, []int64{1000, 10}, nil)
		jl := joinlist{pinnedItem(jt, joinlist{leafItem(0)}, joinlist{leafItem(1)})}
		_, err := planJoinlistSearch(jl, prob)
		if err == nil {
			t.Fatalf("the search planned a pinned %s join with NO SpecialJoinInfo in the list — "+
				"it can only have built an INNER join", joinTypeName(jt))
		}
		if !strings.Contains(err.Error(), "pinned "+joinTypeName(jt)+" join") ||
			!strings.Contains(err.Error(), "no SpecialJoinInfo") {
			t.Fatalf("error %q does not name the shape it refused", err)
		}

		sjType, synL, synR := jt, RelSet(1<<0), RelSet(1<<1)
		if jt == parser.JoinRight {
			sjType, synL, synR = reduceRightLink(synL, synR)
		}
		sj := &SpecialJoinInfo{Jointype: sjType, SynLefthand: synL, SynRighthand: synR,
			MinLefthand: synL, MinRighthand: synR}
		jl[0].sjinfo = sj
		prob.joinInfoList = []*SpecialJoinInfo{sj}
		prob.conjuncts = []Expr{rfjEq(names, 0, 1)}
		rel, err := planJoinlistSearch(jl, prob)
		if err != nil {
			t.Fatalf("pinned %s join WITH its SpecialJoinInfo: %v", joinTypeName(jt), err)
		}
		joins := rfjJoins(rel)
		if len(joins) != 1 || joins[0].Type != JoinTypeLeft {
			t.Fatalf("pinned %s join WITH its SpecialJoinInfo planned as %v, want one LEFT join "+
				"(a RIGHT link is planned as the LEFT join it reduces to)", joinTypeName(jt), joins)
		}
	}
}

// TestSeamPlansALeftLinkInsideOneSearchProblem is C-04a's witness, and it
// replaces P5.9-s's peel test on the same fixture: the corpus shape (Q72's
// `… left outer join promotion …`) is now ONE search problem of four
// relations rather than a three-relation prefix under a spliced spine.
//
// Every qual's destination is still asserted, because the ways to be wrong are
// the same ones and one of them is new:
//
//   - a prefix `ON` qual that never reached the clause list (a cross product);
//   - the LEFT link's `ON` qual missing from the searched tree (it is now the
//     search's own clause and no longer rides on a spliced node);
//   - the LEFT link planned as an INNER join, which is the wrong answer the
//     whole C-03 series exists to prevent.
func TestSeamPlansALeftLinkInsideOneSearchProblem(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c", "d"}
	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10, 100},
		"a JOIN b ON a.x = b.x JOIN c ON b.x = c.x LEFT JOIN d ON c.x = d.x")
	if root, isJoin := node.(*Join); !isJoin || root.Type != JoinTypeLeft {
		t.Fatalf("fixture root is %T, want the LEFT link", node)
	}

	out, residual, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
	if !used {
		t.Fatal("the seam declined a 4-relation chain topped by a LEFT JOIN — the C-04a shape")
	}
	if residual != nil {
		t.Fatalf("residual = %v, want nil: every ON qual is a join clause and the WHERE "+
			"restriction is leaf-local to a PRESERVED-side relation", residual)
	}
	if !isSearchedTree(out) {
		t.Fatalf("the seam returned %T and untagged — the chain was not searched", out)
	}
	joins := rfjJoins(out)
	if len(joins) != 3 {
		t.Fatalf("searched tree has %d joins, want 3 for its 4 relations — the LEFT link is "+
			"IN the problem now, not stacked above it", len(joins))
	}
	// Exactly one of them is the LEFT join, and it is the search's own node.
	nleft := 0
	for _, j := range joins {
		if j.Type == JoinTypeLeft {
			nleft++
		}
		if j.Type != JoinTypeLeft && j.Type != JoinTypeInner && j.Type != JoinTypeCross {
			t.Fatalf("searched tree contains a %v join", j.Type)
		}
	}
	if nleft != 1 {
		t.Fatalf("searched tree has %d LEFT joins, want exactly 1 — a LEFT link planned as an "+
			"INNER join drops the unmatched left rows the statement asked for", nleft)
	}
	// All three ON quals reach the tree, the LEFT link's included.
	got := seamEqualities(out)
	for _, want := range []string{"a0=b0", "b0=c0", "c0=d0"} {
		if !got[want] {
			t.Fatalf("the searched tree does not enforce %s (enforces %v) — a dropped ON qual "+
				"is a cross product, not a slow plan", want, got)
		}
	}
	rfjAssertBindingOrder(t, out, names)
	if n := len(seamLeafLocalFilters(out)); n != 1 {
		t.Fatalf("found %d leaf-local filters, want 1 (the WHERE `a1 > 5` on the preserved side)", n)
	}
}

// TestPGShapedSeamKeepsANullableSideQualAboveTheOuterJoin is the correctness
// edge C-04a is bounded by, and after C-04a it is a LIVE hazard rather than a
// structural impossibility. While the LEFT link was peeled, `d` was outside the
// searched window and no clause producer could reach it; now `d` is a leaf of
// the problem, and `partitionConjunctsForJoinPlanning` has no nullable-side
// guard of its own — a single-relation `WHERE` conjunct on `d` would become a
// leaf-local `Filter` UNDER the LEFT join and keep rows that must be dropped.
//
// The per-qual delay proof (DESIGN §3.5) is what stops that, and this fixture
// is its adjudication: the conjunct must come back as the residual, by
// identity, and no leaf may have acquired a filter.
func TestPGShapedSeamKeepsANullableSideQualAboveTheOuterJoin(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c", "d"}
	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10, 100},
		"a JOIN b ON a.x = b.x JOIN c ON b.x = c.x LEFT JOIN d ON c.x = d.x")
	nullableQual := seamLocal(names, 3) // `d1 > 5`, on the null-extended side

	out, residual, used := tryPGShapedJoinSearch(node, nullableQual, ctx, nil)
	if !used {
		t.Fatal("the seam declined the C-04a shape")
	}
	if residual != nullableQual {
		t.Fatalf("residual = %v, want the nullable-side qual itself: it may be evaluated only "+
			"above the outer join that produces the NULLs", residual)
	}
	if n := len(seamLeafLocalFilters(out)); n != 0 {
		t.Fatalf("found %d leaf-local filters in the searched tree, want 0 — a qual on the "+
			"nullable side was pushed below the outer join", n)
	}
}

// TestPGShapedSeamHoldsAMultiRelationNullableSideQual is the other half of
// §3.5, and the half a single-relation fixture cannot reach. A `WHERE` conjunct
// spanning a PRESERVED and a NULLABLE relation is not a leaf local — it is a
// two-relation clause, and `clausesFor` would apply it AT the LEFT join, i.e.
// as though it had been written in the `ON` clause. That turns "keep the row,
// null-extended" into "drop the row": too few rows, and no row COUNT on the
// preserved side alone would notice.
//
// The delay test is `qual reaches the nullable side`, not `qual is a leaf
// local`, precisely so this conjunct is held too.
func TestPGShapedSeamHoldsAMultiRelationNullableSideQual(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c", "d"}
	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10, 100},
		"a JOIN b ON a.x = b.x JOIN c ON b.x = c.x LEFT JOIN d ON c.x = d.x")
	// `a0 = d0` — spans the preserved side and the nullable one.
	spanning := rfjEq(names, 0, 3)

	out, residual, used := tryPGShapedJoinSearch(node, spanning, ctx, nil)
	if !used {
		t.Fatal("the seam declined the C-04a shape")
	}
	if residual != spanning {
		t.Fatalf("residual = %v, want the spanning WHERE conjunct itself — placing it AT the "+
			"LEFT join would make it an ON condition and drop null-extended rows", residual)
	}
	if got := seamEqualities(out); got["a0=d0"] {
		t.Fatalf("the searched tree enforces the delayed WHERE qual a0=d0 (enforces %v)", got)
	}
}

// TestPGShapedSeamDeclinesAFullSpine: FULL null-extends BOTH of its inputs, and
// its USING coalescing (`UsingLeftCols`/`UsingRightCols`, planner.go) names
// merged-var positions that a re-associated input would have to be re-checked
// against. It stays declined — the one join type `spineLinkSearchable` still
// refuses — and the fixture checks the tree came back untouched rather than
// half-spliced.
func TestPGShapedSeamDeclinesAFullSpine(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c", "d"}
	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10, 100},
		"a JOIN b ON a.x = b.x JOIN c ON b.x = c.x FULL JOIN d ON c.x = d.x")
	pred := seamLocal(names, 0)
	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if used {
		t.Fatal("the seam searched an input of a FULL JOIN")
	}
	if out != node || residual != pred {
		t.Fatal("the seam altered its inputs while declining")
	}
}

// TestSeamPlansARightLinkInsideOneSearchProblem is C-04b's witness, and it
// replaces P5.9-t's peel test on the same fixture. `a ⋈ b ⋈ c RIGHT JOIN d`
// puts the three-relation subproblem on the LEFT of the link — goopg's FROM
// chain is left-deep and a join's right side is a single range var — and that
// side is the NULLABLE one. Before C-04b the link was peeled and the prefix
// searched under it; now the four relations are ONE problem and the link is
// planned as the LEFT join it reduces to (`reduceRightLink`, PG's
// reduce_outer_joins flip), with `d` on the preserved side.
//
// Both halves of P5.9-t are still asserted, because both are wrong answers if
// missed: the ORDER is searchable (every ON qual, the link's included, reaches
// the tree), and the `WHERE` on the nullable prefix is NOT pushable — it comes
// back as the residual by identity and no leaf acquires a filter. Two things
// are new: exactly one LEFT join with `d` alone on its preserved input, and a
// preserved-side `WHERE` that DOES distribute.
func TestSeamPlansARightLinkInsideOneSearchProblem(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c", "d"}
	from := "a JOIN b ON a.x = b.x JOIN c ON b.x = c.x RIGHT JOIN d ON c.x = d.x"

	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10, 100}, from)
	if root, isJoin := node.(*Join); !isJoin || root.Type != JoinTypeRight {
		t.Fatalf("fixture root is %T, want the RIGHT link", node)
	}
	pred := seamLocal(names, 0) // `a1 > 5`, on the null-extended prefix

	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if !used {
		t.Fatal("the seam declined a 4-relation chain topped by a RIGHT JOIN — the C-04b shape")
	}
	if !isSearchedTree(out) {
		t.Fatalf("the seam returned %T and untagged — the chain was not searched", out)
	}
	joins := rfjJoins(out)
	if len(joins) != 3 {
		t.Fatalf("searched tree has %d joins, want 3 for its 4 relations — the RIGHT link is "+
			"IN the problem now, not stacked above it", len(joins))
	}
	var outer *Join
	for _, j := range joins {
		switch j.Type {
		case JoinTypeLeft:
			if outer != nil {
				t.Fatalf("searched tree has two outer joins for one RIGHT link")
			}
			outer = j
		case JoinTypeInner, JoinTypeCross:
		default:
			t.Fatalf("searched tree contains a %v join — a RIGHT link is planned as the LEFT "+
				"join it reduces to, never as RIGHT", j.Type)
		}
	}
	if outer == nil {
		t.Fatal("searched tree has no LEFT join — the RIGHT link was planned as an INNER join, " +
			"which drops the unmatched `d` rows the statement asked for")
	}
	// `d` alone is preserved: it is the LEFT join's outer input, and the whole
	// prefix is its nullable input. The other way round would preserve the
	// wrong side — the right row COUNT on a fixture, the wrong rows on data.
	if nl, nr := rfjLeafCount(outer.Left), rfjLeafCount(outer.Right); nl != 1 || nr != 3 {
		t.Fatalf("LEFT join preserves %d leaves and null-extends %d, want 1 (d) and 3 (the prefix)", nl, nr)
	}
	got := seamEqualities(out)
	for _, want := range []string{"a0=b0", "b0=c0", "c0=d0"} {
		if !got[want] {
			t.Fatalf("the searched tree does not enforce %s (enforces %v) — a dropped ON qual "+
				"is a cross product, not a slow plan", want, got)
		}
	}
	// The WHERE on the nullable prefix is a test on null-extended rows and
	// may not be evaluated below the join that produces the NULLs.
	if residual != pred {
		t.Fatalf("residual = %v, want the WHERE conjunct itself — a qual from above a RIGHT "+
			"JOIN may not be evaluated on its nullable input", residual)
	}
	if n := len(seamLeafLocalFilters(out)); n != 0 {
		t.Fatalf("found %d leaf-local filters in the searched tree, want 0 — a WHERE qual was "+
			"pushed below the join that null-extends that relation", n)
	}
	rfjAssertBindingOrder(t, out, names)

	// The mirror: a WHERE on the PRESERVED side distributes to its leaf.
	node, ctx = seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10, 100}, from)
	out, residual, used = tryPGShapedJoinSearch(node, seamLocal(names, 3), ctx, nil)
	if !used {
		t.Fatal("the seam declined the C-04b shape with a preserved-side WHERE")
	}
	if residual != nil {
		t.Fatalf("residual = %v, want nil: `d1 > 5` is on the preserved side of the RIGHT link", residual)
	}
	if n := len(seamLeafLocalFilters(out)); n != 1 {
		t.Fatalf("found %d leaf-local filters, want 1 (the WHERE `d1 > 5` on the preserved side)", n)
	}
	rfjAssertBindingOrder(t, out, names)
}

// TestSeamPlansARightLinkUnderALeftLinkInOneProblem: before C-04b
// `… RIGHT JOIN c LEFT JOIN d` was a two-link spine, and what had to hold was
// that ONE RIGHT link anywhere on it nullified the prefix for every link
// above. Now both links are links of one four-relation problem, and the same
// fact has to hold per qual: `a1 > 5` reaches the RIGHT link's nullable side
// (which sits on the LEFT link's PRESERVED side) and is still delayed above
// the whole tree.
func TestSeamPlansARightLinkUnderALeftLinkInOneProblem(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c", "d"}
	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10, 100},
		"a JOIN b ON a.x = b.x RIGHT JOIN c ON b.x = c.x LEFT JOIN d ON c.x = d.x")
	pred := seamLocal(names, 0)

	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if !used {
		t.Fatal("the seam declined a RIGHT link under a LEFT one — both are spine links")
	}
	if residual != pred {
		t.Fatalf("residual = %v, want the WHERE conjunct itself — the RIGHT link BELOW the "+
			"topmost LEFT one still null-extends `a`", residual)
	}
	joins := rfjJoins(out)
	if len(joins) != 3 {
		t.Fatalf("searched tree has %d joins, want 3 for its 4 relations", len(joins))
	}
	nleft := 0
	for _, j := range joins {
		if j.Type == JoinTypeLeft {
			nleft++
		}
	}
	if nleft != 2 {
		t.Fatalf("searched tree has %d LEFT joins, want 2 — the RIGHT link reduces to one and "+
			"the LEFT link is the other; one planned as an inner join drops rows", nleft)
	}
	if n := len(seamLeafLocalFilters(out)); n != 0 {
		t.Fatalf("found %d leaf-local filters, want 0 — `a` is null-extended", n)
	}
	got := seamEqualities(out)
	for _, want := range []string{"a0=b0", "b0=c0", "c0=d0"} {
		if !got[want] {
			t.Fatalf("the searched tree does not enforce %s (enforces %v)", want, got)
		}
	}
	rfjAssertBindingOrder(t, out, names)
}

// TestPGShapedSeamDeclinesAnOuterLinkBelowAnInnerOne: the peel only lifts links
// off the TOP of the chain. `a LEFT JOIN b ON … JOIN c ON …` has its outer link
// underneath, where the searched prefix would have to be rebuilt AS an outer
// join — 03 §4.4's real work, not this task's. It must decline, and the fixture
// checks the tree came back untouched rather than half-spliced.
func TestPGShapedSeamDeclinesAnOuterLinkBelowAnInnerOne(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10},
		"a LEFT JOIN b ON a.x = b.x JOIN c ON b.x = c.x")
	pred := seamLocal(names, 0)

	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if used {
		t.Fatal("the seam searched across an outer link buried below an inner one")
	}
	if out != node || residual != pred {
		t.Fatal("the seam altered its inputs while declining")
	}
}

// TestPGShapedSeamDeclinesAOneRelationPrefix: `a LEFT JOIN b` leaves one
// relation below the spine, and one relation is not a search
// (`make_rel_from_joinlist` returns the item). Declining keeps the seam's
// contract honest — a "searched" tree of one leaf would tag a scan and make the
// legacy layout passes skip it for nothing.
func TestPGShapedSeamSearchesAOneRelationPrefixUnderASpine(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b"}
	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 10},
		"a LEFT JOIN b ON a.x = b.x")
	pred := seamLocal(names, 0)

	// M0134-0188: `a LEFT JOIN b` peels to a ONE-relation prefix, and the
	// seam used to decline it — "there is no order to search". There is no
	// order, but there IS an access method: base-rel path generation runs,
	// `add_path` chooses among seq / index / index-only, and the boundary
	// republishes binding order. This is the only seam through which the
	// LEFT side of an outer join reaches cost-based access selection at all
	// (TPC-H Q13's covering scan). A one-relation statement with NO spine
	// stays out — the earlier `nrels < 2` gate — so the single-table paths
	// keep owning those.
	// C-04a: there is no spine any more — the LEFT link is a link of the
	// searched problem — so what this fixture now pins is that the SAME
	// access-method selection still happens, with the two relations in ONE
	// two-relation problem and the qual on the preserved side still consumed
	// into a leaf.
	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if !used {
		t.Fatal("the seam declined a two-relation LEFT JOIN")
	}
	if !isSearchedTree(out) {
		t.Fatalf("the seam returned %T and untagged — the statement was not searched", out)
	}
	joins := rfjJoins(out)
	if len(joins) != 1 || joins[0].Type != JoinTypeLeft {
		t.Fatalf("searched tree has %d joins (%v), want one LEFT join", len(joins), joins)
	}
	// The local qual on `a` is on the PRESERVED side, so the delay proof
	// distributes it and it is attached to the leaf inside the search.
	if residual != nil {
		t.Fatalf("the preserved-side qual was not consumed: residual=%v", residual)
	}
	if n := len(seamLeafLocalFilters(out)); n != 1 {
		t.Fatalf("found %d leaf-local filters, want 1 (the WHERE `a1 > 5`)", n)
	}
}

// TestPGShapedSeamPeelsATwoLinkSpine: Q72's actual shape is TWO stacked left
// outer joins. Before C-04a the recursion had to PEEL both; after it, both are
// links of a single five-relation problem, and what has to hold is that BOTH
// survive as LEFT joins in the searched tree — a chain that admitted one and
// planned the other as an inner join is the failure this fixture is sized for.
func TestPGShapedSeamPeelsATwoLinkSpine(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c", "d", "e"}
	node, ctx := seamChainFromSQL(t, names, []int64{1_000_000, 500_000, 10, 100, 50},
		"a JOIN b ON a.x = b.x JOIN c ON b.x = c.x "+
			"LEFT JOIN d ON c.x = d.x LEFT JOIN e ON d.x = e.x")

	out, _, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
	if !used {
		t.Fatal("the seam declined a 5-relation chain with TWO stacked LEFT JOINs")
	}
	if !isSearchedTree(out) {
		t.Fatalf("the seam returned %T and untagged — the chain was not searched", out)
	}
	joins := rfjJoins(out)
	if len(joins) != 4 {
		t.Fatalf("searched tree has %d joins, want 4 for its 5 relations", len(joins))
	}
	nleft := 0
	for _, j := range joins {
		if j.Type == JoinTypeLeft {
			nleft++
		}
	}
	if nleft != 2 {
		t.Fatalf("searched tree has %d LEFT joins, want 2 — one of the stacked outer links "+
			"was planned as an inner join", nleft)
	}
	got := seamEqualities(out)
	for _, want := range []string{"a0=b0", "b0=c0", "c0=d0", "d0=e0"} {
		if !got[want] {
			t.Fatalf("the searched tree does not enforce %s (enforces %v)", want, got)
		}
	}
	rfjAssertBindingOrder(t, out, names)
}
