package optimizer

import "testing"

// TestMemoizeCallsAreTheOuterPathRows pins M0146-0005's fix: the cache's
// `calls` is the outer PATH's row count (PG's `outer_path->rows`,
// joinpath.c:812-819), not the outer rel's. The two differ for a partial outer,
// whose rows are per worker; pricing a per-worker cache with the whole rel's
// calls overstated its hit ratio and let TPC-H Q3's partial Memoize nested loop
// undercut PG's Parallel Hash Join.
//
// With 10000 rel rows and 200 distinct keys, a serial outer's cache hits
// (10000-200)/10000 = 98% of probes; a per-worker outer of 2000 rows hits only
// (2000-200)/2000 = 90%, so its rescan must cost more — and exactly what
// costMemoizeRescan prices for 2000 calls.
func TestMemoizeCallsAreTheOuterPathRows(t *testing.T) {
	cp := defaultCostParams()
	outerRelids, innerRelids := relsetOf(0), relsetOf(1)
	s := memoTestCtx(t, 10000, 0.02, true)
	outer := scanRel(outerRelids, 10000, estScanPages(10000, 32))
	inner, _ := memoInnerRel(innerRelids, outerRelids, 1)
	ip := inner.CheapestParameterized[1]

	serial := getMemoizePath(s, outer, outer.CheapestTotal, ip, cp)
	partialOuter := *outer.CheapestTotal
	partialOuter.Rows = 2000
	partialOuter.ParallelWorkers = 4
	partial := getMemoizePath(s, outer, &partialOuter, ip, cp)
	if serial == nil || partial == nil {
		t.Fatalf("memoize path missing: serial=%v partial=%v", serial, partial)
	}
	if pathRescanTotal(partial) <= pathRescanTotal(serial) {
		t.Fatalf("per-worker rescan %.4f not above the serial %.4f; calls still read the rel's rows",
			pathRescanTotal(partial), pathRescanTotal(serial))
	}
	nd, def := memoizeKeyNDistinct(s, ip, outer.Relids)
	want, _ := costMemoizeRescan(cp, ip.Cost, ip.Rows, 2000, nd, def, pathNCols(ip), len(ip.IndexClauses), pathWidth(ip), memoizeKeyWidths(s, ip, outer.Relids))
	if got := pathRescanTotal(partial); got != want.Total {
		t.Fatalf("per-worker rescan %.6f, want costMemoizeRescan at 2000 calls %.6f", got, want.Total)
	}
}
