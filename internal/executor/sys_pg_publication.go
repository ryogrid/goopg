package executor

// B3.3 (docs/design/wal-pg-identical-stream/02d §2): CREATE/ALTER/DROP
// PUBLICATION journal as real pg_publication + pg_publication_rel heap rows,
// replacing the bespoke RecordKindCreatePublication(50)/DropPublication(51)/
// AlterPublicationOwner(52). The base pg_publication row is all-scalar
// (name + owner OID + the publish-action bools); a FOR TABLE publication's
// member relations live in pg_publication_rel (one row per table, prrelid =
// the resolved pg_class OID, prqual/prattrs NULL — goopg models neither row
// filters nor column lists). SUBSCRIPTION (kinds 53-55) stays on its bespoke
// records: pg_subscription is a SHARED catalog (global/) and converts in B4.
//
// Column layouts: postgres/src/include/catalog/pg_publication.h,
// pg_publication_rel.h. goopg does not model pubtruncate/pubviaroot, so both
// bools journal as false (matching goopg's own publication semantics).

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// pg_publication / pg_publication_rel relation + index OIDs.
const (
	pgPublicationRelOID             = 6104
	pgPublicationOidIndexOID        = 6110
	pgPublicationPubnameIndexOID    = 6111
	pgPublicationRelRelOID          = 6106
	pgPublicationRelOidIndexOID     = 6112
	pgPublicationRelPrrelidPrpubOID = 6113
)

// PGPublicationColumnsPG18 mirrors FormData_pg_publication (9 columns).
// Exported for the initdb reload.
func PGPublicationColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "pubname", Type: catalog.Type{Name: "name"}},
		{Name: "pubowner", Type: catalog.Type{Name: "oid"}},
		{Name: "puballtables", Type: catalog.Type{Name: "bool"}},
		{Name: "pubinsert", Type: catalog.Type{Name: "bool"}},
		{Name: "pubupdate", Type: catalog.Type{Name: "bool"}},
		{Name: "pubdelete", Type: catalog.Type{Name: "bool"}},
		{Name: "pubtruncate", Type: catalog.Type{Name: "bool"}},
		{Name: "pubviaroot", Type: catalog.Type{Name: "bool"}},
	}
}

// PGPublicationRelColumnsPG18 mirrors FormData_pg_publication_rel (5 columns).
func PGPublicationRelColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "prpubid", Type: catalog.Type{Name: "oid"}},
		{Name: "prrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "prqual", Type: catalog.Type{Name: "pg_node_tree"}},
		{Name: "prattrs", Type: catalog.Type{Name: "int2vector"}},
	}
}

func buildPGPublicationRow(pub *catalog.Publication) Row {
	owner := pub.Owner
	if owner == 0 {
		owner = 10
	}
	return Row{
		NewIntDatum(int64(pub.OID)),
		NewStringDatum(pub.Name),
		NewIntDatum(int64(owner)),
		NewBoolDatum(pub.AllTables),
		NewBoolDatum(pub.PublishInsert),
		NewBoolDatum(pub.PublishUpdate),
		NewBoolDatum(pub.PublishDelete),
		NewBoolDatum(false), // pubtruncate — not modeled
		NewBoolDatum(false), // pubviaroot — not modeled
	}
}

func pgPublicationRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgPublicationRelOID, Fork: storage.MainFork}
}

func pgPublicationRelRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgPublicationRelRelOID, Fork: storage.MainFork}
}

// upsertPublicationCatalogRow journals one publication's CURRENT base-row
// state: INSERT at CREATE, canonical non-HOT heap UPDATE at the cached TID
// for ALTER ... OWNER. Member relations are written separately (they never
// change after CREATE in goopg's model — no ALTER PUBLICATION ADD/DROP TABLE
// durability record exists).
func upsertPublicationCatalogRow(ctx *Context, pub *catalog.Publication) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	row := buildPGPublicationRow(pub)
	var tid storage.ItemPointer
	var err error
	if old, ok := im.PublicationHeapTID(pub.OID); ok {
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(old.Block), Offset: old.Offset}
		tid, err = updateHeapRowCanonicalPG(ctx, pgPublicationRel(), PGPublicationColumnsPG18(), oldTID, row)
	} else {
		tid, err = writeHeapRowCanonical(ctx, pgPublicationRel(), PGPublicationColumnsPG18(), row)
	}
	if err != nil {
		return err
	}
	im.SetPublicationHeapTID(pub.OID, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgPublicationOidIndexOID,
		buildIndexTupleOidKey(blk, off, pub.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgPublicationPubnameIndexOID,
		buildIndexTupleNameKey(blk, off, pub.Name), cmpKeyName); err != nil {
		return err
	}
	mirrorPublicationCatalogFiles(ctx)
	return nil
}

// writePublicationMemberRows journals one pg_publication_rel row per member
// table (FOR TABLE publications). Called only at CREATE — goopg has no
// durable ALTER PUBLICATION ADD/DROP TABLE. prqual/prattrs are NULL.
func writePublicationMemberRows(ctx *Context, pub *catalog.Publication) error {
	if !catalogHeapSyncAvailable(ctx) || pub.AllTables {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	for _, qname := range pub.Tables {
		relOID := publicationTableOID(im, ctx, qname)
		if relOID == 0 {
			continue
		}
		prOID := im.AllocOID()
		row := Row{
			NewIntDatum(int64(prOID)),
			NewIntDatum(int64(pub.OID)),
			NewIntDatum(int64(relOID)),
			NullDatum, // prqual — no row filter
			NullDatum, // prattrs — no column list
		}
		tid, err := writeHeapRowCanonical(ctx, pgPublicationRelRel(), PGPublicationRelColumnsPG18(), row)
		if err != nil {
			return err
		}
		blk, off := uint32(tid.Block), tid.Offset
		if err := insertCanonicalSysBtreeLeaf(ctx, pgPublicationRelOidIndexOID,
			buildIndexTupleOidKey(blk, off, prOID), cmpKeyUint32); err != nil {
			return err
		}
		if err := insertCanonicalSysBtreeLeaf(ctx, pgPublicationRelPrrelidPrpubOID,
			buildIndexTupleOidOidKey(blk, off, relOID, pub.OID), cmpKeyOidOid); err != nil {
			return err
		}
	}
	mirrorPublicationCatalogFiles(ctx)
	return nil
}

// publicationTableOID resolves a publication's stored qualified table name
// ("schema.name") to its pg_class OID.
func publicationTableOID(im *catalog.InMemory, ctx *Context, qname string) uint32 {
	schema, name := "public", qname
	if dot := indexByteLast(qname, '.'); dot >= 0 {
		schema, name = qname[:dot], qname[dot+1:]
	}
	if tbl, ok := im.LookupTable(parser.ObjectName{Schema: schema, Name: name},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)); ok && tbl != nil {
		return tbl.OID
	}
	return 0
}

func indexByteLast(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// deletePublicationCatalogRows stamps xmax on the publication's base row and
// all its pg_publication_rel member rows (DROP PUBLICATION).
func deletePublicationCatalogRows(ctx *Context, pubOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampCatalogRows(ctx, pgPublicationRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == pubOID
	})
	// pg_publication_rel: prpubid is column 1 (offset 4).
	stampCatalogRows(ctx, pgPublicationRelRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 8 && binary.LittleEndian.Uint32(data[4:8]) == pubOID
	})
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		im.DropPublicationHeapTID(pubOID)
	}
	mirrorPublicationCatalogFiles(ctx)
}

// mirrorPublicationCatalogFiles propagates both heaps + their indexes to the
// postgres DB's copies (reload reads base/5).
func mirrorPublicationCatalogFiles(ctx *Context) {
	for _, oid := range []uint32{
		pgPublicationRelOID, pgPublicationOidIndexOID, pgPublicationPubnameIndexOID,
		pgPublicationRelRelOID, pgPublicationRelOidIndexOID, pgPublicationRelPrrelidPrpubOID,
	} {
		_ = mirrorCatalogRelToPostgresDB(ctx, oid)
	}
}
