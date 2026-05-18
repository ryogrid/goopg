package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/storage"
)

const (
	// RecordKindPageImage is a full-page image redo record.
	RecordKindPageImage byte = 1
	// RecordKindCheckpoint marks a consistent recovery boundary.
	RecordKindCheckpoint byte = 2
	// RecordKindBtreeSplit captures a B-tree page split atomically:
	// the post-split image of the left page plus the freshly
	// populated right page in one record. Replay applies both,
	// guaranteeing crash recovery never lands in the torn state
	// where left advertises a right-link to a page that's still
	// the bare smgr.Extend zero/init image. See
	// docs/design/0002-0002-btree-concurrency.md Landing 3a.
	RecordKindBtreeSplit byte = 3
	// RecordKindHeapInsert is a logical change record for one
	// heap insert. The full record is "kind | rel(9) | blk(4) |
	// lineSlot(2) | tuple-bytes". Replay reads the existing page
	// (or InitPage if missing) and re-applies the insert at the
	// recorded slot, keyed off pd_lsn idempotency. See
	// docs/design/0002-0003-redo-records.md.
	RecordKindHeapInsert byte = 4
	// RecordKindBtreeInsert is a logical change record for one
	// B-tree non-split insert. Format:
	// "kind | rel(9) | blk(4) | item-bytes". The item bytes are
	// the same shape internal/access/btree's `item.marshal`
	// produces (keyLen + ptr.block + ptr.offset + key). Replay
	// is idempotent via pd_lsn and applies the item to the
	// existing page in sorted order. See
	// docs/design/0002-0003-redo-records.md.
	RecordKindBtreeInsert byte = 5
	// RecordKindHeapDelete is a logical change record stamping
	// xmax on an existing heap tuple. The MVCC update path
	// emits one for the old image (followed by a HeapInsert for
	// the new); the DELETE path emits one per visible match.
	// Format: "kind | rel(9) | blk(4) | lineSlot(2) | xmax(4)"
	// = 20 bytes, fixed. Replay is idempotent via pd_lsn.
	RecordKindHeapDelete byte = 6
	// RecordKindHeapVacuum is a logical change record for one
	// heap page-prune. VACUUM emits one per pruned page,
	// carrying the 1-based LP_NORMAL slot numbers it reclaimed
	// to LP_UNUSED. Replay calls
	// storage.VacuumHeapPageBySlots with the same list, so
	// the post-replay page is bit-exact with the original
	// post-prune image. Format:
	// "kind | rel(9) | blk(4) | count(2) | slots[count](2 each)"
	// = 16 + 2*count bytes. Replay is idempotent via pd_lsn.
	RecordKindHeapVacuum byte = 7
	// RecordKindXactCommit marks the boundary that releases a
	// transaction's queued changes from the M0008 reorder buffer
	// to the output plugin. Carries the xid so the logical
	// decoder can route it. Crash-recovery is a no-op for this
	// kind — the per-record idempotency in the data records is
	// sufficient to bring storage back to a consistent state;
	// the commit/abort markers exist purely so the logical
	// decoder can make commit/abort decisions. Format:
	// "kind(1) | xid(4)" = 5 bytes. See
	// docs/design/0008-0001-logical-decoding-pipeline.md.
	RecordKindXactCommit byte = 8
	// RecordKindXactAbort marks the boundary that drops a
	// transaction's queued changes from the M0008 reorder buffer
	// without emission. Same format / recovery semantics as
	// RecordKindXactCommit.
	RecordKindXactAbort byte = 9
	// RecordKindHeapLock is the row-lock redo record (M0021
	// tuple-level locking step 3). Stamps an xmax + the
	// HEAP_XMAX_LOCK_ONLY infomask bit + a lock-strength bit on
	// an existing heap tuple. Mirrors upstream's xl_heap_lock
	// record. The record is idempotent via pd_lsn — re-applying
	// after a crash is a no-op when the page already advertises
	// an LSN >= record.endLSN. Format:
	// "kind(1) | rel(9) | blk(4) | lineSlot(2) | xmax(4) |
	//  lockStrength(2)" = 22 bytes.
	RecordKindHeapLock byte = 10
	// RecordKindHeapHotUpdate is a logical HOT-update record (M0046-0001).
	// Encodes the old-slot xmax stamp + HeapHotUpdated infomask + CTID
	// chain linkage + the new tuple bytes (which carry HeapOnlyTuple in
	// their infomask) — all on the same heap page. Replay inserts the
	// new tuple (obtaining newSlot), then stamps the old slot.
	// Format: "kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) |
	//          oldSlot(2) | xmax(4) | newTupleBytes(var)" = 20 bytes fixed.
	RecordKindHeapHotUpdate byte = 13
	// RecordKindHeapPruneOpt is a logical opportunistic-pruning record
	// (M0046-0002). Mirrors PostgreSQL's XLOG_HEAP2_PRUNE. Emitted when
	// the HOT-update path reclaims dead tuple slots inline (without a
	// full VACUUM pass). Same format as RecordKindHeapVacuum so the
	// replay path is identical. Format:
	// "kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) |
	//  count(2) | slots[count](2 each)" = 16 + 2*count bytes.
	RecordKindHeapPruneOpt byte = 14
	// RecordKindSmgrCreate logs the first extension of a relation file
	// (mirrors upstream's XLOG_SMGR_CREATE in
	// postgres/src/include/catalog/storage_xlog.h). Emitted by the buffer
	// pool when Pool.PinNew creates block 0 of a new relation. Redo:
	// ensure the relfile exists with at least one initialised block.
	// Format: "kind(1) | DBOid(4) | RelOid(4) | Fork(1)" = 10 bytes.
	RecordKindSmgrCreate byte = 11
	// RecordKindSmgrTruncate logs a relation-file truncation (mirrors
	// upstream's XLOG_SMGR_TRUNCATE). Emitted by TRUNCATE TABLE. Redo:
	// truncate the relfile to 0 blocks. Same format as SmgrCreate.
	RecordKindSmgrTruncate byte = 12

	// RecordKindXactAssignment records the first lazy XID allocation
	// for one or more subtransactions (M0050-0003). Emitted when a
	// subxact writes for the first time; replay populates the
	// subxact-to-parent map in mvcc.Manager. Format:
	//   kind(1) | parentXid(4) | count(2) | subXids[count](4 each)
	RecordKindXactAssignment byte = 15

	// RecordKindXactRollbackTo records ROLLBACK TO SAVEPOINT (M0050-0003).
	// Replay marks each listed subXid as individually aborted in
	// mvcc.Manager. Format:
	//   kind(1) | parentXid(4) | count(2) | abortedSubXids[count](4 each)
	RecordKindXactRollbackTo byte = 16

	// RecordKindXactSubAbort records a single subxact abort triggered
	// without a named savepoint (M0050-0003). Format:
	//   kind(1) | subXid(4) = 5 bytes total.
	RecordKindXactSubAbort byte = 17

	// RecordKindCreateDatabase records a `CREATE DATABASE <name>` event
	// so the catalog's per-instance database list can be reconstructed
	// after a crash (M0054-0001). The redo path does NOT touch on-disk
	// storage — goopg v0 has no per-database file namespacing — so
	// applyRecord returns (false, nil); the recovery driver in
	// internal/initdb scans the WAL for these records after physical
	// replay and re-registers each database name in the catalog.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes)
	RecordKindCreateDatabase byte = 18

	// RecordKindDropDatabase records a `DROP DATABASE <name>` event.
	// Counterpart to RecordKindCreateDatabase. Same format / replay
	// semantics; the recovery driver removes the name from the
	// catalog instead of adding it.
	RecordKindDropDatabase byte = 19

	// RecordKindCreateIndex records a `CREATE INDEX` event so the
	// catalog's in-memory index registry can be reconstructed after
	// a crash that bypasses SaveCatalog (M0079-0001 / pgbench
	// recovery fix).
	//
	// Background: goopg has no `pg_index` heap relation, so the
	// pg_class row written by `syncIndexToCatalogHeap` (relkind='i')
	// is insufficient to fully reconstruct an Index — the column
	// list, unique flag, primary flag, and owning-table OID are
	// missing. Without WAL-driven recovery the index disappears
	// from the catalog after restart even though its relfile and
	// btree pages are intact on disk; pgbench's `pgbench_accounts.aid`
	// PK was the surfacing case (~70x TPS regression after restart
	// because every UPDATE fell back to a 10M-row Seq Scan).
	//
	// Same redo semantics as RecordKindCreateDatabase: physical
	// replay (`ApplyRecord`) is a no-op — heap pages and btree
	// pages are already restored by other record kinds — and the
	// catalog-side replay runs once after physical recovery
	// finishes via `internal/initdb.replayIndexDDLRecords`.
	//
	// Format:
	//   kind(1) | oid(4) | tableOid(4) | unique(1) | primary(1) |
	//   schemaLen(2) | nameLen(2) | methodLen(2) | numCols(2) |
	//   schema | name | method | colName0Len(2) | colName0 | ... |
	//   colNameKLen(2) | colNameK
	RecordKindCreateIndex byte = 20

	// RecordKindDropIndex is the counterpart to
	// RecordKindCreateIndex. Carries the index OID + qualified
	// name so the recovery driver can locate and unregister it.
	// Format:
	//   kind(1) | oid(4) | schemaLen(2) | nameLen(2) | schema | name
	RecordKindDropIndex byte = 21

	// RecordKindBtreeVacuum is a logical change record for one
	// B-tree page vacuum (M0079-0002). VACUUM emits one per page
	// where dead heap pointers were filtered out. Carries the
	// post-vacuum kept-items as raw bytes (each item is a
	// btree-internal `keyLen | ptr.block | ptr.offset | key`
	// blob — the same shape `pageAddItemRaw` writes) plus the
	// post-vacuum opaque flags so replay can rebuild the page's
	// kept-items projection AND apply the half-dead / deleted
	// flag transition that VacuumIndexPages performs in the
	// same critical section when the page becomes empty.
	//
	// The kept-items-as-raw-bytes encoding (vs PostgreSQL's
	// removed-slot-list) is necessary because goopg's
	// VacuumIndexPages explodes posting-list line pointers into
	// individual (key, TID) items — a slot-list replay couldn't
	// reproduce that explosion without re-running the
	// `pageItems` posting-list expansion. Carrying kept items
	// directly makes replay layout-stable regardless of how the
	// pre-vacuum page packed its line pointers.
	//
	// Replaces the prior FPI-via-`markDirtyWithPageRecord` path
	// (~8 KiB per touched page) with a logical record (~item
	// bytes per page; ~50-90 % smaller for typical leaves).
	// Replay is idempotent via pd_lsn. See
	// `docs/design/0079-0002-btree-record-wal-parity.md`.
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) |
	//   itemCount(2) |
	//   item0Len(2) | item0Bytes | ... | itemKLen(2) | itemKBytes |
	//   opaqueFlags(2)
	RecordKindBtreeVacuum byte = 22

	// btreeVacuumHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) + itemCount(2) = 16. Mirrors
	// heapVacuumHeaderSize.
	btreeVacuumHeaderSize = 16
	// btreeVacuumTrailerSize: opaqueFlags(2). Trails the
	// variable-length item-bytes payload.
	btreeVacuumTrailerSize = 2

	// RecordKindBtreeUnlinkPage is the M0079-0003 atomic
	// page-deletion record covering the four mutations
	// `unlinkEmptyLeaf` performs (left sibling Next pointer,
	// right sibling Prev pointer, leaf opaque flags, parent
	// downlink removal). Counterpart to PostgreSQL's
	// XLOG_BTREE_UNLINK_PAGE. Replaces 4 FPIs (~32 KiB) with a
	// single ~30-50 byte logical record.
	//
	// Each of the 4 pages has its own pd_lsn idempotency check
	// during replay so a partial replay can resume cleanly.
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) |
	//   leafBlk(4) | leafFlagsAfter(2) |
	//   leftSibValid(1) | leftSibBlk(4) | leftSibNewNext(4) |
	//   rightSibValid(1) | rightSibBlk(4) | rightSibNewPrev(4) |
	//   parentValid(1) | parentBlk(4) | parentRemoveSlot(2)
	RecordKindBtreeUnlinkPage byte = 23

	// btreeUnlinkPageSize: 1+4+4+1+4+2+1+4+4+1+4+4+1+4+2 = 41 bytes.
	btreeUnlinkPageSize = 41

	// RecordKindBtreeNewRoot is the M0079-0003 root-replacement
	// record, used when (a) a split bubbles up and creates a
	// new root, or (b) VACUUM empties the entire tree and
	// resets to a single-page empty root. Counterpart to
	// PostgreSQL's XLOG_BTREE_NEWROOT.
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) |
	//   rootBlk(4) | level(4) | itemCount(2) |
	//   [item0Len(2) | item0Bytes | ... | itemKLen(2) | itemKBytes]
	RecordKindBtreeNewRoot byte = 24

	// btreeNewRootHeaderSize: kind(1)+DBOid(4)+RelOid(4)+Fork(1)
	// +rootBlk(4)+level(4)+itemCount(2) = 20.
	btreeNewRootHeaderSize = 20

	// RecordKindBtreeMarkPageHalfDead is the M0079-0003
	// flags-only record emitted at the start of two-phase page
	// deletion. Most M0079-0002 vacuum passes that empty a
	// leaf bundle the BTHalfDead | BTDeleted transition into
	// the BtreeVacuum record's `OpaqueFlags` trailer; this
	// standalone record is for the case where the leaf was
	// already empty before the dead-set scan and only the
	// flag transition needs WAL coverage.
	//
	// Counterpart to PostgreSQL's XLOG_BTREE_MARK_PAGE_HALFDEAD
	// (control-only — goopg does not need the parent /
	// topparent backup blocks because phase 2 is a separate
	// record in our design).
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) |
	//   leafBlk(4) | flagsAfter(2)
	RecordKindBtreeMarkPageHalfDead byte = 25

	// btreeMarkHalfDeadSize: kind(1)+DBOid(4)+RelOid(4)+Fork(1)
	// +leafBlk(4)+flagsAfter(2) = 16.
	btreeMarkHalfDeadSize = 16

	// RecordKindHeapFreeze is the M0080-0001 logical heap-freeze
	// record. VACUUM FREEZE rewrites tuple xmin values to
	// FrozenTransactionID; this record carries the 1-based
	// LP_NORMAL slot numbers whose tuples were frozen so replay
	// can reapply the rewrite deterministically. Counterpart to
	// PostgreSQL's XLOG_HEAP2_FREEZE_PAGE (and the freeze portion
	// of XLOG_HEAP2_PRUNE_VACUUM_SCAN in PG17+).
	//
	// Replaces the FPI fallback at `internal/vacuum/vacuum.go`
	// line 157 with a logical record (~16-200 bytes per page vs
	// 8 KiB FPI).
	//
	// Format identical to RecordKindHeapVacuum:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) |
	//   count(2) | slots[count](2 each)
	RecordKindHeapFreeze byte = 26

	// heapFreezeHeaderSize: kind(1)+DBOid(4)+RelOid(4)+Fork(1)
	// +Block(4)+count(2) = 16. Mirrors heapVacuumHeaderSize.
	heapFreezeHeaderSize = 16

	// RecordKindHeapUpdate is the M0080-0002 atomic non-HOT
	// UPDATE record. Counterpart to PostgreSQL's
	// XLOG_HEAP_UPDATE. Combines the old-tuple xmax stamp +
	// new-tuple insert into a single WAL entry so replay's
	// per-page idempotency can apply both halves under one
	// pd_lsn boundary. Replaces the prior pair (HeapDelete +
	// HeapInsert) for non-HOT UPDATE; the HOT path continues
	// to use `RecordKindHeapHotUpdate` (atomic same-page).
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) |
	//   oldBlk(4) | oldLineSlot(2) | xmax(4) |
	//   newBlk(4) | newLineSlot(2) | tupleLen(4) | tupleBytes
	RecordKindHeapUpdate byte = 27

	// heapUpdateHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + oldBlk(4) + oldLineSlot(2) + xmax(4) +
	// newBlk(4) + newLineSlot(2) + tupleLen(4) = 30.
	heapUpdateHeaderSize = 30

	// RecordKindHeapMultiInsert is the M0080-0002 bulk insert
	// record. Counterpart to PostgreSQL's
	// XLOG_HEAP2_MULTI_INSERT. Carries N tuples destined for
	// the SAME heap page in one record so COPY / bulk INSERT
	// don't pay the per-tuple WAL header overhead.
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) | Block(4) |
	//   count(2) |
	//   [lineSlot0(2) | tupleLen0(4) | tupleBytes0 | ...]
	RecordKindHeapMultiInsert byte = 28

	// heapMultiInsertHeaderSize: kind(1) + DBOid(4) + RelOid(4)
	// + Fork(1) + Block(4) + count(2) = 16.
	heapMultiInsertHeaderSize = 16

	// RecordKindHeapVisible is the M0080-0003 visibility-map
	// update record. Counterpart to PostgreSQL's
	// XLOG_HEAP2_VISIBLE. Carries the heap-block number whose
	// VM bit is being set or cleared, plus the cutoff XID
	// for ALL_VISIBLE / ALL_FROZEN flags.
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) | heapBlk(4) |
	//   flags(1) | cutoffXid(4)
	//
	// flags bits:
	//   0x01 = setAllVisible (vs clearAllVisible)
	//   0x02 = setAllFrozen
	RecordKindHeapVisible byte = 29

	// heapVisibleSize: kind(1)+DBOid(4)+RelOid(4)+Fork(1)
	// +heapBlk(4)+flags(1)+cutoffXid(4) = 19.
	heapVisibleSize = 19

	// RecordKindBtreeReusePage is the M0080-0004 page-recycle
	// notification record. Counterpart to PostgreSQL's
	// XLOG_BTREE_REUSE_PAGE. Emitted when a recycled page is
	// allocated to a new use; tells standbys (when goopg gains
	// hot-standby reads) that the old contents are formally
	// discarded.
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) | blk(4) |
	//   recycledFromXid(4)
	RecordKindBtreeReusePage byte = 30

	// btreeReusePageSize: kind(1)+DBOid(4)+RelOid(4)+Fork(1)
	// +blk(4)+recycledFromXid(4) = 18.
	btreeReusePageSize = 18

	// RecordKindBtreeMetaCleanup is the M0080-0004 metapage
	// cleanup-XID update record. Counterpart to PostgreSQL's
	// XLOG_BTREE_META_CLEANUP. Used by `_bt_set_cleanup_info`
	// to advance the metapage's vacuum-cleanup horizon.
	//
	// Format:
	//   kind(1) | DBOid(4) | RelOid(4) | Fork(1) |
	//   numHeapTuples(8) | lastCleanupNumDeletedTuples(8)
	RecordKindBtreeMetaCleanup byte = 31

	// btreeMetaCleanupSize: kind(1)+DBOid(4)+RelOid(4)+Fork(1)
	// +numHeapTuples(8)+lastCleanupNumDeletedTuples(8) = 26.
	btreeMetaCleanupSize = 26

	// smgrRecordSize: kind(1) + DBOid(4) + RelOid(4) + Fork(1) = 10 bytes.
	smgrRecordSize = 10

	pageImageHeaderSize = 14
	// btreeSplitHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + LeftBlk(4) + RightBlk(4) = 18.
	btreeSplitHeaderSize = 18
	// heapInsertHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) + LineSlot(2) = 16.
	heapInsertHeaderSize = 16
	// btreeInsertHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) = 14.
	btreeInsertHeaderSize = 14
	// heapDeleteSize: kind(1) + DBOid(4) + RelOid(4) + Fork(1)
	// + Block(4) + LineSlot(2) + Xmax(4) = 20.
	heapDeleteSize = 20
	// heapVacuumHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) + Count(2) = 16.
	heapVacuumHeaderSize = 16
	// xactRecordSize: kind(1) + xid(4) = 5. Shared by
	// RecordKindXactCommit and RecordKindXactAbort.
	xactRecordSize = 5
	// heapLockSize: kind(1) + DBOid(4) + RelOid(4) + Fork(1)
	// + Block(4) + LineSlot(2) + Xmax(4) + LockStrength(2) = 22.
	heapLockSize = 22
	// heapHotUpdateHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) + OldSlot(2) + Xmax(4) = 20. Variable
	// new-tuple bytes follow.
	heapHotUpdateHeaderSize = 20
)

// EncodeCreateDatabase encodes a CREATE DATABASE event (M0054-0001).
// Format: kind(1) | nameLen(2) | name(nameLen bytes).
func EncodeCreateDatabase(name string) []byte {
	if len(name) > 0xFFFF {
		// goopg's identifier length cap is far below 64 KiB; truncating
		// here is defensive — this branch is unreachable under normal
		// CREATE DATABASE syntax.
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindCreateDatabase
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeCreateDatabase decodes a RecordKindCreateDatabase payload.
func DecodeCreateDatabase(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: create-database payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateDatabase {
		return "", fmt.Errorf("wal: record kind %d is not create-database", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: create-database payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeDropDatabase encodes a DROP DATABASE event (M0054-0001).
// Format identical to EncodeCreateDatabase.
func EncodeDropDatabase(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropDatabase
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropDatabase decodes a RecordKindDropDatabase payload.
func DecodeDropDatabase(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-database payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropDatabase {
		return "", fmt.Errorf("wal: record kind %d is not drop-database", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-database payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// CreateIndexPayload carries the metadata needed to fully
// reconstruct a btree index in the in-memory catalog during
// recovery. Order of fields mirrors `catalog.Index`'s exported
// state. (M0079-0001.)
type CreateIndexPayload struct {
	OID      uint32
	TableOID uint32
	Schema   string
	Name     string
	Method   string
	Columns  []string
	Unique   bool
	Primary  bool
}

// EncodeCreateIndex encodes a CREATE INDEX event (M0079-0001).
// Format documented at the RecordKindCreateIndex constant.
func EncodeCreateIndex(p CreateIndexPayload) []byte {
	const headerSize = 1 + 4 + 4 + 1 + 1 + 2 + 2 + 2 + 2
	totalLen := headerSize + len(p.Schema) + len(p.Name) + len(p.Method)
	for _, c := range p.Columns {
		totalLen += 2 + len(c)
	}
	out := make([]byte, totalLen)
	out[0] = RecordKindCreateIndex
	binary.LittleEndian.PutUint32(out[1:5], p.OID)
	binary.LittleEndian.PutUint32(out[5:9], p.TableOID)
	if p.Unique {
		out[9] = 1
	}
	if p.Primary {
		out[10] = 1
	}
	binary.LittleEndian.PutUint16(out[11:13], uint16(len(p.Schema)))
	binary.LittleEndian.PutUint16(out[13:15], uint16(len(p.Name)))
	binary.LittleEndian.PutUint16(out[15:17], uint16(len(p.Method)))
	binary.LittleEndian.PutUint16(out[17:19], uint16(len(p.Columns)))
	off := headerSize
	off += copy(out[off:], p.Schema)
	off += copy(out[off:], p.Name)
	off += copy(out[off:], p.Method)
	for _, c := range p.Columns {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(c)))
		off += 2
		off += copy(out[off:], c)
	}
	return out
}

// DecodeCreateIndex decodes a RecordKindCreateIndex payload.
func DecodeCreateIndex(payload []byte) (CreateIndexPayload, error) {
	const headerSize = 1 + 4 + 4 + 1 + 1 + 2 + 2 + 2 + 2
	if len(payload) < headerSize {
		return CreateIndexPayload{}, fmt.Errorf("wal: create-index payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateIndex {
		return CreateIndexPayload{}, fmt.Errorf("wal: record kind %d is not create-index", payload[0])
	}
	p := CreateIndexPayload{
		OID:      binary.LittleEndian.Uint32(payload[1:5]),
		TableOID: binary.LittleEndian.Uint32(payload[5:9]),
		Unique:   payload[9] != 0,
		Primary:  payload[10] != 0,
	}
	schemaLen := int(binary.LittleEndian.Uint16(payload[11:13]))
	nameLen := int(binary.LittleEndian.Uint16(payload[13:15]))
	methodLen := int(binary.LittleEndian.Uint16(payload[15:17]))
	numCols := int(binary.LittleEndian.Uint16(payload[17:19]))
	off := headerSize
	if len(payload) < off+schemaLen+nameLen+methodLen {
		return CreateIndexPayload{}, fmt.Errorf("wal: create-index payload truncated in fixed strings")
	}
	p.Schema = string(payload[off : off+schemaLen])
	off += schemaLen
	p.Name = string(payload[off : off+nameLen])
	off += nameLen
	p.Method = string(payload[off : off+methodLen])
	off += methodLen
	p.Columns = make([]string, 0, numCols)
	for i := 0; i < numCols; i++ {
		if len(payload) < off+2 {
			return CreateIndexPayload{}, fmt.Errorf("wal: create-index payload truncated at column %d header", i)
		}
		colLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+colLen {
			return CreateIndexPayload{}, fmt.Errorf("wal: create-index payload truncated at column %d body", i)
		}
		p.Columns = append(p.Columns, string(payload[off:off+colLen]))
		off += colLen
	}
	return p, nil
}

// DropIndexPayload carries enough metadata to identify which
// index to remove from the catalog during recovery.
// (M0079-0001.)
type DropIndexPayload struct {
	OID    uint32
	Schema string
	Name   string
}

// EncodeDropIndex encodes a DROP INDEX event (M0079-0001).
// Format documented at the RecordKindDropIndex constant.
func EncodeDropIndex(p DropIndexPayload) []byte {
	const headerSize = 1 + 4 + 2 + 2
	out := make([]byte, headerSize+len(p.Schema)+len(p.Name))
	out[0] = RecordKindDropIndex
	binary.LittleEndian.PutUint32(out[1:5], p.OID)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(p.Schema)))
	binary.LittleEndian.PutUint16(out[7:9], uint16(len(p.Name)))
	off := headerSize
	off += copy(out[off:], p.Schema)
	copy(out[off:], p.Name)
	return out
}

// DecodeDropIndex decodes a RecordKindDropIndex payload.
func DecodeDropIndex(payload []byte) (DropIndexPayload, error) {
	const headerSize = 1 + 4 + 2 + 2
	if len(payload) < headerSize {
		return DropIndexPayload{}, fmt.Errorf("wal: drop-index payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropIndex {
		return DropIndexPayload{}, fmt.Errorf("wal: record kind %d is not drop-index", payload[0])
	}
	p := DropIndexPayload{
		OID: binary.LittleEndian.Uint32(payload[1:5]),
	}
	schemaLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	nameLen := int(binary.LittleEndian.Uint16(payload[7:9]))
	off := headerSize
	if len(payload) < off+schemaLen+nameLen {
		return DropIndexPayload{}, fmt.Errorf("wal: drop-index payload truncated")
	}
	p.Schema = string(payload[off : off+schemaLen])
	off += schemaLen
	p.Name = string(payload[off : off+nameLen])
	return p, nil
}

// EncodeXactAssignment encodes a subxact XID assignment record (M0050-0003).
// parentXid is the top-level transaction; subXids lists the subxact XIDs
// that are now children of parentXid. Replay calls Manager.RegisterSubXid
// for each entry. Format: kind(1) | parentXid(4) | count(2) | subXids[].
func EncodeXactAssignment(parentXid storage.TransactionID, subXids []storage.TransactionID) []byte {
	out := make([]byte, 7+4*len(subXids))
	out[0] = RecordKindXactAssignment
	binary.LittleEndian.PutUint32(out[1:5], uint32(parentXid))
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(subXids)))
	for i, s := range subXids {
		binary.LittleEndian.PutUint32(out[7+4*i:], uint32(s))
	}
	return out
}

// DecodeXactAssignment decodes a RecordKindXactAssignment payload.
func DecodeXactAssignment(payload []byte) (parentXid storage.TransactionID, subXids []storage.TransactionID, err error) {
	if len(payload) < 7 {
		return 0, nil, fmt.Errorf("wal: xact-assignment payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindXactAssignment {
		return 0, nil, fmt.Errorf("wal: record kind %d is not xact-assignment", payload[0])
	}
	parentXid = storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5]))
	count := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+4*count {
		return 0, nil, fmt.Errorf("wal: xact-assignment payload truncated (need %d bytes)", 7+4*count)
	}
	subXids = make([]storage.TransactionID, count)
	for i := range subXids {
		subXids[i] = storage.TransactionID(binary.LittleEndian.Uint32(payload[7+4*i:]))
	}
	return parentXid, subXids, nil
}

// EncodeXactRollbackTo encodes a ROLLBACK TO SAVEPOINT record (M0050-0003).
// parentXid is the top-level transaction; abortedSubXids lists the subxact
// XIDs that were individually rolled back. Replay calls Manager.MarkSubxactAborted
// for each. Format: kind(1) | parentXid(4) | count(2) | abortedSubXids[].
func EncodeXactRollbackTo(parentXid storage.TransactionID, abortedSubXids []storage.TransactionID) []byte {
	out := make([]byte, 7+4*len(abortedSubXids))
	out[0] = RecordKindXactRollbackTo
	binary.LittleEndian.PutUint32(out[1:5], uint32(parentXid))
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(abortedSubXids)))
	for i, s := range abortedSubXids {
		binary.LittleEndian.PutUint32(out[7+4*i:], uint32(s))
	}
	return out
}

// DecodeXactRollbackTo decodes a RecordKindXactRollbackTo payload.
func DecodeXactRollbackTo(payload []byte) (parentXid storage.TransactionID, abortedSubXids []storage.TransactionID, err error) {
	if len(payload) < 7 {
		return 0, nil, fmt.Errorf("wal: xact-rollback-to payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindXactRollbackTo {
		return 0, nil, fmt.Errorf("wal: record kind %d is not xact-rollback-to", payload[0])
	}
	parentXid = storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5]))
	count := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+4*count {
		return 0, nil, fmt.Errorf("wal: xact-rollback-to payload truncated (need %d bytes)", 7+4*count)
	}
	abortedSubXids = make([]storage.TransactionID, count)
	for i := range abortedSubXids {
		abortedSubXids[i] = storage.TransactionID(binary.LittleEndian.Uint32(payload[7+4*i:]))
	}
	return parentXid, abortedSubXids, nil
}

// EncodeXactSubAbort encodes a single subxact abort record (M0050-0003).
// Replay calls Manager.MarkSubxactAborted(subXid). Format: kind(1)|subXid(4).
func EncodeXactSubAbort(subXid storage.TransactionID) []byte {
	out := make([]byte, xactRecordSize)
	out[0] = RecordKindXactSubAbort
	binary.LittleEndian.PutUint32(out[1:5], uint32(subXid))
	return out
}

// DecodeXactSubAbort decodes a RecordKindXactSubAbort payload.
func DecodeXactSubAbort(payload []byte) (subXid storage.TransactionID, err error) {
	if len(payload) != xactRecordSize {
		return 0, fmt.Errorf("wal: xact-subabort payload len %d (want %d)", len(payload), xactRecordSize)
	}
	if payload[0] != RecordKindXactSubAbort {
		return 0, fmt.Errorf("wal: record kind %d is not xact-subabort", payload[0])
	}
	return storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5])), nil
}

// EncodeXactCommit encodes a logical-decoding commit marker for
// xid. Crash recovery skips this kind; only the M0008 logical
// decoder consumes it. See
// docs/design/0008-0001-logical-decoding-pipeline.md.
func EncodeXactCommit(xid storage.TransactionID) []byte {
	out := make([]byte, xactRecordSize)
	out[0] = RecordKindXactCommit
	binary.LittleEndian.PutUint32(out[1:5], uint32(xid))
	return out
}

// EncodeXactAbort encodes a logical-decoding abort marker.
func EncodeXactAbort(xid storage.TransactionID) []byte {
	out := make([]byte, xactRecordSize)
	out[0] = RecordKindXactAbort
	binary.LittleEndian.PutUint32(out[1:5], uint32(xid))
	return out
}

// DecodeXactMarker returns the xid carried by a commit or abort
// marker payload. The caller already knows the kind from the
// payload's first byte; this helper just unpacks the xid.
func DecodeXactMarker(payload []byte) (storage.TransactionID, error) {
	if len(payload) != xactRecordSize {
		return 0, fmt.Errorf("wal: invalid xact-marker payload len %d (want %d)", len(payload), xactRecordSize)
	}
	if payload[0] != RecordKindXactCommit && payload[0] != RecordKindXactAbort {
		return 0, fmt.Errorf("wal: record kind %d is not an xact marker", payload[0])
	}
	return storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5])), nil
}

// ReplayStats summarizes one replay run.
type ReplayStats struct {
	Records       int
	Applied       int
	CheckpointLSN uint64
}

// EncodeCheckpoint encodes a checkpoint marker record payload.
func EncodeCheckpoint() []byte {
	return []byte{RecordKindCheckpoint}
}

// EncodeCheckpointCompat encodes a PG-compatible CheckPoint struct
// for emission as an XLogRecord payload. The resulting record will be
// classified as RmgrXLog + XLOG_CHECKPOINT_ONLINE so a PG standby can
// recognise it during recovery (M0102-0007).
//
// redoLSN0 is the 0-based byte position of the first byte of the
// checkpoint record in the WAL stream. It must match the record's
// actual start position exactly — PG's xlogreader validates
// checkPoint.redo against ReadRecPtr.
func EncodeCheckpointCompat(redoLSN0 uint64, tli uint32) []byte {
	// Encode a minimal PG18 CheckPoint struct (sizeof=88).
	// Offsets verified against compiled PG18 binary (DWARF):
	//   redo           XLogRecPtr  8  (offset 0)
	//   ThisTimeLineID TimeLineID  4  (offset 8)
	//   PrevTimeLineID TimeLineID  4  (offset 12)
	//   fullPageWrites bool       1  (offset 16)
	//   [pad]                     3  (offset 17)
	//   wal_level      int        4  (offset 20)
	//   nextXid        FullTxnId  8  (offset 24)
	//   nextOid        Oid        4  (offset 32)
	//   nextMulti      MultiXact  4  (offset 36)
	//   nextMultiOff   MultiXOff  4  (offset 40)
	//   oldestXid      TxnId      4  (offset 44)
	//   oldestXidDB    Oid        4  (offset 48)
	//   oldestMulti    MultiXact  4  (offset 52)
	//   oldestMultiDB  Oid        4  (offset 56)
	//   [pad 4]                   —  (offset 60; pg_time_t alignment)
	//   time           pg_time_t  8  (offset 64)
	//   oldestCommitTsXid TxnId   4  (offset 72)
	//   newestCommitTsXid TxnId   4  (offset 76)
	//   oldestActiveXid TxnId     4  (offset 80)
	//   [trailing pad 4]          —  (offset 84; struct 8-byte align)
	const checkPointSize = 88
	payload := make([]byte, checkPointSize)
	le := binary.LittleEndian
	now := time.Now()

	le.PutUint64(payload[0:8], redoLSN0)           // redo
	le.PutUint32(payload[8:12], tli)               // ThisTimeLineID
	le.PutUint32(payload[20:24], 1)                // wal_level (replica)
	le.PutUint64(payload[24:32], 3)                // nextXid (>= FirstNormalTxnId)
	le.PutUint32(payload[32:36], 16384)            // nextOid
	le.PutUint32(payload[36:40], 1)                // nextMulti
	le.PutUint32(payload[44:48], 3)                // oldestXid
	le.PutUint32(payload[52:56], 1)                // oldestMulti
	// time (pg_time_t=int64, 8-byte aligned → starts at offset 64)
	le.PutUint64(payload[64:72], uint64(now.Unix())) // time
	// After time (offset 72): oldestCommitTsXid, newestCommitTsXid,
	// oldestActiveXid. Each is TransactionId (uint32, 4 bytes).
	// NOTE: pg_time_t alignment forces 4-byte pad before time, pushing
	// offsets: time=64, oldestCommitTsXid=72, newestCommitTsXid=76,
	// oldestActiveXid=80, sizeof(CheckPoint)=88.
	le.PutUint32(payload[72:76], 3) // oldestCommitTsXid
	le.PutUint32(payload[76:80], 3) // newestCommitTsXid
	le.PutUint32(payload[80:84], 3) // oldestActiveXid

	return payload
}

// EncodePageImage encodes one full-page image record payload.
func EncodePageImage(rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) ([]byte, error) {
	if len(page) != storage.BlockSize {
		return nil, fmt.Errorf("wal: page image is %d bytes, want %d", len(page), storage.BlockSize)
	}
	out := make([]byte, pageImageHeaderSize+storage.BlockSize)
	out[0] = RecordKindPageImage
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	copy(out[pageImageHeaderSize:], page)
	return out, nil
}

// EncodeHeapInsert encodes one logical heap-insert redo record.
// `lineSlot` is the 1-based line-pointer slot returned by
// PageAddHeapTuple at original mutation time; replay restores the
// same line-pointer assignment by inserting at that slot.
func EncodeHeapInsert(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, tuple []byte) []byte {
	out := make([]byte, heapInsertHeaderSize+len(tuple))
	out[0] = RecordKindHeapInsert
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], lineSlot)
	copy(out[heapInsertHeaderSize:], tuple)
	return out
}

// DecodeHeapInsert returns the rel + block + lineSlot + tuple
// bytes carried by a HeapInsert record payload.
func DecodeHeapInsert(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, tuple []byte, err error) {
	if len(payload) < heapInsertHeaderSize {
		err = fmt.Errorf("wal: invalid heap-insert payload len %d (want >= %d)", len(payload), heapInsertHeaderSize)
		return
	}
	if payload[0] != RecordKindHeapInsert {
		err = fmt.Errorf("wal: record kind %d is not heap-insert", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	lineSlot = binary.LittleEndian.Uint16(payload[14:16])
	tuple = make([]byte, len(payload)-heapInsertHeaderSize)
	copy(tuple, payload[heapInsertHeaderSize:])
	return
}

// EncodeHeapDelete encodes one logical heap-delete (xmax stamp)
// redo record. oldTuple, when non-nil, is appended after the
// fixed 20-byte header so the logical decoder can reconstruct the
// pre-delete row for logical replication.
func EncodeHeapDelete(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, oldTuple []byte) []byte {
	out := make([]byte, heapDeleteSize+len(oldTuple))
	out[0] = RecordKindHeapDelete
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], lineSlot)
	binary.LittleEndian.PutUint32(out[16:20], uint32(xmax))
	if len(oldTuple) > 0 {
		copy(out[heapDeleteSize:], oldTuple)
	}
	return out
}

// DecodeHeapDelete decodes a HeapDelete record payload. oldTuple is
// non-nil only when the record was encoded with an old-tuple extension
// (payload length > heapDeleteSize); legacy 20-byte records return nil.
func DecodeHeapDelete(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, oldTuple []byte, err error) {
	if len(payload) < heapDeleteSize {
		err = fmt.Errorf("wal: heap-delete payload too short: %d (min %d)", len(payload), heapDeleteSize)
		return
	}
	if payload[0] != RecordKindHeapDelete {
		err = fmt.Errorf("wal: record kind %d is not heap-delete", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	lineSlot = binary.LittleEndian.Uint16(payload[14:16])
	xmax = storage.TransactionID(binary.LittleEndian.Uint32(payload[16:20]))
	if len(payload) > heapDeleteSize {
		oldTuple = make([]byte, len(payload)-heapDeleteSize)
		copy(oldTuple, payload[heapDeleteSize:])
	}
	return
}

// EncodeHeapLock encodes one row-lock redo record (M0021
// tuple-level locking step 3). `xmax` is the locking xact's xid;
// `lockStrength` carries the HeapXmax* lock-mode bits to OR into
// the tuple's infomask alongside HEAP_XMAX_LOCK_ONLY. Mirrors
// upstream's xl_heap_lock at the level of detail goopg's replay
// path needs; XID-tracking and MultiXact handling are deferred.
func EncodeHeapLock(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, lockStrength uint16) []byte {
	out := make([]byte, heapLockSize)
	out[0] = RecordKindHeapLock
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], lineSlot)
	binary.LittleEndian.PutUint32(out[16:20], uint32(xmax))
	binary.LittleEndian.PutUint16(out[20:22], lockStrength)
	return out
}

// DecodeHeapLock decodes a HeapLock record payload.
func DecodeHeapLock(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, lockStrength uint16, err error) {
	if len(payload) != heapLockSize {
		err = fmt.Errorf("wal: invalid heap-lock payload len %d (want %d)", len(payload), heapLockSize)
		return
	}
	if payload[0] != RecordKindHeapLock {
		err = fmt.Errorf("wal: record kind %d is not heap-lock", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	lineSlot = binary.LittleEndian.Uint16(payload[14:16])
	xmax = storage.TransactionID(binary.LittleEndian.Uint32(payload[16:20]))
	lockStrength = binary.LittleEndian.Uint16(payload[20:22])
	return
}

// EncodeHeapHotUpdate encodes one atomic HOT-update redo record
// (M0046-0001). The record captures the old-slot xmax stamp and the
// new tuple bytes (which carry HeapOnlyTuple in their infomask) — both
// on the same heap page. Replay inserts the new tuple (getting
// newSlot), then stamps the old slot via PageStampHotOldTuple.
func EncodeHeapHotUpdate(rel storage.RelFileNode, blk storage.BlockNumber, oldSlot uint16, xmax storage.TransactionID, tupleBytes []byte) []byte {
	out := make([]byte, heapHotUpdateHeaderSize+len(tupleBytes))
	out[0] = RecordKindHeapHotUpdate
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], oldSlot)
	binary.LittleEndian.PutUint32(out[16:20], uint32(xmax))
	copy(out[heapHotUpdateHeaderSize:], tupleBytes)
	return out
}

// DecodeHeapHotUpdate decodes a HeapHotUpdate record payload.
func DecodeHeapHotUpdate(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, oldSlot uint16, xmax storage.TransactionID, tupleBytes []byte, err error) {
	if len(payload) < heapHotUpdateHeaderSize {
		err = fmt.Errorf("wal: invalid heap-hot-update payload len %d (want >= %d)", len(payload), heapHotUpdateHeaderSize)
		return
	}
	if payload[0] != RecordKindHeapHotUpdate {
		err = fmt.Errorf("wal: record kind %d is not heap-hot-update", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	oldSlot = binary.LittleEndian.Uint16(payload[14:16])
	xmax = storage.TransactionID(binary.LittleEndian.Uint32(payload[16:20]))
	tupleBytes = make([]byte, len(payload)-heapHotUpdateHeaderSize)
	copy(tupleBytes, payload[heapHotUpdateHeaderSize:])
	return
}

// heapPruneOptHdrSize: kind(1) + rel(9) + blk(4) + nRedirects(2) + nUnused(2) = 18.
const heapPruneOptHdrSize = 18

// EncodeHeapPruneOpt encodes one opportunistic page-pruning redo record
// (M0046-0002, mirrors PostgreSQL's XLOG_HEAP2_PRUNE). Carries two lists:
//   - redirects: (oldSlot, newSlot) pairs for HOT chain root slots that were
//     converted to ItemIDRedirect so the index entry stays valid.
//   - unused: slot numbers marked ItemIDUnused (HOT-only and standalone dead).
//
// Format:
//
//	kind(1) | rel(9) | blk(4) | nRedirects(2) | nUnused(2) |
//	redirects[nRedirects*4] | unusedSlots[nUnused*2]
func EncodeHeapPruneOpt(rel storage.RelFileNode, blk storage.BlockNumber, redirects [][2]uint16, unused []uint16) []byte {
	sz := heapPruneOptHdrSize + 4*len(redirects) + 2*len(unused)
	out := make([]byte, sz)
	out[0] = RecordKindHeapPruneOpt
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], uint16(len(redirects)))
	binary.LittleEndian.PutUint16(out[16:18], uint16(len(unused)))
	off := heapPruneOptHdrSize
	for _, r := range redirects {
		binary.LittleEndian.PutUint16(out[off:off+2], r[0])
		binary.LittleEndian.PutUint16(out[off+2:off+4], r[1])
		off += 4
	}
	for _, s := range unused {
		binary.LittleEndian.PutUint16(out[off:off+2], s)
		off += 2
	}
	return out
}

// DecodeHeapPruneOpt returns the rel + block + redirect pairs + unused slots
// carried by a HeapPruneOpt record payload.
func DecodeHeapPruneOpt(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, redirects [][2]uint16, unused []uint16, err error) {
	if len(payload) < heapPruneOptHdrSize {
		err = fmt.Errorf("wal: invalid heap-prune-opt payload len %d (want >= %d)", len(payload), heapPruneOptHdrSize)
		return
	}
	if payload[0] != RecordKindHeapPruneOpt {
		err = fmt.Errorf("wal: record kind %d is not heap-prune-opt", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	nRedirects := int(binary.LittleEndian.Uint16(payload[14:16]))
	nUnused := int(binary.LittleEndian.Uint16(payload[16:18]))
	want := heapPruneOptHdrSize + 4*nRedirects + 2*nUnused
	if len(payload) != want {
		err = fmt.Errorf("wal: heap-prune-opt payload len %d want %d (nRedirects=%d nUnused=%d)", len(payload), want, nRedirects, nUnused)
		return
	}
	off := heapPruneOptHdrSize
	redirects = make([][2]uint16, nRedirects)
	for i := range redirects {
		redirects[i][0] = binary.LittleEndian.Uint16(payload[off : off+2])
		redirects[i][1] = binary.LittleEndian.Uint16(payload[off+2 : off+4])
		off += 4
	}
	unused = make([]uint16, nUnused)
	for i := range unused {
		unused[i] = binary.LittleEndian.Uint16(payload[off : off+2])
		off += 2
	}
	return
}

// EncodeSmgrCreate encodes a relation-file creation record.
// Mirrors upstream's XLOG_SMGR_CREATE. The redo handler ensures
// the relfile has at least one initialised block (idempotent).
func EncodeSmgrCreate(rel storage.RelFileNode) []byte {
	out := make([]byte, smgrRecordSize)
	out[0] = RecordKindSmgrCreate
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	return out
}

// DecodeSmgrCreate decodes a SmgrCreate record payload.
func DecodeSmgrCreate(payload []byte) (rel storage.RelFileNode, err error) {
	if len(payload) < smgrRecordSize {
		err = fmt.Errorf("wal: invalid smgr-create payload len %d (want %d)", len(payload), smgrRecordSize)
		return
	}
	if payload[0] != RecordKindSmgrCreate {
		err = fmt.Errorf("wal: record kind %d is not smgr-create", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	return
}

// EncodeSmgrTruncate encodes a relation-file truncation record.
// Mirrors upstream's XLOG_SMGR_TRUNCATE. The redo handler truncates
// the relfile to 0 blocks.
func EncodeSmgrTruncate(rel storage.RelFileNode) []byte {
	out := make([]byte, smgrRecordSize)
	out[0] = RecordKindSmgrTruncate
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	return out
}

// DecodeSmgrTruncate decodes a SmgrTruncate record payload.
func DecodeSmgrTruncate(payload []byte) (rel storage.RelFileNode, err error) {
	if len(payload) < smgrRecordSize {
		err = fmt.Errorf("wal: invalid smgr-truncate payload len %d (want %d)", len(payload), smgrRecordSize)
		return
	}
	if payload[0] != RecordKindSmgrTruncate {
		err = fmt.Errorf("wal: record kind %d is not smgr-truncate", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	return
}

// EncodeHeapVacuum encodes one logical heap-vacuum (page prune)
// redo record. `deadSlots` carries the 1-based LP_NORMAL slot
// numbers to reclaim, in any order — replay treats the list as
// a set.
func EncodeHeapVacuum(rel storage.RelFileNode, blk storage.BlockNumber, deadSlots []uint16) []byte {
	out := make([]byte, heapVacuumHeaderSize+2*len(deadSlots))
	out[0] = RecordKindHeapVacuum
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], uint16(len(deadSlots)))
	for i, s := range deadSlots {
		binary.LittleEndian.PutUint16(out[heapVacuumHeaderSize+2*i:heapVacuumHeaderSize+2*i+2], s)
	}
	return out
}

// DecodeHeapVacuum returns the rel + block + dead-slot list
// carried by a HeapVacuum record payload.
func DecodeHeapVacuum(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, deadSlots []uint16, err error) {
	if len(payload) < heapVacuumHeaderSize {
		err = fmt.Errorf("wal: invalid heap-vacuum payload len %d (want >= %d)", len(payload), heapVacuumHeaderSize)
		return
	}
	if payload[0] != RecordKindHeapVacuum {
		err = fmt.Errorf("wal: record kind %d is not heap-vacuum", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	count := int(binary.LittleEndian.Uint16(payload[14:16]))
	want := heapVacuumHeaderSize + 2*count
	if len(payload) != want {
		err = fmt.Errorf("wal: heap-vacuum payload len %d does not match count=%d (want %d)", len(payload), count, want)
		return
	}
	deadSlots = make([]uint16, count)
	for i := 0; i < count; i++ {
		deadSlots[i] = binary.LittleEndian.Uint16(payload[heapVacuumHeaderSize+2*i : heapVacuumHeaderSize+2*i+2])
	}
	return
}

// BtreeVacuumPayload mirrors `EncodeBtreeVacuum`'s on-wire
// fields for callers that prefer struct construction. KeptItems
// carries each surviving btree item's raw bytes (the same
// blob `pageAddItemRaw` writes) in the order they will be
// re-added to the page. OpaqueFlags is the post-vacuum
// `BTPageOpaque.Flags` value (overwritten verbatim during
// replay). (M0079-0002.)
type BtreeVacuumPayload struct {
	Rel         storage.RelFileNode
	Blk         storage.BlockNumber
	KeptItems   [][]byte
	OpaqueFlags uint16
}

// EncodeBtreeVacuum encodes one logical B-tree vacuum redo
// record. (M0079-0002.)
func EncodeBtreeVacuum(rel storage.RelFileNode, blk storage.BlockNumber, keptItems [][]byte, opaqueFlags uint16) []byte {
	bodySize := 0
	for _, it := range keptItems {
		bodySize += 2 + len(it)
	}
	out := make([]byte, btreeVacuumHeaderSize+bodySize+btreeVacuumTrailerSize)
	out[0] = RecordKindBtreeVacuum
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], uint16(len(keptItems)))
	off := btreeVacuumHeaderSize
	for _, it := range keptItems {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(it)))
		off += 2
		off += copy(out[off:], it)
	}
	binary.LittleEndian.PutUint16(out[off:off+2], opaqueFlags)
	return out
}

// DecodeBtreeVacuum decodes a `RecordKindBtreeVacuum` payload
// into the rel + block + kept-items list + post-vacuum opaque
// flags. (M0079-0002.)
func DecodeBtreeVacuum(payload []byte) (BtreeVacuumPayload, error) {
	var p BtreeVacuumPayload
	if len(payload) < btreeVacuumHeaderSize+btreeVacuumTrailerSize {
		return p, fmt.Errorf("wal: invalid btree-vacuum payload len %d (want >= %d)", len(payload), btreeVacuumHeaderSize+btreeVacuumTrailerSize)
	}
	if payload[0] != RecordKindBtreeVacuum {
		return p, fmt.Errorf("wal: record kind %d is not btree-vacuum", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.Blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	count := int(binary.LittleEndian.Uint16(payload[14:16]))
	off := btreeVacuumHeaderSize
	p.KeptItems = make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		if off+2 > len(payload)-btreeVacuumTrailerSize {
			return p, fmt.Errorf("wal: btree-vacuum payload truncated at item %d header", i)
		}
		itLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if off+itLen > len(payload)-btreeVacuumTrailerSize {
			return p, fmt.Errorf("wal: btree-vacuum payload truncated at item %d body (want %d bytes)", i, itLen)
		}
		// Copy so the caller can use the slice safely after
		// payload buffer is reused.
		dup := make([]byte, itLen)
		copy(dup, payload[off:off+itLen])
		p.KeptItems = append(p.KeptItems, dup)
		off += itLen
	}
	if off+btreeVacuumTrailerSize != len(payload) {
		return p, fmt.Errorf("wal: btree-vacuum payload trailer offset %d (len %d) does not match expected %d", off, len(payload), len(payload)-btreeVacuumTrailerSize)
	}
	p.OpaqueFlags = binary.LittleEndian.Uint16(payload[off : off+2])
	return p, nil
}

// BtreeUnlinkPagePayload mirrors `EncodeBtreeUnlinkPage`'s
// on-wire fields. (M0079-0003.)
type BtreeUnlinkPagePayload struct {
	Rel              storage.RelFileNode
	LeafBlk          storage.BlockNumber
	LeafFlagsAfter   uint16
	HasLeftSib       bool
	LeftSibBlk       storage.BlockNumber
	LeftSibNewNext   storage.BlockNumber
	HasRightSib      bool
	RightSibBlk      storage.BlockNumber
	RightSibNewPrev  storage.BlockNumber
	HasParent        bool
	ParentBlk        storage.BlockNumber
	ParentRemoveSlot uint16
}

// EncodeBtreeUnlinkPage encodes the M0079-0003 atomic
// page-deletion record. Replay reapplies sibling Prev/Next
// patches, the leaf opaque-flags transition, and the parent
// downlink removal under per-page pd_lsn idempotency.
func EncodeBtreeUnlinkPage(p BtreeUnlinkPagePayload) []byte {
	out := make([]byte, btreeUnlinkPageSize)
	out[0] = RecordKindBtreeUnlinkPage
	binary.LittleEndian.PutUint32(out[1:5], p.Rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], p.Rel.RelOid)
	out[9] = byte(p.Rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(p.LeafBlk))
	binary.LittleEndian.PutUint16(out[14:16], p.LeafFlagsAfter)
	if p.HasLeftSib {
		out[16] = 1
	}
	binary.LittleEndian.PutUint32(out[17:21], uint32(p.LeftSibBlk))
	binary.LittleEndian.PutUint32(out[21:25], uint32(p.LeftSibNewNext))
	if p.HasRightSib {
		out[25] = 1
	}
	binary.LittleEndian.PutUint32(out[26:30], uint32(p.RightSibBlk))
	binary.LittleEndian.PutUint32(out[30:34], uint32(p.RightSibNewPrev))
	if p.HasParent {
		out[34] = 1
	}
	binary.LittleEndian.PutUint32(out[35:39], uint32(p.ParentBlk))
	binary.LittleEndian.PutUint16(out[39:41], p.ParentRemoveSlot)
	return out
}

// DecodeBtreeUnlinkPage decodes a `RecordKindBtreeUnlinkPage`
// payload. (M0079-0003.)
func DecodeBtreeUnlinkPage(payload []byte) (BtreeUnlinkPagePayload, error) {
	var p BtreeUnlinkPagePayload
	if len(payload) != btreeUnlinkPageSize {
		return p, fmt.Errorf("wal: btree-unlink-page payload len %d (want %d)", len(payload), btreeUnlinkPageSize)
	}
	if payload[0] != RecordKindBtreeUnlinkPage {
		return p, fmt.Errorf("wal: record kind %d is not btree-unlink-page", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.LeafBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	p.LeafFlagsAfter = binary.LittleEndian.Uint16(payload[14:16])
	p.HasLeftSib = payload[16] != 0
	p.LeftSibBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[17:21]))
	p.LeftSibNewNext = storage.BlockNumber(binary.LittleEndian.Uint32(payload[21:25]))
	p.HasRightSib = payload[25] != 0
	p.RightSibBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[26:30]))
	p.RightSibNewPrev = storage.BlockNumber(binary.LittleEndian.Uint32(payload[30:34]))
	p.HasParent = payload[34] != 0
	p.ParentBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[35:39]))
	p.ParentRemoveSlot = binary.LittleEndian.Uint16(payload[39:41])
	return p, nil
}

// BtreeNewRootPayload mirrors `EncodeBtreeNewRoot`'s on-wire
// fields. (M0079-0003.)
type BtreeNewRootPayload struct {
	Rel     storage.RelFileNode
	RootBlk storage.BlockNumber
	Level   uint32
	Items   [][]byte
}

// EncodeBtreeNewRoot encodes the M0079-0003 root-replacement
// record. Used when (a) a split bubbles up to a new root, or
// (b) VACUUM resets to an empty root after fully emptying the
// tree.
func EncodeBtreeNewRoot(p BtreeNewRootPayload) []byte {
	bodySize := 0
	for _, it := range p.Items {
		bodySize += 2 + len(it)
	}
	out := make([]byte, btreeNewRootHeaderSize+bodySize)
	out[0] = RecordKindBtreeNewRoot
	binary.LittleEndian.PutUint32(out[1:5], p.Rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], p.Rel.RelOid)
	out[9] = byte(p.Rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(p.RootBlk))
	binary.LittleEndian.PutUint32(out[14:18], p.Level)
	binary.LittleEndian.PutUint16(out[18:20], uint16(len(p.Items)))
	off := btreeNewRootHeaderSize
	for _, it := range p.Items {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(it)))
		off += 2
		off += copy(out[off:], it)
	}
	return out
}

// DecodeBtreeNewRoot decodes a `RecordKindBtreeNewRoot` payload.
// (M0079-0003.)
func DecodeBtreeNewRoot(payload []byte) (BtreeNewRootPayload, error) {
	var p BtreeNewRootPayload
	if len(payload) < btreeNewRootHeaderSize {
		return p, fmt.Errorf("wal: btree-newroot payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindBtreeNewRoot {
		return p, fmt.Errorf("wal: record kind %d is not btree-newroot", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.RootBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	p.Level = binary.LittleEndian.Uint32(payload[14:18])
	count := int(binary.LittleEndian.Uint16(payload[18:20]))
	off := btreeNewRootHeaderSize
	p.Items = make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		if off+2 > len(payload) {
			return p, fmt.Errorf("wal: btree-newroot truncated at item %d header", i)
		}
		itLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if off+itLen > len(payload) {
			return p, fmt.Errorf("wal: btree-newroot truncated at item %d body", i)
		}
		dup := make([]byte, itLen)
		copy(dup, payload[off:off+itLen])
		p.Items = append(p.Items, dup)
		off += itLen
	}
	if off != len(payload) {
		return p, fmt.Errorf("wal: btree-newroot trailing bytes (%d remaining)", len(payload)-off)
	}
	return p, nil
}

// BtreeMarkHalfDeadPayload mirrors `EncodeBtreeMarkPageHalfDead`'s
// on-wire fields. (M0079-0003.)
type BtreeMarkHalfDeadPayload struct {
	Rel        storage.RelFileNode
	LeafBlk    storage.BlockNumber
	FlagsAfter uint16
}

// EncodeBtreeMarkPageHalfDead encodes the M0079-0003 leaf-only
// half-dead transition record.
func EncodeBtreeMarkPageHalfDead(p BtreeMarkHalfDeadPayload) []byte {
	out := make([]byte, btreeMarkHalfDeadSize)
	out[0] = RecordKindBtreeMarkPageHalfDead
	binary.LittleEndian.PutUint32(out[1:5], p.Rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], p.Rel.RelOid)
	out[9] = byte(p.Rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(p.LeafBlk))
	binary.LittleEndian.PutUint16(out[14:16], p.FlagsAfter)
	return out
}

// DecodeBtreeMarkPageHalfDead decodes the record. (M0079-0003.)
func DecodeBtreeMarkPageHalfDead(payload []byte) (BtreeMarkHalfDeadPayload, error) {
	var p BtreeMarkHalfDeadPayload
	if len(payload) != btreeMarkHalfDeadSize {
		return p, fmt.Errorf("wal: btree-mark-halfdead payload len %d (want %d)", len(payload), btreeMarkHalfDeadSize)
	}
	if payload[0] != RecordKindBtreeMarkPageHalfDead {
		return p, fmt.Errorf("wal: record kind %d is not btree-mark-halfdead", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.LeafBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	p.FlagsAfter = binary.LittleEndian.Uint16(payload[14:16])
	return p, nil
}

// EncodeHeapFreeze encodes the M0080-0001 heap-freeze record.
// `frozenSlots` is the 1-based ascending list of LP_NORMAL slots
// whose tuple xmin was rewritten to FrozenTransactionID.
func EncodeHeapFreeze(rel storage.RelFileNode, blk storage.BlockNumber, frozenSlots []uint16) []byte {
	out := make([]byte, heapFreezeHeaderSize+2*len(frozenSlots))
	out[0] = RecordKindHeapFreeze
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], uint16(len(frozenSlots)))
	for i, s := range frozenSlots {
		binary.LittleEndian.PutUint16(out[heapFreezeHeaderSize+2*i:heapFreezeHeaderSize+2*i+2], s)
	}
	return out
}

// DecodeHeapFreeze decodes a `RecordKindHeapFreeze` payload.
// (M0080-0001.)
func DecodeHeapFreeze(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, frozenSlots []uint16, err error) {
	if len(payload) < heapFreezeHeaderSize {
		err = fmt.Errorf("wal: invalid heap-freeze payload len %d (want >= %d)", len(payload), heapFreezeHeaderSize)
		return
	}
	if payload[0] != RecordKindHeapFreeze {
		err = fmt.Errorf("wal: record kind %d is not heap-freeze", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	count := int(binary.LittleEndian.Uint16(payload[14:16]))
	want := heapFreezeHeaderSize + 2*count
	if len(payload) != want {
		err = fmt.Errorf("wal: heap-freeze payload len %d does not match count=%d (want %d)", len(payload), count, want)
		return
	}
	frozenSlots = make([]uint16, count)
	for i := 0; i < count; i++ {
		frozenSlots[i] = binary.LittleEndian.Uint16(payload[heapFreezeHeaderSize+2*i : heapFreezeHeaderSize+2*i+2])
	}
	return
}

// HeapUpdatePayload mirrors the M0080-0002 atomic UPDATE
// record's on-wire fields. Captures both the old tuple's
// xmax stamp + the new tuple's insert in one record.
type HeapUpdatePayload struct {
	Rel         storage.RelFileNode
	OldBlk      storage.BlockNumber
	OldLineSlot uint16
	Xmax        storage.TransactionID
	NewBlk      storage.BlockNumber
	NewLineSlot uint16
	Tuple       []byte
}

// EncodeHeapUpdate encodes the M0080-0002 atomic UPDATE
// record. (M0080-0002.)
func EncodeHeapUpdate(p HeapUpdatePayload) []byte {
	out := make([]byte, heapUpdateHeaderSize+len(p.Tuple))
	out[0] = RecordKindHeapUpdate
	binary.LittleEndian.PutUint32(out[1:5], p.Rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], p.Rel.RelOid)
	out[9] = byte(p.Rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(p.OldBlk))
	binary.LittleEndian.PutUint16(out[14:16], p.OldLineSlot)
	binary.LittleEndian.PutUint32(out[16:20], uint32(p.Xmax))
	binary.LittleEndian.PutUint32(out[20:24], uint32(p.NewBlk))
	binary.LittleEndian.PutUint16(out[24:26], p.NewLineSlot)
	binary.LittleEndian.PutUint32(out[26:30], uint32(len(p.Tuple)))
	copy(out[heapUpdateHeaderSize:], p.Tuple)
	return out
}

// DecodeHeapUpdate decodes a `RecordKindHeapUpdate` payload.
// (M0080-0002.)
func DecodeHeapUpdate(payload []byte) (HeapUpdatePayload, error) {
	var p HeapUpdatePayload
	if len(payload) < heapUpdateHeaderSize {
		return p, fmt.Errorf("wal: heap-update payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindHeapUpdate {
		return p, fmt.Errorf("wal: record kind %d is not heap-update", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.OldBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	p.OldLineSlot = binary.LittleEndian.Uint16(payload[14:16])
	p.Xmax = storage.TransactionID(binary.LittleEndian.Uint32(payload[16:20]))
	p.NewBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[20:24]))
	p.NewLineSlot = binary.LittleEndian.Uint16(payload[24:26])
	tupLen := int(binary.LittleEndian.Uint32(payload[26:30]))
	if heapUpdateHeaderSize+tupLen != len(payload) {
		return p, fmt.Errorf("wal: heap-update payload tuple-len mismatch (have %d, want %d)", len(payload)-heapUpdateHeaderSize, tupLen)
	}
	p.Tuple = make([]byte, tupLen)
	copy(p.Tuple, payload[heapUpdateHeaderSize:])
	return p, nil
}

// HeapMultiInsertEntry is one tuple's destination + bytes
// inside a `RecordKindHeapMultiInsert`. (M0080-0002.)
type HeapMultiInsertEntry struct {
	LineSlot uint16
	Tuple    []byte
}

// HeapMultiInsertPayload mirrors the M0080-0002 bulk-insert
// record's on-wire fields.
type HeapMultiInsertPayload struct {
	Rel     storage.RelFileNode
	Blk     storage.BlockNumber
	Entries []HeapMultiInsertEntry
}

// EncodeHeapMultiInsert encodes the M0080-0002 bulk-insert
// record carrying N tuples destined for the same page.
func EncodeHeapMultiInsert(p HeapMultiInsertPayload) []byte {
	bodySize := 0
	for _, e := range p.Entries {
		bodySize += 2 + 4 + len(e.Tuple)
	}
	out := make([]byte, heapMultiInsertHeaderSize+bodySize)
	out[0] = RecordKindHeapMultiInsert
	binary.LittleEndian.PutUint32(out[1:5], p.Rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], p.Rel.RelOid)
	out[9] = byte(p.Rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(p.Blk))
	binary.LittleEndian.PutUint16(out[14:16], uint16(len(p.Entries)))
	off := heapMultiInsertHeaderSize
	for _, e := range p.Entries {
		binary.LittleEndian.PutUint16(out[off:off+2], e.LineSlot)
		off += 2
		binary.LittleEndian.PutUint32(out[off:off+4], uint32(len(e.Tuple)))
		off += 4
		off += copy(out[off:], e.Tuple)
	}
	return out
}

// DecodeHeapMultiInsert decodes a `RecordKindHeapMultiInsert`
// payload. (M0080-0002.)
func DecodeHeapMultiInsert(payload []byte) (HeapMultiInsertPayload, error) {
	var p HeapMultiInsertPayload
	if len(payload) < heapMultiInsertHeaderSize {
		return p, fmt.Errorf("wal: heap-multi-insert payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindHeapMultiInsert {
		return p, fmt.Errorf("wal: record kind %d is not heap-multi-insert", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.Blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	count := int(binary.LittleEndian.Uint16(payload[14:16]))
	off := heapMultiInsertHeaderSize
	p.Entries = make([]HeapMultiInsertEntry, 0, count)
	for i := 0; i < count; i++ {
		if off+6 > len(payload) {
			return p, fmt.Errorf("wal: heap-multi-insert truncated at entry %d header", i)
		}
		slot := binary.LittleEndian.Uint16(payload[off : off+2])
		off += 2
		tupLen := int(binary.LittleEndian.Uint32(payload[off : off+4]))
		off += 4
		if off+tupLen > len(payload) {
			return p, fmt.Errorf("wal: heap-multi-insert truncated at entry %d body", i)
		}
		dup := make([]byte, tupLen)
		copy(dup, payload[off:off+tupLen])
		p.Entries = append(p.Entries, HeapMultiInsertEntry{LineSlot: slot, Tuple: dup})
		off += tupLen
	}
	if off != len(payload) {
		return p, fmt.Errorf("wal: heap-multi-insert trailing bytes (%d remaining)", len(payload)-off)
	}
	return p, nil
}

// HeapVisiblePayload mirrors the M0080-0003 VM update record
// fields.
type HeapVisiblePayload struct {
	Rel       storage.RelFileNode
	HeapBlk   storage.BlockNumber
	Flags     uint8
	CutoffXid storage.TransactionID
}

// HeapVisible flag bits.
const (
	HeapVisibleSetAllVisible uint8 = 0x01
	HeapVisibleSetAllFrozen  uint8 = 0x02
)

// EncodeHeapVisible encodes the M0080-0003 visibility-map
// update record.
func EncodeHeapVisible(p HeapVisiblePayload) []byte {
	out := make([]byte, heapVisibleSize)
	out[0] = RecordKindHeapVisible
	binary.LittleEndian.PutUint32(out[1:5], p.Rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], p.Rel.RelOid)
	out[9] = byte(p.Rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(p.HeapBlk))
	out[14] = p.Flags
	binary.LittleEndian.PutUint32(out[15:19], uint32(p.CutoffXid))
	return out
}

// DecodeHeapVisible decodes a `RecordKindHeapVisible` payload.
func DecodeHeapVisible(payload []byte) (HeapVisiblePayload, error) {
	var p HeapVisiblePayload
	if len(payload) != heapVisibleSize {
		return p, fmt.Errorf("wal: heap-visible payload len %d (want %d)", len(payload), heapVisibleSize)
	}
	if payload[0] != RecordKindHeapVisible {
		return p, fmt.Errorf("wal: record kind %d is not heap-visible", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.HeapBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	p.Flags = payload[14]
	p.CutoffXid = storage.TransactionID(binary.LittleEndian.Uint32(payload[15:19]))
	return p, nil
}

// BtreeReusePagePayload mirrors the M0080-0004 page-recycle
// record fields.
type BtreeReusePagePayload struct {
	Rel             storage.RelFileNode
	Blk             storage.BlockNumber
	RecycledFromXid storage.TransactionID
}

// EncodeBtreeReusePage encodes the M0080-0004 record.
func EncodeBtreeReusePage(p BtreeReusePagePayload) []byte {
	out := make([]byte, btreeReusePageSize)
	out[0] = RecordKindBtreeReusePage
	binary.LittleEndian.PutUint32(out[1:5], p.Rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], p.Rel.RelOid)
	out[9] = byte(p.Rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(p.Blk))
	binary.LittleEndian.PutUint32(out[14:18], uint32(p.RecycledFromXid))
	return out
}

// DecodeBtreeReusePage decodes a `RecordKindBtreeReusePage` payload.
func DecodeBtreeReusePage(payload []byte) (BtreeReusePagePayload, error) {
	var p BtreeReusePagePayload
	if len(payload) != btreeReusePageSize {
		return p, fmt.Errorf("wal: btree-reuse-page payload len %d (want %d)", len(payload), btreeReusePageSize)
	}
	if payload[0] != RecordKindBtreeReusePage {
		return p, fmt.Errorf("wal: record kind %d is not btree-reuse-page", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.Blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	p.RecycledFromXid = storage.TransactionID(binary.LittleEndian.Uint32(payload[14:18]))
	return p, nil
}

// BtreeMetaCleanupPayload mirrors the M0080-0004 metapage
// cleanup-XID record fields.
type BtreeMetaCleanupPayload struct {
	Rel                         storage.RelFileNode
	NumHeapTuples               int64
	LastCleanupNumDeletedTuples int64
}

// EncodeBtreeMetaCleanup encodes the M0080-0004 metapage
// cleanup-XID update record.
func EncodeBtreeMetaCleanup(p BtreeMetaCleanupPayload) []byte {
	out := make([]byte, btreeMetaCleanupSize)
	out[0] = RecordKindBtreeMetaCleanup
	binary.LittleEndian.PutUint32(out[1:5], p.Rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], p.Rel.RelOid)
	out[9] = byte(p.Rel.Fork)
	binary.LittleEndian.PutUint64(out[10:18], uint64(p.NumHeapTuples))
	binary.LittleEndian.PutUint64(out[18:26], uint64(p.LastCleanupNumDeletedTuples))
	return out
}

// DecodeBtreeMetaCleanup decodes a `RecordKindBtreeMetaCleanup`
// payload.
func DecodeBtreeMetaCleanup(payload []byte) (BtreeMetaCleanupPayload, error) {
	var p BtreeMetaCleanupPayload
	if len(payload) != btreeMetaCleanupSize {
		return p, fmt.Errorf("wal: btree-meta-cleanup payload len %d (want %d)", len(payload), btreeMetaCleanupSize)
	}
	if payload[0] != RecordKindBtreeMetaCleanup {
		return p, fmt.Errorf("wal: record kind %d is not btree-meta-cleanup", payload[0])
	}
	p.Rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	p.NumHeapTuples = int64(binary.LittleEndian.Uint64(payload[10:18]))
	p.LastCleanupNumDeletedTuples = int64(binary.LittleEndian.Uint64(payload[18:26]))
	return p, nil
}

// EncodeBtreeInsert encodes one logical B-tree non-split insert
// redo record. The opaque `item` payload is whatever bytes the
// caller stored on the page (in v0,
// internal/access/btree.item.marshal output: keyLen + ptr.block
// + ptr.offset + key).
func EncodeBtreeInsert(rel storage.RelFileNode, blk storage.BlockNumber, item []byte) []byte {
	out := make([]byte, btreeInsertHeaderSize+len(item))
	out[0] = RecordKindBtreeInsert
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	copy(out[btreeInsertHeaderSize:], item)
	return out
}

// DecodeBtreeInsert returns the rel + block + raw item bytes
// carried by a BtreeInsert record payload.
func DecodeBtreeInsert(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, item []byte, err error) {
	if len(payload) < btreeInsertHeaderSize {
		err = fmt.Errorf("wal: invalid btree-insert payload len %d (want >= %d)", len(payload), btreeInsertHeaderSize)
		return
	}
	if payload[0] != RecordKindBtreeInsert {
		err = fmt.Errorf("wal: record kind %d is not btree-insert", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	item = make([]byte, len(payload)-btreeInsertHeaderSize)
	copy(item, payload[btreeInsertHeaderSize:])
	return
}

// EncodeBtreeSplit encodes one atomic B-tree split record. Both
// pages must be exactly storage.BlockSize bytes; the record
// embeds them in left-then-right order so replay applies the new
// right page before any reader could follow left's right-link to
// it.
func EncodeBtreeSplit(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page) ([]byte, error) {
	if len(leftPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split left page is %d bytes, want %d", len(leftPage), storage.BlockSize)
	}
	if len(rightPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split right page is %d bytes, want %d", len(rightPage), storage.BlockSize)
	}
	out := make([]byte, btreeSplitHeaderSize+2*storage.BlockSize)
	out[0] = RecordKindBtreeSplit
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(leftBlk))
	binary.LittleEndian.PutUint32(out[14:18], uint32(rightBlk))
	copy(out[btreeSplitHeaderSize:btreeSplitHeaderSize+storage.BlockSize], leftPage)
	copy(out[btreeSplitHeaderSize+storage.BlockSize:], rightPage)
	return out, nil
}

// DecodeBtreeSplit returns the rel + (left,right) blocks + page
// images carried by a BtreeSplit record payload.
func DecodeBtreeSplit(payload []byte) (rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, err error) {
	want := btreeSplitHeaderSize + 2*storage.BlockSize
	if len(payload) != want {
		err = fmt.Errorf("wal: invalid btree-split payload len %d (want %d)", len(payload), want)
		return
	}
	if payload[0] != RecordKindBtreeSplit {
		err = fmt.Errorf("wal: record kind %d is not btree-split", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	leftBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	rightBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[14:18]))
	leftPage = make(storage.Page, storage.BlockSize)
	copy(leftPage, payload[btreeSplitHeaderSize:btreeSplitHeaderSize+storage.BlockSize])
	rightPage = make(storage.Page, storage.BlockSize)
	copy(rightPage, payload[btreeSplitHeaderSize+storage.BlockSize:])
	return
}

// DecodePageImage decodes a full-page image record payload.
func DecodePageImage(payload []byte) (storage.RelFileNode, storage.BlockNumber, storage.Page, error) {
	if len(payload) != pageImageHeaderSize+storage.BlockSize {
		return storage.RelFileNode{}, storage.InvalidBlockNumber, nil,
			fmt.Errorf("wal: invalid page-image payload len %d", len(payload))
	}
	if payload[0] != RecordKindPageImage {
		return storage.RelFileNode{}, storage.InvalidBlockNumber, nil,
			fmt.Errorf("wal: record kind %d is not page image", payload[0])
	}
	rel := storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk := storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	page := make(storage.Page, storage.BlockSize)
	copy(page, payload[pageImageHeaderSize:])
	return rel, blk, page, nil
}

// ReplayRecords replays decoded WAL records into storage.
//
// M0045-0002: replay starts FROM the last checkpoint (inclusive),
// not up to it. The checkpoint marks a point where all dirty pages
// were flushed; only records AFTER the checkpoint need to be applied
// to recover pages that may not have been flushed before the crash.
// Records before the checkpoint are already on disk and ApplyRecord's
// per-page LSN check makes re-application a safe no-op, but we skip
// them as an optimisation.
//
// If no checkpoint record exists, all records are replayed from the
// start (safe for fresh clusters or WAL without checkpoints).
func ReplayRecords(mgr *storage.Manager, records []Record) (ReplayStats, error) {
	stats := ReplayStats{Records: len(records)}
	startIdx, checkpointLSN := replayStart(records)
	stats.CheckpointLSN = checkpointLSN

	for i, r := range records[startIdx:] {
		applied, err := ApplyRecord(mgr, r)
		if err != nil {
			return stats, fmt.Errorf("wal: replay record %d lsn[%d,%d]: %w", startIdx+i, r.StartLSN, r.EndLSN, err)
		}
		if applied {
			stats.Applied++
		}
	}
	return stats, nil
}

// ApplyRecord applies a single decoded WAL record to storage. It is
// the per-record kernel shared by `ReplayRecords` (crash recovery
// from a slice already trimmed to the last checkpoint) and
// `StreamReplayer` (continuous standby replay driven by a streaming
// `RecordIterator`). Returns `applied=true` when a real page mutation
// happened, `applied=false` for marker-only records (Checkpoint).
//
// All physical and logical applies are individually idempotent via
// `pd_lsn`: re-applying a record that was already persisted is a
// no-op, which means the stream replayer can resume from any point
// the local WAL writer's `WrittenLSN` advertises without bookkeeping
// a separate "apply cursor" — a record that finished on disk but
// crashed before the storage write is re-attempted on restart, and
// one that finished both is silently skipped.
func ApplyRecord(mgr *storage.Manager, r Record) (bool, error) {
	if r.XLog != nil {
		if len(r.Payload) == 0 {
			return replayDecodedXLogRecord(mgr, r)
		}
		if !nativeApplyRecordKindKnown(r.Payload[0]) {
			return replayDecodedXLogRecord(mgr, r)
		}
	}
	if len(r.Payload) == 0 {
		return false, errors.New("wal: empty record payload")
	}
	switch r.Payload[0] {
	case RecordKindPageImage:
		if err := replayPageImage(mgr, r.Payload); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindBtreeSplit:
		if err := replayBtreeSplit(mgr, r.Payload); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapInsert:
		if err := replayHeapInsert(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindBtreeInsert:
		if err := replayBtreeInsert(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapDelete:
		if err := replayHeapDelete(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapLock:
		if err := replayHeapLock(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapVacuum:
		if err := replayHeapVacuum(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindBtreeVacuum:
		if err := replayBtreeVacuum(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindBtreeUnlinkPage:
		if err := replayBtreeUnlinkPage(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindBtreeNewRoot:
		if err := replayBtreeNewRoot(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindBtreeMarkPageHalfDead:
		if err := replayBtreeMarkPageHalfDead(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapFreeze:
		if err := replayHeapFreeze(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapUpdate:
		if err := replayHeapUpdate(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapMultiInsert:
		if err := replayHeapMultiInsert(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapVisible, RecordKindBtreeReusePage, RecordKindBtreeMetaCleanup:
		// Catalog/metadata-only records (M0080-0003 / M0080-0004).
		// VM updates, page-recycle notifications, and metapage
		// cleanup-XID advances do not require a physical replay
		// step in goopg's current design — VM is recomputed by
		// VACUUM after a crash and the cleanup-XID is informational
		// (no producer site relies on its exact value across a
		// crash). Records are recognised so a future hot-standby
		// or VM-backed visibility path can consume them.
		return false, nil
	case RecordKindCheckpoint:
		return false, nil
	case RecordKindXactCommit, RecordKindXactAbort:
		// Logical-decoding markers — physical recovery is a
		// no-op. The per-record idempotency in the data records
		// already brings storage to a consistent state; the
		// markers exist purely so the M0008 logical decoder can
		// drive its reorder buffer. See
		// docs/design/0008-0001-logical-decoding-pipeline.md.
		return false, nil
	case RecordKindCreateDatabase, RecordKindDropDatabase:
		// CREATE/DROP DATABASE records (M0054-0001) carry only a database
		// name; goopg v0 has no per-database file namespacing, so the
		// physical replay path has nothing to do. The recovery driver in
		// internal/initdb/open.go scans the WAL for these records after
		// physical replay and re-applies them to the catalog's database
		// list.
		return false, nil
	case RecordKindCreateIndex, RecordKindDropIndex:
		// CREATE/DROP INDEX records (M0079-0001) carry the catalog
		// metadata that goopg's heap representation cannot fully
		// store (no pg_index relation). The on-disk btree pages and
		// the index relfile are restored by RecordKindBtreeInsert /
		// RecordKindSmgrCreate; the in-memory catalog state is
		// reconstructed by `internal/initdb.replayIndexDDLRecords`
		// after physical replay finishes.
		return false, nil
	case RecordKindXactAssignment, RecordKindXactRollbackTo, RecordKindXactSubAbort:
		// Subxact markers (M0050-0003) — physical page recovery is
		// a no-op. The mvcc.Manager rebuilds its subxact-to-parent
		// map from these records during recovery; the physical replay
		// path (ReplayRecords) has no access to mvcc.Manager. The
		// full integration is wired by the recovery driver in
		// internal/initdb/open.go (M0050-0004).
		return false, nil
	case RecordKindHeapHotUpdate:
		if err := replayHeapHotUpdate(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindHeapPruneOpt:
		if err := replayHeapPruneOpt(mgr, r); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindSmgrCreate:
		if err := replaySmgrCreate(mgr, r.Payload); err != nil {
			return false, err
		}
		return true, nil
	case RecordKindSmgrTruncate:
		if err := replaySmgrTruncate(mgr, r.Payload); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported kind %d", r.Payload[0])
	}
}

func nativeApplyRecordKindKnown(kind byte) bool {
	switch kind {
	case RecordKindPageImage,
		RecordKindBtreeSplit,
		RecordKindHeapInsert,
		RecordKindBtreeInsert,
		RecordKindHeapDelete,
		RecordKindHeapLock,
		RecordKindHeapVacuum,
		RecordKindBtreeVacuum,
		RecordKindBtreeUnlinkPage,
		RecordKindBtreeNewRoot,
		RecordKindBtreeMarkPageHalfDead,
		RecordKindHeapFreeze,
		RecordKindHeapUpdate,
		RecordKindHeapMultiInsert,
		RecordKindHeapVisible,
		RecordKindBtreeReusePage,
		RecordKindBtreeMetaCleanup,
		RecordKindCheckpoint,
		RecordKindXactCommit,
		RecordKindXactAbort,
		RecordKindCreateDatabase,
		RecordKindDropDatabase,
		RecordKindCreateIndex,
		RecordKindDropIndex,
		RecordKindXactAssignment,
		RecordKindXactRollbackTo,
		RecordKindXactSubAbort,
		RecordKindHeapHotUpdate,
		RecordKindHeapPruneOpt,
		RecordKindSmgrCreate,
		RecordKindSmgrTruncate:
		return true
	default:
		return false
	}
}

func replayDecodedXLogRecord(mgr *storage.Manager, r Record) (bool, error) {
	xlog := r.XLog
	if xlog == nil {
		return false, errors.New("wal: empty decoded xlog record")
	}
	switch xlog.Header.Rmid {
	case RmgrXLog:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogXLogParameterChange:
			return replayXLogParameterChange(mgr, xlog)
		default:
			// Other RmgrXLog opcodes (checkpoint, noop, switch, …) need no
			// physical replay action on the standby.
			return false, nil
		}
	case RmgrXact:
		switch xlog.Header.Info & xlogXactOpMask {
		case xlogXactCommit, xlogXactAbort:
			return false, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrStandby:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogStandbyRunningXacts:
			// RUNNING_XACTS seeds standby snapshot/conflict state upstream.
			// goopg's replay path has no consumer for that metadata yet, so
			// treat the record as a recognised no-op and keep failing closed
			// on any other Standby opcode.
			return false, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrHeap:
		switch xlog.Header.Info & xlogHeapOpMask {
		case xlogHeapInsert:
			if err := replayDecodedXLogHeapInsert(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	default:
		return false, unsupportedDecodedXLogRecord(r)
	}
}

// replayXLogParameterChange applies an XLOG_PARAMETER_CHANGE record on the
// standby, mirroring upstream xlog_redo (xlog.c:8558-8620).
//
// The primary emits this record whenever the 8 GUC echo fields diverge from
// what is stored in pg_control.  On replay we decode the 28-byte
// xl_parameter_change payload and write the updated values back to pg_control
// so a PG18 standby's CheckRequiredParameterValues sees consistent values.
//
// Payload layout (little-endian, matching xl_parameter_change in
// src/include/access/xlog_internal.h:273):
//
//	offset 0:  MaxConnections        int32
//	offset 4:  max_worker_processes  int32
//	offset 8:  max_wal_senders       int32
//	offset 12: max_prepared_xacts    int32
//	offset 16: max_locks_per_xact    int32
//	offset 20: wal_level             int32
//	offset 24: wal_log_hints         bool (1 byte)
//	offset 25: track_commit_ts       bool (1 byte)
//	offset 26: padding               2 bytes
func replayXLogParameterChange(mgr *storage.Manager, xlog *XLogDecodedRecord) (bool, error) {
	const minPayload = 26 // 6 × int32 + 2 × bool
	data := xlog.MainData
	if len(data) < minPayload {
		return false, fmt.Errorf("wal: XLOG_PARAMETER_CHANGE payload too short: %d bytes", len(data))
	}
	if mgr == nil {
		// No storage manager means no pg_control to update (e.g. test stubs).
		return false, nil
	}
	dataDir := mgr.DataDir()
	if dataDir == "" {
		return false, nil
	}
	le := binary.LittleEndian
	maxConn := le.Uint32(data[0:])
	maxWorker := le.Uint32(data[4:])
	maxWalSnd := le.Uint32(data[8:])
	maxPrepared := le.Uint32(data[12:])
	maxLocks := le.Uint32(data[16:])
	walLvl := le.Uint32(data[20:])
	walLogHints := data[24] != 0
	trackCommitTS := data[25] != 0
	if err := control.UpdateControlFile(dataDir, func(cd *control.ControlFileData) {
		cd.WalLevel = walLvl
		cd.WalLogHints = walLogHints
		cd.MaxConnections = maxConn
		cd.MaxWorkerProcesses = maxWorker
		cd.MaxWalSenders = maxWalSnd
		cd.MaxPreparedXacts = maxPrepared
		cd.MaxLocksPerXact = maxLocks
		cd.TrackCommitTimestamp = trackCommitTS
	}); err != nil {
		return false, fmt.Errorf("wal: XLOG_PARAMETER_CHANGE: %w", err)
	}
	return true, nil
}

func unsupportedDecodedXLogRecord(r Record) error {
	if r.XLog == nil {
		return errors.New("wal: empty decoded xlog record")
	}
	return fmt.Errorf("wal: unsupported xlog record rmid=%d info=0x%02x lsn[%d,%d]",
		r.XLog.Header.Rmid,
		r.XLog.Header.Info&XLRRmgrInfoMask,
		r.StartLSN,
		r.EndLSN,
	)
}

func replayDecodedXLogHeapInsert(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap-insert missing block 0")
	}
	if block.Rel.Fork != storage.MainFork {
		return fmt.Errorf("wal: xlog heap-insert fork=%d, want main fork", block.Rel.Fork)
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	offnum, err := decodeXLogHeapInsertMainData(xlog.MainData)
	if err != nil {
		return err
	}
	tupleRaw, err := decodeXLogHeapInsertTuple(block, storage.TransactionID(xlog.Header.XID), offnum)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return err
	}
	page := make(storage.Page, storage.BlockSize)
	switch {
	case block.Block < nblocks:
		if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
			return err
		}
		if !storage.IsNew(page) && storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
			return nil
		}
		if block.WillInit || xlog.Header.Info&xlogHeapInit != 0 || storage.IsNew(page) {
			if err := storage.InitPage(page); err != nil {
				return err
			}
		}
	case block.Block == nblocks:
		if err := storage.InitPage(page); err != nil {
			return err
		}
		got, err := mgr.Extend(block.Rel, page)
		if err != nil {
			return err
		}
		if got != block.Block {
			return fmt.Errorf("wal: xlog heap-insert extend returned block %d, want %d", got, block.Block)
		}
	default:
		return fmt.Errorf("wal: xlog heap-insert replay gap block=%d nblocks=%d", block.Block, nblocks)
	}
	got, err := storage.PageInsertItemRawAt(page, offnum, tupleRaw)
	if err != nil {
		return fmt.Errorf("wal: xlog heap-insert apply: %w", err)
	}
	if got != offnum {
		return fmt.Errorf("wal: xlog heap-insert replay slot drift: got %d, want %d (block %d)", got, offnum, block.Block)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

func xlogBlockRefByID(xlog *XLogDecodedRecord, id byte) (XLogBlockRef, bool) {
	if xlog == nil {
		return XLogBlockRef{}, false
	}
	for _, block := range xlog.Blocks {
		if block.ID == id {
			return block, true
		}
	}
	return XLogBlockRef{}, false
}

func restoreDecodedXLogBlockImage(mgr *storage.Manager, block XLogBlockRef, lsn storage.LSN) error {
	if len(block.Image) != storage.BlockSize {
		return fmt.Errorf("wal: xlog block image is %d bytes, want %d", len(block.Image), storage.BlockSize)
	}
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return err
	}
	if block.Block < nblocks {
		existing := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(block.Rel, block.Block, existing); err != nil {
			return err
		}
		if !storage.IsNew(existing) && storage.MustHeader(existing).LSN() >= lsn {
			return nil
		}
	}
	page := make(storage.Page, storage.BlockSize)
	copy(page, block.Image)
	storage.MustHeader(page).SetLSN(lsn)
	return writeBlockOrExtend(mgr, block.Rel, block.Block, page)
}

func decodeXLogHeapInsertMainData(mainData []byte) (uint16, error) {
	if len(mainData) < sizeOfXLogHeapInsertData {
		return 0, fmt.Errorf("wal: invalid xlog heap-insert main-data len %d (want >= %d)", len(mainData), sizeOfXLogHeapInsertData)
	}
	return binary.LittleEndian.Uint16(mainData[0:2]), nil
}

func decodeXLogHeapInsertTuple(block XLogBlockRef, xid storage.TransactionID, offnum uint16) ([]byte, error) {
	if len(block.Data) < sizeOfXLogHeapHeaderData {
		return nil, fmt.Errorf("wal: invalid xlog heap-insert block-data len %d (want >= %d)", len(block.Data), sizeOfXLogHeapHeaderData)
	}
	hoff := block.Data[4]
	tupleData := append([]byte(nil), block.Data[sizeOfXLogHeapHeaderData:]...)
	prefixLen := int(hoff) - storage.SizeOfHeapTupleHeaderData
	if prefixLen > 0 {
		if prefixLen > len(tupleData) {
			return nil, fmt.Errorf("wal: xlog heap-insert tuple prefix len %d exceeds payload len %d", prefixLen, len(tupleData))
		}
		for _, b := range tupleData[:prefixLen] {
			if b != 0 {
				return nil, fmt.Errorf("wal: xlog heap-insert tuple prefix len %d not yet supported", prefixLen)
			}
		}
		tupleData = tupleData[prefixLen:]
	}
	tuple := storage.HeapTuple{
		Header: storage.HeapTupleHeader{
			Xmin:      xid,
			Xmax:      storage.InvalidTransactionID,
			Xvac:      storage.InvalidTransactionID,
			CTID:      storage.ItemPointer{Block: block.Block, Offset: offnum},
			Infomask2: binary.LittleEndian.Uint16(block.Data[0:2]),
			Infomask:  binary.LittleEndian.Uint16(block.Data[2:4]),
			Hoff:      hoff,
		},
		Data: tupleData,
	}
	return tuple.MarshalBinary()
}

// ReplayFromDir reads records from <dataDir>/pg_wal and replays them.
func ReplayFromDir(dataDir string, segmentSize int64) (ReplayStats, error) {
	records, err := ReadAll(filepath.Join(dataDir, "pg_wal"), segmentSize)
	if err != nil {
		return ReplayStats{}, err
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer func() { _ = mgr.Close() }()
	return ReplayRecords(mgr, records)
}

// ReplayFromDirWithMgr replays the WAL segments under walDir into
// the supplied Manager. Used by initdb.Open at startup so the
// runtime's single Manager handles both the replay phase and
// subsequent normal I/O. A missing or empty walDir is treated as
// "nothing to replay" (a freshly initdb'd cluster). segmentSize
// of 0 means use the default DefaultSegmentSize.
func ReplayFromDirWithMgr(mgr *storage.Manager, walDir string, segmentSize int64) (ReplayStats, error) {
	if segmentSize == 0 {
		segmentSize = DefaultSegmentSize
	}
	records, err := ReadAll(walDir, segmentSize)
	if err != nil {
		// Missing pg_wal on a fresh data dir is fine — no records
		// to replay.
		if errors.Is(err, fs.ErrNotExist) {
			return ReplayStats{}, nil
		}
		return ReplayStats{}, err
	}
	return ReplayRecords(mgr, records)
}

// replayHeapVacuum applies one logical heap-vacuum prune record.
// The page must already exist; idempotent via pd_lsn.
func replayHeapVacuum(mgr *storage.Manager, r Record) error {
	rel, blk, deadSlots, err := DecodeHeapVacuum(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-vacuum replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-vacuum replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if _, err := storage.VacuumHeapPageBySlots(page, deadSlots); err != nil {
		return fmt.Errorf("wal: heap-vacuum apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayBtreeVacuum applies one logical B-tree vacuum redo
// record. The page must already exist (CREATE INDEX or earlier
// btree mutation produced it). Idempotent via pd_lsn — the
// record carries the post-vacuum kept-items projection plus
// the post-vacuum opaque flags. (M0079-0002.)
func replayBtreeVacuum(mgr *storage.Manager, r Record) error {
	p, err := DecodeBtreeVacuum(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(p.Rel)
	if err != nil {
		return err
	}
	if p.Blk >= nblocks {
		return fmt.Errorf("wal: btree-vacuum replay: block %d does not exist (nblocks=%d)", p.Blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(p.Rel, p.Blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: btree-vacuum replay: block %d is uninitialised", p.Blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := btree.ReplayVacuumPage(page, p.KeptItems, p.OpaqueFlags); err != nil {
		return fmt.Errorf("wal: btree-vacuum apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(p.Rel, p.Blk, page)
}

// replayHeapUpdate applies the M0080-0002 atomic non-HOT
// UPDATE record. Stamps xmax on the old tuple AND inserts the
// new tuple at the recorded slot. Each page is independently
// pd_lsn-idempotent. (M0080-0002.)
func replayHeapUpdate(mgr *storage.Manager, r Record) error {
	p, err := DecodeHeapUpdate(r.Payload)
	if err != nil {
		return err
	}
	endLSN := storage.LSN(r.EndLSN)
	nblocks, err := mgr.NBlocks(p.Rel)
	if err != nil {
		return err
	}
	if p.OldBlk >= nblocks {
		return fmt.Errorf("wal: heap-update replay: old block %d does not exist (nblocks=%d)", p.OldBlk, nblocks)
	}
	if p.NewBlk >= nblocks {
		return fmt.Errorf("wal: heap-update replay: new block %d does not exist (nblocks=%d)", p.NewBlk, nblocks)
	}
	// Old tuple xmax stamp.
	oldPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(p.Rel, p.OldBlk, oldPage); err != nil {
		return err
	}
	if storage.IsNew(oldPage) {
		return fmt.Errorf("wal: heap-update replay: old block %d uninitialised", p.OldBlk)
	}
	if storage.MustHeader(oldPage).LSN() < endLSN {
		if err := storage.PageSetHeapTupleXmax(oldPage, p.OldLineSlot, p.Xmax); err != nil {
			return fmt.Errorf("wal: heap-update old-stamp: %w", err)
		}
		storage.MustHeader(oldPage).SetLSN(endLSN)
		if err := mgr.WriteBlock(p.Rel, p.OldBlk, oldPage); err != nil {
			return err
		}
	}
	// New tuple insert.
	newPage := make(storage.Page, storage.BlockSize)
	if p.NewBlk == p.OldBlk {
		// Same page: re-read the post-xmax-stamp version.
		if err := mgr.ReadBlock(p.Rel, p.NewBlk, newPage); err != nil {
			return err
		}
	} else {
		if err := mgr.ReadBlock(p.Rel, p.NewBlk, newPage); err != nil {
			return err
		}
	}
	if storage.IsNew(newPage) {
		// Allow uninitialised — first heap insert on a fresh page.
		if err := storage.InitPage(newPage); err != nil {
			return err
		}
	}
	if storage.MustHeader(newPage).LSN() < endLSN {
		tup, err := storage.ParseHeapTuple(p.Tuple)
		if err != nil {
			return fmt.Errorf("wal: heap-update parse new tuple: %w", err)
		}
		got, err := storage.PageAddHeapTuple(newPage, tup)
		if err != nil {
			return fmt.Errorf("wal: heap-update insert new: %w", err)
		}
		if got != p.NewLineSlot {
			return fmt.Errorf("wal: heap-update replay slot drift: got %d, want %d (block %d)", got, p.NewLineSlot, p.NewBlk)
		}
		storage.MustHeader(newPage).SetLSN(endLSN)
		if err := mgr.WriteBlock(p.Rel, p.NewBlk, newPage); err != nil {
			return err
		}
	}
	return nil
}

// replayHeapMultiInsert applies the M0080-0002 bulk-insert
// record. All tuples target the same page; pd_lsn idempotency
// applies to the whole batch. (M0080-0002.)
func replayHeapMultiInsert(mgr *storage.Manager, r Record) error {
	p, err := DecodeHeapMultiInsert(r.Payload)
	if err != nil {
		return err
	}
	endLSN := storage.LSN(r.EndLSN)
	nblocks, err := mgr.NBlocks(p.Rel)
	if err != nil {
		return err
	}
	if p.Blk >= nblocks {
		return fmt.Errorf("wal: heap-multi-insert replay: block %d does not exist (nblocks=%d)", p.Blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(p.Rel, p.Blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		if err := storage.InitPage(page); err != nil {
			return err
		}
	}
	if storage.MustHeader(page).LSN() >= endLSN {
		return nil
	}
	for i, e := range p.Entries {
		tup, err := storage.ParseHeapTuple(e.Tuple)
		if err != nil {
			return fmt.Errorf("wal: heap-multi-insert parse entry %d: %w", i, err)
		}
		got, err := storage.PageAddHeapTuple(page, tup)
		if err != nil {
			return fmt.Errorf("wal: heap-multi-insert insert entry %d: %w", i, err)
		}
		if got != e.LineSlot {
			return fmt.Errorf("wal: heap-multi-insert slot drift: got %d, want %d (entry %d, block %d)", got, e.LineSlot, i, p.Blk)
		}
	}
	storage.MustHeader(page).SetLSN(endLSN)
	return mgr.WriteBlock(p.Rel, p.Blk, page)
}

// replayHeapFreeze applies the M0080-0001 heap-freeze record.
// Reads the page, idempotency-checks via pd_lsn, then calls
// `storage.PageFreezeBySlots` to rewrite the listed tuples'
// xmin to FrozenTransactionID. (M0080-0001.)
func replayHeapFreeze(mgr *storage.Manager, r Record) error {
	rel, blk, frozenSlots, err := DecodeHeapFreeze(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-freeze replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-freeze replay: block %d uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil
	}
	if err := storage.PageFreezeBySlots(page, frozenSlots); err != nil {
		return fmt.Errorf("wal: heap-freeze apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayBtreeUnlinkPage applies the M0079-0003 atomic
// page-deletion record. Each of the four pages (left sibling,
// right sibling, leaf, parent) is independently idempotent
// via pd_lsn — replay can resume cleanly after a partial
// crash. (M0079-0003.)
func replayBtreeUnlinkPage(mgr *storage.Manager, r Record) error {
	p, err := DecodeBtreeUnlinkPage(r.Payload)
	if err != nil {
		return err
	}
	endLSN := storage.LSN(r.EndLSN)

	apply := func(blk storage.BlockNumber, fn func(page storage.Page) error) error {
		nblocks, err := mgr.NBlocks(p.Rel)
		if err != nil {
			return err
		}
		if blk >= nblocks {
			return fmt.Errorf("wal: btree-unlink replay: block %d does not exist (nblocks=%d)", blk, nblocks)
		}
		page := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(p.Rel, blk, page); err != nil {
			return err
		}
		if storage.IsNew(page) {
			return fmt.Errorf("wal: btree-unlink replay: block %d uninitialised", blk)
		}
		if storage.MustHeader(page).LSN() >= endLSN {
			return nil
		}
		if err := fn(page); err != nil {
			return err
		}
		storage.MustHeader(page).SetLSN(endLSN)
		return mgr.WriteBlock(p.Rel, blk, page)
	}

	if p.HasLeftSib {
		if err := apply(p.LeftSibBlk, func(page storage.Page) error {
			return btree.ReplaySetSiblingNext(page, p.LeftSibNewNext)
		}); err != nil {
			return fmt.Errorf("wal: btree-unlink left sib: %w", err)
		}
	}
	if p.HasRightSib {
		if err := apply(p.RightSibBlk, func(page storage.Page) error {
			return btree.ReplaySetSiblingPrev(page, p.RightSibNewPrev)
		}); err != nil {
			return fmt.Errorf("wal: btree-unlink right sib: %w", err)
		}
	}
	if err := apply(p.LeafBlk, func(page storage.Page) error {
		return btree.ReplaySetOpaqueFlags(page, p.LeafFlagsAfter)
	}); err != nil {
		return fmt.Errorf("wal: btree-unlink leaf: %w", err)
	}
	if p.HasParent {
		if err := apply(p.ParentBlk, func(page storage.Page) error {
			return btree.ReplayRemoveParentDownlink(page, p.ParentRemoveSlot)
		}); err != nil {
			return fmt.Errorf("wal: btree-unlink parent: %w", err)
		}
	}
	return nil
}

// replayBtreeNewRoot applies the M0079-0003 root-replacement
// record. Reconstructs the new root page from scratch using
// the carried items and updates the metapage to point at it.
// (M0079-0003.)
func replayBtreeNewRoot(mgr *storage.Manager, r Record) error {
	p, err := DecodeBtreeNewRoot(r.Payload)
	if err != nil {
		return err
	}
	endLSN := storage.LSN(r.EndLSN)
	nblocks, err := mgr.NBlocks(p.Rel)
	if err != nil {
		return err
	}
	if p.RootBlk >= nblocks {
		return fmt.Errorf("wal: btree-newroot replay: root block %d does not exist (nblocks=%d)", p.RootBlk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(p.Rel, p.RootBlk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: btree-newroot replay: root block %d uninitialised", p.RootBlk)
	}
	if storage.MustHeader(page).LSN() < endLSN {
		if err := btree.ReplayNewRootPage(page, p.Level, p.Items); err != nil {
			return fmt.Errorf("wal: btree-newroot apply: %w", err)
		}
		storage.MustHeader(page).SetLSN(endLSN)
		if err := mgr.WriteBlock(p.Rel, p.RootBlk, page); err != nil {
			return err
		}
	}

	// Metapage update — separate idempotency check so a partial
	// replay (root written but meta not) can resume.
	metaPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(p.Rel, btree.MetaBlock, metaPage); err != nil {
		return err
	}
	if storage.IsNew(metaPage) {
		return fmt.Errorf("wal: btree-newroot replay: metapage uninitialised")
	}
	if storage.MustHeader(metaPage).LSN() >= endLSN {
		return nil
	}
	if err := btree.ReplayMetaSetRoot(metaPage, p.RootBlk, p.Level); err != nil {
		return fmt.Errorf("wal: btree-newroot meta apply: %w", err)
	}
	storage.MustHeader(metaPage).SetLSN(endLSN)
	return mgr.WriteBlock(p.Rel, btree.MetaBlock, metaPage)
}

// replayBtreeMarkPageHalfDead applies the M0079-0003 leaf-only
// half-dead transition record. (M0079-0003.)
func replayBtreeMarkPageHalfDead(mgr *storage.Manager, r Record) error {
	p, err := DecodeBtreeMarkPageHalfDead(r.Payload)
	if err != nil {
		return err
	}
	endLSN := storage.LSN(r.EndLSN)
	nblocks, err := mgr.NBlocks(p.Rel)
	if err != nil {
		return err
	}
	if p.LeafBlk >= nblocks {
		return fmt.Errorf("wal: btree-mark-halfdead replay: block %d does not exist (nblocks=%d)", p.LeafBlk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(p.Rel, p.LeafBlk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: btree-mark-halfdead replay: block %d uninitialised", p.LeafBlk)
	}
	if storage.MustHeader(page).LSN() >= endLSN {
		return nil
	}
	if err := btree.ReplaySetOpaqueFlags(page, p.FlagsAfter); err != nil {
		return fmt.Errorf("wal: btree-mark-halfdead apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(endLSN)
	return mgr.WriteBlock(p.Rel, p.LeafBlk, page)
}

// replayHeapLock applies one row-lock redo record. The page must
// already exist (a HeapInsert or earlier mutation produced it).
// Idempotent via pd_lsn — re-applying a record after a crash is a
// no-op when the page already advertises an LSN >= record.endLSN.
func replayHeapLock(mgr *storage.Manager, r Record) error {
	rel, blk, lineSlot, xmax, lockStrength, err := DecodeHeapLock(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-lock replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-lock replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := storage.PageSetHeapTupleLockOnly(page, lineSlot, xmax, lockStrength); err != nil {
		return fmt.Errorf("wal: heap-lock apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapDelete applies one logical xmax-stamp record. The
// page must already exist (HeapInsert or an earlier mutation
// produced it). Idempotent via pd_lsn.
func replayHeapDelete(mgr *storage.Manager, r Record) error {
	rel, blk, lineSlot, xmax, _, err := DecodeHeapDelete(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-delete replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-delete replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := storage.PageSetHeapTupleXmax(page, lineSlot, xmax); err != nil {
		return fmt.Errorf("wal: heap-delete apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayBtreeInsert applies one logical B-tree non-split insert
// to the data file. Idempotent via pd_lsn: skipped if the page
// already advertises an LSN >= record.endLSN. The page must
// already exist; bt-insert is never the first record for a page
// (a split or initial Create produced the page first), so a
// missing block is a hard error.
func replayBtreeInsert(mgr *storage.Manager, r Record) error {
	rel, blk, item, err := DecodeBtreeInsert(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: btree-insert replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: btree-insert replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := btree.ApplyInsertRecord(page, item); err != nil {
		return fmt.Errorf("wal: btree-insert apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapInsert applies one logical heap-insert record to the
// data file. Idempotent via pd_lsn: if the page already carries an
// LSN >= record.endLSN, the change is already persisted and the
// apply is skipped. Otherwise, decode, InitPage if the page is
// missing, PageAddHeapTuple at the recorded slot, set pd_lsn,
// write back.
func replayHeapInsert(mgr *storage.Manager, r Record) error {
	rel, blk, lineSlot, tuple, err := DecodeHeapInsert(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	page := make(storage.Page, storage.BlockSize)
	switch {
	case blk < nblocks:
		if err := mgr.ReadBlock(rel, blk, page); err != nil {
			return err
		}
		if !storage.IsNew(page) {
			if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
				return nil // already applied
			}
		} else {
			if err := storage.InitPage(page); err != nil {
				return err
			}
		}
	case blk == nblocks:
		// Page doesn't exist yet — Extend with an InitPage'd
		// blank, then we'll add the tuple.
		if err := storage.InitPage(page); err != nil {
			return err
		}
		got, err := mgr.Extend(rel, page)
		if err != nil {
			return err
		}
		if got != blk {
			return fmt.Errorf("wal: heap-insert extend returned block %d, want %d", got, blk)
		}
	default:
		return fmt.Errorf("wal: heap-insert replay gap block=%d nblocks=%d", blk, nblocks)
	}

	// Place the tuple at the recorded slot.
	tup, err := storage.ParseHeapTuple(tuple)
	if err != nil {
		return fmt.Errorf("wal: heap-insert decode tuple: %w", err)
	}
	got, err := storage.PageAddHeapTuple(page, tup)
	if err != nil {
		return fmt.Errorf("wal: heap-insert apply: %w", err)
	}
	if got != lineSlot {
		// Slot mismatch is a sign of replay drift — earlier records
		// produced a different layout than original. v0 doesn't
		// support out-of-order slot assignment, so this is a hard
		// error rather than a silent fix-up.
		return fmt.Errorf("wal: heap-insert replay slot drift: got %d, want %d (block %d)", got, lineSlot, blk)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapHotUpdate applies one atomic HOT-update record (M0046-0001).
// The page must already exist (the old tuple is already on it). Idempotent
// via pd_lsn. Replay:
//  1. Insert the new tuple (which carries HeapOnlyTuple in infomask).
//  2. Stamp the old slot: xmax + HeapHotUpdated + CTID = (blk, newSlot).
func replayHeapHotUpdate(mgr *storage.Manager, r Record) error {
	rel, blk, oldSlot, xmax, tupleBytes, err := DecodeHeapHotUpdate(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-hot-update replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-hot-update replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	tup, err := storage.ParseHeapTuple(tupleBytes)
	if err != nil {
		return fmt.Errorf("wal: heap-hot-update decode new tuple: %w", err)
	}
	newSlot, err := storage.PageAddHeapTuple(page, tup)
	if err != nil {
		return fmt.Errorf("wal: heap-hot-update insert new tuple: %w", err)
	}
	if err := storage.PageStampHotOldTuple(page, oldSlot, xmax, blk, newSlot); err != nil {
		return fmt.Errorf("wal: heap-hot-update stamp old tuple: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayHeapPruneOpt applies one opportunistic page-pruning record
// (M0046-0002). Applies redirect pairs (ItemIDRedirect) then marks unused
// slots via VacuumHeapPageBySlots. Idempotent via pd_lsn.
func replayHeapPruneOpt(mgr *storage.Manager, r Record) error {
	rel, blk, redirects, unused, err := DecodeHeapPruneOpt(r.Payload)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	if blk >= nblocks {
		return fmt.Errorf("wal: heap-prune-opt replay: block %d does not exist (nblocks=%d)", blk, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, blk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: heap-prune-opt replay: block %d is uninitialised", blk)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	// Apply redirect line pointer conversions first.
	for _, redir := range redirects {
		if err := storage.PageSetItemIDRedirect(page, redir[0], redir[1]); err != nil {
			return fmt.Errorf("wal: heap-prune-opt redirect slot=%d→%d: %w", redir[0], redir[1], err)
		}
	}
	// Compact the page: mark unused slots and repack live tuples.
	if _, err := storage.VacuumHeapPageBySlots(page, unused); err != nil {
		return fmt.Errorf("wal: heap-prune-opt vacuum: %w", err)
	}
	storage.MustHeader(page).SetPruneXID(0)
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(rel, blk, page)
}

// replayBtreeSplit applies the left then right page images carried
// by an atomic split record. The right page is applied via Extend
// when the relation is one block short of containing it (the
// freshly-allocated case at original record time) and via
// WriteBlock when the file is already long enough (replay re-run
// or the right page somehow already on disk). Apply order is
// left → right so a reader following left's right-link from the
// post-replay state always finds a real right page on disk.
func replayBtreeSplit(mgr *storage.Manager, payload []byte) error {
	rel, leftBlk, rightBlk, leftPage, rightPage, err := DecodeBtreeSplit(payload)
	if err != nil {
		return err
	}
	if err := writeBlockOrExtend(mgr, rel, leftBlk, leftPage); err != nil {
		return fmt.Errorf("apply left block %d: %w", leftBlk, err)
	}
	if err := writeBlockOrExtend(mgr, rel, rightBlk, rightPage); err != nil {
		return fmt.Errorf("apply right block %d: %w", rightBlk, err)
	}
	return nil
}

// writeBlockOrExtend installs `page` at the given block number,
// extending the relation if the block is exactly one past the end.
// It is the shared kernel for replayPageImage and the per-side
// apply in replayBtreeSplit.
func writeBlockOrExtend(mgr *storage.Manager, rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) error {
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	switch {
	case blk < nblocks:
		return mgr.WriteBlock(rel, blk, page)
	case blk == nblocks:
		got, err := mgr.Extend(rel, page)
		if err != nil {
			return err
		}
		if got != blk {
			return fmt.Errorf("wal: extend returned block %d, want %d", got, blk)
		}
		return nil
	default:
		return fmt.Errorf("wal: replay gap block=%d nblocks=%d", blk, nblocks)
	}
}

func replayPageImage(mgr *storage.Manager, payload []byte) error {
	rel, blk, page, err := DecodePageImage(payload)
	if err != nil {
		return err
	}
	return writeBlockOrExtend(mgr, rel, blk, page)
}

// replayStart returns the index of the LAST checkpoint record in
// records plus its EndLSN. Crash recovery should replay records
// starting from this index: everything before the checkpoint was
// already flushed to disk by the checkpoint operation.
//
// If no checkpoint is found, returns (0, 0) — replay all records
// from the beginning (correct for fresh clusters or early startup).
func replayStart(records []Record) (int, uint64) {
	startIdx := 0
	var checkpointLSN uint64
	for i, r := range records {
		if len(r.Payload) == 0 {
			continue
		}
		if r.Payload[0] == RecordKindCheckpoint {
			startIdx = i // start FROM this checkpoint (inclusive)
			checkpointLSN = r.EndLSN
		}
	}
	return startIdx, checkpointLSN
}

// DiscoverLastCheckpointLSN scans the WAL directory for the most
// recent checkpoint record and returns its EndLSN. This is needed
// for M0045-0002's startup replay: begin replay from the last
// checkpoint so post-checkpoint dirty pages are recovered without
// re-reading the entire WAL history.
//
// Because WAL retention removes pre-checkpoint segments, the scan
// must tolerate a non-zero first segment (M0045-0001). ReadAll already
// starts from the first retained segment after the readStream fix.
//
// Returns (0, nil) for a fresh cluster (no WAL segments present).
// Returns an error if WAL segments exist but no checkpoint is found —
// this indicates an unrecoverable cluster state that requires
// re-initialization.
func DiscoverLastCheckpointLSN(walDir string, segmentSize int64) (uint64, error) {
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}
	records, err := ReadAll(walDir, segmentSize)
	if err != nil {
		return 0, fmt.Errorf("wal: discover checkpoint: %w", err)
	}
	if len(records) == 0 {
		return 0, nil // fresh cluster or empty WAL directory
	}
	// Scan for the LAST checkpoint record in the retained range.
	var lastLSN uint64
	for _, r := range records {
		if len(r.Payload) > 0 && r.Payload[0] == RecordKindCheckpoint {
			lastLSN = r.EndLSN
		}
	}
	if lastLSN == 0 {
		return 0, fmt.Errorf("wal: no checkpoint record found in %s; "+
			"the cluster may need to be re-initialized with 'goopg init'", walDir)
	}
	return lastLSN, nil
}

// replaySmgrCreate ensures the relation file identified by the record has at
// least one initialised block. Idempotent: if the file already has blocks,
// this is a no-op. Mirrors XLOG_SMGR_CREATE redo semantics.
func replaySmgrCreate(mgr *storage.Manager, payload []byte) error {
	rel, err := DecodeSmgrCreate(payload)
	if err != nil {
		return err
	}
	n, err := mgr.NBlocks(rel)
	if err != nil {
		return fmt.Errorf("wal: smgr-create replay NBlocks: %w", err)
	}
	if n > 0 {
		return nil // already exists — idempotent
	}
	page := make([]byte, storage.BlockSize)
	if initErr := storage.InitPage(storage.Page(page)); initErr != nil {
		return initErr
	}
	_, err = mgr.Extend(rel, page)
	return err
}

// replaySmgrTruncate truncates the relfile to 0 blocks. Idempotent: if the
// file is already empty (0 blocks), this is a no-op. Mirrors
// XLOG_SMGR_TRUNCATE redo semantics.
func replaySmgrTruncate(mgr *storage.Manager, payload []byte) error {
	rel, err := DecodeSmgrTruncate(payload)
	if err != nil {
		return err
	}
	n, err := mgr.NBlocks(rel)
	if err != nil {
		return fmt.Errorf("wal: smgr-truncate replay NBlocks: %w", err)
	}
	if n == 0 {
		return nil // already empty — idempotent
	}
	return mgr.TruncateRelation(rel)
}
