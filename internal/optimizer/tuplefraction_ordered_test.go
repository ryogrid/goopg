package optimizer

import "testing"

// M0145-0019a: the LIMIT fraction may not hand a Sort a fast-start input.
//
// Measured defect (M0145-0019, TPC-DS Q78 at SF1): goopg resolved the fraction
// at the join search root, where a nested loop priced 5494.86..1147507.76 beat
// a hash join priced 16457.31..37659.80 by 0.6% fractionally — and was then
// fed to a Sort that reads every row, making the winner 30x more expensive.
// Upstream cannot do this: it resolves the fraction on the FINAL rel after the
// Sort is priced (planner.c:439), and `create_ordered_paths` only ever sorts
// `cheapest_total_path` (planner.c:5314, make_ordered_path at :7646+).
//
// The fixture is the Q78 shape in miniature, with the real cost numbers scaled
// to the same relationship: a fast-start/expensive-total path and a
// slow-start/cheap-total path, neither delivering the requested order.
func TestFractionalPathMayNotFeedASortAFastStartInput(t *testing.T) {
	newFixture := func() (*RelOptInfo, *Path, *Path) {
		rel := newRelOptInfo(relsetOf(0), 10317, 240)
		rel.ConsiderStartup = true
		cheapTotal := &Path{Kind: PathHashJoin, Rel: rel, Rows: 10317,
			Cost: Cost{Startup: 16457.31, Total: 37659.80}}
		fastStart := &Path{Kind: PathNestLoop, Rel: rel, Rows: 10317,
			Cost: Cost{Startup: 5494.86, Total: 1147507.76}}
		addPath(rel, cheapTotal, "test")
		addPath(rel, fastStart, "test")
		setCheapest(rel)
		if rel.CheapestTotal != cheapTotal {
			t.Fatalf("fixture broken: cheapest-total is %v", rel.CheapestTotal.Kind)
		}
		return rel, cheapTotal, fastStart
	}
	orderBy := []PathKey{{}}

	// Control FIRST, because it is what proves the fixture reproduces the
	// defect rather than merely passing: with no ordering requested, the
	// fraction legitimately picks the fast-start path. This is the answer
	// M0127-P5.7-b landed and it must not change.
	rel, _, fastStart := newFixture()
	if got := getCheapestFractionalPathOrdered(rel, 100, nil); got != fastStart {
		t.Errorf("LIMIT 100 with no ORDER BY must still choose the fast-start path, got %v", got.Kind)
	}

	// The fix: the same numbers, the same fraction, but the statement wants an
	// ordering neither path delivers. A Sort will be stacked, so the
	// fast-start path's startup cost is not what the statement pays.
	rel, cheapTotal, _ := newFixture()
	if got := getCheapestFractionalPathOrdered(rel, 100, orderBy); got != cheapTotal {
		t.Errorf("with an ORDER BY no path satisfies, the fraction must return the cheapest-total path (the one that gets sorted), got %v", got.Kind)
	}
}

// TestFractionalPathStillPicksAnAlreadySortedFastStartPath is the other half of
// upstream's asymmetry, and without it the fix above would be indistinguishable
// from "ignore the fraction whenever there is an ORDER BY" — which would be a
// different, and wrong, change.
//
// `create_ordered_paths` adds an already-sorted input path to the ordered rel
// AS IS (no Sort, planner.c:5337+), so such a path keeps competing at the
// fraction on its own merits. A fast-start index scan under
// `ORDER BY … LIMIT n` is the canonical case PG plans this way.
func TestFractionalPathStillPicksAnAlreadySortedFastStartPath(t *testing.T) {
	key := PathKey{}
	rel := newRelOptInfo(relsetOf(0), 1000, 32)
	rel.ConsiderStartup = true
	cheapTotal := &Path{Kind: PathSeqScan, Rel: rel, Rows: 1000, Cost: Cost{Startup: 100, Total: 200}}
	sortedFastStart := &Path{Kind: PathIndexScan, Rel: rel, Rows: 1000,
		Cost: Cost{Startup: 0, Total: 1000}, Pathkeys: []PathKey{key}}
	addPath(rel, cheapTotal, "test")
	addPath(rel, sortedFastStart, "test")
	setCheapest(rel)
	if rel.CheapestTotal != cheapTotal {
		t.Fatalf("fixture broken: cheapest-total is %v", rel.CheapestTotal.Kind)
	}

	// LIMIT 10 of 1000 rows: the sorted path costs 10 at the fraction against
	// the seq scan's 101, and it needs no Sort — so it must win.
	if got := getCheapestFractionalPathOrdered(rel, 10, []PathKey{key}); got != sortedFastStart {
		t.Errorf("an already-sorted fast-start path must still win the fraction under ORDER BY … LIMIT, got %v", got.Kind)
	}
	// And a path sorted by something ELSE must not win, even though it is
	// cheaper still at the fraction: the ordering is what decides, not the
	// mere presence of pathkeys. Without this arm the gate could be reading
	// `len(Pathkeys) > 0` and the test would not notice.
	rel2 := newRelOptInfo(relsetOf(0), 1000, 32)
	rel2.ConsiderStartup = true
	cheapTotal2 := &Path{Kind: PathSeqScan, Rel: rel2, Rows: 1000, Cost: Cost{Startup: 100, Total: 200}}
	wrongOrder := &Path{Kind: PathIndexScan, Rel: rel2, Rows: 1000,
		Cost: Cost{Startup: 0, Total: 900}, Pathkeys: []PathKey{{SortAsc: true}}}
	addPath(rel2, cheapTotal2, "test")
	addPath(rel2, wrongOrder, "test")
	setCheapest(rel2)
	if got := getCheapestFractionalPathOrdered(rel2, 10, []PathKey{{SortAsc: false}}); got != cheapTotal2 {
		t.Errorf("a path sorted the WRONG way must not win the fraction — it still needs a Sort, got %v", got.Kind)
	}
}
