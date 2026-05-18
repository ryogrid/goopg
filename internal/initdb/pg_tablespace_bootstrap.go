// M0106-0010 batched-12: seed 2 default pg_tablespace rows and their indexes.
// pg_tablespace is a shared catalog (global/1213); the two default rows
// (pg_default=1663, pg_global=1664) must exist so PG's TABLESPACEOID
// syscache lookups find them during InitPostgres.
//
// Per postgres/src/include/catalog/pg_tablespace_d.h:42-43:
//
//	#define DEFAULTTABLESPACE_OID 1663
//	#define GLOBALTABLESPACE_OID  1664

package initdb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage"
)

// pgTablespaceEntry holds the OID, spcname, and heap-page TID for a seeded
// pg_tablespace row. The TID is used to build the btree index leaves.
type pgTablespaceEntry struct {
	OID     uint32
	Spcname string
	TID     heapTID
}

// pgTablespaceColDefs returns the 5-column PG18 pg_tablespace schema per
// postgres/src/include/catalog/pg_tablespace.h:29-41 and pg_tablespace_d.h.
func pgTablespaceColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "spcname", Type: catalog.Type{Name: "name"}, Ordinal: 1},
		{Name: "spcowner", Type: catalog.Type{Name: "oid"}, Ordinal: 2},
		{Name: "spcacl", Type: catalog.Type{Name: "aclitem[]"}, Ordinal: 3},
		{Name: "spcoptions", Type: catalog.Type{Name: "text[]"}, Ordinal: 4},
	}
}

// bootstrapPgTablespaceTuples overwrites the empty placeholder at global/1213
// with two pg_tablespace heap rows: pg_default (OID 1663) and pg_global
// (OID 1664). Both rows have spcowner=BOOTSTRAP_SUPERUSERID(10) and
// NULL spcacl/spcoptions, matching a fresh vanilla PG18 initdb cluster.
//
// Returns the heap-page TIDs for use by the index bootstrap functions.
func bootstrapPgTablespaceTuples(dataDir string) ([]pgTablespaceEntry, error) {
	cols := pgTablespaceColDefs()

	buildRow := func(oid int64, spcname string) executor.Row {
		return executor.Row{
			executor.NewIntDatum(oid),
			executor.NewStringDatum(spcname),
			executor.NewIntDatum(10), // BOOTSTRAP_SUPERUSERID
			executor.NullDatum,       // spcacl NULL
			executor.NullDatum,       // spcoptions NULL
		}
	}

	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}

	seeds := []struct {
		oid     int64
		spcname string
	}{
		{1663, "pg_default"}, // DEFAULTTABLESPACE_OID
		{1664, "pg_global"},  // GLOBALTABLESPACE_OID
	}

	entries := make([]pgTablespaceEntry, 0, len(seeds))
	for _, s := range seeds {
		row := buildRow(s.oid, s.spcname)
		payload, err := executor.EncodeRowPG(cols, row)
		if err != nil {
			return nil, fmt.Errorf("encode pg_tablespace row %d: %w", s.oid, err)
		}
		bitmap := executor.NullBitmapPG(row)
		tuple := storage.NewHeapTupleWithNulls(
			storage.FrozenTransactionID,
			storage.InvalidTransactionID,
			bitmap,
			payload,
		)
		tuple.Header.SetNatts(len(cols))
		slot, err := storage.PageAddHeapTuple(page, tuple)
		if err != nil {
			return nil, fmt.Errorf("add pg_tablespace tuple %d: %w", s.oid, err)
		}
		entries = append(entries, pgTablespaceEntry{
			OID:     uint32(s.oid),
			Spcname: s.spcname,
			TID:     heapTID{Block: 0, Offset: slot},
		})
	}

	path := filepath.Join(dataDir, "global", "1213")
	if err := os.WriteFile(path, page, 0o600); err != nil {
		return nil, fmt.Errorf("write pg_tablespace heap: %w", err)
	}
	return entries, nil
}

// bootstrapPgTablespaceOidIndex overwrites the empty btree placeholder at
// global/2697 with a populated 2-page btree (metapage + leaf-root) carrying
// one oid-keyed IndexTuple per pg_tablespace row.
//
//	pg_tablespace_oid_index (OID 2697): UNIQUE PRIMARY btree(oid oid_ops)
//	per postgres/src/include/catalog/pg_tablespace.h:52
//	DECLARE_UNIQUE_INDEX_PKEY(pg_tablespace_oid_index, 2697,
//	    TablespaceOidIndexId, pg_tablespace, btree(oid oid_ops));
func bootstrapPgTablespaceOidIndex(dataDir string, entries []pgTablespaceEntry) error {
	sorted := append([]pgTablespaceEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].OID < sorted[j].OID })

	tuples := make([][]byte, len(sorted))
	for i, e := range sorted {
		tuples[i] = pgBuildIndexTupleOidKey(e.TID.Block, e.TID.Offset, e.OID)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_tablespace_oid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1, 0)
	file := append(meta, leaf...)
	return os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(2697, 10)), file, 0o600)
}

// bootstrapPgTablespaceSpcnameIndex overwrites the empty btree placeholder at
// global/2698 with a populated 2-page btree (metapage + leaf-root) carrying
// one name-keyed IndexTuple per pg_tablespace row.
//
//	pg_tablespace_spcname_index (OID 2698): UNIQUE btree(spcname name_ops)
//	per postgres/src/include/catalog/pg_tablespace.h:53
//	DECLARE_UNIQUE_INDEX(pg_tablespace_spcname_index, 2698,
//	    TablespaceNameIndexId, pg_tablespace, btree(spcname name_ops));
func bootstrapPgTablespaceSpcnameIndex(dataDir string, entries []pgTablespaceEntry) error {
	sorted := append([]pgTablespaceEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Spcname < sorted[j].Spcname })

	tuples := make([][]byte, len(sorted))
	for i, e := range sorted {
		tuples[i] = pgBuildIndexTupleNameKey(e.TID.Block, e.TID.Offset, e.Spcname)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_tablespace_spcname_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1, 0)
	file := append(meta, leaf...)
	return os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(2698, 10)), file, 0o600)
}
