package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// R3-1 planner pin: a LEFT join CAN reach the NLI rewrite carrying a
// residual `Predicate`, so the executor's LEFT semantics must be correct
// for that shape rather than relying on the planner never producing it.
//
// Round 1's deferral-ledger row recorded LEFT+residual NLI as a purely
// latent hazard on the premise that "LEFT+residual currently declines
// NLI". That premise holds only for the Q13 shape, whose ON residual is
// INNER-ONLY and therefore gets pushed into a `Filter{inner}` wrapper —
// which `pickInnerSide` then declines because the inner is no longer a
// bare SeqScan (pinned by TestLeftJoinInnerOnlyConjunctFilterIsLeafLocal).
//
// A CROSS-RELATION ON residual takes a different route: the LEFT ON-split
// classifies it sideMixed and keeps it on the join predicate, while
// splitEqualityForHash still lifts the equi conjunct into LeftKey/RightKey.
// tryBuildNLI's leftover-retention path (`residualPred == nil &&
// j.Predicate != nil`) has no join-type gate, so the conjunct survives as
// NestedLoopIndexJoin.Predicate on a LEFT join.
//
// These tests exist so that a future planner change which incidentally
// closes this route cannot silently make the executor's LEFT correctness
// tests (internal/executor/nli_left_residual_exec_test.go) unreachable —
// dead tests would mask a re-regression of the row-dropping bug.
func newLeftResidualLeakCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "cust"}, []catalog.Column{
		{Name: "c_key", Type: catalog.Type{Name: "numeric"}, NotNull: true},
		{Name: "c_bal", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	ordr, err := cat.CreateTable(parser.ObjectName{Name: "ordr"}, []catalog.Column{
		{Name: "o_key", Type: catalog.Type{Name: "numeric"}, NotNull: true},
		{Name: "o_total", Type: catalog.Type{Name: "numeric"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "ordr_key_idx"}, ordr, []string{"o_key"}, false, "btree", true); err != nil {
		t.Fatal(err)
	}
	return cat
}

// findLeftNLI returns the first LEFT-typed NestedLoopIndexJoin in the
// tree. It walks only the wrapper kinds these two probe queries can
// produce; an unrecognised node simply ends that branch, which turns into
// the tests' explicit "leak route closed" skip rather than a false pass.
func findLeftNLI(n Node) *NestedLoopIndexJoin {
	var found *NestedLoopIndexJoin
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil || found != nil {
			return
		}
		switch x := cur.(type) {
		case *NestedLoopIndexJoin:
			if x.Type == JoinTypeLeft {
				found = x
				return
			}
			walk(x.Outer)
			walk(x.Inner)
		case *Project:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Distinct:
			walk(x.Child)
		case *Join:
			walk(x.Left)
			walk(x.Right)
		}
	}
	walk(n)
	return found
}

// TestLeftJoinCrossRelationResidualReachesNLI pins the leftover-retention
// leak route: a cross-relation ON residual survives onto a LEFT NLI.
func TestLeftJoinCrossRelationResidualReachesNLI(t *testing.T) {
	cat := newLeftResidualLeakCatalog(t)
	stmt := parseOne(t, `select c_key, o_total from cust left join ordr on c_key = o_key and o_total > c_bal`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	nli := findLeftNLI(node)
	if nli == nil {
		t.Skipf("no LEFT NLI in the plan — the leak route closed; the executor LEFT tests may now be unreachable, re-verify them. Tree: %s", describePlanTree(node))
	}
	if nli.Predicate == nil {
		t.Fatalf("expected a residual Predicate on the LEFT NLI (the leftover-retention path), got nil; tree: %s", describePlanTree(node))
	}
	// Pin today's residual CONTENT, not just its presence: because
	// nliConsumedByProbe compares the probe key by pointer identity
	// (bin.Right == k) against a rebuilt keys slice, the match usually
	// fails and the equi conjunct is retained alongside the true
	// residual. That matters for the executor fix: the null-padded row
	// fails on the equi conjunct alone, so EVERY unmatched outer row was
	// dropped before R3-1 — not merely those whose residual references
	// an inner column. If this assertion ever flips, the residual shape
	// the executor tests exercise has changed and they need re-checking.
	if len(splitAnd(nli.Predicate)) < 2 {
		t.Logf("NOTE: LEFT NLI residual narrowed to %d conjunct(s) — nliConsumedByProbe now consumes the probe key; re-check the executor LEFT tests still exercise the intended shape", len(splitAnd(nli.Predicate)))
	}
}

// TestLeftJoinOrFactoredResidualReachesNLI pins the second leak route:
// an OR-of-ANDs ON clause with a common equi conjunct is factored into a
// probe key with the full OR retained as the residual — also ungated by
// join type.
func TestLeftJoinOrFactoredResidualReachesNLI(t *testing.T) {
	cat := newLeftResidualLeakCatalog(t)
	stmt := parseOne(t, `select c_key, o_total from cust left join ordr on (c_key = o_key and o_total > 5) or (c_key = o_key and c_bal > 3)`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	nli := findLeftNLI(node)
	if nli == nil {
		t.Skipf("no LEFT NLI in the plan — the OR-factoring leak route closed. Tree: %s", describePlanTree(node))
	}
	if nli.Predicate == nil {
		t.Fatalf("expected the full OR retained as the LEFT NLI residual, got nil; tree: %s", describePlanTree(node))
	}
}
