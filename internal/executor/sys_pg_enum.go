package executor

// B2.1d (docs/design/wal-pg-identical-stream/02d §1): enum labels journal as
// real pg_enum heap rows (one per label) with entries in all three pg_enum
// indexes, giving enums restart durability for the FIRST time (labels
// previously lived only in the in-memory registry — no WAL record, no
// reload). CREATE TYPE AS ENUM = one INSERT per label; ALTER TYPE ADD VALUE
// = one INSERT; RENAME VALUE = delete+insert (stable label OID); DROP TYPE
// stamps every label row. The pg_enum VIEW stays virtual (now rendering the
// real label OIDs).

import (
	"encoding/binary"
	"math"
	"strconv"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pg_enum relation + index OIDs (postgres/src/include/catalog/pg_enum.h).
const (
	pgEnumRelOID                = 3501
	pgEnumOidIndexOID           = 3502
	pgEnumTypidLabelIndexOID    = 3503
	pgEnumTypidSortOrderIndexID = 3534
)

// PGEnumColumnsPG18 mirrors FormData_pg_enum: oid, enumtypid,
// enumsortorder (float4 — goopg's engine-wide text-varlena convention),
// enumlabel (name). Exported for the initdb reload.
func PGEnumColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "enumtypid", Type: catalog.Type{Name: "oid"}},
		// enumsortorder: real binary float4 (M0111-0002 closed — the former
		// xid-bits encode-hint produced byte-identical pages, so no
		// migration is needed).
		{Name: "enumsortorder", Type: catalog.Type{Name: "float4"}},
		{Name: "enumlabel", Type: catalog.Type{Name: "name"}},
	}
}

// buildPGEnumRow builds one pg_enum row for (enum type, label).
func buildPGEnumRow(et *catalog.EnumType, ev catalog.EnumValue) Row {
	return Row{
		NewIntDatum(int64(ev.OID)), // 1 oid
		NewIntDatum(int64(et.OID)), // 2 enumtypid
		// Full-precision text datum (newNumericFromFloat truncates at 6
		// decimals; deep BEFORE/AFTER midpoints exceed that) — the float4
		// encoder parses it back to IEEE bits.
		NewStringDatum(strconv.FormatFloat(ev.SortOrder, 'g', -1, 32)), // 3 enumsortorder
		NewStringDatum(ev.Label), // 4 enumlabel
	}
}

func pgEnumRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgEnumRelOID,
		Fork:   storage.MainFork,
	}
}

// buildIndexTupleOidNameKey builds the 80-byte (oid, NameData) IndexTuple
// for pg_enum_typid_label_index (3503) — executor twin of initdb's
// pgBuildIndexTupleOidNameKey.
func buildIndexTupleOidNameKey(heapBlk uint32, heapOff uint16, oid uint32, name string) []byte {
	const (
		hoff        = sysIndexTupleHoff
		nameDataLen = 64
		size        = 80 // MAXALIGN(8 + 4 + pad4 + 64)? — PG packs oid(4) then name (align 'c'): 8+4+64=76 → MAXALIGN 80
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:hoff+4], oid)
	n := len(name)
	if n > nameDataLen {
		n = nameDataLen
	}
	copy(out[hoff+4:hoff+4+n], name[:n])
	return out
}

// cmpKeyOidName compares (uint32, NameData[64]) keys.
func cmpKeyOidName(a, b []byte) int {
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	return cmpKeyName(a[4:], b[4:])
}

// buildIndexTupleOidFloat4Key builds the 16-byte (oid, float4) IndexTuple
// for pg_enum_typid_sortorder_index (3534). The sortorder is stored as
// IEEE-754 float32 LE bits (index keys are fixed-width even though goopg's
// HEAP float4 convention is text-varlena — the index is goopg-private
// runtime state validated by the same descent machinery).
func buildIndexTupleOidFloat4Key(heapBlk uint32, heapOff uint16, oid uint32, sort float64) []byte {
	const (
		hoff = sysIndexTupleHoff
		size = 16
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:hoff+4], oid)
	le.PutUint32(out[hoff+4:hoff+8], math.Float32bits(float32(sort)))
	return out
}

// cmpKeyOidFloat4 compares (uint32, float4-bits) keys numerically.
func cmpKeyOidFloat4(a, b []byte) int {
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	fa := math.Float32frombits(binary.LittleEndian.Uint32(a[4:8]))
	fb := math.Float32frombits(binary.LittleEndian.Uint32(b[4:8]))
	switch {
	case fa < fb:
		return -1
	case fa > fb:
		return 1
	default:
		return 0
	}
}

// writeEnumLabelRow journals one label as a pg_enum heap INSERT plus all
// three index entries.
func writeEnumLabelRow(ctx *Context, et *catalog.EnumType, ev catalog.EnumValue) error {
	tid, err := writeHeapRowCanonical(ctx, pgEnumRel(ctx), PGEnumColumnsPG18(), buildPGEnumRow(et, ev))
	if err != nil {
		return err
	}
	blk, off := uint32(tid.Block), tid.Offset
	if err := insertCanonicalSysBtreeLeaf(ctx, pgEnumOidIndexOID,
		buildIndexTupleOidKey(blk, off, ev.OID), cmpKeyUint32); err != nil {
		return err
	}
	if err := insertCanonicalSysBtreeLeaf(ctx, pgEnumTypidLabelIndexOID,
		buildIndexTupleOidNameKey(blk, off, et.OID, ev.Label), cmpKeyOidName); err != nil {
		return err
	}
	return insertCanonicalSysBtreeLeaf(ctx, pgEnumTypidSortOrderIndexID,
		buildIndexTupleOidFloat4Key(blk, off, et.OID, ev.SortOrder), cmpKeyOidFloat4)
}

// deleteEnumLabelRowByOID stamps xmax on one label row (RENAME VALUE's
// delete half; the reinsert keeps the same label OID).
func deleteEnumLabelRowByOID(ctx *Context, labelOID uint32, xmax storage.TransactionID) {
	stampCatalogRows(ctx, pgEnumRel(ctx), xmax, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == labelOID
	})
}

// deleteEnumLabelRowsByTypid stamps xmax on every label row of an enum
// (DROP TYPE). Column 1 (enumtypid) sits at data offset 4.
func deleteEnumLabelRowsByTypid(ctx *Context, enumOID uint32, xmax storage.TransactionID) {
	stampCatalogRows(ctx, pgEnumRel(ctx), xmax, func(data []byte) bool {
		if len(data) < 8 {
			return false
		}
		return binary.LittleEndian.Uint32(data[4:8]) == enumOID
	})
}

// mirrorEnumCatalogFiles propagates the pg_enum heap + its three indexes to
// the postgres DB's copies (reload reads base/5).
func mirrorEnumCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgEnumRelOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgEnumOidIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgEnumTypidLabelIndexOID)
	_ = mirrorCatalogRelToPostgresDB(ctx, pgEnumTypidSortOrderIndexID)
}
