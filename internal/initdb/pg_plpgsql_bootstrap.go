package initdb

import (
	"fmt"

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

// bootstrapPgPlpgsql writes the pg_extension row PG 18.3's initdb leaves after
// CREATE EXTENSION plpgsql (initdb.c load_plpgsql), with its indexes 3080
// (oid) / 3081 (extname). The handler functions and the pg_language row join
// the pg_proc and pg_language seeds, the extension's pg_depend rows come with
// bootstrapPgDepend, and the descriptions with catalog.PGInitdbDescriptions.
// Must run after bootstrapMappedLocalCatalogHeaps, whose placeholder would
// overwrite 3079. M0149-0001g slice 29.
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

	return nil
}
