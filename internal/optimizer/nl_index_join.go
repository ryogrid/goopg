package optimizer

// Index-probe helpers shared by the PG-shaped search's parameterised index
// paths (pathparamindex.go) and the jointree pull-up's census.
//
// This file used to hold M0054-0006's nested-loop-index post-pass,
// `rewriteJoinsToNLI`: a walk over the finished plan that turned an eligible
// `*Join` the search had NOT built into a fused `*NestedLoopIndexJoin`, behind
// the per-statement `goopg.enable_nestloop_index` and the GOOPG_NLI_COSTGATE
// escape hatch. PG has no such pass — its parameterised index nested loop is
// an ordinary path from add_paths_to_joinrel / match_unsorted_outer
// (joinpath.c). M0147-0050 measured it corpus-inert; once M0147-0053 let the
// search build its last known customer (an outer join with a preserved-side
// ON conjunct), M0147-0051 deleted it with every helper it alone reached.

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
)

// eligibleLeadingPrefixIndexes lists every index on tbl that can start a
// probe bound by innerToOuter, in IndexesOnTable order — the candidate set
// PG's build_index_paths walks, one path per index (indxpath.c).
func eligibleLeadingPrefixIndexes(cat catalog.Catalog, tbl *catalog.Table, innerToOuter map[string]Expr) []*catalog.Index {
	var out []*catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if _, ok := leadingPrefixProbeKeys(tbl, idx, innerToOuter); ok {
			out = append(out, idx)
		}
	}
	return out
}

// leadingPrefixProbeKeys is the per-index eligibility rule shared by
// pickIndexCoveringLeadingPrefix and eligibleLeadingPrefixIndexes: idx can
// drive a probe when it is a non-partial btree whose LEADING column is bound
// (PG's amoptionalkey rule, indxpath.c:1029), and it returns the outer
// expressions bound to the gapless leading prefix.
func leadingPrefixProbeKeys(tbl *catalog.Table, idx *catalog.Index, innerToOuter map[string]Expr) ([]Expr, bool) {
	if !isBTreeIndex(idx) {
		return nil, false
	}
	// Partial index without a proven predicate — decline, same rule and
	// reason as `findBTreeIndexForColumn` / `addOneOrderedIndexPath`.
	// A parameterized inner-side scan drops rows just as silently.
	if idx.HasPredicate {
		return nil, false
	}
	if len(idx.Columns) == 0 {
		return nil, false
	}
	keys := make([]Expr, 0, len(idx.Columns))
	for _, col := range idx.Columns {
		outer, ok := innerToOuter[col]
		if !ok {
			// The first unbound column ends the probe. Columns after
			// it are unreachable: a btree scan is ordered by the whole
			// key, so an unbound column makes every later one a filter
			// rather than a bound. This is PG's `amoptionalkey` rule —
			// only the LEADING column must be bound (indxpath.c:1029).
			break
		}
		keys = append(keys, outer)
	}
	if len(keys) == 0 {
		// Leading column unbound: this index cannot start a scan for
		// this parameterisation at all.
		return nil, false
	}
	// The columns after the bound prefix are unbound, and goopg stores
	// no index entry whose key has a NULL column: a probe on the prefix
	// would miss every inner row with a NULL there (`r.a = o.x` on
	// (a, b) lost (10, NULL)). Only an index whose unbound key columns
	// are all NOT NULL is complete for the probe.
	if !indexUnboundKeysNotNull(tbl, idx, len(keys)) {
		return nil, false
	}
	return keys, true
}

// isBTreeIndex returns true when idx.Method is "btree" (case-insensitive).
func isBTreeIndex(idx *catalog.Index) bool {
	m := idx.Method
	// avoid pulling in strings.ToLower for a hot path; method is
	// always either "btree" or "BTREE".
	if m == "btree" || m == "BTREE" {
		return true
	}
	return false
}

// nliProbeIndexName names the index a rewrite-built NLI probes, for the
// M0145-0007 census line. The probe is an `*IndexScan` in the ordinary case
// and an `*IndexOnlyScan` when `indexOnlyNLIInner` promoted it; anything else
// is reported as-is rather than guessed at.
func nliProbeIndexName(inner Node) string {
	switch x := inner.(type) {
	case *IndexScan:
		return x.Index.Name
	case *IndexOnlyScan:
		return x.Index.Name
	}
	return fmt.Sprintf("(%T)", inner)
}
