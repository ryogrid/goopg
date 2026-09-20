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

// M0145-0005 slice 4 — one-relation and degenerate scopes through the
// same entry. On the jointree arm the `isSimpleSingle` bypass, the
// `GOOPG_ONEREL_SEARCH` floor and the filterless-statement gate are all
// lifted: `make_one_rel` runs `set_base_rel_pathlists` (allpaths.c:221)
// for every base rel before `make_rel_from_joinlist` counts items, so a
// one-item joinlist is a searched problem and a WHERE-less scope still
// reaches the seam. The legacy arm keeps every gate byte for byte.

// The pins read `treeHasSearched` (onerelreroute_test.go) — the
// provenance marker that separates "the search built this" from "the
// syntactic path produced the same shape".

// TestJointreeSearchesOneRelationScope is slice 4's headline pin: on the
// jointree arm a single-table+WHERE statement plans through the generic
// arm — Filter, then tryJoinSearch — so the plan carries the
// searched-subtree tag the rule-chooser path never stamps. The legacy
// arm keeps the isSimpleSingle bypass: no tag.
func TestJointreeSearchesOneRelationScope(t *testing.T) {
	cat := jtpCatalog(t)
	jointreePlan := planOnPipeline(t, `SELECT k FROM jtp_o WHERE k > 5`, cat, true)
	if !treeHasSearched(jointreePlan) {
		t.Fatalf("jointree arm planned a single-table statement without the search; tree: %s", describePlanTree(jointreePlan))
	}
	legacyPlan := planOnPipeline(t, `SELECT k FROM jtp_o WHERE k > 5`, cat, false)
	if treeHasSearched(legacyPlan) {
		t.Fatalf("legacy arm searched a one-relation statement — the bypass is gone; tree: %s", describePlanTree(legacyPlan))
	}
}

// TestJointreeSearchesAFilterlessScope pins the degenerate arm: no WHERE
// at all still reaches the seam on the jointree arm — a bare scan for
// the one-item statement, a searched join for a filterless comma inner
// join — while the legacy arm leaves both shapes untouched (the
// joinTreeHasOuterLink gate sees no outer link and there is no Filter
// arm without a WHERE).
func TestJointreeSearchesAFilterlessScope(t *testing.T) {
	cat := jtpCatalog(t)
	for _, sql := range []string{
		`SELECT k FROM jtp_o`,
		`SELECT * FROM jtp_o, jtp_i`,
	} {
		if !treeHasSearched(planOnPipeline(t, sql, cat, true)) {
			t.Fatalf("jointree arm left a filterless scope unsearched: %s", sql)
		}
		if treeHasSearched(planOnPipeline(t, sql, cat, false)) {
			t.Fatalf("legacy arm searched a filterless scope: %s", sql)
		}
	}
}

// TestJointreePullsExistsOverSingleTable is the semantic witness the
// floor lift unlocks: `FROM t WHERE EXISTS (…)` is a single-FROM-item
// statement, so before slice 4 the EXISTS rode the isSimpleSingle bypass
// and decorrelated only in the post-hoc unnest below — a Semi join the
// search never saw. On the jointree arm it now pulls up into the one
// searched problem: a searched SEMI join with no residual ExistsExpr.
func TestJointreePullsExistsOverSingleTable(t *testing.T) {
	cat := jtpCatalog(t)
	node := planOnPipeline(t,
		`SELECT k FROM jtp_o WHERE EXISTS (SELECT 1 FROM jtp_i WHERE jtp_i.j = jtp_o.k)`, cat, true)
	j := findSemiOrAntiJoin(node)
	if j == nil || j.Type != JoinTypeSemi {
		t.Fatalf("single-table EXISTS did not become a searched SEMI join; tree: %s", describePlanTree(node))
	}
	if !treeHasSearched(node) {
		t.Fatalf("the SEMI join is not a search product — pull-up ran but the scope stayed unsearched; tree: %s", describePlanTree(node))
	}
	if planHasExistsExpr(node) {
		t.Fatalf("EXISTS leaked into a residual; tree: %s", describePlanTree(node))
	}
}

// TestJointreeAdmitsAOneRelProblem pins the floor itself at the seam:
// nprefix=1 on the jointree arm is a searched problem even with
// GOOPG_ONEREL_SEARCH off — the env knob now governs the legacy arm
// alone.
func TestJointreeAdmitsAOneRelProblem(t *testing.T) {
	withPGShapedDP(t)
	defer func(v bool) { jointreePipeline = v }(jointreePipeline)
	jointreePipeline = true
	defer setOneRelSearchForTest(false)()

	names := []string{"a"}
	node, ctx := seamFixture(names, []int64{100_000})
	out, residual, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
	if !used {
		t.Fatal("the seam declined a one-relation problem on the jointree arm")
	}
	if out == nil {
		t.Fatal("the seam consumed a one-relation problem but returned a nil tree")
	}
	if residual != nil {
		t.Fatalf("residual = %v, want nil — a leaf-local restriction distributes", residual)
	}
}

// TestLegacyArmDeclinesAOneRelProblem is the arm-scoping pin: with the
// pipeline knob off and GOOPG_ONEREL_SEARCH unset the floor stays 2 —
// slice 4 must not leak the lift onto the arm the env knob still owns.
func TestLegacyArmDeclinesAOneRelProblem(t *testing.T) {
	withPGShapedDP(t)
	defer func(v bool) { jointreePipeline = v }(jointreePipeline)
	jointreePipeline = false
	defer setOneRelSearchForTest(false)()

	names := []string{"a"}
	node, ctx := seamFixture(names, []int64{100_000})
	if _, _, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil); used {
		t.Fatal("the legacy arm searched a one-relation problem with both knobs off — the floor is gone")
	}
}
