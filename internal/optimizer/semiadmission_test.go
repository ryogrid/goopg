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

	if err := addPathsToJoinrel(nil, joinrel, outer, inner, clauses, cp, sj); err != nil {
		t.Fatalf("addPathsToJoinrel: %v", err)
	}
	var nli *Path
	for _, p := range joinrel.Pathlist {
		if p.Kind == PathNestLoop && p.Jointype == parser.JoinSemi {
			nli = p
			break
		}
	}
	if nli == nil {
		t.Fatalf("no SEMI nested-loop path filed (pathlist=%v) — admission BLOCKED here",
			kindsOf(joinrel.Pathlist))
	}
	outerTotal := outer.CheapestTotal.Cost.Total
	if nli.Cost.Total < outerTotal {
		t.Fatalf("SEMI NLI total %.2f < outer total %.2f — violates the PG inequality",
			nli.Cost.Total, outerTotal)
	}
	t.Logf("SEMI NLI filed: rows=%.0f total=%.2f (outer %.2f)",
		nli.Rows, nli.Cost.Total, outerTotal)
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
