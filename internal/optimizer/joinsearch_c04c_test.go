package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// C-04c — below-inner and non-first-comma outer links.
//
// The two shapes the file header used to name as declines, and the machinery
// each of them needed on top of C-04a/b's per-link cut:
//
//   - BELOW AN INNER LINK. `a LEFT JOIN b ON … JOIN c ON …` reaches the walk
//     with the LEFT link on the INNER link's left input, which C-04a's
//     `onSpine` flag cleared. Admitting it needs one thing C-04a/b never did:
//     an inner `ON` qual can now sit ABOVE an outer link, so the licence "an
//     inner qual may go anywhere at or above its join" no longer implies "at
//     or above every outer link". `chainOnQual.belowNullable` carries the
//     proof obligation and the seam declines when it cannot be met.
//   - A NON-FIRST COMMA FROM ITEM. `FROM a, b LEFT JOIN c ON …` writes the
//     link's `ON` qual in the SECOND item's own coordinates, so the seam has
//     to re-base it. `rebaseChainQual` does that on `cloneExprRefs`, which is
//     exhaustive by a build-time gate and ABORTS on a type it does not know —
//     the property `shiftColumnRefsBy`, the rewriter the header declined to
//     use, does not have.

// c04cItemLocalEq is `rfjEq` written in ONE FROM item's own coordinates: the
// column indices are relative to `itemStartLeaf`, which is what
// `planFromItem` resolves for a non-first comma item (`leftCtx` starts at that
// item's base range variable, offset 0).
func c04cItemLocalEq(names []string, itemStartLeaf, a, b int) Expr {
	return &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Name: names[a] + "0", Index: (a - itemStartLeaf) * rfjWidth, SourceTableIdx: int16(a)},
		Right: &ColumnRef{Name: names[b] + "0", Index: (b - itemStartLeaf) * rfjWidth, SourceTableIdx: int16(b)},
	}
}

// c04cBelowInner builds `a LEFT JOIN b ON a.a0 = b.b0 JOIN c ON <top>`: the
// LEFT link on the INNER link's left input, both quals in the statement's
// coordinates (one comma item, so the item's space IS the statement's).
func c04cBelowInner(t *testing.T, names []string, rows []int64, top Expr) (Node, *resolveContext) {
	t.Helper()
	node, ctx := seamFixture(names, rows)
	a, b, c := seamLeaves(t, node)
	lower := &Join{Type: JoinTypeLeft, Left: a, Right: b,
		schema: appendSchema(a.Output(), b.Output()), Predicate: rfjEq(names, 0, 1)}
	root := &Join{Type: JoinTypeInner, Left: lower, Right: c,
		schema: appendSchema(lower.Output(), c.Output()), Predicate: top}
	ctx.joinlist, ctx.joinInfoList = deconstructJointreeScopedSJI(
		parseFrom(t, "a LEFT JOIN b ON a.a0 = b.b0 JOIN c ON a.a0 = c.c0"),
		defaultCollapseLimits(), pgShapedCollapseEnabled(), nil)
	return root, ctx
}

// TestSeamPlansALeftLinkBelowAnInnerLink is C-04c's first subject. Before it
// the walk returned the LEFT node as ONE opaque leaf, the leaf count
// disagreed with the binding count and the whole statement fell back to the
// syntactic tree.
//
// The assertions are the ones a wrong answer would fail, not "used == true":
// exactly one LEFT join survives in the searched tree (an INNER one there
// drops the unmatched `a` rows — the Q72 failure), and both quals are still
// enforced (a dropped `ON` qual is a cross product, not a slow plan).
func TestSeamPlansALeftLinkBelowAnInnerLink(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	// The INNER link's qual reads the LEFT link's PRESERVED side only, so it
	// carries no delay obligation — the admitted case.
	node, ctx := c04cBelowInner(t, names, []int64{100_000, 50_000, 10}, rfjEq(names, 0, 2))
	out, residual, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
	if !used {
		t.Fatal("the seam declined a LEFT link below an INNER link — C-04c's subject")
	}
	nleft := 0
	for _, j := range rfjJoins(out) {
		if j.Type == JoinTypeLeft {
			nleft++
		}
	}
	if nleft != 1 {
		t.Fatalf("searched tree has %d LEFT joins, want exactly 1: the admitted link must keep its jointype", nleft)
	}
	got := seamEqualities(out)
	for _, want := range []string{"a0=b0", "a0=c0"} {
		if !got[want] {
			t.Fatalf("the searched tree does not enforce %s (enforces %v)", want, got)
		}
	}
	if residual != nil {
		t.Fatalf("residual = %v, want nil (the WHERE restriction is preserved-side and leaf-local)", residual)
	}
	rfjAssertBindingOrder(t, out, names)
}

// TestSeamDeclinesAnInnerOnQualReachingALinkBelowItsNullableSide is the
// obligation the shape above carries, and it is a WRONG-ANSWER guard.
//
// `a LEFT JOIN b ON a.a0 = b.b0 JOIN c ON b.b0 = c.c0`: the INNER link's qual
// reads the LEFT link's NULLABLE side from ABOVE it.
// `partitionConjunctsForJoinPlanning` has no nullable-side guard, and the
// search is free to place a spanning clause at a join inside the nullable
// side, so the seam declines rather than half-proving the placement. The
// decline is exactly the pre-C-04c behaviour for this shape (fall back to the
// syntactic tree), so nothing regresses; upstream's fix is the
// `required_relids` widening, ledgered as the resume point.
func TestSeamDeclinesAnInnerOnQualReachingALinkBelowItsNullableSide(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := c04cBelowInner(t, names, []int64{100_000, 50_000, 10}, rfjEq(names, 1, 2))
	pred := seamLocal(names, 0)
	out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
	if used {
		t.Fatal("the seam searched an INNER ON qual reading the nullable side of a LEFT link below it")
	}
	if out != node || residual != pred {
		t.Fatal("the seam altered its inputs while declining")
	}
}

// C-04c INVESTIGATED the shape C-04b's decline pin names — an outer link on a
// RIGHT link's nullable side, `(a LEFT JOIN b) RIGHT JOIN c`, which is
// `c LEFT JOIN (a LEFT JOIN b)` — and LEFT IT DECLINED, with a reason.
//
// Admitted, it returns rows PG does not. `buildJoinRelRestrictList`
// (joinrestrict.go) classifies the LOWER link's own `ON` clause as an
// outer-join FILTER clause for the upper link, because its relids are a subset
// of the upper SpecialJoinInfo's nullable hand and it touches only one side of
// the upper pair — and then applies it AT the upper join, where it filters
// exactly the rows that join exists to null-extend. Measured on
// `nsj_t LEFT JOIN nsj_p ON t.id = p.id RIGHT JOIN nsj_q ON t.id = q.id`: the
// `t` row with no `p` match carries a NULL through `t.id = p.id`, so its `q`
// row came back null-extended instead of matched (executor
// `TestNonSpineOuterAdmissionValues`, "left link under a right link's nullable
// side", which pins the correct rows through the DECLINE).
//
// Upstream cannot reach it: a clause applied at a lower join is removed from
// the per-rel `joininfo` lists, while goopg re-scans one flat clause list for
// every pair. The fix is a clause-provenance change in `joinrestrict.go` with
// its own blast radius, so it is ledgered
// (`c04c-nested-outer-refilters-lower-on-qual`) rather than smuggled into this
// slice, and `TestSeamDeclinesAnOuterLinkUnderARightLinksNullableSide`
// (joinsearch_rightlink_test.go) stays as written.

// TestSeamPlansALeftLinkOnANonFirstCommaItem is C-04c's second subject, and
// the assertion that matters is the RE-BASED qual: the fixture writes
// `b0 = c0` in the second item's own coordinates (b0 at index 0), which in the
// statement's space is `a0 = b0`. A seam that admitted the shape without
// re-basing would enforce the wrong equality and the test would see `a0=b0`
// instead of `b0=c0` — a wrong answer, silently.
func TestSeamPlansALeftLinkOnANonFirstCommaItem(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := seamFixture(names, []int64{100_000, 50_000, 10})
	a, b, c := seamLeaves(t, node)
	item := &Join{Type: JoinTypeLeft, Left: b, Right: c,
		schema:    appendSchema(b.Output(), c.Output()),
		Predicate: c04cItemLocalEq(names, 1, 1, 2)}
	root := &Join{Type: JoinTypeCross, Left: a, Right: item,
		schema: appendSchema(a.Output(), item.Output())}
	ctx.joinlist, ctx.joinInfoList = deconstructJointreeScopedSJI(
		parseFrom(t, "a, b LEFT JOIN c ON b.b0 = c.c0"),
		defaultCollapseLimits(), pgShapedCollapseEnabled(), nil)

	out, _, used := tryPGShapedJoinSearch(root, seamLocal(names, 0), ctx, nil)
	if !used {
		t.Fatal("the seam declined a LEFT link on a non-first comma FROM item")
	}
	got := seamEqualities(out)
	if !got["b0=c0"] {
		t.Fatalf("the searched tree enforces %v, want b0=c0: the item-local ON qual was not re-based", got)
	}
	if got["a0=b0"] {
		t.Fatalf("the searched tree enforces %v, which includes a0=b0 — the un-re-based reading of the same qual", got)
	}
	nleft := 0
	for _, j := range rfjJoins(out) {
		if j.Type == JoinTypeLeft {
			nleft++
		}
	}
	if nleft != 1 {
		t.Fatalf("searched tree has %d LEFT joins, want exactly 1", nleft)
	}
	rfjAssertBindingOrder(t, out, names)
}

// TestSeamPlansAnInnerLinkOnANonFirstCommaItem pins the same re-base for the
// INNER link the old `base != 0` decline also refused. The decline was one
// check for both link types and lifting it is one change; stating the inner
// half separately keeps its blast radius visible.
func TestSeamPlansAnInnerLinkOnANonFirstCommaItem(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := seamFixture(names, []int64{100_000, 50_000, 10})
	a, b, c := seamLeaves(t, node)
	item := &Join{Type: JoinTypeInner, Left: b, Right: c,
		schema:    appendSchema(b.Output(), c.Output()),
		Predicate: c04cItemLocalEq(names, 1, 1, 2)}
	root := &Join{Type: JoinTypeCross, Left: a, Right: item,
		schema: appendSchema(a.Output(), item.Output())}
	ctx.joinlist, ctx.joinInfoList = deconstructJointreeScopedSJI(
		parseFrom(t, "a, b JOIN c ON b.b0 = c.c0"),
		defaultCollapseLimits(), pgShapedCollapseEnabled(), nil)

	out, _, used := tryPGShapedJoinSearch(root, seamLocal(names, 0), ctx, nil)
	if !used {
		t.Fatal("the seam declined an INNER link on a non-first comma FROM item")
	}
	got := seamEqualities(out)
	if !got["b0=c0"] || got["a0=b0"] {
		t.Fatalf("the searched tree enforces %v, want exactly the re-based b0=c0", got)
	}
}

// TestRebaseChainQualShiftsEveryColumnRef is the unit gate on the re-basing
// primitive, and each case is a node type `shiftColumnRefsBy` — the rewriter
// the file header declined to re-base with — either handles or silently
// no-ops. A no-op leaves a ColumnRef reading a different relation's column,
// which is why this is asserted by walking the RESULT for unshifted indices
// rather than by eyeballing one arm.
func TestRebaseChainQualShiftsEveryColumnRef(t *testing.T) {
	col := func(i int) Expr { return &ColumnRef{Name: "x", Index: i} }
	cases := []struct {
		name string
		expr Expr
	}{
		{"binary op", &BinaryOp{Op: parser.OpEq, Left: col(0), Right: col(3)}},
		{"is null", &IsNullExpr{Operand: col(1)}},
		{"unary", &UnaryOp{Op: parser.OpSub, Operand: col(2)}},
		{"cast", &CastExpr{Operand: col(4)}},
		{"nested", &BinaryOp{Op: parser.OpAnd,
			Left:  &IsNullExpr{Operand: col(0)},
			Right: &BinaryOp{Op: parser.OpGt, Left: &CastExpr{Operand: col(5)}, Right: &IntegerConst{Value: 1}}}},
	}
	const delta = 7
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := c04cColumnIndexes(tc.expr)
			out, ok := rebaseChainQual(tc.expr, delta)
			if !ok {
				t.Fatalf("rebaseChainQual declined %s", tc.name)
			}
			after := c04cColumnIndexes(out)
			if len(before) != len(after) {
				t.Fatalf("%d ColumnRefs before, %d after", len(before), len(after))
			}
			for i := range before {
				if after[i] != before[i]+delta {
					t.Fatalf("ColumnRef %d: index %d -> %d, want %d", i, before[i], after[i], before[i]+delta)
				}
			}
			// The ORIGINAL must be untouched: the seam may still decline, and
			// a decline returns `node`/`pred` by identity.
			if again := c04cColumnIndexes(tc.expr); !c04cIntsEqual(again, before) {
				t.Fatalf("rebaseChainQual mutated its input: %v -> %v", before, again)
			}
		})
	}
}

// TestRebaseChainQualDeclinesASublink pins the fail-closed half: a qual
// carrying an inner plan is DECLINED, not shifted, because the inner plan's
// coordinate space is not the one being shifted and no driver in this package
// descends a subplan.
func TestRebaseChainQualDeclinesASublink(t *testing.T) {
	e := &BinaryOp{Op: parser.OpEq,
		Left:  &ColumnRef{Name: "x", Index: 0},
		Right: &SubqueryExpr{Plan: &SeqScan{}}}
	if _, ok := rebaseChainQual(e, 3); ok {
		t.Fatal("rebaseChainQual shifted a qual containing a sublink; the inner plan is a different coordinate space")
	}
}

func c04cColumnIndexes(e Expr) []int {
	var out []int
	walkExprRefs(e, scopeIgnore, exprVisitor{Visit: func(x Expr) bool {
		if cr, ok := x.(*ColumnRef); ok {
			out = append(out, cr.Index)
		}
		return true
	}})
	return out
}

func c04cIntsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
