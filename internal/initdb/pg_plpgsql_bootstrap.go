package initdb

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/executor"
)

// pgExtensionEntry is one pg_extension row (extconfig / extcondition NULL).
type pgExtensionEntry struct {
	OID         uint32
	Name        string
	Namespace   uint32
	Relocatable bool
	Version     string
}

// pgDependEntry is one pg_depend row (FormData_pg_depend).
type pgDependEntry struct {
	ClassID, ObjID       uint32
	ObjSubID             int32
	RefClassID, RefObjID uint32
	RefObjSubID          int32
	DepType              byte
}

// bootstrapPgPlpgsql writes what PG 18.3's initdb leaves after CREATE
// EXTENSION plpgsql (initdb.c load_plpgsql): the pg_extension row with its
// indexes 3080 (oid) / 3081 (extname), and the extension's pg_depend rows with
// pg_depend's indexes 2673 (classid, objid, objsubid) / 2674 (refclassid,
// refobjid, refobjsubid). The handler functions and the pg_language row join
// the pg_proc and pg_language seeds; the descriptions come with
// catalog.PGInitdbDescriptions. Must run after bootstrapMappedLocalCatalogHeaps,
// whose placeholders would overwrite 2608 / 3079. M0149-0001g slice 29.
func bootstrapPgPlpgsql(dataDir string) error {
	e := pgPlpgsqlExtension
	extRow := executor.Row{
		executor.NewIntDatum(int64(e.OID)), executor.NewStringDatum(e.Name), executor.NewIntDatum(10),
		executor.NewIntDatum(int64(e.Namespace)), executor.NewBoolDatum(e.Relocatable), executor.NewStringDatum(e.Version),
		executor.NullDatum, executor.NullDatum,
	}
	extTIDs, err := writeMultiPageHeapRows(dataDir, "3079", executor.PGExtensionColumnsPG18(), []executor.Row{extRow})
	if err != nil {
		return fmt.Errorf("pg_extension heap: %w", err)
	}
	t := extTIDs[0]
	oidIdx, err := pgBuildBtreeBulkLoad([][]byte{pgBuildIndexTupleOidKey(t.Block, t.Offset, e.OID)}, 1)
	if err != nil {
		return fmt.Errorf("pg_extension_oid_index: %w", err)
	}
	if err := writeLocalIndexFile(dataDir, 3080, oidIdx); err != nil {
		return err
	}
	nameIdx, err := pgBuildBtreeBulkLoadSized([][]byte{pgBuildIndexTupleNameKey(t.Block, t.Offset, e.Name)}, 72, 1)
	if err != nil {
		return fmt.Errorf("pg_extension_name_index: %w", err)
	}
	if err := writeLocalIndexFile(dataDir, 3081, nameIdx); err != nil {
		return err
	}

	rows := make([]executor.Row, len(pgPlpgsqlDepends))
	for i, d := range pgPlpgsqlDepends {
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
		items := make([]item, len(pgPlpgsqlDepends))
		for i, d := range pgPlpgsqlDepends {
			items[i] = item{ix.keys(d), tids[i]}
		}
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
