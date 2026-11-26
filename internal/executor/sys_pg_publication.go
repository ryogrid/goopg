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
// pg_publication_rel.h. goopg does not model pubviaroot, so it journals as
// false (matching goopg's own publication semantics).

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

	// pg_publication_namespace (pg_publication_namespace.h): the FOR
	// TABLES IN SCHEMA members. M0149-0053a.
	pgPublicationNamespaceRelOID       = 6237
	pgPublicationNamespaceOidIndexOID  = 6238
	pgPublicationNamespacePnnspidPnpub = 6239
)

// PGPublicationColumnsPG18 mirrors FormData_pg_publication (10 columns).
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
		{Name: "pubgencols", Type: catalog.Type{Name: "char"}},
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

// PGPublicationNamespaceColumnsPG18 mirrors FormData_pg_publication_namespace
// (oid, pnpubid, pnnspid).
func PGPublicationNamespaceColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "pnpubid", Type: catalog.Type{Name: "oid"}},
		{Name: "pnnspid", Type: catalog.Type{Name: "oid"}},
	}
}

func pgPublicationNamespaceRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgPublicationNamespaceRelOID, Fork: storage.MainFork}
}

// writePublicationNamespaceRows journals one pg_publication_namespace row
// per schema added to pub (publication_add_schema): oid, pnpubid, pnnspid,
// plus both index entries.
func writePublicationNamespaceRows(ctx *Context, pub *catalog.Publication, schemas []string) error {
	if !catalogHeapSyncAvailable(ctx) || len(schemas) == 0 {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	for _, sch := range schemas {
		nsp := im.SchemaOID(sch, dbOid)
		if nsp == 0 {
			continue
		}
		pnOID := im.AllocOID()
		row := Row{NewIntDatum(int64(pnOID)), NewIntDatum(int64(pub.OID)), NewIntDatum(int64(nsp))}
		tid, err := writeHeapRowCanonical(ctx, pgPublicationNamespaceRel(), PGPublicationNamespaceColumnsPG18(), row)
		if err != nil {
			return err
		}
		blk, off := uint32(tid.Block), tid.Offset
		if err := insertCanonicalSysBtreeLeaf(ctx, pgPublicationNamespaceOidIndexOID,
			buildIndexTupleOidKey(blk, off, pnOID), cmpKeyUint32); err != nil {
			return err
		}
		if err := insertCanonicalSysBtreeLeaf(ctx, pgPublicationNamespacePnnspidPnpub,
			buildIndexTupleOidOidKey(blk, off, nsp, pub.OID), cmpKeyOidOid); err != nil {
			return err
		}
	}
	mirrorPublicationCatalogFiles(ctx)
	return nil
}

// deletePublicationNamespaceRows stamps xmax on publication pubOID's
// pg_publication_namespace rows for the given schemas (ALTER PUBLICATION
// ... DROP / SET TABLES IN SCHEMA).
func deletePublicationNamespaceRows(ctx *Context, pubOID uint32, schemas []string) {
	if !catalogHeapSyncAvailable(ctx) || len(schemas) == 0 {
		return
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return
	}
	dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	nsps := map[uint32]bool{}
	for _, sch := range schemas {
		if oid := im.SchemaOID(sch, dbOid); oid != 0 {
			nsps[oid] = true
		}
	}
	if len(nsps) == 0 {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	// pnpubid at offset 4, pnnspid at offset 8.
	stampCatalogRows(ctx, pgPublicationNamespaceRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 12 && binary.LittleEndian.Uint32(data[4:8]) == pubOID &&
			nsps[binary.LittleEndian.Uint32(data[8:12])]
	})
	mirrorPublicationCatalogFiles(ctx)
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
		NewBoolDatum(pub.PublishTruncate),
		NewBoolDatum(false), // pubviaroot — not modeled
		NewStringDatum(string(pub.GenColsType())),
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
// table (FOR TABLE publications). prqual/prattrs are NULL.
func writePublicationMemberRows(ctx *Context, pub *catalog.Publication) error {
	return writePublicationMemberRowsFor(ctx, pub, pub.Tables)
}

// writePublicationMemberRowsFor journals pg_publication_rel rows for the
// given member tables of pub (CREATE PUBLICATION's list, or the tables an
// ALTER PUBLICATION added).
func writePublicationMemberRowsFor(ctx *Context, pub *catalog.Publication, tables []string) error {
	if !catalogHeapSyncAvailable(ctx) || pub.AllTables || len(tables) == 0 {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	for _, qname := range tables {
		relOID := publicationTableOID(im, ctx, qname)
		if relOID == 0 {
			continue
		}
		prOID := im.AllocOID()
		prattrs := NullDatum
		if cols, ok := pub.Columns[qname]; ok {
			if tbl := publicationTable(im, ctx, qname); tbl != nil {
				prattrs = NewBytesDatum(pgInt2VectorBytes(publicationColumnAttnums(tbl, cols)))
			}
		}
		row := Row{
			NewIntDatum(int64(prOID)),
			NewIntDatum(int64(pub.OID)),
			NewIntDatum(int64(relOID)),
			NullDatum, // prqual — no row filter
			prattrs,
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
	if tbl := publicationTable(im, ctx, qname); tbl != nil {
		return tbl.OID
	}
	return 0
}

// publicationTable resolves a publication member's qualified name in the
// connection's database (nil when it does not exist).
func publicationTable(im *catalog.InMemory, ctx *Context, qname string) *catalog.Table {
	schema, name := "public", qname
	if dot := indexByteLast(qname, '.'); dot >= 0 {
		schema, name = qname[:dot], qname[dot+1:]
	}
	if tbl, ok := im.LookupTable(parser.ObjectName{Schema: schema, Name: name},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)); ok && tbl != nil {
		return tbl
	}
	return nil
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
	// pg_publication_namespace: pnpubid is column 1 (offset 4).
	stampCatalogRows(ctx, pgPublicationNamespaceRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 8 && binary.LittleEndian.Uint32(data[4:8]) == pubOID
	})
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		im.DropPublicationHeapTID(pubOID)
	}
	mirrorPublicationCatalogFiles(ctx)
}

// deletePublicationMemberRows stamps xmax on the pg_publication_rel rows of
// the given member tables of publication pubOID (ALTER PUBLICATION ... DROP /
// SET TABLE).
func deletePublicationMemberRows(ctx *Context, pubOID uint32, tables []string) {
	if !catalogHeapSyncAvailable(ctx) || len(tables) == 0 {
		return
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return
	}
	rels := map[uint32]bool{}
	for _, q := range tables {
		if oid := publicationTableOID(im, ctx, q); oid != 0 {
			rels[oid] = true
		}
	}
	if len(rels) == 0 {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	// pg_publication_rel: prpubid at offset 4, prrelid at offset 8.
	stampCatalogRows(ctx, pgPublicationRelRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 12 && binary.LittleEndian.Uint32(data[4:8]) == pubOID &&
			rels[binary.LittleEndian.Uint32(data[8:12])]
	})
	mirrorPublicationCatalogFiles(ctx)
}

// mirrorPublicationCatalogFiles propagates both heaps + their indexes to the
// postgres DB's copies (reload reads base/5).
func mirrorPublicationCatalogFiles(ctx *Context) {
	for _, oid := range []uint32{
		pgPublicationRelOID, pgPublicationOidIndexOID, pgPublicationPubnameIndexOID,
		pgPublicationRelRelOID, pgPublicationRelOidIndexOID, pgPublicationRelPrrelidPrpubOID,
		pgPublicationNamespaceRelOID, pgPublicationNamespaceOidIndexOID, pgPublicationNamespacePnnspidPnpub,
	} {
		_ = mirrorCatalogRelToPostgresDB(ctx, oid)
	}
}

// publicationColumnAttnums is a column list's attnums in table order
// (prattrs).
func publicationColumnAttnums(tbl *catalog.Table, cols []string) []int16 {
	want := map[string]bool{}
	for _, c := range cols {
		want[c] = true
	}
	var out []int16
	for _, c := range tbl.Columns {
		if want[c.Name] && !c.Dropped {
			out = append(out, int16(c.Ordinal+1))
		}
	}
	return out
}
