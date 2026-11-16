package initdb

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/executor"
)

// pgDependMissingObjects are objects a PG 18.3 initdb creates that goopg's
// bootstrap image does not carry yet, so their pg_depend rows are left out
// rather than pointing at nothing: pg_settings' two rewrite rules
// pg_settings_u (12108) and pg_settings_n (12109), created by
// system_views.sql's CREATE RULE (the UPDATE-through-set_config rules).
// Ledgered under M0149-0001g slice 31.
var pgDependMissingObjects = map[uint32]bool{12108: true, 12109: true}

// pgDependBootstrapRows is pgDependInitdb without the rows whose depender or
// referenced object is in pgDependMissingObjects.
func pgDependBootstrapRows() []pgDependEntry {
	out := make([]pgDependEntry, 0, len(pgDependInitdb))
	for _, d := range pgDependInitdb {
		if pgDependMissingObjects[d.ObjID] || pgDependMissingObjects[d.RefObjID] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// bootstrapPgDepend writes the pg_depend rows of a PG 18.3 initdb
// (pgDependInitdb, scripts/gen-pg-depend.py) to base/{1,5}/2608 in heap order,
// with pg_depend's indexes 2673 (classid, objid, objsubid) and 2674
// (refclassid, refobjid, refobjsubid). These are the dependencies initdb's
// post-bootstrap SQL records — system views and their rules and rowtypes,
// information_schema, the snowball text-search objects, plpgsql. A PG node
// uses them for DROP … CASCADE / RESTRICT and pg_dump's extension-membership
// test. Bootstrap-catalog objects are pinned by OID and need no rows. Must run
// after bootstrapMappedLocalCatalogHeaps, whose placeholder would overwrite
// 2608. M0149-0001g slice 31 (slice 29 wrote only plpgsql's seven rows).
func bootstrapPgDepend(dataDir string) error {
	deps := pgDependBootstrapRows()
	rows := make([]executor.Row, len(deps))
	for i, d := range deps {
		rows[i] = executor.Row{
			executor.NewIntDatum(int64(d.ClassID)), executor.NewIntDatum(int64(d.ObjID)), executor.NewIntDatum(int64(d.ObjSubID)),
			executor.NewIntDatum(int64(d.RefClassID)), executor.NewIntDatum(int64(d.RefObjID)), executor.NewIntDatum(int64(d.RefObjSubID)),
			executor.NewStringDatum(string(d.DepType)),
		}
	}
	tids, err := writeMultiPageHeapRows(dataDir, "2608", executor.PGDependColumnsPG18(), rows)
	if err != nil {
		return fmt.Errorf("pg_depend heap: %w", err)
	}
	for _, ix := range []struct {
		oid  uint32
		keys func(d pgDependEntry) []uint32
	}{
		{2673, func(d pgDependEntry) []uint32 { return []uint32{d.ClassID, d.ObjID, uint32(d.ObjSubID)} }},
		{2674, func(d pgDependEntry) []uint32 { return []uint32{d.RefClassID, d.RefObjID, uint32(d.RefObjSubID)} }},
	} {
		type item struct {
			keys []uint32
			tid  heapTID
		}
		items := make([]item, len(deps))
		for i, d := range deps {
			items[i] = item{ix.keys(d), tids[i]}
		}
		// objsubid / refobjsubid are never negative in these rows, so the
		// unsigned comparison equals int4_ops order.
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i], items[j]
			for k := range a.keys {
				if a.keys[k] != b.keys[k] {
					return a.keys[k] < b.keys[k]
				}
			}
			if a.tid.Block != b.tid.Block {
				return a.tid.Block < b.tid.Block
			}
			return a.tid.Offset < b.tid.Offset
		})
		tuples := make([][]byte, len(items))
		for i, it := range items {
			tuples[i] = pgBuildIndexTupleOidKeys(it.tid.Block, it.tid.Offset, it.keys)
		}
		img, err := pgBuildBtreeBulkLoadSized(tuples, len(tuples[0]), 3)
		if err != nil {
			return fmt.Errorf("pg_depend index %d: %w", ix.oid, err)
		}
		if err := writeLocalIndexFile(dataDir, ix.oid, img); err != nil {
			return err
		}
	}
	return nil
}
