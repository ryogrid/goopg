package optimizer

// R75: SEMI admission keeper — a SEMI joinrel with a parameterised
// index inner is admitted through the existing arms
// (addPathsToJoinrel → addNLIPaths) and the filed NLI path satisfies
// the PG inequality (Total ≥ outer Total).
import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestSemiAdmissionFilesPricedNLI(t *testing.T) {
	cp := defaultCostParams()
	a, b := relsetOf(0), relsetOf(1)

	outer := scanRel(a, 10000, 100)
	inner := nliInnerRel(b, 5000, a, indexProbeCost(cp))
	joinrel := newRelOptInfo(a|b, 2300, 64)
	clauses := []*restrictInfo{equiClause(a, b)}
	sj := mkSJ(parser.JoinSemi, a, b)

	// M0142-0008a-3(iii) lifted the hash decline for SEMI/ANTI, so this
	// equi-keyed pairing now also gets a hash candidate that competes with
	// NLI on cost — the cheaper one wins addPath's dominance pruning and the
	// other need not survive into Pathlist. What R75 actually needs to keep
	// proving is that admission through addPathsToJoinrel reaches
	// addNLIPaths (and now addHashJoinPath) and prices whichever wins
	// correctly — not that the NLI path specifically survives — so this
	// checks both were OFFERED (traced) and that the SURVIVING SEMI path
	// satisfies the PG inequality.
	lines := captureTrace(t, func() {
		if err := addPathsToJoinrel(nil, joinrel, outer, inner, clauses, cp, sj); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
	})
	offered := producersIn(lines)
	if !offered["join.nestloop"] {
		t.Fatal("no nested-loop path OFFERED for the SEMI pairing")
	}
	if !offered["join.hash"] {
		t.Fatal("no hash path OFFERED for the SEMI pairing — M0142-0008a-3(iii) should have lifted the decline")
	}

	var winner *Path
	for _, p := range joinrel.Pathlist {
		if p.Jointype == parser.JoinSemi {
			winner = p
			break
		}
	}
	if winner == nil {
		t.Fatalf("no SEMI path survived to Pathlist (pathlist=%v) — admission BLOCKED here",
			kindsOf(joinrel.Pathlist))
	}
	outerTotal := outer.CheapestTotal.Cost.Total
	if winner.Cost.Total < outerTotal {
		t.Fatalf("SEMI winner (kind=%d) total %.2f < outer total %.2f — violates the PG inequality",
			winner.Kind, winner.Cost.Total, outerTotal)
	}
	t.Logf("SEMI winner: kind=%d rows=%.0f total=%.2f (outer %.2f)",
		winner.Kind, winner.Rows, winner.Cost.Total, outerTotal)
}

// TestSemiProbeSpliceStampsPricedCost is R77's keeper: the production
// post-pass stamps an arm-priced cost onto a rewrite-built SEMI NLI
// (carrier-unset in, carrier-set out, Total ≥ outer display).
func TestSemiProbeSpliceStampsPricedCost(t *testing.T) {
	cat, outer, inner := condLowerFixture(t)
	nli, ok := tryBuildNLI(condLowerJoin(outer, inner, JoinTypeSemi, nil), cat)
	if !ok {
		t.Fatal("semi join on the nation PK must rewrite to an NLI")
	}
	if !carrierUnset(nli) {
		t.Fatal("fixture must start unpriced (carrier-unset)")
	}
	stampSemiProbePrices(nli, cat, defaultCostParams())
	pc, set := nli.PlanCostInfo()
	if !set {
		t.Fatal("post-pass must stamp the NLI node")
	}
	outerTotal := legacyDisplayCostOf(outer).TotalCost
	if pc.TotalCost < outerTotal {
		t.Fatalf("stamped total %.2f < outer display %.2f — violates the PG inequality",
			pc.TotalCost, outerTotal)
	}
	t.Logf("stamped SEMI: rows=%.0f total=%.2f (outer %.2f)", pc.PlanRows, pc.TotalCost, outerTotal)
}
