package optimizer

import (
	"testing"
)

// M0145-0005 slice 1 — the jointree arm stops peeling the top-level outer
// spine and hands the search the whole node tree and unpeeled joinlist.
//
// The reachability argument the slice rests on: since C-04a/b relaxed LEFT
// and RIGHT to collapse-dependent, `joinPinned` pins only FULL (plus a
// LEFT/RIGHT stacked over a FULL pin via `pinnedOverAPinnedSide`), and
// `spineLinkSearchable` never certifies FULL — so a certified non-empty
// spine was already unreachable. Flat outer spines were therefore searched
// whole on BOTH arms before this slice (`a LEFT b LEFT c` deconstructs to
// flat leaf items + two SpecialJoinInfos, the walk's `outerChainLink`s feed
// the clause pool, `joinIsLegal` orders the DP). Slice 1 makes the knob arm
// stop running the peel/splice machinery at all: pinned-top statements —
// FULL or outer-over-FULL — decline downstream at `extractSearchLeaves`'s
// leaf-count check or `makeRelFromJoinlist`'s `pinnedUnsearchable` arm, the
// same `used=false` syntactic fall-back the peel's refusal produced.
//
// The fixture is `a LEFT JOIN b LEFT JOIN c`, the smallest two-link outer
// spine: both links must survive as outer joins (an INNER drops the
// unmatched rows), in order (legality pins it), with both ON quals enforced
// (a dropped qual is a cross product, not a slow plan).

// m0145LeftSpine builds `a LEFT JOIN b ON a.a0 = b.b0 LEFT JOIN c ON
// b.b0 = c.c0` over the fixture's CROSS-chain leaves, with the joinlist and
// join_info_list the real deconstruction produces.
func m0145LeftSpine(t *testing.T, names []string, rows []int64) (Node, *resolveContext) {
	t.Helper()
	node, ctx := seamFixture(names, rows)
	a, b, c := seamLeaves(t, node)
	inner := &Join{Type: JoinTypeLeft, Left: a, Right: b,
		schema: appendSchema(a.Output(), b.Output()), Predicate: rfjEq(names, 0, 1)}
	root := &Join{Type: JoinTypeLeft, Left: inner, Right: c,
		schema: appendSchema(inner.Output(), c.Output()), Predicate: rfjEq(names, 1, 2)}
	ctx.joinlist, ctx.joinInfoList = deconstructJointreeScopedSJI(
		parseFrom(t, "a LEFT JOIN b ON a.a0 = b.b0 LEFT JOIN c ON b.b0 = c.c0"),
		defaultCollapseLimits(), nil)
	return root, ctx
}

// m0145AssertSearchedOuterSpine is the single-pass pin both arms share: the
// returned tree is a search product (not the syntactic `node` handed in),
// both links are outer-preserving, and both quals are enforced.
func m0145AssertSearchedOuterSpine(t *testing.T, node, out Node, residual Expr, used bool, names []string) {
	t.Helper()
	if !used {
		t.Fatal("the seam declined a two-link flat LEFT spine")
	}
	if out == node {
		t.Fatal("returned the syntactic node — the outer links were not searched")
	}
	if n := rfjOuterPreserving(out); n != 2 {
		t.Fatalf("searched tree has %d outer-preserving joins, want 2: "+
			"both LEFT links must survive (an INNER drops the unmatched rows)", n)
	}
	got := seamEqualities(out)
	for _, want := range []string{"a0=b0", "b0=c0"} {
		if !got[want] {
			t.Fatalf("the searched tree does not enforce %s (enforces %v)", want, got)
		}
	}
	// The leaf-local restriction is on the PRESERVED side of both links, so
	// it distributes rather than holding above — residual stays nil.
	if residual != nil {
		t.Fatalf("residual = %v, want nil (preserved-side leaf-local restriction distributes)", residual)
	}
	rfjAssertBindingOrder(t, out, names)
}

// TestJointreeSearchesAFlatOuterSpine is slice 1's witness: with the peel
// retired on the knob arm, the whole jointree reaches the search in one
// pass — every leaf a searched item, both outer links constrained by their
// SpecialJoinInfos through `joinIsLegal`.
func TestJointreeSearchesAFlatOuterSpine(t *testing.T) {
	withPGShapedDP(t)
	defer func(v bool) { jointreePipeline = v }(jointreePipeline)
	jointreePipeline = true

	names := []string{"a", "b", "c"}
	node, ctx := m0145LeftSpine(t, names, []int64{100_000, 50_000, 10})
	out, residual, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
	m0145AssertSearchedOuterSpine(t, node, out, residual, used, names)
}

// TestLegacyArmSearchesTheSameFlatOuterSpine is the parity pin: the flat
// LEFT/RIGHT spine was already a single searched problem on the legacy arm
// (the peel only ever saw pinned tops), so slice 1 changes no reachable
// behaviour there — it retires machinery the arm no longer runs.
func TestLegacyArmSearchesTheSameFlatOuterSpine(t *testing.T) {
	withPGShapedDP(t)
	defer func(v bool) { jointreePipeline = v }(jointreePipeline)
	jointreePipeline = false

	names := []string{"a", "b", "c"}
	node, ctx := m0145LeftSpine(t, names, []int64{100_000, 50_000, 10})
	out, residual, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
	m0145AssertSearchedOuterSpine(t, node, out, residual, used, names)
}

// TestJointreeDeclinesAFullSpine is the fail-closed pin: a top-level FULL
// link pins the joinlist and carries no searched producer, so the knob arm
// must still decline — now via `extractSearchLeaves`'s leaf-count check
// (the FULL join is an opaque leaf while the joinlist counts its two
// members) instead of the retired peel's `spineLinkSearchable` refusal. The
// syntactic tree stands, exactly the outcome the peel produced.
func TestJointreeDeclinesAFullSpine(t *testing.T) {
	withPGShapedDP(t)
	defer func(v bool) { jointreePipeline = v }(jointreePipeline)
	jointreePipeline = true

	names := []string{"a", "b", "c"}
	node, ctx := seamFixture(names, []int64{100_000, 50_000, 10})
	a, b, c := seamLeaves(t, node)
	inner := &Join{Type: JoinTypeLeft, Left: a, Right: b,
		schema: appendSchema(a.Output(), b.Output()), Predicate: rfjEq(names, 0, 1)}
	root := &Join{Type: JoinTypeFull, Left: inner, Right: c,
		schema: appendSchema(inner.Output(), c.Output()), Predicate: rfjEq(names, 1, 2)}
	ctx.joinlist, ctx.joinInfoList = deconstructJointreeScopedSJI(
		parseFrom(t, "a LEFT JOIN b ON a.a0 = b.b0 FULL JOIN c ON b.b0 = c.c0"),
		defaultCollapseLimits(), nil)
	if _, _, used := tryPGShapedJoinSearch(root, seamLocal(names, 0), ctx, nil); used {
		t.Fatal("the knob arm searched a FULL-topped spine — fail-closed is broken")
	}
}
