package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAddHashJoinPath_TriesTheCheapestStartupOuter pins M0146-0214 slice 4:
// hash_inner_and_outer (joinpath.c) tries the outer rel's cheapest-STARTUP path
// against the cheapest-total inner before the cheapest-total pair. When the two
// hash joins' totals tie within add_path's fuzz, the fast-start one survives.
// TPC-DS Q64's GEQO tours price a hash join over a fast-start nested loop that
// the total-only pairing never built (tour 12: PG 19412.99..109340.49, goopg
// 36116.84..109310.27 before this).
func TestAddHashJoinPath_TriesTheCheapestStartupOuter(t *testing.T) {
	cp := defaultCostParams()
	probeRel := scanRel(relsetOf(0), 10000, 100)
	total := probeRel.CheapestTotal
	total.Cost = Cost{Startup: 500, Total: 1000}
	startup := &Path{Kind: total.Kind, Rel: probeRel, Rows: total.Rows, Cost: Cost{Startup: 10, Total: 1005}}
	probeRel.Pathlist = append(probeRel.Pathlist, startup)
	probeRel.CheapestStartup = startup
	buildRel := scanRel(relsetOf(1), 10000, 100)
	joinRel := newRelOptInfo(probeRel.Relids|buildRel.Relids, 5000, 64)

	addHashJoinPath(joinRel, probeRel, buildRel, cp, parser.JoinInner, nil, nil, 0, hashJoinFinalCostInput{}, uniqueSideNone, nil)

	var overStartup, overTotal bool
	for _, p := range joinRel.Pathlist {
		if p.Kind != PathHashJoin || len(p.Children) != 2 {
			continue
		}
		overStartup = overStartup || p.Children[0] == startup
		overTotal = overTotal || p.Children[0] == total
	}
	if !overStartup {
		t.Fatalf("no hash join over the cheapest-startup outer; pathlist %d paths", len(joinRel.Pathlist))
	}
	if overTotal {
		t.Fatalf("the total-outer hash join (fuzzily equal total, worse startup) should be dominated by the startup-outer one")
	}
}
