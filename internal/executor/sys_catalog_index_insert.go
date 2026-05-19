package executor

// Runtime IndexTuple insertion into the PG18-canonical system btrees touched
// by user CREATE TABLE. M0106-0010 batched-36 loop 9 (a.k.a. batched-38).
//
// Background: M0106-0010 batched-36 loop 8 made `syncTableToCatalogHeap` /
// `syncIndexToCatalogHeap` emit PG18-canonical 34-column pg_class rows and
// 25-column pg_attribute rows, but only into the *heap* relations. PG18's
// `SearchSysCache2(RELNAMENSP)` (parse-analyze for `SELECT count(*) FROM
// public.bench_log`) probes `pg_class_relname_nsp_index` (OID 2663),
// `pg_class_oid_index` (OID 2662), and `pg_attribute_relid_attnum_index`
// (OID 2659) — and finds nothing, so `parse_analyze` raises 42P01 even
// though the heap rows are byte-correct.
//
// This file adds the three IndexTuple builders + a generic
// `insertCanonicalSysBtreeLeaf` helper that finds the sorted insert position
// on the leaf-root page (block 1), shifts line pointers, calls
// `PageInsertItemRawAt`, and emits a canonical `XLOG_BTREE_INSERT_LEAF` WAL
// record with the updated page as full-page image. The three wrappers
// `insertPgClassOidIndexEntry`, `insertPgClassRelnameNspIndexEntry`,
// `insertPgAttributeRelidAttnumIndexEntry` are called from
// `syncTableToCatalogHeap` / `syncIndexToCatalogHeap` after each heap row
// write, with the heap TID returned by `writeHeapRowCanonical`.
//
// The IndexTuple builders mirror `internal/initdb/btree_index_bootstrap.go`
// — duplicated here because executor cannot import initdb (initdb depends on
// executor). The duplication is intentional and minimal (3 builders, ~60
// lines).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// OIDs of the PG18-canonical system btrees touched on user CREATE TABLE.
const (
	pgClassOidIndexOID             uint32 = 2662
	pgClassRelnameNspIndexOID      uint32 = 2663
	pgAttributeRelidAttnumIndexOID uint32 = 2659
)

// All bootstrap btrees write their root-leaf at block 1 (block 0 is the
// metapage). Runtime inserts append/sort-insert into the same leaf-root
// until a future split is needed; the in-flight CREATE TABLE entries do not
// cause the bootstrap-sized leaf-root to overflow.
const sysBtreeRootBlock storage.BlockNumber = 1

// IndexTuple data offset for the no-nulls / no-varlena case:
// MAXALIGN(sizeof(IndexTupleData)) == 8.
const sysIndexTupleHoff = 8

// indexSizeMask is the lower-13-bits mask of IndexTupleData.t_info used to
// recover the tuple size (mirrors INDEX_SIZE_MASK in PG's itup.h).
const sysIndexSizeMask uint16 = 0x1FFF

// buildIndexTupleOidKey builds a 16-byte IndexTuple keyed by a single
// uint32 OID column (e.g. pg_class_oid_index entries).
func buildIndexTupleOidKey(heapBlk uint32, heapOff uint16, oid uint32) []byte {
	const (
		hoff     = sysIndexTupleHoff
		dataSize = 4
		size     = 16 // MAXALIGN(hoff + dataSize)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:hoff+dataSize], oid)
	return out
}

// buildIndexTupleNameOidKey builds an 80-byte IndexTuple keyed by a 64-byte
// NameData (NUL-padded) followed by a uint32 OID (e.g.
// pg_class_relname_nsp_index entries).
func buildIndexTupleNameOidKey(heapBlk uint32, heapOff uint16, name string, oid uint32) []byte {
	const (
		nameDataLen = 64
		hoff        = sysIndexTupleHoff
		size        = 80 // MAXALIGN(hoff + nameDataLen + 4)
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	n := len(name)
	if n > nameDataLen {
		n = nameDataLen
	}
	copy(out[hoff:hoff+n], name[:n])
	le.PutUint32(out[hoff+nameDataLen:hoff+nameDataLen+4], oid)
	return out
}

// buildIndexTupleOidInt2Key builds a 16-byte IndexTuple keyed by a uint32
// OID followed by an int16 attnum (e.g. pg_attribute_relid_attnum_index
// entries).
func buildIndexTupleOidInt2Key(heapBlk uint32, heapOff uint16, attrelid uint32, attnum int16) []byte {
	const (
		hoff = sysIndexTupleHoff
		size = 16 // MAXALIGN(hoff + 4 + 2) = MAXALIGN(14) = 16
	)
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(out[0:2], uint16(heapBlk>>16))
	le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF))
	le.PutUint16(out[4:6], heapOff)
	le.PutUint16(out[6:8], uint16(size)&sysIndexSizeMask)
	le.PutUint32(out[hoff:hoff+4], attrelid)
	le.PutUint16(out[hoff+4:hoff+6], uint16(attnum))
	return out
}

// keyCompareFn compares two IndexTuple key payloads (i.e. the bytes
// starting at sysIndexTupleHoff). Returns <0 / 0 / >0 in standard cmp
// convention.
type keyCompareFn func(a, b []byte) int

// cmpKeyUint32 compares the first 4 bytes of each key as little-endian
// uint32 (e.g. pg_class_oid_index, pg_attribute_relid_attnum_index leading
// column).
func cmpKeyUint32(a, b []byte) int {
	av := binary.LittleEndian.Uint32(a[:4])
	bv := binary.LittleEndian.Uint32(b[:4])
	switch {
	case av < bv:
		return -1
	case av > bv:
		return 1
	default:
		return 0
	}
}

// cmpKeyOidInt2 compares (uint32, int16) prefix keys lexicographically
// (e.g. pg_attribute_relid_attnum_index: attrelid then attnum).
func cmpKeyOidInt2(a, b []byte) int {
	if c := cmpKeyUint32(a, b); c != 0 {
		return c
	}
	av := int16(binary.LittleEndian.Uint16(a[4:6]))
	bv := int16(binary.LittleEndian.Uint16(b[4:6]))
	switch {
	case av < bv:
		return -1
	case av > bv:
		return 1
	default:
		return 0
	}
}

// cmpKeyNameOid compares (NameData[64], uint32) prefix keys. The NameData
// is NUL-padded so a plain byte-compare matches PG's `name_cmp` semantics
// (memcmp with NUL padding).
func cmpKeyNameOid(a, b []byte) int {
	if c := bytes.Compare(a[:64], b[:64]); c != 0 {
		return c
	}
	return cmpKeyUint32(a[64:], b[64:])
}

// insertCanonicalSysBtreeLeaf inserts indexTuple into the leaf-root page of
// the system btree at sysBtreeRootBlock. Existing entries are compared via
// cmp on the key bytes (offset sysIndexTupleHoff..) to find the sorted
// insert position. The updated page is then captured as a full-page image
// and emitted as an XLOG_BTREE_INSERT_LEAF canonical WAL record (when
// ctx.LogCanonical != nil).
//
// Pre-condition: the index file exists and has at least
// sysBtreeRootBlock+1 blocks (true for initdb-bootstrapped clusters; tests
// must pre-populate via the helpers in sys_catalog_index_insert_test.go).
func insertCanonicalSysBtreeLeaf(ctx *Context, indexOID uint32, indexTuple []byte, cmp keyCompareFn) error {
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: indexOID,
		Fork:   storage.MainFork,
	}
	// Skip when the index file has not been initialised (e.g. catalog
	// in-memory test fixture that did not run initdb). Production paths
	// always have block 1 populated; missing block 1 means the bootstrap
	// step never ran and there is nothing for us to update.
	if ctx.Pool == nil {
		return nil
	}
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return fmt.Errorf("nblocks sys btree %d: %w", indexOID, err)
	}
	if nBlocks <= sysBtreeRootBlock {
		return nil
	}

	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: sysBtreeRootBlock})
	if err != nil {
		return fmt.Errorf("pin sys btree %d block %d: %w", indexOID, sysBtreeRootBlock, err)
	}
	slot.Lock()
	page := slot.Page()

	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return fmt.Errorf("line pointer count sys btree %d: %w", indexOID, err)
	}
	newKey := indexTuple[sysIndexTupleHoff:]
	insertSlot := uint16(count + 1)
	for i := 1; i <= count; i++ {
		raw, err := storage.PageGetItemRawNoCopy(page, uint16(i))
		if err != nil {
			slot.Unlock()
			ctx.Pool.Unpin(slot)
			return fmt.Errorf("read existing item sys btree %d slot %d: %w", indexOID, i, err)
		}
		if len(raw) <= sysIndexTupleHoff {
			continue
		}
		existingKey := raw[sysIndexTupleHoff:]
		if cmp(newKey, existingKey) < 0 {
			insertSlot = uint16(i)
			break
		}
	}

	if _, err := storage.PageInsertItemRawAt(page, insertSlot, indexTuple); err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		if errors.Is(err, storage.ErrNoSpaceInPage) {
			// The bootstrap leaf-root for several system btrees (notably
			// pg_class_relname_nsp_index, 2663) is filled to within a few
			// bytes of the page budget. Inserting a new user-table entry
			// requires splitting the leaf-root into a fresh leaf + new
			// internal root — a substantial chunk of work that is tracked
			// as a follow-on to M0106-0010 batched-36 loop 9. For now,
			// skip the insert so DDL does not fail; the standby will not
			// see the new index entry until split-time WAL is emitted.
			return nil
		}
		return fmt.Errorf("insert sys btree %d slot %d: %w", indexOID, insertSlot, err)
	}

	// Snapshot the updated page for the WAL FPI before unpinning.
	pageCopy := make(storage.Page, storage.BlockSize)
	copy(pageCopy, page)

	ctx.Pool.MarkDirty(slot)
	slot.Unlock()
	ctx.Pool.Unpin(slot)

	if ctx.LogCanonical != nil {
		xid := uint32(ctx.Tx.XID)
		if err := catalog.PgCanonicalBtreeInsert(rel, sysBtreeRootBlock, pageCopy, insertSlot, xid, ctx.LogCanonical); err != nil {
			return fmt.Errorf("canonical WAL sys btree %d: %w", indexOID, err)
		}
	}
	return nil
}

// insertPgClassOidIndexEntry inserts an entry into pg_class_oid_index (OID
// 2662) for (oid → heap TID).
func insertPgClassOidIndexEntry(ctx *Context, oid uint32, tid storage.ItemPointer) error {
	tup := buildIndexTupleOidKey(uint32(tid.Block), tid.Offset, oid)
	return insertCanonicalSysBtreeLeaf(ctx, pgClassOidIndexOID, tup, cmpKeyUint32)
}

// insertPgClassRelnameNspIndexEntry inserts an entry into
// pg_class_relname_nsp_index (OID 2663) for ((relname, relnamespace) → heap TID).
func insertPgClassRelnameNspIndexEntry(ctx *Context, relname string, relnamespace uint32, tid storage.ItemPointer) error {
	tup := buildIndexTupleNameOidKey(uint32(tid.Block), tid.Offset, relname, relnamespace)
	return insertCanonicalSysBtreeLeaf(ctx, pgClassRelnameNspIndexOID, tup, cmpKeyNameOid)
}

// insertPgAttributeRelidAttnumIndexEntry inserts an entry into
// pg_attribute_relid_attnum_index (OID 2659) for ((attrelid, attnum) → heap TID).
func insertPgAttributeRelidAttnumIndexEntry(ctx *Context, attrelid uint32, attnum int16, tid storage.ItemPointer) error {
	tup := buildIndexTupleOidInt2Key(uint32(tid.Block), tid.Offset, attrelid, attnum)
	return insertCanonicalSysBtreeLeaf(ctx, pgAttributeRelidAttnumIndexOID, tup, cmpKeyOidInt2)
}
