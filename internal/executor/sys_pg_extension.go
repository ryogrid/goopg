package executor

// M0130-S3: CREATE/DROP EXTENSION journals as real pg_extension heap rows
// (base/<db>/3079) + index entries (3080 oid / 3081 name), replacing the
// in-memory-only registry. Reload reads the heap on restart.
//
// pg_extension (3079) is a per-database catalog; column layout mirrors
// FormData_pg_extension (postgres/src/include/catalog/pg_extension.h):
// oid, extname, extowner, extnamespace, extrelocatable, extversion,
// extconfig, extcondition. extconfig/extcondition are always NULL in goopg
// (no extension config tables).

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const (
	pgExtensionRelOID       = 3079
	pgExtensionOidIndexOID  = 3080
	pgExtensionNameIndexOID = 3081
)

// PGExtensionColumnsPG18 mirrors FormData_pg_extension (8 columns).
// Exported for the initdb reload.
func PGExtensionColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "extname", Type: catalog.Type{Name: "name"}},
		{Name: "extowner", Type: catalog.Type{Name: "oid"}},
		{Name: "extnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "extrelocatable", Type: catalog.Type{Name: "bool"}},
		{Name: "extversion", Type: catalog.Type{Name: "text"}},
		{Name: "extconfig", Type: catalog.Type{Name: "oid[]"}},
		{Name: "extcondition", Type: catalog.Type{Name: "text[]"}},
	}
}

// buildPGExtensionRow builds a pg_extension heap row from an extensionRow
// registry entry. extconfig/extcondition are always NULL in goopg (no
// extension config tables). extrelocatable is always false (goopg does not
// support ALTER EXTENSION SET SCHEMA).
func buildPGExtensionRow(extOID, namespaceOID uint32, name, version string) Row {
	return Row{
		NewIntDatum(int64(extOID)),     // oid
		NewStringDatum(name),           // extname
		NewIntDatum(10),                // extowner (bootstrap superuser)
		NewIntDatum(int64(namespaceOID)), // extnamespace
		NewBoolDatum(false),            // extrelocatable
		NewStringDatum(version),        // extversion
		NullDatum,                      // extconfig
		NullDatum,                      // extcondition
	}
}

func pgExtensionRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  tableCatalogHeapDBOid(ctx),
		RelOid: pgExtensionRelOID,
		Fork:   storage.MainFork,
	}
}

// writeExtensionCatalogRow journals CREATE EXTENSION: a heap INSERT into
// base/<db>/3079 + both index entries (3080 oid / 3081 name).
func writeExtensionCatalogRow(ctx *Context, extOID, namespaceOID uint32, name, version string) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	row := buildPGExtensionRow(extOID, namespaceOID, name, version)
	tid, err := writeHeapRowCanonical(ctx, pgExtensionRel(ctx), PGExtensionColumnsPG18(), row)
	if err != nil {
		return fmt.Errorf("pg_extension: %w", err)
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgExtensionOidIndexOID,
		buildIndexTupleOidKey(blk, off, extOID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgExtensionNameIndexOID,
		buildIndexTupleNameKey(blk, off, name), cmpKeyName); err != nil {
		return err
	}
	mirrorExtensionCatalogFiles(ctx)
	return nil
}

// deleteExtensionCatalogRow stamps xmax on the extension's pg_extension row
// (DROP EXTENSION).
func deleteExtensionCatalogRow(ctx *Context, extOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgExtensionRel(ctx), ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == extOID
	})
	mirrorExtensionCatalogFiles(ctx)
}

// mirrorExtensionCatalogFiles propagates the pg_extension heap + both indexes
// to the postgres DB's copies (reload reads base/5).
func mirrorExtensionCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgExtensionRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgExtensionOidIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgExtensionNameIndexOID)
}
