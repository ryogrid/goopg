package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
)

// Small-dimension detection — M0125-0043.
//
// Until this milestone the "small dimension" property was a NAME LOOKUP: two
// production writers (`internal/executor/operators_ddl.go` at CREATE TABLE and
// `internal/initdb/open.go` at catalog reload) set `catalog.Table.SmallDimension`
// for a relation literally called `region` or `nation`, the two tiny TPC-H
// dimension tables. That made a benchmark's schema part of goopg's production
// planner: a user table called `nation` got a different plan shape than the same
// table called `nations`, and every non-TPC-H tiny dimension got none of the
// treatment. Both writers are gone; the property is now DERIVED from the
// relation's size, which is what the flag always meant.
//
// The derivation runs at plan-build time, at the scan leaf, and is stamped onto
// the node (`SeqScan.SmallDim` / `IndexScan.SmallDim`) exactly like
// `EstRelRows` — for the same reason recorded on that field: the consumers
// (`IsSmallDimensionSide`) take only a `Node` and run with no catalog in scope,
// including from EXPLAIN inside the executor.
//
// See docs/design/0125-0043-smalldimension-name-tag-extinction.md.

// smallDimensionMaxRows is the row count at or below which a base relation
// counts as a small dimension.
//
// 1024 is deliberately the same number as `smallAnchorRowsThreshold`
// (equiv_class.go): both answer the question "is this relation small enough
// that the planner may pin decisions on it without stats", and having them
// disagree would mean a relation could anchor an inferred equality edge while
// being denied the build side of the very join that edge produces.
//
// What the number means in the two states the planner actually meets:
//
//   - ANALYZEd: the real row count. TPC-H `region` is 5 and `nation` is 25, so
//     both stay flagged, and `supplier` (10k at SF=1) does not.
//   - Cold (goopg's normal state — TableStats does not survive a restart, and
//     ANALYZE stats are per-connection): the estimate_rel_size fallback derives
//     rows from the LIVE block count. Upstream's never-analyzed 10-page floor
//     (`neverAnalyzedMinPages`) dominates there, so any relation occupying
//     fewer than 10 blocks estimates `10 * density` rows — 170 for both
//     `region` and `nation` on the TPC-H schema. The threshold is therefore
//     effectively "the whole heap fits in well under a hundred kilobytes",
//     which is the physical property the name tag was standing in for.
//
// A relation the catalog cannot report a block count for (no storage — the
// in-memory catalogs most planner unit tests build) estimates 0 and is NOT
// small: "no estimate" must never read as "tiny".
const smallDimensionMaxRows = 1024

// smallDimensionTag computes the small-dimension property of a base relation.
// It is the single derivation point; every scan leaf stamps its result.
//
// `tbl.SmallDimension` survives as an EXPLICIT HINT with no production writer:
// `internal/testutil/tpch/tpch.go` sets it on catalog-only fixtures that have
// no heap to measure, so the planner's TPC-H unit tests keep exercising the
// small-dimension paths without a loaded cluster. Production relations reach
// the same answer through their size.
func smallDimensionTag(cat catalog.Catalog, tbl *catalog.Table) bool {
	if tbl == nil {
		return false
	}
	if tbl.SmallDimension {
		return true
	}
	if tbl.Virtual {
		// A virtual relation has no heap to measure. Its row count is whatever
		// its VirtualRows closure produces, which is not knowable here.
		return false
	}
	rows := smallDimensionRows(cat, tbl)
	return rows > 0 && rows <= smallDimensionMaxRows
}

// smallDimensionRows is the row count smallDimensionTag thresholds: the
// ANALYZE-derived count when this session has one, and otherwise the
// block-count fallback (estimate_rel_size).
//
// The fallback is read UNGATED — deliberately not through
// `relSizeFallbackRows`. GOOPG_RELSIZE_FALLBACK stages which *cost* consumers
// trust a block-derived cardinality; turning it off must restore
// pre-M0125-0003 plans, and pre-M0125-0003 plans had the small-dimension
// property populated (by name) at every stage. Gating it here would make
// `GOOPG_RELSIZE_FALLBACK=0` silently drop the build-side pinning that
// M0054-0010 added, which is a different change than the knob promises.
func smallDimensionRows(cat catalog.Catalog, tbl *catalog.Table) int64 {
	if rows := tableRows(tbl); rows > 0 {
		return rows
	}
	return estimateTableRowsFallback(cat, tbl)
}

// smallDimensionSide reports whether a plan node reads from a relation the
// planner tagged small, tolerating a nil node and falling back to the catalog
// hint when the caller has a binding but no scan (the bushy DP's
// `estimateBaseRelInfo` is called with a nil leaf for bindings that produced no
// scan of their own).
func smallDimensionSide(n Node, tbl *catalog.Table) bool {
	if IsSmallDimensionSide(n) {
		return true
	}
	return n == nil && tbl != nil && tbl.SmallDimension
}
