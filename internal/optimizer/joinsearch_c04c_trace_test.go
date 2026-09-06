package optimizer

// C-04c enum-trace evidence, and the C-04a/b regression mirrors.
//
// C-03d's fixtures drive the SEARCH directly because the seam still peeled
// those links. C-04c's shapes reach the search through the production seam, so
// these drive `tryPGShapedJoinSearch` and read the two provenance channels off
// it — DPTRACE for "was the pairing offered / declined and why", DPPATH for
// "did a path get offered and accepted, with which jointype". Neither
// substitutes for the other (pathtrace.go header, R4).

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// c04cTraceSeam runs the seam under both trace gates and returns the emitted
// lines. `used` is returned so a fixture that silently DECLINED cannot pass by
// finding no offending line.
func c04cTraceSeam(t *testing.T, node Node, pred Expr, ctx *resolveContext) (lines []string, used bool) {
	t.Helper()
	enableDPTrace(t)
	lines = captureTrace(t, func() {
		_, _, used = tryPGShapedJoinSearch(node, pred, ctx, nil)
	})
	return lines, used
}

// c04cJointypesFor returns the set of jointype labels DPPATH stamped on paths
// over the given relset, plus how many of those lines were accepted.
func c04cJointypesFor(lines []string, relids string) (map[string]bool, int, int) {
	types, offered, accepted := map[string]bool{}, 0, 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "DPPATH") || !strings.Contains(l, "relids="+relids) {
			continue
		}
		offered++
		for _, jt := range []string{"jointype=left", "jointype=right", "jointype=inner", "jointype=full", "jointype=semi", "jointype=anti"} {
			if strings.Contains(l, jt) {
				types[jt] = true
			}
		}
		if strings.Contains(l, "verdict=accepted") {
			accepted++
		}
	}
	return types, offered, accepted
}

// TestEnumTraceAdmitsALeftLinkBelowAnInnerLink is C-04c's named gate for the
// first shape: the LEFT pairing {a} | {b} — reached only because the walk no
// longer stops at a link below an INNER one — is OFFERED and its paths are
// ACCEPTED carrying jointype=left. A pairing that produced only inner paths
// would drop the unmatched `a` rows and no row count on this fixture would
// notice.
func TestEnumTraceAdmitsALeftLinkBelowAnInnerLink(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := c04cBelowInner(t, names, []int64{100_000, 50_000, 10}, rfjEq(names, 0, 2))
	lines, used := c04cTraceSeam(t, node, seamLocal(names, 0), ctx)
	if !used {
		t.Fatal("the seam declined; the trace below would prove nothing")
	}
	types, offered, accepted := c04cJointypesFor(lines, "{0,1}")
	if offered == 0 {
		t.Fatal("no DPPATH line for the {a,b} relset: the LEFT pairing produced no path")
	}
	if !types["jointype=left"] {
		t.Fatalf("paths over {a,b} are stamped %v, want jointype=left", types)
	}
	if types["jointype=inner"] {
		t.Fatalf("an INNER path was offered for the LEFT pairing (%v) — it would drop the unmatched rows", types)
	}
	if accepted == 0 {
		t.Fatalf("%d LEFT paths offered over {a,b}, none accepted", offered)
	}
}

// TestEnumTraceAdmitsALeftLinkOnANonFirstCommaItem is the same gate for the
// second shape, which additionally depends on the ON qual having been re-based:
// an un-re-based qual would name `a0 = b0`, the {b,c} pairing would carry no
// clause and the search would never offer it.
func TestEnumTraceAdmitsALeftLinkOnANonFirstCommaItem(t *testing.T) {
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

	lines, used := c04cTraceSeam(t, root, seamLocal(names, 0), ctx)
	if !used {
		t.Fatal("the seam declined a LEFT link on a non-first comma item")
	}
	types, offered, accepted := c04cJointypesFor(lines, "{1,2}")
	if offered == 0 {
		t.Fatal("no DPPATH line for the {b,c} relset: the re-based ON qual did not reach the clause list")
	}
	if !types["jointype=left"] || types["jointype=inner"] {
		t.Fatalf("paths over {b,c} are stamped %v, want jointype=left only", types)
	}
	if accepted == 0 {
		t.Fatalf("%d LEFT paths offered over {b,c}, none accepted", offered)
	}
}

// TestBelowInnerLeftLinkSurvivesCollapseSplit is C-04a's Q72 pin
// (`TestLeftLinkSurvivesCollapseSplit`) and C-04b's mirror, for C-04c's shape:
// the per-problem SJI remap (`sjInfosInItemSpace`) must carry a BELOW-INNER
// LEFT link through the `join_collapse_limit` sub-problem split too.
//
// Without the remap the SJI's hands name statement-leaf bits no item-space
// joinrel ever contains, `joinIsLegal` never finds it relevant, and the link is
// searched as an INNER join — Q72's 100 → 84 rows. The chain here puts the LEFT
// link FIRST and the inner links above it, which is exactly the position C-04a
// could not reach, so a remap that happened to work only for a top-of-chain
// link would fail here.
func TestBelowInnerLeftLinkSurvivesCollapseSplit(t *testing.T) {
	withPGShapedDP(t)
	for _, n := range []int{4, 8, 9, 10, 11} {
		names := make([]string, n)
		rows := make([]int64, n)
		for i := 0; i < n; i++ {
			names[i] = string(rune('a' + i))
			rows[i] = int64(1000 * (i + 1))
		}
		// `a LEFT JOIN b ON … JOIN c ON a.x = c.x JOIN d ON a.x = d.x …`: the
		// LEFT link is the FIRST one and every link above it is INNER. Every
		// inner qual is anchored on `a`, the LEFT link's PRESERVED side, so
		// none of them carries the delay obligation that would decline the
		// statement — the pin is about the SJI remap, not about qual routing.
		from := names[0] + " LEFT JOIN " + names[1] + " ON " + names[0] + ".x = " + names[1] + ".x"
		for i := 2; i < n; i++ {
			from += " JOIN " + names[i] + " ON " + names[0] + ".x = " + names[i] + ".x"
		}
		node, ctx := seamChainFromSQL(t, names, rows, from)
		// `seamChainFromSQL` reads the SQL for its join TYPES and shape only
		// and gives every link its own `names[i] = names[i+1]` equality, so
		// the inner quals have to be re-anchored on `a` here — otherwise link
		// 1 reads `b` and the statement declines for the (correct, but
		// unrelated) nullable-side reason, and the pin proves nothing.
		links := rfjJoins(node) // pre-order over a left-deep chain: topmost first
		for k, j := range links {
			if j.Type == JoinTypeLeft {
				continue
			}
			j.Predicate = rfjEq(names, 0, n-1-k)
		}
		out, _, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
		if !used {
			t.Fatalf("n=%d: the seam declined a below-inner LEFT link whose inner quals "+
				"are all preserved-side; the pin would be vacuous", n)
		}
		nleft := 0
		for _, j := range rfjJoins(out) {
			if j.Type == JoinTypeLeft {
				nleft++
			}
		}
		if nleft != 1 {
			t.Errorf("n=%d: LEFT link lost through the collapse split (left=%d)", n, nleft)
		}
	}
}

// TestSeamBelowInnerLeftLinkOverDerivedInputsDeclines is the C-04a Q78 firewall
// verified on C-04c's shape, and it is built the way `with.go` builds a CTE
// binding — a SYNTHESISED, NON-NIL `catalog.Table` under a pushed-down
// `*Filter` — because that is exactly how Q78's leaves escaped the firewall's
// first version. A fixture with a nil table, or a bare `*CTEScan`, would pass
// on the broken classifier and prove nothing.
func TestSeamBelowInnerLeftLinkOverDerivedInputsDeclines(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	node, ctx := seamChainFromSQLWrapped(t, names, []int64{100_000, 50_000, 10},
		"a LEFT JOIN b ON a.x = b.x JOIN c ON a.x = c.x",
		func(i int, leaf Node) Node { return &Filter{Child: &CTEScan{Name: names[i]}} })
	for i := range ctx.bindings {
		ctx.bindings[i].table = &catalog.Table{Name: names[i]}
	}
	// Rebuild the chain over the wrapped leaves with the qual shape the
	// admitted case needs (top link reads the preserved side).
	chain := node.(*Join)
	chain.Predicate = rfjEq(names, 0, 2)
	out, residual, used := tryPGShapedJoinSearch(chain, seamLocal(names, 0), ctx, nil)
	if used {
		t.Fatal("the seam searched a below-inner LEFT link over Filter-wrapped CTE leaves; " +
			"the outer-over-derived firewall must decline it (C-04a Q78: 15 s -> 327 s timeout)")
	}
	if out != chain || residual == nil {
		t.Fatal("the seam altered its inputs while declining")
	}
	// And the same shape over BASE leaves is searched, so the decline above is
	// the firewall and not some unrelated refusal.
	base, bctx := seamChainFromSQL(t, names, []int64{100_000, 50_000, 10},
		"a LEFT JOIN b ON a.x = b.x JOIN c ON a.x = c.x")
	base.(*Join).Predicate = rfjEq(names, 0, 2)
	if _, _, okBase := tryPGShapedJoinSearch(base, seamLocal(names, 0), bctx, nil); !okBase {
		t.Fatal("the base-leaf control also declined; the firewall test above is vacuous")
	}
}

// TestProblemPairsOuterWithDerivedBelowInnerLink is the firewall's unit view of
// the shape above: the SJI hands describe a link whose preserved side is item 0
// and whose nullable side is item 1, with an INNER link above it — the outer
// hand still touches a derived item and must decline.
func TestProblemPairsOuterWithDerivedBelowInnerLink(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{wrappedCTELeaf("a"), wrappedCTELeaf("b"), wrappedCTELeaf("c")},
		relInfos: []baseRelInfo{{table: cteTable("a")}, {table: cteTable("b")}, {table: cteTable("c")}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}, {lo: 2, hi: 3}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinLeft, SynLefthand: 1 << 0, SynRighthand: 1 << 1,
			MinLefthand: 1 << 0, MinRighthand: 1 << 1},
	}
	if !problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("a below-inner LEFT link over Filter-wrapped CTE leaves must decline")
	}
}
