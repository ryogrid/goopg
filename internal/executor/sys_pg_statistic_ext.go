package executor

// B5 Bstat (retire RmgrGoopgCatalog=128): extended-statistics objects
// (CREATE/DROP/ALTER STATISTICS) now journal as real pg_statistic_ext heap
// rows (per-DB, base/<dbOid>/3381) via XLOG_HEAP_INSERT/DELETE, replacing the
// goopg-private RecordKindCreateStatistics(95)/DropStatistics(96)/
// AlterStatisticsRename(97)/Owner(98)/SetSchema(99) records. pg_statistic_ext
// was a VIRTUAL view over the statisticsObjs registry; the registry stays as
// the query path (a PG standby reads the heap — the "registry shadows the
// nailed heap" pattern from B3.3 pg_publication) and the heap row is the
// standby copy + reload source. A real PG18 standby replays the heap inserts
// (no rmid-128).
//
// Column layout mirrors FormData_pg_statistic_ext (PHYSICAL attnum order —
// stxkeys precedes the CATALOG_VARLEN group, unlike goopg's virtual builder):
//   oid, stxrelid, stxname, stxnamespace, stxowner, stxkeys(int2vector),
//   stxstattarget(int2, nullable), stxkind(char[]), stxexprs(pg_node_tree).
// stxkeys carries the simple-column attnums (faithful; decoded on reload back
// to column names via the owning table); stxkind carries the requested-kind
// chars ('d'/'f'/'m', plus 'e' when the object has expression targets); stxkind
// is decoded back to goopg's kind strings. stxexprs stores the deparsed
// expression targets as a goopg text[] literal (the pg_node_tree-as-text
// convention shared with pg_attrdef.adbin / pg_index.indpred — canonical
// node-tree bytes are a SEPARATE track, only needed when a standby EVALUATES
// the statistics, never at WAL replay). Header: postgres/src/include/catalog/
// pg_statistic_ext.h.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const pgStatisticExtRelOID = 3381 // pg_statistic_ext

// statKindToChar maps goopg's lowercased kind strings to PG's stxkind chars
// (pg_statistic_ext.h STATS_EXT_*). 'e' (expressions) is not a requestable
// kind — it is added implicitly when the object has expression targets — so it
// is not in this map.
var statKindToChar = map[string]byte{
	"ndistinct":    'd', // STATS_EXT_NDISTINCT
	"dependencies": 'f', // STATS_EXT_DEPENDENCIES
	"mcv":          'm', // STATS_EXT_MCV
}

// statCharToKind is the reload inverse of statKindToChar.
var statCharToKind = map[byte]string{
	'd': "ndistinct",
	'f': "dependencies",
	'm': "mcv",
}

// PGStatisticExtColumnsPG18 mirrors FormData_pg_statistic_ext in PG physical
// attnum order. Exported for the initdb heap reload (loadStatisticsExtFromHeap).
func PGStatisticExtColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "stxrelid", Type: catalog.Type{Name: "oid"}, Ordinal: 1},
		{Name: "stxname", Type: catalog.Type{Name: "name"}, Ordinal: 2},
		{Name: "stxnamespace", Type: catalog.Type{Name: "oid"}, Ordinal: 3},
		{Name: "stxowner", Type: catalog.Type{Name: "oid"}, Ordinal: 4},
		{Name: "stxkeys", Type: catalog.Type{Name: "int2vector"}, Ordinal: 5},
		{Name: "stxstattarget", Type: catalog.Type{Name: "int2"}, Ordinal: 6},
		{Name: "stxkind", Type: catalog.Type{Name: "char[]"}, Ordinal: 7},
		{Name: "stxexprs", Type: catalog.Type{Name: "text"}, Ordinal: 8},
	}
}

// pgStatisticExtRel is the connecting database's own pg_statistic_ext heap
// (base/<dbOid>/3381 via tableCatalogHeapDBOid — same routing as the pg_class /
// pg_attribute / pg_attrdef rows).
func pgStatisticExtRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{DBOid: tableCatalogHeapDBOid(ctx), RelOid: pgStatisticExtRelOID, Fork: storage.MainFork}
}

// syncStatisticExtRow writes one pg_statistic_ext heap row for a statistics
// object. Called from CREATE STATISTICS (fresh) and from each ALTER STATISTICS
// re-sync (the caller stamps the old row via stampStatisticExtRows first).
func syncStatisticExtRow(ctx *Context, obj *catalog.StatisticsObject) error {
	// stxkeys: map each simple ON-column name to its 1-based attnum in the
	// owning table. The table must be registered (it is — CREATE STATISTICS
	// resolved it, and reload runs after the table load).
	var attnums []int16
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok && obj.TableOID != 0 && len(obj.Columns) > 0 {
		if tbl, _, ok := im.LookupTableByOIDAllDBs(obj.TableOID); ok && tbl != nil {
			for _, colName := range obj.Columns {
				for i := range tbl.Columns {
					if tbl.Columns[i].Name == colName {
						attnums = append(attnums, int16(tbl.Columns[i].Ordinal+1))
						break
					}
				}
			}
		}
	}

	// stxkind: the requested-kind chars, plus 'e' when the object has
	// expression targets (mirrors CreateStatistics building stxkind).
	var kindChars []byte
	for _, k := range obj.Kinds {
		if ch, ok := statKindToChar[k]; ok {
			kindChars = append(kindChars, ch)
		}
	}
	if obj.HasExpr {
		kindChars = append(kindChars, 'e') // STATS_EXT_EXPRESSIONS
	}

	// stxnamespace: resolve the schema name to its namespace OID.
	nsOID := namespaceOIDForSchema(ctx.Catalog, obj.Schema)

	// stxstattarget: NULL when unset (PG default), else the int2 value.
	targetDatum := NullDatum
	if obj.StatTarget != nil {
		targetDatum = NewIntDatum(int64(int16(*obj.StatTarget)))
	}

	// stxkeys / stxkind: KindBytes passthrough to the int2vector / char[] codec
	// arms; an empty list writes the empty ArrayType (KindString sentinel).
	keysDatum := NewStringDatum("")
	if len(attnums) > 0 {
		keysDatum = NewBytesDatum(pgInt2VectorBytes(attnums))
	}
	kindDatum := NewStringDatum("")
	if len(kindChars) > 0 {
		kindDatum = NewBytesDatum(pgCharArrayBytes(kindChars))
	}

	// stxexprs: deparsed expression targets as a goopg text[] literal (empty
	// string when the object has no expression targets).
	exprsText := ""
	if len(obj.Exprs) > 0 {
		exprsText = formatTextArray(obj.Exprs)
	}

	row := Row{
		NewIntDatum(int64(obj.OID)),           // oid
		NewIntDatum(int64(obj.TableOID)),      // stxrelid
		NewStringDatum(obj.Name),              // stxname
		NewIntDatum(int64(nsOID)),             // stxnamespace
		NewIntDatum(int64(obj.OwnerOrDefault())), // stxowner
		keysDatum,                             // stxkeys
		targetDatum,                           // stxstattarget
		kindDatum,                             // stxkind
		NewStringDatum(exprsText),             // stxexprs
	}
	if _, err := writeHeapRowCanonical(ctx, pgStatisticExtRel(ctx), PGStatisticExtColumnsPG18(), row); err != nil {
		return err
	}
	return mirrorStatisticExtToPostgresDB(ctx)
}

// mirrorStatisticExtToPostgresDB copies the base/1/3381 pg_statistic_ext heap
// into base/5/3381 so a real PG standby connecting via dbname=postgres (DBOid=5)
// reads the runtime-written rows — the same base/1→base/5 mirror every other
// converted catalog uses (mirrorTouchedCatalogsToPostgresDB). Only needed when
// the write targeted the DefaultDBOid heap (a distinct-dbOid database has its
// own base/<dbOid>/3381 that the standby reads directly). No-op otherwise.
func mirrorStatisticExtToPostgresDB(ctx *Context) error {
	if tableCatalogHeapDBOid(ctx) != catalog.DefaultDBOid {
		return nil
	}
	return mirrorCatalogRelToPostgresDB(ctx, pgStatisticExtRelOID)
}

// stampStatisticExtRows stamps xmax on every live pg_statistic_ext row whose
// oid matches statOID (bytes 0:4 of the canonical row). Used on DROP STATISTICS
// and before an ALTER re-sync so the reload's liveness filter skips the old
// version.
func stampStatisticExtRows(ctx *Context, statOID uint32, xmax storage.TransactionID) {
	rel := pgStatisticExtRel(ctx)
	stampCatalogRows(ctx, rel, xmax, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == statOID
	})
	// Reflect the xmax stamp on the base/5 mirror so a standby (dbname=postgres)
	// sees the row as deleted too. The ALTER re-sync path stamps then writes (its
	// syncStatisticExtRow mirrors afterward); the DROP path stamps only, so the
	// mirror here is what carries the delete across for DROP.
	_ = mirrorStatisticExtToPostgresDB(ctx)
}

// pgCharArrayBytes builds the on-disk char[] (elemtype CHAR=18) ArrayType blob
// for a non-empty list of 1-byte chars. Mirrors pgInt2VectorBytes / the PG
// 1-D ArrayType layout (24-byte header incl. the 4-byte varlena length, then
// 1-byte elements).
func pgCharArrayBytes(chars []byte) []byte {
	const hdrSize = 24
	total := hdrSize + len(chars)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)  // ndim
	binary.LittleEndian.PutUint32(buf[8:12], 0) // dataoffset (no nulls)
	binary.LittleEndian.PutUint32(buf[12:16], 18) // CHAROID
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(chars))) // dim
	binary.LittleEndian.PutUint32(buf[20:24], 1)                  // lbound
	copy(buf[hdrSize:], chars)
	return buf
}
