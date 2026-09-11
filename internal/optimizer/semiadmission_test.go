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
