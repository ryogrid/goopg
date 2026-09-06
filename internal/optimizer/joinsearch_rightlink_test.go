package optimizer

// C-04b (P3-04) — RIGHT admission into the join search. The witness is in
// joinsearchspine_test.go (TestSeamPlansARightLinkInsideOneSearchProblem);
// this file holds the pins C-04a taught us to write BEFORE the suite gates
// run rather than after they fail:
//
//   - the outer-over-derived firewall, end to end through the seam, on the
//     fixture shape that actually escaped in C-04a (Filter-wrapped CTE leaves
//     with synthesised non-nil tables) — the decline is read off the trace
//     channel, not inferred from `used == false`;
//   - the DPPATH evidence that the reduced link is OFFERED and ACCEPTED as
//     jointype=left, and never as right;
//   - the inner-ON-under-nullable guard, with its mutation control (the same
//     conjunct under a LEFT link's preserved side is still admitted);
//   - the ON-qual destinations and the nested-outer declines, mirrored.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

const rightChain3 = "a JOIN b ON a.x = b.x RIGHT JOIN c ON b.x = c.x"

// seamDeclineReasons returns the `seam-decline reason=…` values in a trace.
func seamDeclineReasons(lines []string) []string {
	var out []string
	for _, l := range lines {
		if !strings.Contains(l, "seam-decline") {
			continue
		}
		for _, f := range strings.Fields(l) {
			if strings.HasPrefix(f, "reason=") {
				out = append(out, strings.TrimPrefix(f, "reason="))
			}
		}
	}
	return out
}

// TestSeamRightLinkOverDerivedInputsDeclines is the C-04a Q78 lesson applied
// before the gate can teach it again. Three CTE outputs, each reaching the
// search as `Filter -> CTEScan` over with.go's synthesised (non-nil) table,
// joined by an inner link and a RIGHT link. The firewall must decline the
// problem, and the decline must be the FIREWALL's — `outer-over-derived` on
// the trace — not some earlier gate that would make the test pass for the
// wrong reason. Mutation-checked: the same chain over base leaves is searched.
func TestSeamRightLinkOverDerivedInputsDeclines(t *testing.T) {
	withPGShapedDP(t)
	enableDPTrace(t)
	names := []string{"a", "b", "c"}
	rows := []int64{1, 1, 1} // rowest A3: every CTE output estimates to one row
	wrapCTE := func(i int, leaf Node) Node {
		return &Filter{
			Child:     &CTEScan{Name: names[i], Alias: names[i], schema: leaf.Output()},
			Predicate: seamLocal(names, i),
		}
	}

	node, ctx := seamChainFromSQLWrapped(t, names, rows, rightChain3, wrapCTE)
	var used bool
	lines := captureTrace(t, func() {
		_, _, used = tryPGShapedJoinSearch(node, nil, ctx, nil)
	})
	if used {
		t.Fatal("the seam searched a RIGHT link over three CTE outputs at rows=1 — the Q78 " +
			"shape, where an epsilon Nested Loop victory over full CTE outputs cost 20x")
	}
	reasons := seamDeclineReasons(lines)
	if len(reasons) == 0 || reasons[len(reasons)-1] != "outer-over-derived" {
		t.Fatalf("declined, but not by the firewall: reasons=%v, want the last to be "+
			"outer-over-derived (a decline elsewhere proves nothing about the classifier)", reasons)
	}

	// Mutation control: base leaves, same chain, searched.
	node, ctx = seamChainFromSQL(t, names, rows, rightChain3)
	if _, _, used = tryPGShapedJoinSearch(node, nil, ctx, nil); !used {
		t.Fatal("the same RIGHT chain over BASE leaves was declined — the firewall is " +
			"firing on something other than the derived inputs")
	}
}

// TestSeamRightLinkDPPathOfferedAndAccepted is the enum-trace evidence C-04b's
// gate asks for: on `a ⋈ b RIGHT JOIN c` the joinrel covering all three
// relations — the only pairing that includes `c`, and therefore the outer
// join — has paths OFFERED and ACCEPTED carrying jointype=left. No path
// anywhere carries jointype=right: the search never builds RIGHT, it builds
// the LEFT join the link reduces to (`reduceRightLink`), and PG's own
// JOIN_RIGHT is the reversed direction C-03b declines.
func TestSeamRightLinkDPPathOfferedAndAccepted(t *testing.T) {
	withPGShapedDP(t)
	enableDPTrace(t)
	names := []string{"a", "b", "c"}
	node, ctx := seamChainFromSQL(t, names, []int64{100_000, 50_000, 10}, rightChain3)
	var used bool
	lines := captureTrace(t, func() {
		_, _, used = tryPGShapedJoinSearch(node, nil, ctx, nil)
	})
	if !used {
		t.Fatalf("seam declined the C-04b shape: %v", seamDeclineReasons(lines))
	}
	var offered, accepted, inner int
	for _, l := range lines {
		if !strings.HasPrefix(l, "DPPATH") {
			continue
		}
		if strings.Contains(l, "jointype=right") {
			t.Errorf("a path carries jointype=right: %q", l)
		}
		switch {
		case strings.Contains(l, "relids={0,1,2}"):
			offered++
			if !strings.Contains(l, "jointype=left") {
				t.Errorf("path over the outer joinrel is stamped otherwise: %q", l)
			}
			if strings.Contains(l, "verdict=accepted") {
				accepted++
			}
		case strings.Contains(l, "relids={0,1}"):
			if strings.Contains(l, "jointype=inner") {
				inner++
			}
		}
	}
	if offered == 0 {
		t.Fatal("no path was offered over the outer joinrel {a,b,c}")
	}
	if accepted == 0 {
		t.Errorf("%d LEFT paths offered over {a,b,c}, none accepted", offered)
	}
	if inner == 0 {
		t.Error("the nullable prefix {a,b} produced no inner-join path — it must be complete before c joins")
	}
}

// TestSeamDeclinesAnUnconsumedInnerOnQualUnderARightLink pins the guard
// C-04a never needed. `a ⋈ b`'s ON qual is an OR-of-ANDs: the clause producer
// takes its shared equality and leaves the OR itself for the residual `Filter`
// above the WHOLE searched tree — which is above the RIGHT link, where the OR
// would test null-extended rows and drop every unmatched `c` row. The seam
// must decline (`inner-on-qual-under-nullable`), and the decline is
// mutation-checked against the same OR under a LEFT link's PRESERVED side,
// where the residual is a correct place and the statement is searched.
func TestSeamDeclinesAnUnconsumedInnerOnQualUnderARightLink(t *testing.T) {
	withPGShapedDP(t)
	enableDPTrace(t)
	names := []string{"a", "b", "c"}
	rows := []int64{100_000, 50_000, 10}

	node, ctx := seamChainFromSQL(t, names, rows, rightChain3)
	node.(*Join).Left.(*Join).Predicate = seamOrOfAnds(names, 0, 1)
	var used bool
	lines := captureTrace(t, func() {
		_, _, used = tryPGShapedJoinSearch(node, nil, ctx, nil)
	})
	if used {
		t.Fatal("the seam searched a RIGHT link whose nullable prefix carries an inner ON " +
			"qual the search cannot consume — that qual would land ABOVE the outer join")
	}
	if reasons := seamDeclineReasons(lines); len(reasons) == 0 || reasons[len(reasons)-1] != "inner-on-qual-under-nullable" {
		t.Fatalf("declined for %v, want inner-on-qual-under-nullable", reasons)
	}

	// Mutation control: the same OR on a LEFT link's preserved side.
	node, ctx = seamChainFromSQL(t, names, rows, "a JOIN b ON a.x = b.x LEFT JOIN c ON b.x = c.x")
	node.(*Join).Left.(*Join).Predicate = seamOrOfAnds(names, 0, 1)
	out, residual, used := tryPGShapedJoinSearch(node, nil, ctx, nil)
	if !used {
		t.Fatal("the same OR under a LEFT link's PRESERVED side was declined — the guard is " +
			"firing on inner ON quals that never reach a nullable side")
	}
	if residual == nil {
		t.Fatal("the OR was consumed by the search; the fixture no longer exercises the residual")
	}
	if got := seamEqualities(out); !got["a0=b0"] {
		t.Fatalf("the OR's shared equality a0=b0 did not reach the tree (enforces %v)", got)
	}
}

// TestSeamRightLinkOnQualDestinations mirrors `outerOnQualsOK`'s two
// admissible destinations for a RIGHT link, with the sides swapped: a
// NULLABLE-side-only ON conjunct now reads a PREFIX relation (`a1 > 5` in the
// RIGHT link's ON) and becomes a leaf local under the join, which is exact;
// a PRESERVED-side-only one (`c1 > 5`) has no destination in a searched tree
// and declines the statement.
func TestSeamRightLinkOnQualDestinations(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	rows := []int64{100_000, 50_000, 10}

	// Nullable-side-only: `… RIGHT JOIN c ON b0 = c0 AND a1 > 5`.
	node, ctx := seamChainFromSQL(t, names, rows, rightChain3)
	root := node.(*Join)
	root.Predicate = &BinaryOp{Op: parser.OpAnd, Left: root.Predicate, Right: seamLocal(names, 0)}
	out, residual, used := tryPGShapedJoinSearch(node, nil, ctx, nil)
	if !used {
		t.Fatal("the seam declined a RIGHT link with a nullable-side-only ON conjunct")
	}
	if residual != nil {
		t.Fatalf("residual = %v, want nil — `a1 > 5` is a filter on the nullable input, exact below the join", residual)
	}
	if n := len(seamLeafLocalFilters(out)); n != 1 {
		t.Fatalf("found %d leaf-local filters, want 1 (the ON conjunct on `a`)", n)
	}
	if got := seamEqualities(out); !got["b0=c0"] {
		t.Fatalf("the RIGHT link's own equality did not reach the tree (enforces %v)", got)
	}

	// Preserved-side-only: `… RIGHT JOIN c ON b0 = c0 AND c1 > 5`.
	node, ctx = seamChainFromSQL(t, names, rows, rightChain3)
	root = node.(*Join)
	root.Predicate = &BinaryOp{Op: parser.OpAnd, Left: root.Predicate, Right: seamLocal(names, 2)}
	pred := seamLocal(names, 1)
	out, residual, used = tryPGShapedJoinSearch(node, pred, ctx, nil)
	if used {
		t.Fatal("the seam searched a RIGHT link with a PRESERVED-side-only ON conjunct — " +
			"pushed into c's scan it would drop rows that must be null-extended")
	}
	if out != node || residual != pred {
		t.Fatal("the seam altered its inputs while declining")
	}
}

// TestSeamDeclinesAnOuterLinkUnderARightLinksNullableSide: the walk's
// on-spine flag marks PRESERVED-side descent, and a RIGHT link's left input
// is its nullable side. An outer link found there — `(a LEFT JOIN b) RIGHT
// JOIN c`, which is `c LEFT JOIN (a LEFT JOIN b)`, or a second RIGHT — is
// C-04c's nullable-side shape and declines, tree untouched. (`a RIGHT JOIN b
// RIGHT JOIN c` reaches production with its first link already flipped by
// S9.4; the fixture bypasses that pass, which is the point — the seam must
// hold on its own.)
func TestSeamDeclinesAnOuterLinkUnderARightLinksNullableSide(t *testing.T) {
	withPGShapedDP(t)
	names := []string{"a", "b", "c"}
	for _, from := range []string{
		"a LEFT JOIN b ON a.x = b.x RIGHT JOIN c ON b.x = c.x",
		"a RIGHT JOIN b ON a.x = b.x RIGHT JOIN c ON b.x = c.x",
	} {
		node, ctx := seamChainFromSQL(t, names, []int64{100_000, 50_000, 10}, from)
		pred := seamLocal(names, 0)
		out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
		if used {
			t.Fatalf("%q: the seam searched an outer link on a RIGHT link's nullable side", from)
		}
		if out != node || residual != pred {
			t.Fatalf("%q: the seam altered its inputs while declining", from)
		}
	}
	// And the preserved-side stack is admitted: RIGHT under LEFT is covered
	// by TestSeamPlansARightLinkUnderALeftLinkInOneProblem; here, LEFT under
	// RIGHT's preserved side cannot occur (the preserved side is one leaf),
	// so the spine below a RIGHT link is always an inner chain.
}
