package executor

// B-05a: extended-statistics ANALYZE hook.
//
// Oracle: BuildRelationExtStatistics / statext_compute_stattarget /
// statext_store in postgres/src/backend/statistics/extended_stats.c.
//
// buildAndStoreExtStatistics runs at the end of analyzeRelationWith (where
// the reservoir sample, the table descriptor, the table-wide target and the
// exact row count are all in scope) and writes one pg_statistic_ext_data row
// per eligible statistics object via syncStatisticExtDataRow. Per kind:
//
//	ndistinct    built (extstats_ndistinct.go, mvdistinct.c port)
//	dependencies built (extstats_dependencies.go, dependencies.c port)
//	mcv          DEFERRED — stxdmcv NULL (TOAST wall, see buildStatisticExtDataRow)
//	expressions  DEFERRED — stxdexpr NULL + HasExpr objects skipped outright
//	             (no per-expression ANALYZE in the sample scan)

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// extStatsTarget mirrors statext_compute_stattarget
// (extended_stats.c:343-379): the object's own target wins when set
// (ALTER STATISTICS ... SET STATISTICS n, stxstattarget>=0 — including 0,
// which disables the build); otherwise the maximum target over the member
// columns (ALTER ... ALTER COLUMN ... SET STATISTICS, where an explicit 0
// beats the -1 default and therefore also disables the build); otherwise
// the table-wide target (default_statistics_target upstream, the `target`
// analyzeRelationWith was given here).
func extStatsTarget(obj *catalog.StatisticsObject, tbl *catalog.Table, defaultTarget int) int {
	if obj.StatTarget != nil && *obj.StatTarget >= 0 {
		return *obj.StatTarget
	}
	best := -1
	if tbl != nil {
		for _, name := range obj.Columns {
			for i := range tbl.Columns {
				if tbl.Columns[i].Name == name {
					if t := tbl.Columns[i].StatTarget; t != nil && *t > best {
						best = *t
					}
					break
				}
			}
		}
	}
	if best < 0 {
		return defaultTarget
	}
	return best
}

// extStatsKinds reports which buildable kinds an object requests. An empty
// Kinds list is the default (all kinds enabled — see StatisticsObject.Kinds);
// 'm' maps to the deferred MCV kind and never sets a build flag. Expression
// targets ('e') are not a Kinds entry at all — they arrive via HasExpr and
// skip the whole object (see below).
func extStatsKinds(obj *catalog.StatisticsObject) (ndistinct, dependencies bool) {
	if len(obj.Kinds) == 0 {
		return true, true
	}
	for _, k := range obj.Kinds {
		switch k {
		case "ndistinct":
			ndistinct = true
		case "dependencies":
			dependencies = true
		}
	}
	return ndistinct, dependencies
}

// resolveExtStatsColumns maps an object's simple-column names to reservoir
// indexes and 1-based attnums, mirroring lookup_var_attr_stats: any
// unresolvable column (dropped/renamed since CREATE STATISTICS) makes the
// whole object unbuildable — the caller warns and moves on, like the
// oracle's WARNING + continue.
func resolveExtStatsColumns(obj *catalog.StatisticsObject, tbl *catalog.Table) (colIdxs []int, attnums []int16, ok bool) {
	for _, name := range obj.Columns {
		found := false
		for i := range tbl.Columns {
			if tbl.Columns[i].Name == name && !tbl.Columns[i].Dropped {
				colIdxs = append(colIdxs, i)
				attnums = append(attnums, int16(tbl.Columns[i].Ordinal+1))
				found = true
				break
			}
		}
		if !found {
			return nil, nil, false
		}
	}
	return colIdxs, attnums, true
}

// extStatsCatalog peels wrapper catalogs (SearchPathCatalog et al. — same
// loop as analyzeOp.partitionChildren) down to the *InMemory registry that
// owns the statistics objects.
func extStatsCatalog(cat catalog.Catalog) *catalog.InMemory {
	type unwrapper interface{ Unwrap() catalog.Catalog }
	for c := cat; c != nil; {
		if im, ok := c.(*catalog.InMemory); ok {
			return im
		}
		u, ok := c.(unwrapper)
		if !ok {
			return nil
		}
		c = u.Unwrap()
	}
	return nil
}

// buildAndStoreExtStatistics is the B-05a ANALYZE hook body. ctx supplies the
// heap-write handles (Pool/TxnMgr); target is the table-wide statistics
// target; reservoir the sample; totalRows the exact visible-tuple count.
// Best-effort like persistStatsToPGStatistic: per-object problems warn (or
// skip silently where upstream does), and a heap-write failure aborts the
// remaining objects with an error the caller swallows.
func buildAndStoreExtStatistics(ctx *Context, tbl *catalog.Table, target int, reservoir []Row, totalRows int64) error {
	if ctx == nil || ctx.Pool == nil || tbl == nil {
		return nil
	}
	im := extStatsCatalog(ctx.Catalog)
	if im == nil {
		return nil
	}
	// fetch_statentries_for_relation: only objects on this table.
	objects := im.StatisticsObjectsForTable(tbl.OID)
	for _, obj := range objects {
		// B-05a ledger-defer, expression statistics: an object with
		// expression targets needs per-expression ANALYZE
		// (examine_expression/compute_expr_stats over evaluated
		// expression values); the sample scan holds base columns only,
		// so the whole object is skipped — the flat-column subset would
		// describe the wrong attribute set. Skip point; warn like the
		// oracle's uncomputable-object WARNING.
		if obj.HasExpr {
			ctx.AddWarning(fmt.Sprintf("statistics object %q could not be computed for relation %q: expression statistics are not yet built", obj.Name, tbl.Name))
			continue
		}
		colIdxs, attnums, ok := resolveExtStatsColumns(obj, tbl)
		if !ok || len(colIdxs) == 0 {
			ctx.AddWarning(fmt.Sprintf("statistics object %q could not be computed for relation %q", obj.Name, tbl.Name))
			continue
		}
		// Don't rebuild objects with statistics target 0 — leave whatever
		// row is already stored, exactly like per-column SET STATISTICS 0
		// (columnStatsTarget) and the oracle's `if (stattarget == 0)
		// continue`.
		if extStatsTarget(obj, tbl, target) == 0 {
			continue
		}
		wantND, wantDep := extStatsKinds(obj)
		var ndBlob, depBlob []byte
		if wantND {
			if nd := buildExtNDistinct(reservoir, colIdxs, attnums, float64(totalRows)); nd != nil {
				ndBlob = serializeExtNDistinct(nd)
			}
		}
		if wantDep {
			if deps := buildExtDependencies(reservoir, colIdxs, attnums); deps != nil {
				depBlob = serializeExtDependencies(deps)
			}
		}
		// Catalog heap does NOT toast: NULL any kind whose blob would not
		// fit the page instead of writing a corrupt oversized tuple
		// (persistStatsToPGStatistic truncates pg_statistic rows for the
		// same wall; ext blobs have no truncatable middle — a partial
		// ndistinct item list would fail exact-consumption decode — so
		// the kind is dropped whole). ndistinct at the 8-column cap is
		// ~4 KB and always fits; this guard exists for adversarial
		// dependency enumerations.
		ndBlob, depBlob = fitExtStatsBlobs(stxOIDOf(obj), ndBlob, depBlob)
		if err := syncStatisticExtDataRow(ctx, obj.OID, ndBlob, depBlob); err != nil {
			return err
		}
	}
	return nil
}

// stxOIDOf is a nil-safe OID fetch for the size-guard call below.
func stxOIDOf(obj *catalog.StatisticsObject) uint32 {
	if obj == nil {
		return 0
	}
	return obj.OID
}

// fitExtStatsBlobs NULLs kinds (dependencies first — the unbounded one) until
// the _data row fits MaxHeapTupleSize. Returns the surviving blobs.
func fitExtStatsBlobs(stxOID uint32, ndistinct, deps []byte) ([]byte, []byte) {
	cols := PGStatisticExtDataColumnsPG18()
	fits := func(nd, dp []byte) bool {
		n, err := pgStatisticRowTupleLen(cols, buildStatisticExtDataRow(stxOID, nd, dp))
		return err == nil && n <= storage.MaxHeapTupleSize
	}
	if fits(ndistinct, deps) {
		return ndistinct, deps
	}
	if fits(ndistinct, nil) {
		return ndistinct, nil
	}
	if fits(nil, deps) {
		return nil, deps
	}
	return nil, nil
}
