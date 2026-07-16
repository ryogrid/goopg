package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
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

	// (RecordKindXactCommitInval, formerly byte 32, was retired in A9: relcache
	// invalidations are carried as the HAS_INVALS chunk on the PG xl_xact_commit
	// record — see EncodeXactCommitPG / xactCommitCarriesInvals — so the standalone
	// native record is no longer emitted. Value 32 is left unassigned.)

	// RecordKindClogTruncate logs a CLOG (pg_xact) truncation: the oldest
	// XID whose commit status is still retained. Emitted by
	// mvcc.CLog.TruncateCLOG after VACUUM/freeze advances the cluster
	// datfrozenxid, so a standby learns the new valid xid before the next
	// checkpoint and crash recovery re-applies the (idempotent) truncation.
	// Mirrors PG's XLOG_CLOG record with op CLOG_TRUNCATE (see
	// postgres/src/backend/access/transam/clog.c:WriteTruncateXlogRec and
	// clog_redo's CLOG_TRUNCATE branch, postgres/src/include/access/clog.h
	// xl_clog_truncate). goopg carries only the oldestXid (the cutoff page is
	// derivable and the cluster is single-database), so the wire format is
	// "kind(1) | oldestXid(4)" = 5 bytes — identical to the xact markers.
	// Physical page recovery is a no-op (clog is a write-behind cache); the
	// recovery driver in internal/initdb replays it against the CLog.
	RecordKindClogTruncate byte = 33

	// RecordKindCreateTransform records a `CREATE TRANSFORM FOR <type>
	// LANGUAGE <lang> ...` event so the catalog's in-memory transform
	// registry (catalog.InMemory.transforms, which backs the pg_transform
	// virtual view) survives a restart. Like CREATE SCHEMA, CREATE TRANSFORM
	// is a catalog-only side effect with no per-object on-disk file
	// namespace, so the physical redo path is a no-op (applyRecord returns
	// (false, nil)); the recovery driver in
	// internal/initdb/transform_ddl_recovery.go scans the WAL for these
	// records after physical replay and re-registers each transform with its
	// original OID. Mirrors RecordKindCreateSchema (M0110-0003). DU-002
	// (M0119-0004) restart-persistence follow-up.
	// Format:
	//   kind(1) | oid(4) | fromFuncOID(4) | toFuncOID(4) | typeLen(2) | type(typeLen bytes) | langLen(2) | lang(langLen bytes)
	RecordKindCreateTransform byte = 36

	// RecordKindDropTransform records a `DROP TRANSFORM FOR <type> LANGUAGE
	// <lang>` event. Counterpart to RecordKindCreateTransform; the recovery
	// driver removes the (type, lang) pair from the catalog instead of
	// adding it. The OID/function OIDs are not needed on drop.
	// Format:
	//   kind(1) | typeLen(2) | type(typeLen bytes) | langLen(2) | lang(langLen bytes)
	RecordKindDropTransform byte = 37

	// RecordKindCreateCast records a `CREATE CAST (source AS target) ...`
	// event so the catalog's in-memory cast registry (catalog.InMemory.casts,
	// which backs the pg_cast virtual view) survives a restart. Like CREATE
	// TRANSFORM, CREATE CAST is a catalog-only side effect with no
	// per-object on-disk file namespace, so the physical redo path is a
	// no-op (applyRecord returns (false, nil)); the recovery driver in
	// internal/initdb/cast_ddl_recovery.go scans the WAL for these records
	// after physical replay and re-registers each cast with its original
	// OID. Mirrors RecordKindCreateTransform (M0119-0004). DU-002
	// restart-persistence follow-up.
	// Format:
	//   kind(1) | oid(4) | funcOID(4) | context(1) | method(1) | sourceLen(2) | source(sourceLen bytes) | targetLen(2) | target(targetLen bytes)
	RecordKindCreateCast byte = 38

	// RecordKindDropCast records a `DROP CAST (source AS target)` event.
	// Counterpart to RecordKindCreateCast; the recovery driver removes the
	// (source, target) pair from the catalog instead of adding it. The
	// OID/funcOID/context/method are not needed on drop.
	// Format:
	//   kind(1) | sourceLen(2) | source(sourceLen bytes) | targetLen(2) | target(targetLen bytes)
	RecordKindDropCast byte = 39

	// RecordKindCreateConversion records a `CREATE [DEFAULT] CONVERSION
	// <name> FOR <src> TO <dest> FROM <func>` event so the catalog's
	// in-memory conversion registry (catalog.InMemory.userConversions, which
	// backs the pg_conversion virtual view) survives a restart. Like CREATE
	// CAST/TRANSFORM, CREATE CONVERSION is a catalog-only side effect with no
	// per-object on-disk file namespace, so the physical redo path is a
	// no-op (applyRecord returns (false, nil)); the recovery driver in
	// internal/initdb/conversion_ddl_recovery.go scans the WAL for these
	// records after physical replay and re-registers each conversion with
	// its original OID. Unlike casts/transforms, a conversion is
	// schema-scoped, so replay of this record must happen after schema
	// replay (replaySchemaDDLRecords) has repopulated the schema OID map.
	// Mirrors RecordKindCreateCast (M0119-0004). DU-002 restart-persistence
	// follow-up.
	// Format:
	//   kind(1) | oid(4) | ownerOID(4) | funcOID(4) | forEncoding(4) | toEncoding(4) | defaultFlag(1) |
	//   nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
	//   procSchemaLen(2) | procSchema(procSchemaLen bytes) | procNameLen(2) | procName(procNameLen bytes)
	RecordKindCreateConversion byte = 40

	// RecordKindDropConversion records a `DROP CONVERSION <name>` event.
	// Counterpart to RecordKindCreateConversion; the recovery driver removes
	// the (schema, name) pair from the catalog instead of adding it. The
	// OID/encodings/function are not needed on drop.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes)
	RecordKindDropConversion byte = 41

	// RecordKindCreateCollation records a `CREATE COLLATION <name> (...)`
	// event so the catalog's in-memory collation registry
	// (catalog.InMemory.userCollations, which backs the pg_collation virtual
	// view) survives a restart. Like CREATE CAST/TRANSFORM/CONVERSION,
	// CREATE COLLATION is a catalog-only side effect with no per-object
	// on-disk file namespace, so the physical redo path is a no-op
	// (applyRecord returns (false, nil)); the recovery driver in
	// internal/initdb/collation_ddl_recovery.go scans the WAL for these
	// records after physical replay and re-registers each collation with its
	// original OID. Like a conversion, a collation is schema-scoped, so
	// replay of this record must happen after schema replay
	// (replaySchemaDDLRecords) has repopulated the schema OID map. Mirrors
	// RecordKindCreateConversion (M0119-0004). DU-002 restart-persistence
	// follow-up.
	// Format:
	//   kind(1) | oid(4) | ownerOID(4) | encoding(4) | provider(1) | deterministicFlag(1) |
	//   nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
	//   collateLen(2) | collate(collateLen bytes) | ctypeLen(2) | ctype(ctypeLen bytes) |
	//   localeLen(2) | locale(localeLen bytes) | rulesLen(2) | rules(rulesLen bytes)
	RecordKindCreateCollation byte = 42

	// RecordKindDropCollation records a `DROP COLLATION <name>` event.
	// Counterpart to RecordKindCreateCollation; the recovery driver removes
	// the (schema, name) pair from the catalog instead of adding it. The
	// OID/provider/locale fields are not needed on drop.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes)
	RecordKindDropCollation byte = 43

	// RecordKindAlterCollationRename records an `ALTER COLLATION <name>
	// RENAME TO <newname>` event so the rename survives a restart. Like
	// CREATE/DROP COLLATION, goopg has no per-collation file namespace, so
	// the physical redo path is a no-op; the recovery driver in
	// internal/initdb/collation_ddl_recovery.go re-applies the rename to the
	// catalog's collation registry after physical replay + schema replay.
	// Mirrors RecordKindCreateCollation. DU-002 restart-persistence
	// follow-up (M0119-0004, loop #50/executor's execAlterCollation).
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
	//   newNameLen(2) | newName(newNameLen bytes)
	RecordKindAlterCollationRename byte = 44

	// RecordKindAlterCollationOwner records an `ALTER COLLATION <name> OWNER
	// TO <role>` event so the ownership change survives a restart. Same
	// no-op physical redo path as RecordKindAlterCollationRename.
	// Format:
	//   kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes)
	RecordKindAlterCollationOwner byte = 45

	// RecordKindCreateAggregate records a `CREATE AGGREGATE <name> (...)`
	// event so the catalog's in-memory aggregate registry
	// (catalog.InMemory.userAggregates, which backs the pg_aggregate/pg_proc
	// virtual views and the planner's isUserAggregateFunc lookup) survives a
	// restart. Like CREATE CAST/TRANSFORM/CONVERSION/COLLATION, CREATE
	// AGGREGATE is a catalog-only side effect with no per-object on-disk file
	// namespace, so the physical redo path is a no-op (applyRecord returns
	// (false, nil)); the recovery driver in
	// internal/initdb/aggregate_ddl_recovery.go scans the WAL for these
	// records after physical replay and re-registers each aggregate with its
	// original OID. Unlike a collation/conversion, a user aggregate has no
	// Schema field yet (catalog.UserAggregate — see the DU-002 slice 405
	// ledger row's resume point (a)), so replay does not depend on schema
	// replay having run first. DU-002 restart-persistence follow-up
	// (M0119-0004, slice 405 ledger resume point (c)).
	// Format:
	//   kind(1) | oid(4) | sfuncStrictFlag(1) | variadicFlag(1) |
	//   nameLen(2) | name(nameLen bytes) | sTypeLen(2) | sType(sTypeLen bytes) |
	//   sFuncLen(2) | sFunc(sFuncLen bytes) | finalFuncLen(2) | finalFunc(finalFuncLen bytes) |
	//   combineFuncLen(2) | combineFunc(combineFuncLen bytes) | initCondLen(2) | initCond(initCondLen bytes) |
	//   finalFuncModifyLen(2) | finalFuncModify(finalFuncModifyLen bytes) |
	//   argTypesCount(2) | for each: argTypeLen(2) argType(argTypeLen bytes)
	RecordKindCreateAggregate byte = 46

	// RecordKindAlterAggregateRename records an `ALTER AGGREGATE name(args)
	// RENAME TO newname` event so the rename survives a restart. Same no-op
	// physical redo path as RecordKindCreateAggregate.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes) | newNameLen(2) | newName(newNameLen bytes)
	RecordKindAlterAggregateRename byte = 47

	// RecordKindDropAggregate records a `DROP AGGREGATE name(args)` event so
	// the removal survives a restart. Same no-op physical redo path as
	// RecordKindCreateAggregate. Mirrors RecordKindDropCollation.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes)
	RecordKindDropAggregate byte = 48

	// RecordKindAlterAggregateOwner records an `ALTER AGGREGATE name(args)
	// OWNER TO newowner` event so the ownership change survives a restart.
	// Same no-op physical redo path as RecordKindCreateAggregate. Mirrors
	// RecordKindAlterCollationOwner.
	// Format:
	//   kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes)
	RecordKindAlterAggregateOwner byte = 49

	// RecordKindCreatePublication records a `CREATE PUBLICATION name ...`
	// event so it survives a restart. goopg has no per-publication on-disk
	// file namespace (catalog.PubSub is a pure in-memory registry, unlike
	// pg_class/pg_attribute-backed objects), so the physical redo path is a
	// no-op; the recovery driver in internal/initdb/pubsub_ddl_recovery.go
	// scans the WAL for these records after physical replay and
	// re-registers each publication with its original OID/owner. Mirrors
	// RecordKindCreateCollation. DU-002 restart-persistence follow-up
	// (M0119-0004, loop #67 ledger resume point).
	// Format:
	//   kind(1) | oid(4) | ownerOID(4) |
	//   flags(1: bit0=AllTables bit1=PublishInsert bit2=PublishUpdate bit3=PublishDelete) |
	//   nameLen(2) | name(nameLen bytes) |
	//   tablesCount(2) | for each: tableLen(2) table(tableLen bytes)
	RecordKindCreatePublication byte = 50

	// RecordKindDropPublication records a `DROP PUBLICATION name` event.
	// Counterpart to RecordKindCreatePublication; same no-op physical redo
	// path. Mirrors RecordKindDropCollation.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes)
	RecordKindDropPublication byte = 51

	// RecordKindAlterPublicationOwner records an `ALTER PUBLICATION name
	// OWNER TO newowner` event. Same no-op physical redo path as
	// RecordKindCreatePublication. Mirrors RecordKindAlterCollationOwner.
	// Format:
	//   kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes)
	RecordKindAlterPublicationOwner byte = 52

	// RecordKindCreateSubscription records a `CREATE SUBSCRIPTION name ...`
	// event so it survives a restart. Same rationale/no-op physical redo
	// path as RecordKindCreatePublication.
	// Format:
	//   kind(1) | oid(4) | ownerOID(4) | enabledFlag(1) |
	//   nameLen(2) | name(nameLen bytes) |
	//   conninfoLen(2) | conninfo(conninfoLen bytes) |
	//   slotNameLen(2) | slotName(slotNameLen bytes) |
	//   pubCount(2) | for each: pubLen(2) pub(pubLen bytes)
	RecordKindCreateSubscription byte = 53

	// RecordKindDropSubscription records a `DROP SUBSCRIPTION name` event.
	// Counterpart to RecordKindCreateSubscription; same no-op physical redo
	// path.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes)
	RecordKindDropSubscription byte = 54

	// RecordKindAlterSubscriptionOwner records an `ALTER SUBSCRIPTION name
	// OWNER TO newowner` event. Same no-op physical redo path as
	// RecordKindCreateSubscription.
	// Format:
	//   kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes)
	RecordKindAlterSubscriptionOwner byte = 55

	// RecordKindCreateEventTrigger records a `CREATE EVENT TRIGGER name ON
	// event ... EXECUTE FUNCTION func()` event so it survives a restart.
	// goopg has no per-event-trigger on-disk file namespace (catalog.InMemory's
	// eventTriggers map is a pure in-memory registry, like catalog.PubSub), so
	// the physical redo path is a no-op; the recovery driver in
	// internal/initdb/event_trigger_ddl_recovery.go scans the WAL for these
	// records after physical replay and re-registers each event trigger with
	// its original OID. Mirrors RecordKindCreatePublication. DU-002
	// restart-persistence follow-up (M0119-0004, loop #70 ledger resume
	// point).
	// Format:
	//   kind(1) | oid(4) | ownerOID(4) | funcOID(4) |
	//   eventLen(2) | event(eventLen bytes) |
	//   nameLen(2) | name(nameLen bytes) |
	//   tagsCount(2) | for each: tagLen(2) tag(tagLen bytes)
	RecordKindCreateEventTrigger byte = 56

	// RecordKindDropEventTrigger records a `DROP EVENT TRIGGER name` event.
	// Counterpart to RecordKindCreateEventTrigger; same no-op physical redo
	// path. Mirrors RecordKindDropPublication.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes)
	RecordKindDropEventTrigger byte = 57

	// RecordKindAlterEventTriggerEnabled records an `ALTER EVENT TRIGGER name
	// {DISABLE | ENABLE [REPLICA|ALWAYS]}` event. code is the raw
	// pg_event_trigger.evtenabled value ('D'/'O'/'R'/'A'). Same no-op
	// physical redo path as RecordKindCreateEventTrigger.
	// Format:
	//   kind(1) | code(1) | nameLen(2) | name(nameLen bytes)
	RecordKindAlterEventTriggerEnabled byte = 58

	// RecordKindAlterEventTriggerRename records an `ALTER EVENT TRIGGER name
	// RENAME TO newname` event. Same no-op physical redo path as
	// RecordKindCreateEventTrigger.
	// Format:
	//   kind(1) | oldNameLen(2) | oldName(oldNameLen bytes) |
	//   newNameLen(2) | newName(newNameLen bytes)
	RecordKindAlterEventTriggerRename byte = 59

	// RecordKindAlterEventTriggerOwner records an `ALTER EVENT TRIGGER name
	// OWNER TO newowner` event. Same no-op physical redo path as
	// RecordKindCreateEventTrigger.
	// Format:
	//   kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes)
	RecordKindAlterEventTriggerOwner byte = 60

	// RecordKindCreateFunction records a `CREATE [OR REPLACE] FUNCTION` /
	// `CREATE [OR REPLACE] PROCEDURE` event so it survives a restart. goopg
	// has no per-routine on-disk file namespace (catalog.Routines is a pure
	// in-memory registry, unlike pg_class/pg_attribute-backed objects), so
	// the physical redo path is a no-op; the recovery driver in
	// internal/initdb/function_ddl_recovery.go scans the WAL for these
	// records after physical replay and re-registers each routine with its
	// original OID. Mirrors RecordKindCreateEventTrigger. DU-002
	// restart-persistence follow-up (M0119-0004, loop #71 ledger resume
	// point). CREATE OR REPLACE reuses the OID of the routine it replaces
	// (Routines.Create's own contract), so a plain re-apply of this record
	// for an unchanged signature is naturally idempotent. Encoded via the
	// struct-based EncodeCreateFunction/DecodeCreateFunction pair (too many
	// fields for a flat positional signature); see CreateFunctionPayload.
	RecordKindCreateFunction byte = 61

	// RecordKindDropFunction records a `DROP FUNCTION`/`DROP PROCEDURE`
	// removal (including a CASCADE-dependent drop) by OID, so it survives a
	// restart. The OID (not name+signature) is carried because DROP
	// FUNCTION's own overload resolution already happened live — replaying
	// by OID sidesteps re-resolving a possibly-ambiguous bare name against a
	// partially-replayed registry. Format:
	//   kind(1) | oid(4)
	RecordKindDropFunction byte = 62

	// RecordKindAlterFunctionRename records an `ALTER FUNCTION/PROCEDURE/
	// ROUTINE name(args) RENAME TO newname` event. Format:
	//   kind(1) | oid(4) | newNameLen(2) | newName(newNameLen bytes)
	RecordKindAlterFunctionRename byte = 63

	// RecordKindAlterFunctionFlags records an `ALTER FUNCTION/PROCEDURE/
	// ROUTINE` attribute change (VOLATILE/STABLE/IMMUTABLE, SECURITY
	// DEFINER/INVOKER, LEAKPROOF, STRICT/CALLED ON NULL INPUT) as a full
	// post-mutation snapshot of the four mutable attributes (not a
	// which-clause-was-present delta) — simpler to replay and matches how
	// execAlterFunction itself always leaves all four fields in a concrete
	// state. Format:
	//   kind(1) | oid(4) | flags(1: bit0=SecurityDefiner bit1=Leakproof
	//   bit2=Strict) | volatileLen(2) | volatile(volatileLen bytes)
	RecordKindAlterFunctionFlags byte = 64

	// RecordKindAlterFunctionOwner records an `ALTER FUNCTION/PROCEDURE/
	// ROUTINE name(args) OWNER TO newowner` event (M0097-0150). Format:
	//   kind(1) | ownerOID(4) | oid(4)
	RecordKindAlterFunctionOwner byte = 121

	// RecordKindAlterFunctionConfig records the post-mutation snapshot of a
	// routine's pg_proc.proconfig array after an `ALTER FUNCTION/PROCEDURE/
	// ROUTINE ... SET name = value / RESET name / RESET ALL` clause (DU-002
	// proconfig follow-up to M0097-0150). Like RecordKindAlterFunctionFlags,
	// this logs the whole resulting array rather than the individual clause
	// so replay is a straight overwrite, not order-dependent op replay.
	// EncodeAlterFunctionConfig/DecodeAlterFunctionConfig.
	RecordKindAlterFunctionConfig byte = 123

	// RecordKindAlterFunctionSetSchema records an `ALTER FUNCTION/PROCEDURE/
	// ROUTINE name(args) SET SCHEMA newschema` event (M0097-0150). Format:
	//   kind(1) | oid(4) | newSchemaLen(2) | newSchema(newSchemaLen bytes)
	RecordKindAlterFunctionSetSchema byte = 122

	// RecordKindCreateTablespace records a `CREATE TABLESPACE name [OWNER
	// owner] LOCATION 'location'` event (M0122-0007 tablespace-registry
	// restart-durability follow-up). goopg's tablespace registry
	// (catalog.InMemory.tablespaces) is a pure in-memory map with no backing
	// heap relation, so the physical redo path is a no-op; the recovery
	// driver in internal/initdb/tablespace_ddl_recovery.go re-registers the
	// tablespace (with its original OID) after physical replay. Mirrors
	// RecordKindCreateSchema. Format:
	//   kind(1) | oid(4) | nameLen(2)+name | ownerLen(2)+owner |
	//   locationLen(2)+location.
	RecordKindCreateTablespace byte = 124

	// RecordKindDropTablespace records a `DROP TABLESPACE name` event.
	// Counterpart to RecordKindCreateTablespace; same no-op physical redo
	// path. Format: kind(1) | nameLen(2) | name(nameLen bytes).
	RecordKindDropTablespace byte = 125

	// RecordKindCreateForeignServer records a `CREATE SERVER name [TYPE
	// 'type'] [VERSION 'version'] FOREIGN DATA WRAPPER fdwname [OPTIONS
	// (...)]` event (M0122-0007 foreign-server registry restart-durability
	// follow-up). goopg's foreign-server registry
	// (catalog.InMemory.foreignServers) is a pure in-memory map with no
	// backing heap relation, so the physical redo path is a no-op; the
	// recovery driver in internal/initdb/foreignserver_ddl_recovery.go
	// re-registers the server (with its original OID) after physical
	// replay. Mirrors RecordKindCreateTablespace. Owner is deliberately not
	// carried: nothing in the codebase sets ForeignServer.Owner away from
	// its zero value yet (no `ALTER SERVER ... OWNER TO`), so there is
	// nothing to persist. Format:
	//   kind(1) | oid(4) | nameLen(2)+name | fdwNameLen(2)+fdwName |
	//   srvTypeLen(2)+srvType | srvVersionLen(2)+srvVersion |
	//   optionsCount(2) | (optLen(2)+opt)*.
	RecordKindCreateForeignServer byte = 126

	// RecordKindDropForeignServer records a `DROP SERVER name` event.
	// Counterpart to RecordKindCreateForeignServer; same no-op physical redo
	// path. Format: kind(1) | nameLen(2) | name(nameLen bytes).
	RecordKindDropForeignServer byte = 127

	// RecordKindCreateUserMapping records a `CREATE USER MAPPING FOR user
	// SERVER server [OPTIONS (...)]` event (M0122-0007 foreign-server
	// registry restart-durability follow-up — the user-mapping resume point
	// named in that milestone's ledger row). goopg's user-mapping registry
	// (catalog.InMemory.userMappings) is a pure in-memory map keyed by
	// "<user>\x00<server>" with no backing heap relation, so the physical
	// redo path is a no-op; the recovery driver in
	// internal/initdb/usermapping_ddl_recovery.go re-registers the mapping
	// (with its original OID) after physical replay. Mirrors
	// RecordKindCreateForeignServer. Format:
	//   kind(1) | oid(4) | userLen(2)+user | serverLen(2)+server |
	//   optionsCount(2) | (optLen(2)+opt)*.
	RecordKindCreateUserMapping byte = 128

	// RecordKindDropUserMapping records a `DROP USER MAPPING FOR user SERVER
	// server` event. Counterpart to RecordKindCreateUserMapping; same no-op
	// physical redo path. Format: kind(1) | userLen(2)+user |
	// serverLen(2)+server.
	RecordKindDropUserMapping byte = 129

	// RecordKindAlterConversionRename records an `ALTER CONVERSION name
	// RENAME TO newname` event, mirroring RecordKindAlterCollationRename.
	// Same no-op physical redo path — only the in-memory conversion
	// registry's name changes. M0122-0007 4e follow-up (DU-002 round-trip
	// probe unblock).
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | newNameLen(2) | newName(newNameLen bytes)
	RecordKindAlterConversionRename byte = 130

	// RecordKindAlterConversionOwner records an `ALTER CONVERSION name OWNER
	// TO role` event, mirroring RecordKindAlterCollationOwner. Same no-op
	// physical redo path — only pg_conversion.conowner metadata changes.
	// M0122-0007 4e follow-up (DU-002 round-trip probe unblock).
	// Format:
	//   kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes) |
	//   schemaLen(2) | schema(schemaLen bytes)
	RecordKindAlterConversionOwner byte = 131

	// RecordKindAlterConversionSetSchema records an `ALTER CONVERSION name
	// SET SCHEMA newschema` move, mirroring RecordKindAlterCollationSetSchema.
	// Same no-op physical redo path — only pg_conversion.connamespace
	// metadata changes. M0122-0007 4e follow-up (DU-002 round-trip probe
	// unblock).
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | newSchemaLen(2) | newSchema(newSchemaLen bytes)
	RecordKindAlterConversionSetSchema byte = 132

	// RecordKindSequenceState records the FULL state of one sequence
	// (definition + current counter) so sequences — including the implicit
	// sequences backing SERIAL/IDENTITY columns — survive a restart. goopg's
	// sequence registry is in-memory only (executor seqRegistry), and the
	// pg_attribute heap stores a serial column's atttypid as the base integer
	// type (PG-canonical, readable by a real PG18 standby), so without this
	// record both the sequence and the column's serial-ness vanished on
	// restart and auto-increment INSERTs failed (surfaced by WordPress:
	// wp_usermeta.umeta_id NOT NULL violations after the first restart).
	//
	// The record is emitted (a) whenever a sequence is registered or altered
	// (CREATE TABLE with SERIAL/IDENTITY, CREATE/ALTER SEQUENCE, setval,
	// TRUNCATE ... RESTART IDENTITY), and (b) periodically from nextval —
	// every 32nd fetch it logs the state with Current advanced 32 values
	// AHEAD of the fetched value, mirroring upstream's SEQ_LOG_VALS
	// pre-logging (postgres/src/backend/commands/sequence.c, xl_seq_rec):
	// replaying the pre-logged horizon never repeats a handed-out value, at
	// the cost of a gap of at most 32 values after a crash — exactly PG's
	// documented behavior. The periodic re-emit also makes actively-used
	// sequences self-healing against checkpoint-driven WAL segment pruning
	// (the same latent limitation the whole logical-DDL record family has).
	//
	// Replay is last-record-wins (replaySequenceDDLRecords): each record
	// fully re-registers the sequence, so create/alter/setval/advance all
	// share this one kind. Format:
	//   kind(1) | flags(1: bit0=cycle bit1=called) | identityKind(1:
	//   0=none 1=BY DEFAULT 2=ALWAYS) | start(8) | increment(8) | min(8) |
	//   max(8) | cache(8) | current(8) | nameLen(2)+name |
	//   dataTypeLen(2)+dataType | ownedByLen(2)+ownedBy |
	//   colSpellingLen(2)+colSpelling
	// ownedBy is "table.column" for implicit serial/identity sequences (and
	// explicit OWNED BY); colSpelling is the serial spelling ("bigserial",
	// "serial", ...) when the sequence backs a SERIAL column, so replay can
	// restore the column's catalog type (the auto-increment path keys on it).
	RecordKindSequenceState byte = 65

	// RecordKindDropSequence records a sequence removal (DROP SEQUENCE, or
	// DROP TABLE cascading to the implicit sequences it owns) so replay does
	// not resurrect it. Format: kind(1) | nameLen(2) | name.
	RecordKindDropSequence byte = 66

	// RecordKindRoleState records the FULL state of one role (name +
	// attribute flags + password verifier) so CREATE/ALTER ROLE survive a
	// restart, like PostgreSQL's pg_authid (which goopg only bootstraps at
	// initdb — runtime role DDL was in-memory-only before this record).
	// Passwords are stored as verifiers, never plaintext-by-default: the
	// server-side handler turns `PASSWORD 'x'` into an upstream-format
	// SCRAM-SHA-256 verifier (auth.NewSCRAMSecret, mirroring PG's
	// encrypt_password in postgres/src/backend/libpq/crypt.c) before the
	// record is emitted, so the WAL carries the same secret shape as
	// pg_authid.rolpassword. Emitted on CREATE ROLE/USER/GROUP and ALTER
	// ROLE/USER; replay is last-record-wins so both share this one kind.
	// The WAL records are the crash-recovery TAIL only: the durable base
	// store is the pg_authid heap file (global/1260), rewritten atomically
	// on every role DDL (initdb.SyncPgAuthidFile) exactly like PostgreSQL's
	// pg_authid heap is the durable store and its WAL records the tail.
	// Startup loads the heap first, then replays these records on top.
	// Format:
	//   kind(1) | flags(1: bit0=canLogin bit1=superuser) | credType(1:
	//   0=none 1=plaintext 2=md5 3=scram-sha-256) | oid(4) |
	//   nameLen(2)+name | secretLen(2)+secret
	RecordKindRoleState byte = 67

	// RecordKindDropRole records a role removal (DROP ROLE/USER/GROUP) so
	// replay does not resurrect it. Format: kind(1) | nameLen(2) | name.
	RecordKindDropRole byte = 68

	// RecordKindColumnDefaults records a table's column DEFAULT expressions
	// (as SQL text) so they survive a restart. catalog.Column.DefaultExpr is
	// an in-memory parser AST that loadUserTablesFromHeap cannot reconstruct
	// — pg_attribute carries only atthasdef and goopg writes no pg_attrdef
	// heap rows at runtime — so before this record an INSERT omitting a
	// defaulted column inserted NULL after a restart (WordPress:
	// `wp_posts.comment_count bigint NOT NULL DEFAULT 0` raised a NOT NULL
	// violation on every post-restart post creation). Emitted from
	// syncTableToCatalogHeap (the single funnel every table-persisting DDL
	// path goes through), one record per table carrying ALL its defaulted
	// columns; replay is last-record-wins and re-parses each expression via
	// parser.ParseExpr. Upstream analog: pg_attrdef
	// (postgres/src/backend/catalog/heap.c StoreAttrDefault). Format:
	//   kind(1) | tableOID(4) | count(2) | count × (nameLen(2)+name |
	//   exprLen(2)+exprSQL)
	RecordKindColumnDefaults byte = 69

	// RecordKindCreateAccessMethod records a `CREATE ACCESS METHOD name TYPE
	// {INDEX|TABLE} HANDLER handler_name` event so it survives a restart.
	// goopg has no per-access-method on-disk file namespace (catalog.InMemory's
	// accessMethods map is a pure in-memory registry, like eventTriggers), so
	// the physical redo path is a no-op; the recovery driver in
	// internal/initdb/access_method_ddl_recovery.go scans the WAL for these
	// records after physical replay and re-registers each access method with
	// its original OID. Mirrors RecordKindCreateEventTrigger. DU-002
	// restart-persistence follow-up (M0119-0004, DU-002 slice 426 ledger
	// resume point).
	// Format:
	//   kind(1) | oid(4) | handlerOID(4) | amType(1: 'i' or 't') |
	//   nameLen(2) | name(nameLen bytes)
	RecordKindCreateAccessMethod byte = 70

	// RecordKindDropAccessMethod records a `DROP ACCESS METHOD name` event.
	// Counterpart to RecordKindCreateAccessMethod; same no-op physical redo
	// path. Mirrors RecordKindDropEventTrigger.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes)
	RecordKindDropAccessMethod byte = 71

	// RecordKindAlterRoleRename records an `ALTER ROLE/USER <name> RENAME TO
	// <newname>` event so the rename survives a restart. Like
	// RecordKindRoleState, runtime role DDL never writes the pg_authid heap
	// directly (only the periodic full-registry SyncPgAuthidFile rewrite
	// does), so the physical replay path is a no-op; the recovery driver in
	// internal/initdb/role_ddl_recovery.go re-keys the catalog role registry
	// entry (preserving its OID) after physical replay. root-0021 follow-up
	// (M0119-0004).
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes) | newNameLen(2) | newName(newNameLen bytes)
	RecordKindAlterRoleRename byte = 72

	// RecordKindAlterDatabaseSetConfig records an `ALTER DATABASE <name> SET
	// <config> = <value>` event so the pg_db_role_setting.setconfig override
	// survives a restart (M0119-0004-ACLHEAP ALTER DATABASE ... SET
	// follow-up). goopg has no per-database file namespace, so the physical
	// redo path is a no-op; the recovery driver in
	// internal/initdb/database_config_recovery.go scans the WAL for these
	// records after physical replay and re-applies them to
	// catalog.InMemory's dbRoleSettings registry via SetDatabaseConfig.
	// Format:
	//   kind(1) | dbOid(4) | nameLen(2) | name(nameLen bytes) | valueLen(2) | value(valueLen bytes)
	RecordKindAlterDatabaseSetConfig byte = 73

	// RecordKindAlterDatabaseResetConfig records an `ALTER DATABASE <name>
	// RESET <config>` event. Same no-op physical redo path as
	// RecordKindAlterDatabaseSetConfig.
	// Format:
	//   kind(1) | dbOid(4) | nameLen(2) | name(nameLen bytes)
	RecordKindAlterDatabaseResetConfig byte = 74

	// RecordKindAlterDatabaseResetAllConfig records an `ALTER DATABASE
	// <name> RESET ALL` event. Same no-op physical redo path as
	// RecordKindAlterDatabaseSetConfig.
	// Format:
	//   kind(1) | dbOid(4)
	RecordKindAlterDatabaseResetAllConfig byte = 75

	// RecordKindAlterRoleSetConfig records an `ALTER ROLE <name> [IN
	// DATABASE <dbname>] SET <config> = <value>` event so the
	// pg_db_role_setting.setconfig override survives a restart
	// (M0119-0004-ACLHEAP ALTER ROLE ... SET follow-up). Same no-op
	// physical redo path as RecordKindAlterDatabaseSetConfig; the recovery
	// driver in internal/initdb/role_config_recovery.go re-applies these to
	// catalog.InMemory's roleSettings registry via SetRoleConfig.
	// Format:
	//   kind(1) | roleOid(4) | dbOid(4) | nameLen(2) | name(nameLen bytes) | valueLen(2) | value(valueLen bytes)
	RecordKindAlterRoleSetConfig byte = 76

	// RecordKindAlterRoleResetConfig records an `ALTER ROLE <name> [IN
	// DATABASE <dbname>] RESET <config>` event. Same no-op physical redo
	// path as RecordKindAlterRoleSetConfig.
	// Format:
	//   kind(1) | roleOid(4) | dbOid(4) | nameLen(2) | name(nameLen bytes)
	RecordKindAlterRoleResetConfig byte = 77

	// RecordKindAlterRoleResetAllConfig records an `ALTER ROLE <name> [IN
	// DATABASE <dbname>] RESET ALL` event. Same no-op physical redo path as
	// RecordKindAlterRoleSetConfig.
	// Format:
	//   kind(1) | roleOid(4) | dbOid(4)
	RecordKindAlterRoleResetAllConfig byte = 78

	// RecordKindGrantRoleMembership records a `GRANT <role> TO <member>
	// [WITH { ADMIN | INHERIT | SET } { OPTION | TRUE | FALSE } [, ...]]`
	// event so the pg_auth_members row survives a restart
	// (M0119-0004-ACLHEAP, GRANT/REVOKE ROLE membership). goopg has no
	// per-role file namespace, so the physical redo path is a no-op; the
	// recovery driver in internal/initdb/role_membership_recovery.go scans
	// the WAL for these records after physical replay and re-applies them to
	// catalog.InMemory's roleMembers registry via GrantRoleMembership (which
	// mints a fresh OID at replay time — pg_auth_members.oid is not dumped
	// by pg_dump/pg_dumpall, so OID stability across a restart is not
	// required).
	// Format:
	//   kind(1) | roleOid(4) | memberOid(4) | grantorOid(4) | options(1)
	RecordKindGrantRoleMembership byte = 79

	// RecordKindRevokeRoleMembership records a `REVOKE
	// [{ADMIN|INHERIT|SET} OPTION FOR] <role> FROM <member>` event. Same
	// no-op physical redo path as RecordKindGrantRoleMembership.
	// Format:
	//   kind(1) | roleOid(4) | memberOid(4) | revokeOption(1)
	RecordKindRevokeRoleMembership byte = 80

	// RecordKindCreateRangeType records a `CREATE TYPE name AS RANGE
	// (subtype = ..., multirange_type_name = ...)` event so it survives a
	// restart. goopg has no per-range-type on-disk file namespace (like
	// `CREATE ACCESS METHOD`, catalog.InMemory's rangeTypes map is a pure
	// in-memory registry), so the physical redo path is a no-op; the
	// recovery driver in internal/initdb/range_type_ddl_recovery.go scans the
	// WAL for these records after physical replay and re-registers each
	// range type with its original OIDs. Mirrors RecordKindCreateAccessMethod.
	// DU-002 restart-persistence follow-up (M0110-0001, DU-002 slice 429
	// ledger resume point, sub-item (c)); arrayOid/multirangeArrayOid added by
	// the array-type follow-up so the auto-generated `_name` array types
	// survive a restart with the same OIDs too.
	// Format:
	//   kind(1) | oid(4) | multirangeOid(4) | opclassOid(4) | arrayOid(4) |
	//   multirangeArrayOid(4) | subtypeNameLen(2)+subtypeName |
	//   nameLen(2)+name | mrNameLen(2)+mrName
	RecordKindCreateRangeType byte = 81

	// RecordKindDropRangeType records a `DROP TYPE name` event for a
	// user-defined range type. Counterpart to RecordKindCreateRangeType; same
	// no-op physical redo path. Mirrors RecordKindDropAccessMethod.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes)
	RecordKindDropRangeType byte = 82

	// RecordKindCreateOperator records a `CREATE OPERATOR name (...)` event
	// so it survives a restart. goopg has no per-operator on-disk file
	// namespace (like range types, catalog.InMemory's userOperators map is
	// a pure in-memory registry), so the physical redo path is a no-op; the
	// recovery driver in internal/initdb/operator_ddl_recovery.go scans the
	// WAL for these records after physical replay and re-registers each
	// operator with its original OID (plus its COMMUTATOR/NEGATOR/RESTRICT/
	// JOIN cross-references, which are themselves just OIDs by the time
	// CREATE OPERATOR's live two-pass resolution finishes). Mirrors
	// RecordKindCreateRangeType. DU-002 restart-persistence follow-up
	// (M0119-0004/M0110-0001, discovered while verifying the loop #64 CREATE
	// TYPE ... AS RANGE opclass/collation follow-up — see ledger). Encoded
	// via the struct-based EncodeCreateOperator/DecodeCreateOperator pair;
	// see CreateOperatorPayload.
	RecordKindCreateOperator byte = 83

	// RecordKindDropOperator records a `DROP OPERATOR name (...)` removal by
	// OID, so it survives a restart. Counterpart to RecordKindCreateOperator;
	// same no-op physical redo path. OID (not name+arg-types) is carried
	// because DROP OPERATOR's own overload resolution already happened live,
	// mirroring RecordKindDropFunction's identical rationale.
	// Format:
	//   kind(1) | oid(4)
	RecordKindDropOperator byte = 84

	// RecordKindCreateOperatorFamily records a `CREATE OPERATOR FAMILY name
	// USING method` event so it survives a restart. goopg has no
	// per-operator-family on-disk file namespace (catalog.InMemory's
	// userOperatorFamilies map is a pure in-memory registry, like
	// userOperators), so the physical redo path is a no-op; the recovery
	// driver in internal/initdb/operator_class_ddl_recovery.go scans the WAL
	// for these records after physical replay and re-registers each family
	// with its original OID. Also covers the anonymous family PG
	// auto-creates when CREATE OPERATOR CLASS omits its own FAMILY clause
	// (execCreateOpClass's own RegisterUserOperatorFamily call). Mirrors
	// RecordKindCreateOperator. DU-002 restart-persistence follow-up
	// (M0119-0004/M0110-0001, closing the loop #65/#66 ledger row's "still
	// open" item (1)).
	// Format:
	//   kind(1) | oid(4) | method(4) | schemaLen(2)+schema | nameLen(2)+name
	RecordKindCreateOperatorFamily byte = 85

	// RecordKindCreateOperatorClass records a `CREATE OPERATOR CLASS name
	// [DEFAULT] FOR TYPE type USING method [FAMILY family] [AS ...]` event's
	// own pg_opclass row (the AS-list members are separate
	// RecordKindCreateAmOpMember/RecordKindCreateAmProcMember records
	// appended right after this one). Same no-op physical redo path as
	// RecordKindCreateOperatorFamily. FamilyOID is carried directly (not
	// re-resolved by name) because the owning family's own create record
	// always precedes this one in WAL order and
	// RegisterUserOperatorFamilyDuringRecovery preserves its OID exactly.
	// DU-002 restart-persistence follow-up (M0119-0004/M0110-0001).
	// Format:
	//   kind(1) | oid(4) | method(4) | familyOid(4) | inTypeOid(4) |
	//   keyTypeOid(4) | isDefault(1) | schemaLen(2)+schema | nameLen(2)+name
	RecordKindCreateOperatorClass byte = 86

	// RecordKindDropOperatorClass records a `DROP OPERATOR CLASS name USING
	// method` removal by OID (mirrors RecordKindDropOperator's rationale:
	// DROP OPERATOR CLASS's own name/method resolution already happened
	// live). Recovery also purges every amop/amproc row owned by this class,
	// mirroring DropUserOperatorClass's live cleanup. Same no-op physical
	// redo path.
	// Format:
	//   kind(1) | oid(4)
	RecordKindDropOperatorClass byte = 87

	// RecordKindCreateAmOpMember records one pg_amop row — an OPERATOR entry
	// from either a CREATE OPERATOR CLASS ... AS list or an ALTER OPERATOR
	// FAMILY ... ADD list (registerOpClassMembers handles both identically;
	// ClassOID is 0 for the latter's "loose" members, matching
	// dependVirtualRows' INTERNAL-vs-AUTO dependency distinction). Same
	// no-op physical redo path as RecordKindCreateOperatorClass. DU-002
	// restart-persistence follow-up (M0119-0004/M0110-0001).
	// Format:
	//   kind(1) | oid(4) | familyOid(4) | classOid(4) | leftType(4) |
	//   rightType(4) | strategy(4) | operOid(4) | method(4) | sortFamilyOid(4)
	RecordKindCreateAmOpMember byte = 88

	// RecordKindDropAmOpMember records an `ALTER OPERATOR FAMILY ... DROP
	// OPERATOR strategy (lefttype, righttype)` removal, keyed the same way
	// RemoveAmOpMember is (no OID — the member is looked up by its unique
	// (family, lefttype, righttype, strategy) index, mirroring
	// dropOperators' own GetSysCacheOid4 lookup). Same no-op physical redo
	// path. DU-002 restart-persistence follow-up (M0119-0004/M0110-0001).
	// Format:
	//   kind(1) | familyOid(4) | leftType(4) | rightType(4) | strategy(4)
	RecordKindDropAmOpMember byte = 89

	// RecordKindCreateAmProcMember records one pg_amproc row — a FUNCTION
	// entry from either a CREATE OPERATOR CLASS ... AS list or an ALTER
	// OPERATOR FAMILY ... ADD list. Mirrors RecordKindCreateAmOpMember.
	// Format:
	//   kind(1) | oid(4) | familyOid(4) | classOid(4) | leftType(4) |
	//   rightType(4) | procNum(4) | procOid(4) | method(4)
	RecordKindCreateAmProcMember byte = 90

	// RecordKindDropAmProcMember records an `ALTER OPERATOR FAMILY ... DROP
	// FUNCTION procnum (lefttype, righttype)` removal, keyed the same way
	// RemoveAmProcMember is. Mirrors RecordKindDropAmOpMember.
	// Format:
	//   kind(1) | familyOid(4) | leftType(4) | rightType(4) | procNum(4)
	RecordKindDropAmProcMember byte = 91

	// RecordKindDropOperatorFamily records a `DROP OPERATOR FAMILY name USING
	// method` removal by OID (mirrors RecordKindDropOperatorClass's
	// rationale: DROP OPERATOR FAMILY's own name/method resolution and
	// member-purge already happened live). Same no-op physical redo path.
	// DU-002 restart-persistence follow-up (M0119-0004/M0110-0001, closing
	// the loop #69 ledger row's "DROP OPERATOR FAMILY never actually calls
	// DropUserOperatorFamily" discovery).
	// Format:
	//   kind(1) | oid(4)
	RecordKindDropOperatorFamily byte = 92

	// RecordKindAlterCollationSetSchema records an `ALTER COLLATION name SET
	// SCHEMA newschema` move, the last previously-unmodelled ALTER COLLATION
	// form (RENAME TO / OWNER TO already had dedicated record kinds 44/45).
	// Same no-op physical redo path — only pg_collation.collnamespace
	// metadata changes. DU-002 slice 442.
	// Format:
	//   kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | newSchemaLen(2) | newSchema(newSchemaLen bytes)
	RecordKindAlterCollationSetSchema byte = 93

	// RecordKindRenameIndex records an `ALTER INDEX name RENAME TO newname`
	// event on a real (non-TOAST) index — previously a functional no-op with
	// no catalog mutation at all, so there was nothing to WAL-log (DU-002
	// slice 443). Same no-op physical redo path as RecordKindCreateIndex /
	// RecordKindDropIndex: only the in-memory index registry's name/key
	// changes; btree pages and the pg_class row are untouched by a rename.
	// Format:
	//   kind(1) | schemaLen(2) | schema(schemaLen bytes) | oldNameLen(2) |
	//   oldName(oldNameLen bytes) | newNameLen(2) | newName(newNameLen bytes)
	RecordKindRenameIndex byte = 94

	// RecordKindCreateMatView records a materialized view's defining query
	// (as SQL text) and populated state so both survive a restart. A matview
	// is a *catalog.Table with IsMatView=true, View (parser AST) and ViewDef
	// (raw SQL) set — pg_class.relkind carries 'm' on disk (buildUserPGClassRow),
	// but loadUserTablesFromHeap cannot reconstruct the AST from relkind alone,
	// and syncTableToCatalogHeap writes no pg_rewrite-equivalent heap rows. Before
	// this record, a restarted matview reloaded as an ordinary relkind='m'-blind
	// table with View=nil (loadUserTablesFromHeap only recognized relkind='r'),
	// losing both its matview-ness and its refresh query. Emitted from
	// syncTableToCatalogHeap (the same single funnel as RecordKindColumnDefaults),
	// one record per matview, last-record-wins; replay re-parses the query via
	// parser.Parse. Upstream analog: pg_rewrite (postgres/src/backend/rewrite/rewriteDefine.c).
	// Format: kind(1) | tableOID(4) | populated(1) | queryLen(2) | querySQL
	RecordKindCreateMatView byte = 102

	// RecordKindCreateView records a plain (non-materialized) view's defining
	// query (as SQL text) so it survives a restart. A view is a *catalog.Table
	// with View (parser AST) and ViewDef (raw SQL) set, Virtual=true, and
	// pg_class.relkind='v' on disk (buildUserPGClassRow) — but a view has no
	// physical heap storage and no pg_rewrite-equivalent heap rows, so
	// loadUserTablesFromHeap can reconstruct the column list from pg_attribute
	// but not the AST. Before this record, `execCreateView` never called
	// syncTableToCatalogHeap at all, so a plain view did not survive a restart
	// even as a downgraded relation — it simply ceased to exist. Emitted from
	// syncTableToCatalogHeap (the same funnel as RecordKindCreateMatView), one
	// record per view, last-record-wins; replay re-parses the query via
	// parser.Parse. Upstream analog: pg_rewrite
	// (postgres/src/backend/rewrite/rewriteDefine.c).
	// Format: kind(1) | tableOID(4) | queryLen(2) | querySQL
	RecordKindCreateView byte = 103

	// RecordKindCreateTSDict records a `CREATE TEXT SEARCH DICTIONARY name
	// (TEMPLATE = tmpl [, opt = val, ...])` event (DU-002 restart-persistence
	// follow-up to slice 437, M0119-0004). goopg has no per-dictionary file
	// namespace, so the physical redo path is a no-op (ApplyRecord returns
	// (false, nil)); the recovery driver in
	// internal/initdb/tsdict_ddl_recovery.go scans the WAL for these records
	// after physical replay (must run after replaySchemaDDLRecords — a
	// dictionary is schema-scoped) and re-registers each dictionary with its
	// original OID. Mirrors RecordKindCreateConversion.
	// Format: kind(1) | oid(4) | ownerOID(4) | template(4) | nameLen(2) |
	//   name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
	//   initOptionLen(2) | initOption(initOptionLen bytes)
	RecordKindCreateTSDict byte = 104

	// RecordKindDropTSDict records a `DROP TEXT SEARCH DICTIONARY <name>`
	// event. Counterpart to RecordKindCreateTSDict; the recovery driver
	// removes the (schema, name) pair from the catalog instead of adding it.
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes)
	RecordKindDropTSDict byte = 105

	// RecordKindCreateTSConfig records a `CREATE TEXT SEARCH CONFIGURATION
	// name (PARSER = parser_name)` event (DU-002 restart-persistence
	// follow-up to slice 446, M0119-0004). Like RecordKindCreateTSDict, this
	// is a catalog-only event with no physical page state; the recovery
	// driver in internal/initdb/tsconfig_ddl_recovery.go re-applies it after
	// schema replay. The configuration's ADD MAPPING entries are recorded
	// separately by RecordKindAddTSConfigMapping (one record per ALTER ...
	// ADD MAPPING statement, replayed in WAL order after the CREATE record).
	// Format: kind(1) | oid(4) | ownerOID(4) | parser(4) | nameLen(2) |
	//   name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes)
	RecordKindCreateTSConfig byte = 106

	// RecordKindAddTSConfigMapping records an `ALTER TEXT SEARCH
	// CONFIGURATION name ADD MAPPING FOR tok [, ...] WITH dict [, ...]`
	// event. Replayed after its configuration's RecordKindCreateTSConfig
	// record (WAL is scanned in order).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | tokenTypeLen(2) | tokenType(tokenTypeLen bytes) |
	//   dictCount(2) | dictOID(4) * dictCount
	RecordKindAddTSConfigMapping byte = 107

	// RecordKindDropTSConfig records a `DROP TEXT SEARCH CONFIGURATION
	// <name>` event. Counterpart to RecordKindCreateTSConfig; the recovery
	// driver removes the (schema, name) pair (and its mappings) from the
	// catalog instead of adding it.
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes)
	RecordKindDropTSConfig byte = 108

	// RecordKindDropTSConfigMapping records an `ALTER TEXT SEARCH
	// CONFIGURATION name DROP MAPPING FOR tokenType` event. Replayed after
	// its configuration's own RecordKindCreateTSConfig. DU-002
	// restart-persistence follow-up to the slice 446 RENAME/SET SCHEMA/DROP
	// MAPPING follow-up (M0119-0004).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | tokenTypeLen(2) | tokenType(tokenTypeLen bytes)
	RecordKindDropTSConfigMapping byte = 109

	// RecordKindRenameTSConfig records an `ALTER TEXT SEARCH CONFIGURATION
	// name RENAME TO newName` event, mirroring RecordKindAlterCollationRename.
	// DU-002 restart-persistence follow-up to the slice 446 RENAME/SET
	// SCHEMA/DROP MAPPING follow-up (M0119-0004).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | newNameLen(2) | newName(newNameLen bytes)
	RecordKindRenameTSConfig byte = 110

	// RecordKindSetTSConfigSchema records an `ALTER TEXT SEARCH
	// CONFIGURATION name SET SCHEMA newSchema` event, mirroring
	// RecordKindAlterCollationSetSchema. DU-002 restart-persistence
	// follow-up to the slice 446 RENAME/SET SCHEMA/DROP MAPPING follow-up
	// (M0119-0004).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | newSchemaLen(2) | newSchema(newSchemaLen bytes)
	RecordKindSetTSConfigSchema byte = 111

	// RecordKindReplaceTSConfigMappingDict records an `ALTER TEXT SEARCH
	// CONFIGURATION name ALTER MAPPING [FOR tok [, ...]] REPLACE olddict
	// WITH newdict` event, mirroring RecordKindDropTSConfigMapping. An empty
	// token-type list means the bare REPLACE form (matches every mapped
	// token type). DU-002 replacedict follow-up (M0119-0004).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | tokenCount(2) |
	//   (tokenTypeLen(2) | tokenType(tokenTypeLen bytes)) * tokenCount |
	//   oldOID(4) | newOID(4)
	RecordKindReplaceTSConfigMappingDict byte = 112

	// RecordKindAlterTSConfigMapping records an `ALTER TEXT SEARCH
	// CONFIGURATION name ALTER MAPPING FOR tok [, ...] WITH dict [, ...]`
	// override event — one record per named token type, each carrying that
	// token type's complete replacement dictionary list, mirroring
	// RecordKindAddTSConfigMapping's shape exactly (the two forms only differ
	// in whether an existing entry is overwritten or 23505s). DU-002 slice
	// 446 follow-up (M0119-0004).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | tokenTypeLen(2) |
	//   tokenType(tokenTypeLen bytes) | dictCount(2) | dictOID(4) * dictCount.
	RecordKindAlterTSConfigMapping byte = 113

	// RecordKindRenameTSDict records an `ALTER TEXT SEARCH DICTIONARY name
	// RENAME TO newName` event, mirroring RecordKindRenameTSConfig. DU-002
	// ALTER TEXT SEARCH DICTIONARY follow-up (M0119-0004).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | newNameLen(2) | newName(newNameLen bytes).
	RecordKindRenameTSDict byte = 114

	// RecordKindSetTSDictSchema records an `ALTER TEXT SEARCH DICTIONARY
	// name SET SCHEMA newSchema` event, mirroring
	// RecordKindSetTSConfigSchema. DU-002 ALTER TEXT SEARCH DICTIONARY
	// follow-up (M0119-0004).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | newSchemaLen(2) | newSchema(newSchemaLen bytes).
	RecordKindSetTSDictSchema byte = 115

	// RecordKindAlterTSDictOptions records an `ALTER TEXT SEARCH DICTIONARY
	// name ( key [= value] [, ...] )` event. Unlike the CREATE-time
	// dictinitoption, replaying this record does not re-run the
	// remove-then-maybe-add merge (catalog.InMemory.AlterTSDictOptions) —
	// it carries the already-computed final serialized dictinitoption text
	// (mirroring how RecordKindCreateTSDict itself stores a pre-serialized
	// string rather than a structured option list), so replay is a plain
	// overwrite via AlterTSDictOptionsDuringRecovery. DU-002 ALTER TEXT
	// SEARCH DICTIONARY follow-up (M0119-0004).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
	//   schema(schemaLen bytes) | initOptionLen(2) | initOption(initOptionLen bytes).
	RecordKindAlterTSDictOptions byte = 116

	// RecordKindAlterRangeTypeRename records an `ALTER TYPE name RENAME TO
	// newName` event for a user-defined range type, mirroring
	// RecordKindAlterCollationRename. Range types are not schema-scoped
	// (keyed by name only, like an access method), so unlike the collation
	// record there is no schema field. M0122-0005 restart-persistence
	// follow-up (deferral ledger 2026-07-06 row, resume point (1)).
	// Format: kind(1) | nameLen(2) | name(nameLen bytes) | newNameLen(2) |
	//   newName(newNameLen bytes).
	RecordKindAlterRangeTypeRename byte = 117

	// RecordKindAlterRangeTypeOwner records an `ALTER TYPE name OWNER TO
	// role` event for a user-defined range type, mirroring
	// RecordKindAlterCollationOwner (no schema field, same reasoning as
	// RecordKindAlterRangeTypeRename). M0122-0005 restart-persistence
	// follow-up (deferral ledger 2026-07-06 row, resume point (1)).
	// Format: kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes).
	RecordKindAlterRangeTypeOwner byte = 118

	// RecordKindCreateDomain records a `CREATE DOMAIN name AS basetype ...`
	// event so a domain survives a restart. catalog.InMemory's domains map is
	// a pure in-memory registry (no per-domain on-disk file namespace, like
	// range types/access methods), so the physical redo path is a no-op; the
	// recovery driver in internal/initdb/domain_ddl_recovery.go re-registers
	// the domain (including every CHECK constraint and its OID) after
	// physical replay. Domains are not schema-scoped (keyed by name only,
	// like a range type). M0122-0005 restart-persistence follow-up (deferral
	// ledger 2026-07-06 row: "domains have no restart persistence at all").
	// Format: kind(1) | oid(4) | arrayOID(4) | baseOID(4) | ownerOID(4) |
	//   flags(1: bit0=NotNull bit1=BaseIsEnum) | nameLen(2)+name |
	//   baseNameLen(2)+baseName | baseArgsCount(2) + baseArgsCount×int64(8) |
	//   defaultLen(2)+defaultSQL | checksCount(2) + checksCount× (
	//     checkOID(4) | checkNameLen(2)+checkName | exprLen(2)+expr |
	//     inValuesCount(2) + inValuesCount×(len(2)+value) ).
	RecordKindCreateDomain byte = 119

	// RecordKindDropDomain records a `DROP DOMAIN name` event. Counterpart to
	// RecordKindCreateDomain; same no-op physical redo path.
	// Format: kind(1) | nameLen(2) | name(nameLen bytes).
	RecordKindDropDomain byte = 120

	// defaultRecoveryDBOid is the database OID used by
	// ProcessCommittedInvalidationMessages when unlinking the per-database
	// pg_internal.init. Matches catalog.DefaultDBOid = 1 (v0 single-database
	// cluster). Kept as a local constant to avoid a circular import.
	defaultRecoveryDBOid uint32 = 1

	// btreeMetaCleanupSize: kind(1)+DBOid(4)+RelOid(4)+Fork(1)
	// +numHeapTuples(8)+lastCleanupNumDeletedTuples(8) = 26.
	btreeMetaCleanupSize = 26

	// smgrRecordSize: kind(1) + DBOid(4) + RelOid(4) + Fork(1) = 10 bytes.
	smgrRecordSize = 10

	pageImageHeaderSize = 14
	// btreeSplitHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + LeftBlk(4) + RightBlk(4) + SibBlk(4) = 22.
	// SibBlk is the old right sibling whose btpo_prev is relinked
	// to RightBlk on a non-rightmost split; it is
	// storage.InvalidBlockNumber for a rightmost split (no third
	// page follows in the payload).
	btreeSplitHeaderSize = 22
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

	// XactRecordSize is the exported size for external callers (e.g.
	// initdb crash-recovery xact-stamp pass). (M0106-0013)
	XactRecordSize = xactRecordSize
	// heapLockSize: kind(1) + DBOid(4) + RelOid(4) + Fork(1)
	// + Block(4) + LineSlot(2) + Xmax(4) + LockStrength(2) = 22.
	heapLockSize = 22
	// heapHotUpdateHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + Block(4) + OldSlot(2) + Xmax(4) = 20. Variable
	// new-tuple bytes follow.
	heapHotUpdateHeaderSize = 20
)

// EncodeCreateDatabase encodes a CREATE DATABASE event (M0054-0001).
// Format: kind(1) | nameLen(2) | name(nameLen bytes) | owner(4, datdba OID,
// M0122-0007) | oid(4, real pg_database.oid, M0122-0007
// physical-storage-isolation slice 1).
func EncodeCreateDatabase(name string, owner, oid uint32) []byte {
	if len(name) > 0xFFFF {
		// goopg's identifier length cap is far below 64 KiB; truncating
		// here is defensive — this branch is unreachable under normal
		// CREATE DATABASE syntax.
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name)+4+4)
	out[0] = RecordKindCreateDatabase
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	binary.LittleEndian.PutUint32(out[3+len(name):], owner)
	binary.LittleEndian.PutUint32(out[3+len(name)+4:], oid)
	return out
}

// DecodeCreateDatabase decodes a RecordKindCreateDatabase payload. owner
// defaults to catalog.BootstrapSuperuserOID when the payload predates the
// M0122-0007 owner suffix (no trailing 4 bytes) — keeps replay of a WAL
// stream written before this change working. oid defaults to 0 (catalog's
// DatabaseOid "no override" sentinel) when the payload predates the
// M0122-0007 physical-storage-isolation slice-1 oid suffix.
func DecodeCreateDatabase(payload []byte) (name string, owner uint32, oid uint32, err error) {
	if len(payload) < 3 {
		return "", 0, 0, fmt.Errorf("wal: create-database payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateDatabase {
		return "", 0, 0, fmt.Errorf("wal: record kind %d is not create-database", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", 0, 0, fmt.Errorf("wal: create-database payload truncated (need %d bytes)", 3+nameLen)
	}
	name = string(payload[3 : 3+nameLen])
	owner = catalog.BootstrapSuperuserOID
	if len(payload) >= 3+nameLen+4 {
		owner = binary.LittleEndian.Uint32(payload[3+nameLen : 3+nameLen+4])
	}
	if len(payload) >= 3+nameLen+4+4 {
		oid = binary.LittleEndian.Uint32(payload[3+nameLen+4 : 3+nameLen+4+4])
	}
	return name, owner, oid, nil
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

// SequenceStatePayload is the decoded form of a RecordKindSequenceState
// record: one sequence's full definition plus its current counter. See the
// RecordKindSequenceState constant for the on-disk format and the emit
// policy (create/alter/setval snapshots + every-32-nextval pre-logging,
// mirroring upstream SEQ_LOG_VALS in postgres/src/backend/commands/sequence.c).
type SequenceStatePayload struct {
	Name         string
	Start        int64
	Increment    int64
	Min          int64
	Max          int64
	Cache        int64
	Current      int64 // raw counter: next nextval returns Current+Increment
	Cycle        bool
	Called       bool
	DataType     string // "smallint" | "integer" | "bigint"
	OwnedBy      string // "table.column" for implicit/OWNED BY sequences, else ""
	ColSpelling  string // serial spelling ("bigserial", ...) when backing a SERIAL column
	IdentityKind byte   // 0=none, 1=GENERATED BY DEFAULT, 2=GENERATED ALWAYS
	DBOid        uint32 // owning database oid (M0122-0007 4e); 0 on a pre-4e record, see DecodeSequenceState
}

// EncodeSequenceState encodes a RecordKindSequenceState record.
func EncodeSequenceState(p SequenceStatePayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindSequenceState)
	var flags byte
	if p.Cycle {
		flags |= 1 << 0
	}
	if p.Called {
		flags |= 1 << 1
	}
	buf.WriteByte(flags)
	buf.WriteByte(p.IdentityKind)
	var i64 [8]byte
	for _, v := range []int64{p.Start, p.Increment, p.Min, p.Max, p.Cache, p.Current} {
		binary.LittleEndian.PutUint64(i64[:], uint64(v))
		buf.Write(i64[:])
	}
	writeStr16 := func(s string) {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
		}
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(s)))
		buf.Write(l[:])
		buf.WriteString(s)
	}
	writeStr16(p.Name)
	writeStr16(p.DataType)
	writeStr16(p.OwnedBy)
	writeStr16(p.ColSpelling)
	// DBOid is appended as a trailing 4-byte field (M0122-0007 4e) so a
	// pre-existing WAL record with no trailer still decodes — see
	// DecodeSequenceState's short-read handling.
	var oidBuf [4]byte
	binary.LittleEndian.PutUint32(oidBuf[:], p.DBOid)
	buf.Write(oidBuf[:])
	return buf.Bytes()
}

// DecodeSequenceState decodes a RecordKindSequenceState payload.
func DecodeSequenceState(payload []byte) (SequenceStatePayload, error) {
	var p SequenceStatePayload
	const fixed = 1 + 1 + 1 + 6*8 // kind + flags + identityKind + six int64s
	if len(payload) < fixed {
		return p, fmt.Errorf("wal: sequence-state payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindSequenceState {
		return p, fmt.Errorf("wal: record kind %d is not sequence-state", payload[0])
	}
	flags := payload[1]
	p.Cycle = flags&(1<<0) != 0
	p.Called = flags&(1<<1) != 0
	p.IdentityKind = payload[2]
	off := 3
	for _, dst := range []*int64{&p.Start, &p.Increment, &p.Min, &p.Max, &p.Cache, &p.Current} {
		*dst = int64(binary.LittleEndian.Uint64(payload[off : off+8]))
		off += 8
	}
	readStr16 := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: sequence-state payload truncated at offset %d", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: sequence-state string truncated (need %d bytes at %d)", l, off)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	var err error
	if p.Name, err = readStr16(); err != nil {
		return p, err
	}
	if p.DataType, err = readStr16(); err != nil {
		return p, err
	}
	if p.OwnedBy, err = readStr16(); err != nil {
		return p, err
	}
	if p.ColSpelling, err = readStr16(); err != nil {
		return p, err
	}
	// DBOid is 0 (DefaultDBOid via NamespaceDBOid) for a pre-4e payload that
	// predates the trailing dbOid field.
	if len(payload) >= off+4 {
		p.DBOid = binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
	}
	return p, nil
}

// RoleStatePayload is the decoded form of a RecordKindRoleState record: one
// role's name, OID, attribute flags, and stored credential. See the
// RecordKindRoleState constant for the on-disk format and emit policy.
//
// CreateDB/CreateRole/Replication/BypassRLS/ConnLimit/ValidUntil mirror
// catalog.RoleAttrs' identically-named fields (DU-002 slice 439 follow-up).
type RoleStatePayload struct {
	Name        string
	OID         uint32 // registry OID — kept stable across restarts
	CanLogin    bool
	Superuser   bool
	CreateDB    bool
	CreateRole  bool
	Replication bool
	BypassRLS   bool
	ConnLimit   int32
	ValidUntil  string
	CredType    byte   // 0=none, 1=plaintext, 2=md5, 3=scram-sha-256
	Secret      string // stored verifier (or plaintext for CredType 1)
}

// EncodeRoleState encodes a RecordKindRoleState record.
func EncodeRoleState(p RoleStatePayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindRoleState)
	var flags byte
	if p.CanLogin {
		flags |= 1 << 0
	}
	if p.Superuser {
		flags |= 1 << 1
	}
	if p.CreateDB {
		flags |= 1 << 2
	}
	if p.CreateRole {
		flags |= 1 << 3
	}
	if p.Replication {
		flags |= 1 << 4
	}
	if p.BypassRLS {
		flags |= 1 << 5
	}
	buf.WriteByte(flags)
	buf.WriteByte(p.CredType)
	var oid [4]byte
	binary.LittleEndian.PutUint32(oid[:], p.OID)
	buf.Write(oid[:])
	var connLimit [4]byte
	binary.LittleEndian.PutUint32(connLimit[:], uint32(p.ConnLimit))
	buf.Write(connLimit[:])
	writeStr16 := func(s string) {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
		}
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(s)))
		buf.Write(l[:])
		buf.WriteString(s)
	}
	writeStr16(p.Name)
	writeStr16(p.Secret)
	writeStr16(p.ValidUntil)
	return buf.Bytes()
}

// DecodeRoleState decodes a RecordKindRoleState payload.
func DecodeRoleState(payload []byte) (RoleStatePayload, error) {
	var p RoleStatePayload
	if len(payload) < 11 {
		return p, fmt.Errorf("wal: role-state payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindRoleState {
		return p, fmt.Errorf("wal: record kind %d is not role-state", payload[0])
	}
	flags := payload[1]
	p.CanLogin = flags&(1<<0) != 0
	p.Superuser = flags&(1<<1) != 0
	p.CreateDB = flags&(1<<2) != 0
	p.CreateRole = flags&(1<<3) != 0
	p.Replication = flags&(1<<4) != 0
	p.BypassRLS = flags&(1<<5) != 0
	p.CredType = payload[2]
	p.OID = binary.LittleEndian.Uint32(payload[3:7])
	p.ConnLimit = int32(binary.LittleEndian.Uint32(payload[7:11]))
	off := 11
	readStr16 := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: role-state payload truncated at offset %d", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: role-state string truncated (need %d bytes at %d)", l, off)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	var err error
	if p.Name, err = readStr16(); err != nil {
		return p, err
	}
	if p.Secret, err = readStr16(); err != nil {
		return p, err
	}
	if p.ValidUntil, err = readStr16(); err != nil {
		return p, err
	}
	return p, nil
}

// EncodeDropRole encodes a RecordKindDropRole record.
// Format identical to EncodeDropDatabase: kind(1) | nameLen(2) | name.
func EncodeDropRole(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropRole
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropRole decodes a RecordKindDropRole payload.
func DecodeDropRole(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-role payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropRole {
		return "", fmt.Errorf("wal: record kind %d is not drop-role", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-role payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeAlterRoleRename encodes a RecordKindAlterRoleRename record.
func EncodeAlterRoleRename(name, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	var buf bytes.Buffer
	buf.WriteByte(RecordKindAlterRoleRename)
	var l [2]byte
	binary.LittleEndian.PutUint16(l[:], uint16(len(name)))
	buf.Write(l[:])
	buf.WriteString(name)
	binary.LittleEndian.PutUint16(l[:], uint16(len(newName)))
	buf.Write(l[:])
	buf.WriteString(newName)
	return buf.Bytes()
}

// DecodeAlterRoleRename decodes a RecordKindAlterRoleRename payload.
func DecodeAlterRoleRename(payload []byte) (name, newName string, err error) {
	if len(payload) < 3 {
		return "", "", fmt.Errorf("wal: alter-role-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterRoleRename {
		return "", "", fmt.Errorf("wal: record kind %d is not alter-role-rename", payload[0])
	}
	off := 1
	readStr16 := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: alter-role-rename payload truncated at offset %d", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: alter-role-rename string truncated (need %d bytes at %d)", l, off)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr16(); err != nil {
		return "", "", err
	}
	if newName, err = readStr16(); err != nil {
		return "", "", err
	}
	return name, newName, nil
}

// EncodeAlterDatabaseSetConfig encodes an `ALTER DATABASE ... SET name =
// value` event (M0119-0004-ACLHEAP ALTER DATABASE ... SET follow-up).
// Format: kind(1) | dbOid(4) | nameLen(2) | name(nameLen bytes) | valueLen(2) | value(valueLen bytes).
func EncodeAlterDatabaseSetConfig(dbOid uint32, name, value string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(value) > 0xFFFF {
		value = value[:0xFFFF]
	}
	var buf bytes.Buffer
	buf.WriteByte(RecordKindAlterDatabaseSetConfig)
	var d [4]byte
	binary.LittleEndian.PutUint32(d[:], dbOid)
	buf.Write(d[:])
	var l [2]byte
	binary.LittleEndian.PutUint16(l[:], uint16(len(name)))
	buf.Write(l[:])
	buf.WriteString(name)
	binary.LittleEndian.PutUint16(l[:], uint16(len(value)))
	buf.Write(l[:])
	buf.WriteString(value)
	return buf.Bytes()
}

// DecodeAlterDatabaseSetConfig decodes a RecordKindAlterDatabaseSetConfig payload.
func DecodeAlterDatabaseSetConfig(payload []byte) (dbOid uint32, name, value string, err error) {
	if len(payload) < 7 {
		return 0, "", "", fmt.Errorf("wal: alter-database-set-config payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterDatabaseSetConfig {
		return 0, "", "", fmt.Errorf("wal: record kind %d is not alter-database-set-config", payload[0])
	}
	dbOid = binary.LittleEndian.Uint32(payload[1:5])
	off := 5
	readStr16 := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: alter-database-set-config payload truncated at offset %d", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: alter-database-set-config string truncated (need %d bytes at %d)", l, off)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr16(); err != nil {
		return 0, "", "", err
	}
	if value, err = readStr16(); err != nil {
		return 0, "", "", err
	}
	return dbOid, name, value, nil
}

// EncodeAlterDatabaseResetConfig encodes an `ALTER DATABASE ... RESET name`
// event. Format: kind(1) | dbOid(4) | nameLen(2) | name(nameLen bytes).
func EncodeAlterDatabaseResetConfig(dbOid uint32, name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	var buf bytes.Buffer
	buf.WriteByte(RecordKindAlterDatabaseResetConfig)
	var d [4]byte
	binary.LittleEndian.PutUint32(d[:], dbOid)
	buf.Write(d[:])
	var l [2]byte
	binary.LittleEndian.PutUint16(l[:], uint16(len(name)))
	buf.Write(l[:])
	buf.WriteString(name)
	return buf.Bytes()
}

// DecodeAlterDatabaseResetConfig decodes a RecordKindAlterDatabaseResetConfig payload.
func DecodeAlterDatabaseResetConfig(payload []byte) (dbOid uint32, name string, err error) {
	if len(payload) < 7 {
		return 0, "", fmt.Errorf("wal: alter-database-reset-config payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterDatabaseResetConfig {
		return 0, "", fmt.Errorf("wal: record kind %d is not alter-database-reset-config", payload[0])
	}
	dbOid = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return 0, "", fmt.Errorf("wal: alter-database-reset-config payload truncated (need %d bytes)", 7+nameLen)
	}
	return dbOid, string(payload[7 : 7+nameLen]), nil
}

// EncodeAlterDatabaseResetAllConfig encodes an `ALTER DATABASE ... RESET
// ALL` event. Format: kind(1) | dbOid(4).
func EncodeAlterDatabaseResetAllConfig(dbOid uint32) []byte {
	out := make([]byte, 5)
	out[0] = RecordKindAlterDatabaseResetAllConfig
	binary.LittleEndian.PutUint32(out[1:5], dbOid)
	return out
}

// DecodeAlterDatabaseResetAllConfig decodes a RecordKindAlterDatabaseResetAllConfig payload.
func DecodeAlterDatabaseResetAllConfig(payload []byte) (dbOid uint32, err error) {
	if len(payload) < 5 {
		return 0, fmt.Errorf("wal: alter-database-reset-all-config payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterDatabaseResetAllConfig {
		return 0, fmt.Errorf("wal: record kind %d is not alter-database-reset-all-config", payload[0])
	}
	return binary.LittleEndian.Uint32(payload[1:5]), nil
}

// EncodeAlterRoleSetConfig encodes an `ALTER ROLE ... [IN DATABASE ...] SET
// name = value` event (M0119-0004-ACLHEAP ALTER ROLE ... SET follow-up).
// Format: kind(1) | roleOid(4) | dbOid(4) | nameLen(2) | name(nameLen bytes) | valueLen(2) | value(valueLen bytes).
func EncodeAlterRoleSetConfig(roleOid, dbOid uint32, name, value string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(value) > 0xFFFF {
		value = value[:0xFFFF]
	}
	var buf bytes.Buffer
	buf.WriteByte(RecordKindAlterRoleSetConfig)
	var d [4]byte
	binary.LittleEndian.PutUint32(d[:], roleOid)
	buf.Write(d[:])
	binary.LittleEndian.PutUint32(d[:], dbOid)
	buf.Write(d[:])
	var l [2]byte
	binary.LittleEndian.PutUint16(l[:], uint16(len(name)))
	buf.Write(l[:])
	buf.WriteString(name)
	binary.LittleEndian.PutUint16(l[:], uint16(len(value)))
	buf.Write(l[:])
	buf.WriteString(value)
	return buf.Bytes()
}

// DecodeAlterRoleSetConfig decodes a RecordKindAlterRoleSetConfig payload.
func DecodeAlterRoleSetConfig(payload []byte) (roleOid, dbOid uint32, name, value string, err error) {
	if len(payload) < 11 {
		return 0, 0, "", "", fmt.Errorf("wal: alter-role-set-config payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterRoleSetConfig {
		return 0, 0, "", "", fmt.Errorf("wal: record kind %d is not alter-role-set-config", payload[0])
	}
	roleOid = binary.LittleEndian.Uint32(payload[1:5])
	dbOid = binary.LittleEndian.Uint32(payload[5:9])
	off := 9
	readStr16 := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: alter-role-set-config payload truncated at offset %d", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: alter-role-set-config string truncated (need %d bytes at %d)", l, off)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr16(); err != nil {
		return 0, 0, "", "", err
	}
	if value, err = readStr16(); err != nil {
		return 0, 0, "", "", err
	}
	return roleOid, dbOid, name, value, nil
}

// EncodeAlterRoleResetConfig encodes an `ALTER ROLE ... [IN DATABASE ...]
// RESET name` event.
// Format: kind(1) | roleOid(4) | dbOid(4) | nameLen(2) | name(nameLen bytes).
func EncodeAlterRoleResetConfig(roleOid, dbOid uint32, name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	var buf bytes.Buffer
	buf.WriteByte(RecordKindAlterRoleResetConfig)
	var d [4]byte
	binary.LittleEndian.PutUint32(d[:], roleOid)
	buf.Write(d[:])
	binary.LittleEndian.PutUint32(d[:], dbOid)
	buf.Write(d[:])
	var l [2]byte
	binary.LittleEndian.PutUint16(l[:], uint16(len(name)))
	buf.Write(l[:])
	buf.WriteString(name)
	return buf.Bytes()
}

// DecodeAlterRoleResetConfig decodes a RecordKindAlterRoleResetConfig payload.
func DecodeAlterRoleResetConfig(payload []byte) (roleOid, dbOid uint32, name string, err error) {
	if len(payload) < 11 {
		return 0, 0, "", fmt.Errorf("wal: alter-role-reset-config payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterRoleResetConfig {
		return 0, 0, "", fmt.Errorf("wal: record kind %d is not alter-role-reset-config", payload[0])
	}
	roleOid = binary.LittleEndian.Uint32(payload[1:5])
	dbOid = binary.LittleEndian.Uint32(payload[5:9])
	nameLen := int(binary.LittleEndian.Uint16(payload[9:11]))
	if len(payload) < 11+nameLen {
		return 0, 0, "", fmt.Errorf("wal: alter-role-reset-config payload truncated (need %d bytes)", 11+nameLen)
	}
	return roleOid, dbOid, string(payload[11 : 11+nameLen]), nil
}

// EncodeAlterRoleResetAllConfig encodes an `ALTER ROLE ... [IN DATABASE
// ...] RESET ALL` event.
// Format: kind(1) | roleOid(4) | dbOid(4).
func EncodeAlterRoleResetAllConfig(roleOid, dbOid uint32) []byte {
	out := make([]byte, 9)
	out[0] = RecordKindAlterRoleResetAllConfig
	binary.LittleEndian.PutUint32(out[1:5], roleOid)
	binary.LittleEndian.PutUint32(out[5:9], dbOid)
	return out
}

// DecodeAlterRoleResetAllConfig decodes a RecordKindAlterRoleResetAllConfig payload.
func DecodeAlterRoleResetAllConfig(payload []byte) (roleOid, dbOid uint32, err error) {
	if len(payload) < 9 {
		return 0, 0, fmt.Errorf("wal: alter-role-reset-all-config payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterRoleResetAllConfig {
		return 0, 0, fmt.Errorf("wal: record kind %d is not alter-role-reset-all-config", payload[0])
	}
	return binary.LittleEndian.Uint32(payload[1:5]), binary.LittleEndian.Uint32(payload[5:9]), nil
}

// Tri-state bit layout for EncodeGrantRoleMembership/DecodeGrantRoleMembership's
// options byte: each of admin/inherit/set gets a "specified" bit (the WITH
// clause named it — nil vs non-nil *bool) and a "value" bit (meaningful only
// when specified).
const (
	roleGrantOptAdminSpecified   = 1 << 0
	roleGrantOptAdminValue       = 1 << 1
	roleGrantOptInheritSpecified = 1 << 2
	roleGrantOptInheritValue     = 1 << 3
	roleGrantOptSetSpecified     = 1 << 4
	roleGrantOptSetValue         = 1 << 5
)

// EncodeGrantRoleMembership encodes a `GRANT <role> TO <member> [WITH {
// ADMIN | INHERIT | SET } { OPTION | TRUE | FALSE } [, ...]]` event.
// admin/inherit/set are tri-state (nil = not specified in the WITH clause).
// Format: kind(1) | roleOid(4) | memberOid(4) | grantorOid(4) | options(1).
func EncodeGrantRoleMembership(roleOid, memberOid, grantorOid uint32, admin, inherit, set *bool) []byte {
	out := make([]byte, 14)
	out[0] = RecordKindGrantRoleMembership
	binary.LittleEndian.PutUint32(out[1:5], roleOid)
	binary.LittleEndian.PutUint32(out[5:9], memberOid)
	binary.LittleEndian.PutUint32(out[9:13], grantorOid)
	var opts byte
	if admin != nil {
		opts |= roleGrantOptAdminSpecified
		if *admin {
			opts |= roleGrantOptAdminValue
		}
	}
	if inherit != nil {
		opts |= roleGrantOptInheritSpecified
		if *inherit {
			opts |= roleGrantOptInheritValue
		}
	}
	if set != nil {
		opts |= roleGrantOptSetSpecified
		if *set {
			opts |= roleGrantOptSetValue
		}
	}
	out[13] = opts
	return out
}

// DecodeGrantRoleMembership decodes a RecordKindGrantRoleMembership payload.
func DecodeGrantRoleMembership(payload []byte) (roleOid, memberOid, grantorOid uint32, admin, inherit, set *bool, err error) {
	if len(payload) < 14 {
		return 0, 0, 0, nil, nil, nil, fmt.Errorf("wal: grant-role-membership payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindGrantRoleMembership {
		return 0, 0, 0, nil, nil, nil, fmt.Errorf("wal: record kind %d is not grant-role-membership", payload[0])
	}
	roleOid = binary.LittleEndian.Uint32(payload[1:5])
	memberOid = binary.LittleEndian.Uint32(payload[5:9])
	grantorOid = binary.LittleEndian.Uint32(payload[9:13])
	opts := payload[13]
	if opts&roleGrantOptAdminSpecified != 0 {
		v := opts&roleGrantOptAdminValue != 0
		admin = &v
	}
	if opts&roleGrantOptInheritSpecified != 0 {
		v := opts&roleGrantOptInheritValue != 0
		inherit = &v
	}
	if opts&roleGrantOptSetSpecified != 0 {
		v := opts&roleGrantOptSetValue != 0
		set = &v
	}
	return roleOid, memberOid, grantorOid, admin, inherit, set, nil
}

// revokeRoleMembershipOptionByte maps a RoleMembershipChange.RevokeOption
// string ("" | "admin" | "inherit" | "set") to/from the single wire byte
// EncodeRevokeRoleMembership persists. M0119-0004-ACLHEAP.
var revokeRoleMembershipOptionByte = map[string]byte{"": 0, "admin": 1, "inherit": 2, "set": 3}
var revokeRoleMembershipOptionName = []string{"", "admin", "inherit", "set"}

// EncodeRevokeRoleMembership encodes a `REVOKE [{ADMIN|INHERIT|SET} OPTION
// FOR] <role> FROM <member> [GRANTED BY <grantor>]` event. grantorOid
// identifies the single (role, member, grantor) row this revoke targets —
// real PG's (roleid, member, grantor) unique index allows independent rows
// from different grantors on the same (role, member) pair, so the grantor
// must be persisted to replay the correct row on recovery. revokeOption is
// "" for a plain REVOKE or one of "admin"/"inherit"/"set" for the OPTION FOR
// prefix (see catalog.InMemory.RevokeRoleMembership).
// Format: kind(1) | roleOid(4) | memberOid(4) | grantorOid(4) | revokeOption(1).
func EncodeRevokeRoleMembership(roleOid, memberOid, grantorOid uint32, revokeOption string) []byte {
	out := make([]byte, 14)
	out[0] = RecordKindRevokeRoleMembership
	binary.LittleEndian.PutUint32(out[1:5], roleOid)
	binary.LittleEndian.PutUint32(out[5:9], memberOid)
	binary.LittleEndian.PutUint32(out[9:13], grantorOid)
	out[13] = revokeRoleMembershipOptionByte[revokeOption]
	return out
}

// DecodeRevokeRoleMembership decodes a RecordKindRevokeRoleMembership payload.
func DecodeRevokeRoleMembership(payload []byte) (roleOid, memberOid, grantorOid uint32, revokeOption string, err error) {
	if len(payload) < 14 {
		return 0, 0, 0, "", fmt.Errorf("wal: revoke-role-membership payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindRevokeRoleMembership {
		return 0, 0, 0, "", fmt.Errorf("wal: record kind %d is not revoke-role-membership", payload[0])
	}
	roleOid = binary.LittleEndian.Uint32(payload[1:5])
	memberOid = binary.LittleEndian.Uint32(payload[5:9])
	grantorOid = binary.LittleEndian.Uint32(payload[9:13])
	optByte := payload[13]
	if int(optByte) >= len(revokeRoleMembershipOptionName) {
		return 0, 0, 0, "", fmt.Errorf("wal: revoke-role-membership unknown option byte %d", optByte)
	}
	revokeOption = revokeRoleMembershipOptionName[optByte]
	return roleOid, memberOid, grantorOid, revokeOption, nil
}

// ColumnDefaultEntry is one (column, DEFAULT expression SQL) pair inside a
// RecordKindColumnDefaults record.
type ColumnDefaultEntry struct {
	Name string
	Expr string // DEFAULT expression as SQL text (parser.ParseExpr round-trips it)
}

// ColumnDefaultsPayload is the decoded form of a RecordKindColumnDefaults
// record: every defaulted column of one table. See the constant for the emit
// policy (per-table snapshot from syncTableToCatalogHeap, last-record-wins).
type ColumnDefaultsPayload struct {
	TableOID uint32
	Defaults []ColumnDefaultEntry
}

// EncodeColumnDefaults encodes a RecordKindColumnDefaults record.
func EncodeColumnDefaults(p ColumnDefaultsPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindColumnDefaults)
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], p.TableOID)
	buf.Write(b4[:])
	var b2 [2]byte
	binary.LittleEndian.PutUint16(b2[:], uint16(len(p.Defaults)))
	buf.Write(b2[:])
	writeStr16 := func(s string) {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
		}
		binary.LittleEndian.PutUint16(b2[:], uint16(len(s)))
		buf.Write(b2[:])
		buf.WriteString(s)
	}
	for _, d := range p.Defaults {
		writeStr16(d.Name)
		writeStr16(d.Expr)
	}
	return buf.Bytes()
}

// DecodeColumnDefaults decodes a RecordKindColumnDefaults payload.
func DecodeColumnDefaults(payload []byte) (ColumnDefaultsPayload, error) {
	var p ColumnDefaultsPayload
	if len(payload) < 7 {
		return p, fmt.Errorf("wal: column-defaults payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindColumnDefaults {
		return p, fmt.Errorf("wal: record kind %d is not column-defaults", payload[0])
	}
	p.TableOID = binary.LittleEndian.Uint32(payload[1:5])
	count := int(binary.LittleEndian.Uint16(payload[5:7]))
	off := 7
	readStr16 := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: column-defaults payload truncated at offset %d", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: column-defaults string truncated (need %d bytes at %d)", l, off)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	for i := 0; i < count; i++ {
		var d ColumnDefaultEntry
		var err error
		if d.Name, err = readStr16(); err != nil {
			return p, err
		}
		if d.Expr, err = readStr16(); err != nil {
			return p, err
		}
		p.Defaults = append(p.Defaults, d)
	}
	return p, nil
}

// MatViewPayload is the decoded form of a RecordKindCreateMatView record: one
// materialized view's defining query and populated state.
type MatViewPayload struct {
	TableOID    uint32
	IsPopulated bool
	Query       string // SELECT body as SQL text (parser.Parse round-trips it)
}

// EncodeMatView encodes a RecordKindCreateMatView record.
func EncodeMatView(p MatViewPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateMatView)
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], p.TableOID)
	buf.Write(b4[:])
	if p.IsPopulated {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
	q := p.Query
	if len(q) > 0xFFFF {
		q = q[:0xFFFF]
	}
	var b2 [2]byte
	binary.LittleEndian.PutUint16(b2[:], uint16(len(q)))
	buf.Write(b2[:])
	buf.WriteString(q)
	return buf.Bytes()
}

// DecodeMatView decodes a RecordKindCreateMatView payload.
func DecodeMatView(payload []byte) (MatViewPayload, error) {
	var p MatViewPayload
	if len(payload) < 8 {
		return p, fmt.Errorf("wal: matview payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateMatView {
		return p, fmt.Errorf("wal: record kind %d is not create-matview", payload[0])
	}
	p.TableOID = binary.LittleEndian.Uint32(payload[1:5])
	p.IsPopulated = payload[5] != 0
	qLen := int(binary.LittleEndian.Uint16(payload[6:8]))
	if len(payload) < 8+qLen {
		return p, fmt.Errorf("wal: matview payload truncated (need %d bytes at 8)", qLen)
	}
	p.Query = string(payload[8 : 8+qLen])
	return p, nil
}

// ViewPayload is the decoded form of a RecordKindCreateView record: one plain
// view's defining query.
type ViewPayload struct {
	TableOID uint32
	Query    string // SELECT body as SQL text (parser.Parse round-trips it)
}

// EncodeView encodes a RecordKindCreateView record.
func EncodeView(p ViewPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateView)
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], p.TableOID)
	buf.Write(b4[:])
	q := p.Query
	if len(q) > 0xFFFF {
		q = q[:0xFFFF]
	}
	var b2 [2]byte
	binary.LittleEndian.PutUint16(b2[:], uint16(len(q)))
	buf.Write(b2[:])
	buf.WriteString(q)
	return buf.Bytes()
}

// DecodeView decodes a RecordKindCreateView payload.
func DecodeView(payload []byte) (ViewPayload, error) {
	var p ViewPayload
	if len(payload) < 7 {
		return p, fmt.Errorf("wal: view payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateView {
		return p, fmt.Errorf("wal: record kind %d is not create-view", payload[0])
	}
	p.TableOID = binary.LittleEndian.Uint32(payload[1:5])
	qLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+qLen {
		return p, fmt.Errorf("wal: view payload truncated (need %d bytes at 7)", qLen)
	}
	p.Query = string(payload[7 : 7+qLen])
	return p, nil
}

// EncodeCreateAccessMethod encodes a CREATE ACCESS METHOD event (DU-002
// restart-persistence follow-up to M0119-0004, DU-002 slice 426 ledger
// resume point). The OID is carried so recovery re-registers the access
// method identically to the live server. Format documented at the
// RecordKindCreateAccessMethod constant.
func EncodeCreateAccessMethod(name, amType string, oid, handlerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	amByte := byte('i')
	if len(amType) > 0 {
		amByte = amType[0]
	}
	out := make([]byte, 12+len(name))
	out[0] = RecordKindCreateAccessMethod
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], handlerOID)
	out[9] = amByte
	binary.LittleEndian.PutUint16(out[10:12], uint16(len(name)))
	copy(out[12:], name)
	return out
}

// DecodeCreateAccessMethod decodes a RecordKindCreateAccessMethod payload.
func DecodeCreateAccessMethod(payload []byte) (name, amType string, oid, handlerOID uint32, err error) {
	if len(payload) < 12 {
		return "", "", 0, 0, fmt.Errorf("wal: create-access-method payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateAccessMethod {
		return "", "", 0, 0, fmt.Errorf("wal: record kind %d is not create-access-method", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	handlerOID = binary.LittleEndian.Uint32(payload[5:9])
	amType = string(payload[9:10])
	nameLen := int(binary.LittleEndian.Uint16(payload[10:12]))
	if len(payload) < 12+nameLen {
		return "", "", 0, 0, fmt.Errorf("wal: create-access-method payload truncated (need %d bytes)", 12+nameLen)
	}
	name = string(payload[12 : 12+nameLen])
	return name, amType, oid, handlerOID, nil
}

// EncodeDropAccessMethod encodes a DROP ACCESS METHOD event (DU-002
// restart-persistence follow-up to M0119-0004, DU-002 slice 426 ledger
// resume point). Format documented at the RecordKindDropAccessMethod
// constant.
func EncodeDropAccessMethod(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropAccessMethod
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropAccessMethod decodes a RecordKindDropAccessMethod payload.
func DecodeDropAccessMethod(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-access-method payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropAccessMethod {
		return "", fmt.Errorf("wal: record kind %d is not drop-access-method", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-access-method payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeCreateRangeType encodes a CREATE TYPE ... AS RANGE event (DU-002
// restart-persistence follow-up to M0110-0001, DU-002 slice 429 ledger
// resume point, sub-item (c)). All four OIDs (range, its auto-generated
// array, the auto-generated multirange, and the multirange's own
// auto-generated array — array-type follow-up) are carried so recovery
// re-registers the range type identically to the live server, plus
// collationOID (RangeType.CollationOID — a resolved explicit `collation`
// option or the subtype's own default; sub-item (a) follow-up) so a
// restarted server doesn't silently drop that resolution back to the
// unconditional default. Format documented at the RecordKindCreateRangeType
// constant.
func EncodeCreateRangeType(name, subtypeName, multirangeName string, oid, arrayOID, multirangeOID, multirangeArrayOID, opclassOID, collationOID, ownerOID uint32) []byte {
	if len(subtypeName) > 0xFFFF {
		subtypeName = subtypeName[:0xFFFF]
	}
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(multirangeName) > 0xFFFF {
		multirangeName = multirangeName[:0xFFFF]
	}
	out := make([]byte, 35+len(subtypeName)+len(name)+len(multirangeName))
	out[0] = RecordKindCreateRangeType
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], multirangeOID)
	binary.LittleEndian.PutUint32(out[9:13], opclassOID)
	binary.LittleEndian.PutUint32(out[13:17], arrayOID)
	binary.LittleEndian.PutUint32(out[17:21], multirangeArrayOID)
	binary.LittleEndian.PutUint32(out[21:25], collationOID)
	binary.LittleEndian.PutUint32(out[25:29], ownerOID)
	off := 29
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(subtypeName)))
	off += 2
	copy(out[off:], subtypeName)
	off += len(subtypeName)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(multirangeName)))
	off += 2
	copy(out[off:], multirangeName)
	return out
}

// DecodeCreateRangeType decodes a RecordKindCreateRangeType payload.
func DecodeCreateRangeType(payload []byte) (name, subtypeName, multirangeName string, oid, arrayOID, multirangeOID, multirangeArrayOID, opclassOID, collationOID, ownerOID uint32, err error) {
	if len(payload) < 31 {
		return "", "", "", 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("wal: create-range-type payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateRangeType {
		return "", "", "", 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("wal: record kind %d is not create-range-type", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	multirangeOID = binary.LittleEndian.Uint32(payload[5:9])
	opclassOID = binary.LittleEndian.Uint32(payload[9:13])
	arrayOID = binary.LittleEndian.Uint32(payload[13:17])
	multirangeArrayOID = binary.LittleEndian.Uint32(payload[17:21])
	collationOID = binary.LittleEndian.Uint32(payload[21:25])
	ownerOID = binary.LittleEndian.Uint32(payload[25:29])
	off := 29
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-range-type payload truncated (length prefix)")
		}
		n := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+n {
			return "", fmt.Errorf("wal: create-range-type payload truncated (need %d bytes)", off+n)
		}
		s := string(payload[off : off+n])
		off += n
		return s, nil
	}
	if subtypeName, err = readStr(); err != nil {
		return "", "", "", 0, 0, 0, 0, 0, 0, 0, err
	}
	if name, err = readStr(); err != nil {
		return "", "", "", 0, 0, 0, 0, 0, 0, 0, err
	}
	if multirangeName, err = readStr(); err != nil {
		return "", "", "", 0, 0, 0, 0, 0, 0, 0, err
	}
	return name, subtypeName, multirangeName, oid, arrayOID, multirangeOID, multirangeArrayOID, opclassOID, collationOID, ownerOID, nil
}

// EncodeDropRangeType encodes a DROP TYPE event for a user-defined range
// type (DU-002 restart-persistence follow-up to M0110-0001, DU-002 slice 429
// ledger resume point, sub-item (c)). Format documented at the
// RecordKindDropRangeType constant.
func EncodeDropRangeType(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropRangeType
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropRangeType decodes a RecordKindDropRangeType payload.
func DecodeDropRangeType(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-range-type payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropRangeType {
		return "", fmt.Errorf("wal: record kind %d is not drop-range-type", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-range-type payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeAlterRangeTypeRename encodes an `ALTER TYPE name RENAME TO newName`
// event for a user-defined range type. Format documented at the
// RecordKindAlterRangeTypeRename constant.
func EncodeAlterRangeTypeRename(name, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(newName))
	out[0] = RecordKindAlterRangeTypeRename
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeAlterRangeTypeRename decodes a RecordKindAlterRangeTypeRename
// payload.
func DecodeAlterRangeTypeRename(payload []byte) (name, newName string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: alter-range-type-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterRangeTypeRename {
		return "", "", fmt.Errorf("wal: record kind %d is not alter-range-type-rename", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: alter-range-type-rename payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", fmt.Errorf("wal: alter-range-type-rename payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, newName, nil
}

// EncodeAlterRangeTypeOwner encodes an `ALTER TYPE name OWNER TO role` event
// for a user-defined range type. Format documented at the
// RecordKindAlterRangeTypeOwner constant.
func EncodeAlterRangeTypeOwner(name string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 7+len(name))
	out[0] = RecordKindAlterRangeTypeOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(name)))
	copy(out[7:], name)
	return out
}

// DecodeAlterRangeTypeOwner decodes a RecordKindAlterRangeTypeOwner payload.
func DecodeAlterRangeTypeOwner(payload []byte) (name string, ownerOID uint32, err error) {
	if len(payload) < 7 {
		return "", 0, fmt.Errorf("wal: alter-range-type-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterRangeTypeOwner {
		return "", 0, fmt.Errorf("wal: record kind %d is not alter-range-type-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return "", 0, fmt.Errorf("wal: alter-range-type-owner payload truncated (need %d bytes)", 7+nameLen)
	}
	name = string(payload[7 : 7+nameLen])
	return name, ownerOID, nil
}

// DomainCheckPayload is one CHECK constraint carried by a
// RecordKindCreateDomain record, mirroring catalog.DomainCheck.
type DomainCheckPayload struct {
	OID      uint32
	Name     string
	Expr     string
	InValues []string
}

// CreateDomainPayload carries the metadata needed to fully reconstruct a
// catalog.Domain during WAL replay. Format documented at the
// RecordKindCreateDomain constant.
type CreateDomainPayload struct {
	Name       string
	OID        uint32
	ArrayOID   uint32
	BaseName   string
	BaseArgs   []int64
	BaseOID    uint32
	BaseIsEnum bool
	NotNull    bool
	Owner      uint32
	DefaultSQL string // "" means no DEFAULT
	Checks     []DomainCheckPayload
}

// EncodeCreateDomain encodes a CREATE DOMAIN event (M0122-0005
// restart-persistence follow-up). Format documented at the
// RecordKindCreateDomain constant.
func EncodeCreateDomain(p CreateDomainPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateDomain)
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], p.OID)
	buf.Write(b4[:])
	binary.LittleEndian.PutUint32(b4[:], p.ArrayOID)
	buf.Write(b4[:])
	binary.LittleEndian.PutUint32(b4[:], p.BaseOID)
	buf.Write(b4[:])
	binary.LittleEndian.PutUint32(b4[:], p.Owner)
	buf.Write(b4[:])
	var flags byte
	if p.NotNull {
		flags |= 1
	}
	if p.BaseIsEnum {
		flags |= 2
	}
	buf.WriteByte(flags)
	var b2 [2]byte
	writeStr16 := func(s string) {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
		}
		binary.LittleEndian.PutUint16(b2[:], uint16(len(s)))
		buf.Write(b2[:])
		buf.WriteString(s)
	}
	writeStr16(p.Name)
	writeStr16(p.BaseName)
	if len(p.BaseArgs) > 0xFFFF {
		p.BaseArgs = p.BaseArgs[:0xFFFF]
	}
	binary.LittleEndian.PutUint16(b2[:], uint16(len(p.BaseArgs)))
	buf.Write(b2[:])
	var b8 [8]byte
	for _, a := range p.BaseArgs {
		binary.LittleEndian.PutUint64(b8[:], uint64(a))
		buf.Write(b8[:])
	}
	writeStr16(p.DefaultSQL)
	if len(p.Checks) > 0xFFFF {
		p.Checks = p.Checks[:0xFFFF]
	}
	binary.LittleEndian.PutUint16(b2[:], uint16(len(p.Checks)))
	buf.Write(b2[:])
	for _, c := range p.Checks {
		binary.LittleEndian.PutUint32(b4[:], c.OID)
		buf.Write(b4[:])
		writeStr16(c.Name)
		writeStr16(c.Expr)
		if len(c.InValues) > 0xFFFF {
			c.InValues = c.InValues[:0xFFFF]
		}
		binary.LittleEndian.PutUint16(b2[:], uint16(len(c.InValues)))
		buf.Write(b2[:])
		for _, v := range c.InValues {
			writeStr16(v)
		}
	}
	return buf.Bytes()
}

// DecodeCreateDomain decodes a RecordKindCreateDomain payload.
func DecodeCreateDomain(payload []byte) (CreateDomainPayload, error) {
	var p CreateDomainPayload
	if len(payload) < 18 {
		return p, fmt.Errorf("wal: create-domain payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateDomain {
		return p, fmt.Errorf("wal: record kind %d is not create-domain", payload[0])
	}
	p.OID = binary.LittleEndian.Uint32(payload[1:5])
	p.ArrayOID = binary.LittleEndian.Uint32(payload[5:9])
	p.BaseOID = binary.LittleEndian.Uint32(payload[9:13])
	p.Owner = binary.LittleEndian.Uint32(payload[13:17])
	flags := payload[17]
	p.NotNull = flags&1 != 0
	p.BaseIsEnum = flags&2 != 0
	off := 18
	readStr16 := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-domain payload truncated at offset %d", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-domain string truncated (need %d bytes at %d)", l, off)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	var err error
	if p.Name, err = readStr16(); err != nil {
		return p, err
	}
	if p.BaseName, err = readStr16(); err != nil {
		return p, err
	}
	if len(payload) < off+2 {
		return p, fmt.Errorf("wal: create-domain payload truncated at offset %d (base args count)", off)
	}
	argCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	for i := 0; i < argCount; i++ {
		if len(payload) < off+8 {
			return p, fmt.Errorf("wal: create-domain payload truncated (base arg at %d)", off)
		}
		p.BaseArgs = append(p.BaseArgs, int64(binary.LittleEndian.Uint64(payload[off:off+8])))
		off += 8
	}
	if p.DefaultSQL, err = readStr16(); err != nil {
		return p, err
	}
	if len(payload) < off+2 {
		return p, fmt.Errorf("wal: create-domain payload truncated at offset %d (checks count)", off)
	}
	checkCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	for i := 0; i < checkCount; i++ {
		var c DomainCheckPayload
		if len(payload) < off+4 {
			return p, fmt.Errorf("wal: create-domain payload truncated (check oid at %d)", off)
		}
		c.OID = binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
		if c.Name, err = readStr16(); err != nil {
			return p, err
		}
		if c.Expr, err = readStr16(); err != nil {
			return p, err
		}
		if len(payload) < off+2 {
			return p, fmt.Errorf("wal: create-domain payload truncated (invalues count at %d)", off)
		}
		inCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		for j := 0; j < inCount; j++ {
			v, verr := readStr16()
			if verr != nil {
				return p, verr
			}
			c.InValues = append(c.InValues, v)
		}
		p.Checks = append(p.Checks, c)
	}
	return p, nil
}

// EncodeDropDomain encodes a DROP DOMAIN event. Format documented at the
// RecordKindDropDomain constant.
func EncodeDropDomain(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropDomain
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropDomain decodes a RecordKindDropDomain payload.
func DecodeDropDomain(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-domain payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropDomain {
		return "", fmt.Errorf("wal: record kind %d is not drop-domain", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-domain payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// CreateOperatorPayload carries the metadata needed to fully reconstruct a
// catalog.UserOperator during WAL replay. Schema is carried as a bare name
// (not NamespaceOID) so recovery re-resolves it against the recovered
// schema registry, mirroring CreateAggregateDuringRecovery's identical
// choice — replay order does not guarantee a schema keeps the same OID
// across a crash/restart cycle.
type CreateOperatorPayload struct {
	OID           uint32
	Schema        string
	Name          string
	LeftType      string
	RightType     string
	FuncOID       uint32
	Owner         uint32
	CommutatorOID uint32
	NegatorOID    uint32
	RestrictOID   uint32
	JoinOID       uint32
	CanMerge      bool
	CanHash       bool
}

// EncodeCreateOperator encodes a CREATE OPERATOR event (DU-002
// restart-persistence follow-up to M0119-0004/M0110-0001). Format
// documented at the RecordKindCreateOperator constant.
func EncodeCreateOperator(p CreateOperatorPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateOperator)
	var u32 [4]byte
	putU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(u32[:], v)
		buf.Write(u32[:])
	}
	putU32(p.OID)
	putU32(p.FuncOID)
	putU32(p.Owner)
	putU32(p.CommutatorOID)
	putU32(p.NegatorOID)
	putU32(p.RestrictOID)
	putU32(p.JoinOID)
	var flags byte
	if p.CanMerge {
		flags |= 1 << 0
	}
	if p.CanHash {
		flags |= 1 << 1
	}
	buf.WriteByte(flags)
	writeWALStr := func(s string) {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
		}
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(s)))
		buf.Write(l[:])
		buf.WriteString(s)
	}
	writeWALStr(p.Schema)
	writeWALStr(p.Name)
	writeWALStr(p.LeftType)
	writeWALStr(p.RightType)
	return buf.Bytes()
}

// DecodeCreateOperator decodes a RecordKindCreateOperator payload.
func DecodeCreateOperator(payload []byte) (CreateOperatorPayload, error) {
	var p CreateOperatorPayload
	if len(payload) < 30 {
		return p, fmt.Errorf("wal: create-operator payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateOperator {
		return p, fmt.Errorf("wal: record kind %d is not create-operator", payload[0])
	}
	off := 1
	readU32 := func() uint32 {
		v := binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
		return v
	}
	p.OID = readU32()
	p.FuncOID = readU32()
	p.Owner = readU32()
	p.CommutatorOID = readU32()
	p.NegatorOID = readU32()
	p.RestrictOID = readU32()
	p.JoinOID = readU32()
	flags := payload[off]
	off++
	p.CanMerge = flags&(1<<0) != 0
	p.CanHash = flags&(1<<1) != 0
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-operator payload truncated (length prefix)")
		}
		n := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+n {
			return "", fmt.Errorf("wal: create-operator payload truncated (need %d bytes)", off+n)
		}
		s := string(payload[off : off+n])
		off += n
		return s, nil
	}
	var err error
	if p.Schema, err = readStr(); err != nil {
		return p, err
	}
	if p.Name, err = readStr(); err != nil {
		return p, err
	}
	if p.LeftType, err = readStr(); err != nil {
		return p, err
	}
	if p.RightType, err = readStr(); err != nil {
		return p, err
	}
	return p, nil
}

// EncodeDropOperator encodes a DROP OPERATOR event by OID. Format documented
// at the RecordKindDropOperator constant.
func EncodeDropOperator(oid uint32) []byte {
	out := make([]byte, 5)
	out[0] = RecordKindDropOperator
	binary.LittleEndian.PutUint32(out[1:5], oid)
	return out
}

// DecodeDropOperator decodes a RecordKindDropOperator payload.
func DecodeDropOperator(payload []byte) (oid uint32, err error) {
	if len(payload) < 5 {
		return 0, fmt.Errorf("wal: drop-operator payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropOperator {
		return 0, fmt.Errorf("wal: record kind %d is not drop-operator", payload[0])
	}
	return binary.LittleEndian.Uint32(payload[1:5]), nil
}

// CreateOperatorFamilyPayload carries the metadata needed to fully
// reconstruct a catalog.UserOperatorFamily during WAL replay. Schema is
// carried as a bare name so recovery re-resolves it against the recovered
// schema registry, mirroring CreateOperatorPayload's identical choice.
type CreateOperatorFamilyPayload struct {
	OID    uint32
	Schema string
	Name   string
	Method uint32
}

// EncodeCreateOperatorFamily encodes a CREATE OPERATOR FAMILY event. Format
// documented at the RecordKindCreateOperatorFamily constant.
func EncodeCreateOperatorFamily(p CreateOperatorFamilyPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateOperatorFamily)
	var u32 [4]byte
	putU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(u32[:], v)
		buf.Write(u32[:])
	}
	putU32(p.OID)
	putU32(p.Method)
	writeWALStr := func(s string) {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
		}
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(s)))
		buf.Write(l[:])
		buf.WriteString(s)
	}
	writeWALStr(p.Schema)
	writeWALStr(p.Name)
	return buf.Bytes()
}

// DecodeCreateOperatorFamily decodes a RecordKindCreateOperatorFamily payload.
func DecodeCreateOperatorFamily(payload []byte) (CreateOperatorFamilyPayload, error) {
	var p CreateOperatorFamilyPayload
	if len(payload) < 9 {
		return p, fmt.Errorf("wal: create-operator-family payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateOperatorFamily {
		return p, fmt.Errorf("wal: record kind %d is not create-operator-family", payload[0])
	}
	off := 1
	readU32 := func() uint32 {
		v := binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
		return v
	}
	p.OID = readU32()
	p.Method = readU32()
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-operator-family payload truncated (length prefix)")
		}
		n := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+n {
			return "", fmt.Errorf("wal: create-operator-family payload truncated (need %d bytes)", off+n)
		}
		s := string(payload[off : off+n])
		off += n
		return s, nil
	}
	var err error
	if p.Schema, err = readStr(); err != nil {
		return p, err
	}
	if p.Name, err = readStr(); err != nil {
		return p, err
	}
	return p, nil
}

// CreateOperatorClassPayload carries the metadata needed to fully
// reconstruct a catalog.UserOperatorClass during WAL replay. FamilyOID is
// carried directly — see the RecordKindCreateOperatorClass constant for why
// that is safe (the owning family's own create record always precedes this
// one).
type CreateOperatorClassPayload struct {
	OID        uint32
	Schema     string
	Name       string
	Method     uint32
	FamilyOID  uint32
	InTypeOID  uint32
	KeyTypeOID uint32
	IsDefault  bool
}

// EncodeCreateOperatorClass encodes a CREATE OPERATOR CLASS event's own
// pg_opclass row. Format documented at the RecordKindCreateOperatorClass
// constant.
func EncodeCreateOperatorClass(p CreateOperatorClassPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateOperatorClass)
	var u32 [4]byte
	putU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(u32[:], v)
		buf.Write(u32[:])
	}
	putU32(p.OID)
	putU32(p.Method)
	putU32(p.FamilyOID)
	putU32(p.InTypeOID)
	putU32(p.KeyTypeOID)
	if p.IsDefault {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
	writeWALStr := func(s string) {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
		}
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(s)))
		buf.Write(l[:])
		buf.WriteString(s)
	}
	writeWALStr(p.Schema)
	writeWALStr(p.Name)
	return buf.Bytes()
}

// DecodeCreateOperatorClass decodes a RecordKindCreateOperatorClass payload.
func DecodeCreateOperatorClass(payload []byte) (CreateOperatorClassPayload, error) {
	var p CreateOperatorClassPayload
	if len(payload) < 22 {
		return p, fmt.Errorf("wal: create-operator-class payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateOperatorClass {
		return p, fmt.Errorf("wal: record kind %d is not create-operator-class", payload[0])
	}
	off := 1
	readU32 := func() uint32 {
		v := binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
		return v
	}
	p.OID = readU32()
	p.Method = readU32()
	p.FamilyOID = readU32()
	p.InTypeOID = readU32()
	p.KeyTypeOID = readU32()
	p.IsDefault = payload[off] != 0
	off++
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-operator-class payload truncated (length prefix)")
		}
		n := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+n {
			return "", fmt.Errorf("wal: create-operator-class payload truncated (need %d bytes)", off+n)
		}
		s := string(payload[off : off+n])
		off += n
		return s, nil
	}
	var err error
	if p.Schema, err = readStr(); err != nil {
		return p, err
	}
	if p.Name, err = readStr(); err != nil {
		return p, err
	}
	return p, nil
}

// EncodeDropOperatorClass encodes a DROP OPERATOR CLASS event by OID. Format
// documented at the RecordKindDropOperatorClass constant.
func EncodeDropOperatorClass(oid uint32) []byte {
	out := make([]byte, 5)
	out[0] = RecordKindDropOperatorClass
	binary.LittleEndian.PutUint32(out[1:5], oid)
	return out
}

// DecodeDropOperatorClass decodes a RecordKindDropOperatorClass payload.
func DecodeDropOperatorClass(payload []byte) (oid uint32, err error) {
	if len(payload) < 5 {
		return 0, fmt.Errorf("wal: drop-operator-class payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropOperatorClass {
		return 0, fmt.Errorf("wal: record kind %d is not drop-operator-class", payload[0])
	}
	return binary.LittleEndian.Uint32(payload[1:5]), nil
}

// EncodeDropOperatorFamily encodes a DROP OPERATOR FAMILY event by OID.
// Format documented at the RecordKindDropOperatorFamily constant.
func EncodeDropOperatorFamily(oid uint32) []byte {
	out := make([]byte, 5)
	out[0] = RecordKindDropOperatorFamily
	binary.LittleEndian.PutUint32(out[1:5], oid)
	return out
}

// DecodeDropOperatorFamily decodes a RecordKindDropOperatorFamily payload.
func DecodeDropOperatorFamily(payload []byte) (oid uint32, err error) {
	if len(payload) < 5 {
		return 0, fmt.Errorf("wal: drop-operator-family payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropOperatorFamily {
		return 0, fmt.Errorf("wal: record kind %d is not drop-operator-family", payload[0])
	}
	return binary.LittleEndian.Uint32(payload[1:5]), nil
}

// AmOpMemberPayload carries the metadata needed to fully reconstruct a
// catalog.AmOpMember (one pg_amop row) during WAL replay.
type AmOpMemberPayload struct {
	OID           uint32
	FamilyOID     uint32
	ClassOID      uint32
	LeftType      uint32
	RightType     uint32
	Strategy      uint32
	OperOID       uint32
	Method        uint32
	SortFamilyOID uint32
}

// EncodeCreateAmOpMember encodes a pg_amop-row-creation event. Format
// documented at the RecordKindCreateAmOpMember constant.
func EncodeCreateAmOpMember(p AmOpMemberPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateAmOpMember)
	var u32 [4]byte
	putU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(u32[:], v)
		buf.Write(u32[:])
	}
	putU32(p.OID)
	putU32(p.FamilyOID)
	putU32(p.ClassOID)
	putU32(p.LeftType)
	putU32(p.RightType)
	putU32(p.Strategy)
	putU32(p.OperOID)
	putU32(p.Method)
	putU32(p.SortFamilyOID)
	return buf.Bytes()
}

// DecodeCreateAmOpMember decodes a RecordKindCreateAmOpMember payload.
func DecodeCreateAmOpMember(payload []byte) (AmOpMemberPayload, error) {
	var p AmOpMemberPayload
	if len(payload) < 37 {
		return p, fmt.Errorf("wal: create-amop-member payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateAmOpMember {
		return p, fmt.Errorf("wal: record kind %d is not create-amop-member", payload[0])
	}
	off := 1
	readU32 := func() uint32 {
		v := binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
		return v
	}
	p.OID = readU32()
	p.FamilyOID = readU32()
	p.ClassOID = readU32()
	p.LeftType = readU32()
	p.RightType = readU32()
	p.Strategy = readU32()
	p.OperOID = readU32()
	p.Method = readU32()
	p.SortFamilyOID = readU32()
	return p, nil
}

// EncodeDropAmOpMember encodes an ALTER OPERATOR FAMILY ... DROP OPERATOR
// removal, keyed the same way RemoveAmOpMember is. Format documented at the
// RecordKindDropAmOpMember constant.
func EncodeDropAmOpMember(familyOID, leftType, rightType, strategy uint32) []byte {
	out := make([]byte, 17)
	out[0] = RecordKindDropAmOpMember
	binary.LittleEndian.PutUint32(out[1:5], familyOID)
	binary.LittleEndian.PutUint32(out[5:9], leftType)
	binary.LittleEndian.PutUint32(out[9:13], rightType)
	binary.LittleEndian.PutUint32(out[13:17], strategy)
	return out
}

// DecodeDropAmOpMember decodes a RecordKindDropAmOpMember payload.
func DecodeDropAmOpMember(payload []byte) (familyOID, leftType, rightType, strategy uint32, err error) {
	if len(payload) < 17 {
		return 0, 0, 0, 0, fmt.Errorf("wal: drop-amop-member payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropAmOpMember {
		return 0, 0, 0, 0, fmt.Errorf("wal: record kind %d is not drop-amop-member", payload[0])
	}
	familyOID = binary.LittleEndian.Uint32(payload[1:5])
	leftType = binary.LittleEndian.Uint32(payload[5:9])
	rightType = binary.LittleEndian.Uint32(payload[9:13])
	strategy = binary.LittleEndian.Uint32(payload[13:17])
	return familyOID, leftType, rightType, strategy, nil
}

// AmProcMemberPayload carries the metadata needed to fully reconstruct a
// catalog.AmProcMember (one pg_amproc row) during WAL replay.
type AmProcMemberPayload struct {
	OID       uint32
	FamilyOID uint32
	ClassOID  uint32
	LeftType  uint32
	RightType uint32
	ProcNum   uint32
	ProcOID   uint32
	Method    uint32
}

// EncodeCreateAmProcMember encodes a pg_amproc-row-creation event. Format
// documented at the RecordKindCreateAmProcMember constant.
func EncodeCreateAmProcMember(p AmProcMemberPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateAmProcMember)
	var u32 [4]byte
	putU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(u32[:], v)
		buf.Write(u32[:])
	}
	putU32(p.OID)
	putU32(p.FamilyOID)
	putU32(p.ClassOID)
	putU32(p.LeftType)
	putU32(p.RightType)
	putU32(p.ProcNum)
	putU32(p.ProcOID)
	putU32(p.Method)
	return buf.Bytes()
}

// DecodeCreateAmProcMember decodes a RecordKindCreateAmProcMember payload.
func DecodeCreateAmProcMember(payload []byte) (AmProcMemberPayload, error) {
	var p AmProcMemberPayload
	if len(payload) < 33 {
		return p, fmt.Errorf("wal: create-amproc-member payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateAmProcMember {
		return p, fmt.Errorf("wal: record kind %d is not create-amproc-member", payload[0])
	}
	off := 1
	readU32 := func() uint32 {
		v := binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
		return v
	}
	p.OID = readU32()
	p.FamilyOID = readU32()
	p.ClassOID = readU32()
	p.LeftType = readU32()
	p.RightType = readU32()
	p.ProcNum = readU32()
	p.ProcOID = readU32()
	p.Method = readU32()
	return p, nil
}

// EncodeDropAmProcMember encodes an ALTER OPERATOR FAMILY ... DROP FUNCTION
// removal, keyed the same way RemoveAmProcMember is. Format documented at
// the RecordKindDropAmProcMember constant.
func EncodeDropAmProcMember(familyOID, leftType, rightType, procNum uint32) []byte {
	out := make([]byte, 17)
	out[0] = RecordKindDropAmProcMember
	binary.LittleEndian.PutUint32(out[1:5], familyOID)
	binary.LittleEndian.PutUint32(out[5:9], leftType)
	binary.LittleEndian.PutUint32(out[9:13], rightType)
	binary.LittleEndian.PutUint32(out[13:17], procNum)
	return out
}

// DecodeDropAmProcMember decodes a RecordKindDropAmProcMember payload.
func DecodeDropAmProcMember(payload []byte) (familyOID, leftType, rightType, procNum uint32, err error) {
	if len(payload) < 17 {
		return 0, 0, 0, 0, fmt.Errorf("wal: drop-amproc-member payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropAmProcMember {
		return 0, 0, 0, 0, fmt.Errorf("wal: record kind %d is not drop-amproc-member", payload[0])
	}
	familyOID = binary.LittleEndian.Uint32(payload[1:5])
	leftType = binary.LittleEndian.Uint32(payload[5:9])
	rightType = binary.LittleEndian.Uint32(payload[9:13])
	procNum = binary.LittleEndian.Uint32(payload[13:17])
	return familyOID, leftType, rightType, procNum, nil
}

// EncodeDropSequence encodes a RecordKindDropSequence record.
// Format identical to EncodeDropDatabase: kind(1) | nameLen(2) | name, plus a
// trailing 4-byte dbOid (M0122-0007 4e) appended after the name so a
// pre-existing WAL record with no trailer still decodes — see
// DecodeDropSequence's short-read handling.
func EncodeDropSequence(name string, dbOid uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name)+4)
	out[0] = RecordKindDropSequence
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	binary.LittleEndian.PutUint32(out[3+len(name):], dbOid)
	return out
}

// DecodeDropSequence decodes a RecordKindDropSequence payload. dbOid is 0
// (DefaultDBOid via NamespaceDBOid) for a pre-4e payload that predates the
// trailing dbOid field.
func DecodeDropSequence(payload []byte) (name string, dbOid uint32, err error) {
	if len(payload) < 3 {
		return "", 0, fmt.Errorf("wal: drop-sequence payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropSequence {
		return "", 0, fmt.Errorf("wal: record kind %d is not drop-sequence", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", 0, fmt.Errorf("wal: drop-sequence payload truncated (need %d bytes)", 3+nameLen)
	}
	name = string(payload[3 : 3+nameLen])
	if off := 3 + nameLen; len(payload) >= off+4 {
		dbOid = binary.LittleEndian.Uint32(payload[off : off+4])
	}
	return name, dbOid, nil
}





// EncodeCreateTablespace encodes a CREATE TABLESPACE event (M0122-0007
// tablespace-registry restart-durability follow-up). The OID is carried so
// recovery re-registers the tablespace with the same identifier the live
// server assigned.
// Format: kind(1) | oid(4) | nameLen(2)+name | ownerLen(2)+owner |
//
//	locationLen(2)+location.
func EncodeCreateTablespace(name, owner, location string, oid uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(owner) > 0xFFFF {
		owner = owner[:0xFFFF]
	}
	if len(location) > 0xFFFF {
		location = location[:0xFFFF]
	}
	out := make([]byte, 11+len(name)+len(owner)+len(location))
	out[0] = RecordKindCreateTablespace
	binary.LittleEndian.PutUint32(out[1:5], oid)
	off := 5
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:off+len(name)], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(owner)))
	off += 2
	copy(out[off:off+len(owner)], owner)
	off += len(owner)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(location)))
	off += 2
	copy(out[off:off+len(location)], location)
	return out
}

// DecodeCreateTablespace decodes a RecordKindCreateTablespace payload.
func DecodeCreateTablespace(payload []byte) (name, owner, location string, oid uint32, err error) {
	if len(payload) < 7 {
		return "", "", "", 0, fmt.Errorf("wal: create-tablespace payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateTablespace {
		return "", "", "", 0, fmt.Errorf("wal: record kind %d is not create-tablespace", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	off := 5
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-tablespace payload truncated at length prefix (offset %d)", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-tablespace payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", 0, err
	}
	if owner, err = readStr(); err != nil {
		return "", "", "", 0, err
	}
	if location, err = readStr(); err != nil {
		return "", "", "", 0, err
	}
	return name, owner, location, oid, nil
}

// EncodeDropTablespace encodes a DROP TABLESPACE event.
// Format: kind(1) | nameLen(2) | name(nameLen bytes).
func EncodeDropTablespace(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropTablespace
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropTablespace decodes a RecordKindDropTablespace payload.
func DecodeDropTablespace(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-tablespace payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropTablespace {
		return "", fmt.Errorf("wal: record kind %d is not drop-tablespace", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-tablespace payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeCreateForeignServer encodes a CREATE SERVER event (M0122-0007
// foreign-server registry restart-durability follow-up). Format: kind(1) |
// oid(4) | nameLen(2)+name | fdwNameLen(2)+fdwName | srvTypeLen(2)+srvType |
// srvVersionLen(2)+srvVersion | optionsCount(2) | (optLen(2)+opt)* | dbOid(4).
// dbOid is a trailing-appended field (M0122-0007 4e follow-up 36, mirroring
// EncodeDropSequence's dbOid trailer) so a pre-follow-up-36 payload still
// decodes: DecodeCreateForeignServer returns dbOid 0 (DefaultDBOid via
// NamespaceDBOid) when the trailer is absent.
func EncodeCreateForeignServer(name, fdwName, srvType, srvVersion string, options []string, oid uint32, dbOid uint32) []byte {
	clip := func(s string) string {
		if len(s) > 0xFFFF {
			return s[:0xFFFF]
		}
		return s
	}
	name, fdwName, srvType, srvVersion = clip(name), clip(fdwName), clip(srvType), clip(srvVersion)
	if len(options) > 0xFFFF {
		options = options[:0xFFFF]
	}
	size := 4 + 2 + len(name) + 2 + len(fdwName) + 2 + len(srvType) + 2 + len(srvVersion) + 2
	for _, opt := range options {
		if len(opt) > 0xFFFF {
			opt = opt[:0xFFFF]
		}
		size += 2 + len(opt)
	}
	size += 4 // trailing dbOid
	out := make([]byte, 1+size)
	out[0] = RecordKindCreateForeignServer
	binary.LittleEndian.PutUint32(out[1:5], oid)
	off := 5
	writeStr := func(s string) {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(s)))
		off += 2
		copy(out[off:off+len(s)], s)
		off += len(s)
	}
	writeStr(name)
	writeStr(fdwName)
	writeStr(srvType)
	writeStr(srvVersion)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(options)))
	off += 2
	for _, opt := range options {
		if len(opt) > 0xFFFF {
			opt = opt[:0xFFFF]
		}
		writeStr(opt)
	}
	binary.LittleEndian.PutUint32(out[off:off+4], dbOid)
	off += 4
	return out[:off]
}

// DecodeCreateForeignServer decodes a RecordKindCreateForeignServer payload.
// dbOid is 0 (DefaultDBOid via NamespaceDBOid) for a pre-follow-up-36
// payload that predates the trailing dbOid field.
func DecodeCreateForeignServer(payload []byte) (name, fdwName, srvType, srvVersion string, options []string, oid uint32, dbOid uint32, err error) {
	if len(payload) < 5 {
		return "", "", "", "", nil, 0, 0, fmt.Errorf("wal: create-foreign-server payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateForeignServer {
		return "", "", "", "", nil, 0, 0, fmt.Errorf("wal: record kind %d is not create-foreign-server", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	off := 5
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-foreign-server payload truncated at length prefix (offset %d)", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-foreign-server payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", "", nil, 0, 0, err
	}
	if fdwName, err = readStr(); err != nil {
		return "", "", "", "", nil, 0, 0, err
	}
	if srvType, err = readStr(); err != nil {
		return "", "", "", "", nil, 0, 0, err
	}
	if srvVersion, err = readStr(); err != nil {
		return "", "", "", "", nil, 0, 0, err
	}
	if len(payload) < off+2 {
		return "", "", "", "", nil, 0, 0, fmt.Errorf("wal: create-foreign-server payload truncated at options count (offset %d)", off)
	}
	optCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if optCount > 0 {
		options = make([]string, 0, optCount)
		for i := 0; i < optCount; i++ {
			opt, oerr := readStr()
			if oerr != nil {
				return "", "", "", "", nil, 0, 0, oerr
			}
			options = append(options, opt)
		}
	}
	if len(payload) >= off+4 {
		dbOid = binary.LittleEndian.Uint32(payload[off : off+4])
	}
	return name, fdwName, srvType, srvVersion, options, oid, dbOid, nil
}

// EncodeDropForeignServer encodes a DROP SERVER event.
// Format: kind(1) | nameLen(2) | name(nameLen bytes) | dbOid(4). dbOid is a
// trailing-appended field (M0122-0007 4e follow-up 36, mirroring
// EncodeDropSequence's dbOid trailer).
func EncodeDropForeignServer(name string, dbOid uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name)+4)
	out[0] = RecordKindDropForeignServer
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	binary.LittleEndian.PutUint32(out[3+len(name):], dbOid)
	return out
}

// DecodeDropForeignServer decodes a RecordKindDropForeignServer payload.
// dbOid is 0 (DefaultDBOid via NamespaceDBOid) for a pre-follow-up-36
// payload that predates the trailing dbOid field.
func DecodeDropForeignServer(payload []byte) (name string, dbOid uint32, err error) {
	if len(payload) < 3 {
		return "", 0, fmt.Errorf("wal: drop-foreign-server payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropForeignServer {
		return "", 0, fmt.Errorf("wal: record kind %d is not drop-foreign-server", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", 0, fmt.Errorf("wal: drop-foreign-server payload truncated (need %d bytes)", 3+nameLen)
	}
	name = string(payload[3 : 3+nameLen])
	if off := 3 + nameLen; len(payload) >= off+4 {
		dbOid = binary.LittleEndian.Uint32(payload[off : off+4])
	}
	return name, dbOid, nil
}

// EncodeCreateUserMapping encodes a CREATE USER MAPPING event (M0122-0007
// user-mapping registry restart-durability follow-up). Format: kind(1) |
// oid(4) | userLen(2)+user | serverLen(2)+server | optionsCount(2) |
// (optLen(2)+opt)* | dbOid(4). dbOid is a trailing-appended field (M0122-0007
// 4e follow-up 37, mirroring EncodeCreateForeignServer's dbOid trailer) so a
// pre-follow-up-37 payload still decodes: DecodeCreateUserMapping returns
// dbOid 0 (DefaultDBOid via NamespaceDBOid) when the trailer is absent.
func EncodeCreateUserMapping(user, server string, options []string, oid uint32, dbOid uint32) []byte {
	clip := func(s string) string {
		if len(s) > 0xFFFF {
			return s[:0xFFFF]
		}
		return s
	}
	user, server = clip(user), clip(server)
	if len(options) > 0xFFFF {
		options = options[:0xFFFF]
	}
	size := 4 + 2 + len(user) + 2 + len(server) + 2
	for _, opt := range options {
		if len(opt) > 0xFFFF {
			opt = opt[:0xFFFF]
		}
		size += 2 + len(opt)
	}
	size += 4 // trailing dbOid
	out := make([]byte, 1+size)
	out[0] = RecordKindCreateUserMapping
	binary.LittleEndian.PutUint32(out[1:5], oid)
	off := 5
	writeStr := func(s string) {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(s)))
		off += 2
		copy(out[off:off+len(s)], s)
		off += len(s)
	}
	writeStr(user)
	writeStr(server)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(options)))
	off += 2
	for _, opt := range options {
		if len(opt) > 0xFFFF {
			opt = opt[:0xFFFF]
		}
		writeStr(opt)
	}
	binary.LittleEndian.PutUint32(out[off:off+4], dbOid)
	off += 4
	return out[:off]
}

// DecodeCreateUserMapping decodes a RecordKindCreateUserMapping payload.
// dbOid is 0 (DefaultDBOid via NamespaceDBOid) for a pre-follow-up-37
// payload that predates the trailing dbOid field.
func DecodeCreateUserMapping(payload []byte) (user, server string, options []string, oid uint32, dbOid uint32, err error) {
	if len(payload) < 5 {
		return "", "", nil, 0, 0, fmt.Errorf("wal: create-user-mapping payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateUserMapping {
		return "", "", nil, 0, 0, fmt.Errorf("wal: record kind %d is not create-user-mapping", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	off := 5
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-user-mapping payload truncated at length prefix (offset %d)", off)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-user-mapping payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if user, err = readStr(); err != nil {
		return "", "", nil, 0, 0, err
	}
	if server, err = readStr(); err != nil {
		return "", "", nil, 0, 0, err
	}
	if len(payload) < off+2 {
		return "", "", nil, 0, 0, fmt.Errorf("wal: create-user-mapping payload truncated at options count (offset %d)", off)
	}
	optCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if optCount > 0 {
		options = make([]string, 0, optCount)
		for i := 0; i < optCount; i++ {
			opt, oerr := readStr()
			if oerr != nil {
				return "", "", nil, 0, 0, oerr
			}
			options = append(options, opt)
		}
	}
	if len(payload) >= off+4 {
		dbOid = binary.LittleEndian.Uint32(payload[off : off+4])
	}
	return user, server, options, oid, dbOid, nil
}

// EncodeDropUserMapping encodes a DROP USER MAPPING event. Format: kind(1) |
// userLen(2)+user | serverLen(2)+server | dbOid(4). dbOid is a
// trailing-appended field (M0122-0007 4e follow-up 37, mirroring
// EncodeDropForeignServer's dbOid trailer).
func EncodeDropUserMapping(user, server string, dbOid uint32) []byte {
	if len(user) > 0xFFFF {
		user = user[:0xFFFF]
	}
	if len(server) > 0xFFFF {
		server = server[:0xFFFF]
	}
	out := make([]byte, 5+len(user)+len(server)+4)
	out[0] = RecordKindDropUserMapping
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(user)))
	copy(out[3:3+len(user)], user)
	off := 3 + len(user)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(server)))
	off += 2
	copy(out[off:off+len(server)], server)
	off += len(server)
	binary.LittleEndian.PutUint32(out[off:off+4], dbOid)
	off += 4
	return out[:off]
}

// DecodeDropUserMapping decodes a RecordKindDropUserMapping payload. dbOid
// is 0 (DefaultDBOid via NamespaceDBOid) for a pre-follow-up-37 payload that
// predates the trailing dbOid field.
func DecodeDropUserMapping(payload []byte) (user, server string, dbOid uint32, err error) {
	if len(payload) < 3 {
		return "", "", 0, fmt.Errorf("wal: drop-user-mapping payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropUserMapping {
		return "", "", 0, fmt.Errorf("wal: record kind %d is not drop-user-mapping", payload[0])
	}
	userLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+userLen+2 {
		return "", "", 0, fmt.Errorf("wal: drop-user-mapping payload truncated (need %d bytes)", 3+userLen+2)
	}
	user = string(payload[3 : 3+userLen])
	off := 3 + userLen
	serverLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+serverLen {
		return "", "", 0, fmt.Errorf("wal: drop-user-mapping payload truncated (need %d bytes)", off+serverLen)
	}
	server = string(payload[off : off+serverLen])
	off += serverLen
	if len(payload) >= off+4 {
		dbOid = binary.LittleEndian.Uint32(payload[off : off+4])
	}
	return user, server, dbOid, nil
}

// EncodeCreateTransform encodes a CREATE TRANSFORM event (M0119-0004 restart
// persistence). The OID and resolved from/to function OIDs are carried so
// recovery re-registers the transform identically to the live server.
// Format: kind(1) | oid(4) | fromFuncOID(4) | toFuncOID(4) | typeLen(2) |
// type(typeLen bytes) | langLen(2) | lang(langLen bytes).
func EncodeCreateTransform(typeName, lang string, oid, fromFuncOID, toFuncOID uint32) []byte {
	if len(typeName) > 0xFFFF {
		typeName = typeName[:0xFFFF]
	}
	if len(lang) > 0xFFFF {
		lang = lang[:0xFFFF]
	}
	out := make([]byte, 17+len(typeName)+len(lang))
	out[0] = RecordKindCreateTransform
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], fromFuncOID)
	binary.LittleEndian.PutUint32(out[9:13], toFuncOID)
	binary.LittleEndian.PutUint16(out[13:15], uint16(len(typeName)))
	off := 15
	copy(out[off:], typeName)
	off += len(typeName)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(lang)))
	off += 2
	copy(out[off:], lang)
	return out
}

// DecodeCreateTransform decodes a RecordKindCreateTransform payload.
func DecodeCreateTransform(payload []byte) (typeName, lang string, oid, fromFuncOID, toFuncOID uint32, err error) {
	if len(payload) < 15 {
		return "", "", 0, 0, 0, fmt.Errorf("wal: create-transform payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateTransform {
		return "", "", 0, 0, 0, fmt.Errorf("wal: record kind %d is not create-transform", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	fromFuncOID = binary.LittleEndian.Uint32(payload[5:9])
	toFuncOID = binary.LittleEndian.Uint32(payload[9:13])
	typeLen := int(binary.LittleEndian.Uint16(payload[13:15]))
	off := 15
	if len(payload) < off+typeLen+2 {
		return "", "", 0, 0, 0, fmt.Errorf("wal: create-transform payload truncated (need %d bytes)", off+typeLen+2)
	}
	typeName = string(payload[off : off+typeLen])
	off += typeLen
	langLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+langLen {
		return "", "", 0, 0, 0, fmt.Errorf("wal: create-transform payload truncated (need %d bytes)", off+langLen)
	}
	lang = string(payload[off : off+langLen])
	return typeName, lang, oid, fromFuncOID, toFuncOID, nil
}

// EncodeDropTransform encodes a DROP TRANSFORM event (M0119-0004 restart
// persistence). Format: kind(1) | typeLen(2) | type(typeLen bytes) |
// langLen(2) | lang(langLen bytes).
func EncodeDropTransform(typeName, lang string) []byte {
	if len(typeName) > 0xFFFF {
		typeName = typeName[:0xFFFF]
	}
	if len(lang) > 0xFFFF {
		lang = lang[:0xFFFF]
	}
	out := make([]byte, 5+len(typeName)+len(lang))
	out[0] = RecordKindDropTransform
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(typeName)))
	off := 3
	copy(out[off:], typeName)
	off += len(typeName)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(lang)))
	off += 2
	copy(out[off:], lang)
	return out
}

// DecodeDropTransform decodes a RecordKindDropTransform payload.
func DecodeDropTransform(payload []byte) (typeName, lang string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: drop-transform payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropTransform {
		return "", "", fmt.Errorf("wal: record kind %d is not drop-transform", payload[0])
	}
	typeLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+typeLen+2 {
		return "", "", fmt.Errorf("wal: drop-transform payload truncated (need %d bytes)", off+typeLen+2)
	}
	typeName = string(payload[off : off+typeLen])
	off += typeLen
	langLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+langLen {
		return "", "", fmt.Errorf("wal: drop-transform payload truncated (need %d bytes)", off+langLen)
	}
	lang = string(payload[off : off+langLen])
	return typeName, lang, nil
}

// EncodeCreateCast encodes a CREATE CAST event (DU-002 restart-persistence
// follow-up to M0119-0004). The OID and resolved function OID are carried so
// recovery re-registers the cast identically to the live server. context and
// method are each a single PG catalog char ('e'/'a'/'i' and 'b'/'i'/'f'
// respectively) but are wire-encoded as length-prefixed strings for symmetry
// with the rest of the record and to tolerate an empty value defensively.
// Format: kind(1) | oid(4) | funcOID(4) | context(1) | method(1) |
// sourceLen(2) | source(sourceLen bytes) | targetLen(2) | target(targetLen bytes).
func EncodeCreateCast(source, target, context, method string, oid, funcOID uint32) []byte {
	if len(source) > 0xFFFF {
		source = source[:0xFFFF]
	}
	if len(target) > 0xFFFF {
		target = target[:0xFFFF]
	}
	var contextByte, methodByte byte
	if len(context) > 0 {
		contextByte = context[0]
	}
	if len(method) > 0 {
		methodByte = method[0]
	}
	out := make([]byte, 15+len(source)+len(target))
	out[0] = RecordKindCreateCast
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], funcOID)
	out[9] = contextByte
	out[10] = methodByte
	binary.LittleEndian.PutUint16(out[11:13], uint16(len(source)))
	off := 13
	copy(out[off:], source)
	off += len(source)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(target)))
	off += 2
	copy(out[off:], target)
	return out
}

// DecodeCreateCast decodes a RecordKindCreateCast payload.
func DecodeCreateCast(payload []byte) (source, target, context, method string, oid, funcOID uint32, err error) {
	if len(payload) < 13 {
		return "", "", "", "", 0, 0, fmt.Errorf("wal: create-cast payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateCast {
		return "", "", "", "", 0, 0, fmt.Errorf("wal: record kind %d is not create-cast", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	funcOID = binary.LittleEndian.Uint32(payload[5:9])
	if payload[9] != 0 {
		context = string([]byte{payload[9]})
	}
	if payload[10] != 0 {
		method = string([]byte{payload[10]})
	}
	sourceLen := int(binary.LittleEndian.Uint16(payload[11:13]))
	off := 13
	if len(payload) < off+sourceLen+2 {
		return "", "", "", "", 0, 0, fmt.Errorf("wal: create-cast payload truncated (need %d bytes)", off+sourceLen+2)
	}
	source = string(payload[off : off+sourceLen])
	off += sourceLen
	targetLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+targetLen {
		return "", "", "", "", 0, 0, fmt.Errorf("wal: create-cast payload truncated (need %d bytes)", off+targetLen)
	}
	target = string(payload[off : off+targetLen])
	return source, target, context, method, oid, funcOID, nil
}

// EncodeDropCast encodes a DROP CAST event (DU-002 restart-persistence
// follow-up to M0119-0004). Format: kind(1) | sourceLen(2) |
// source(sourceLen bytes) | targetLen(2) | target(targetLen bytes).
func EncodeDropCast(source, target string) []byte {
	if len(source) > 0xFFFF {
		source = source[:0xFFFF]
	}
	if len(target) > 0xFFFF {
		target = target[:0xFFFF]
	}
	out := make([]byte, 5+len(source)+len(target))
	out[0] = RecordKindDropCast
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(source)))
	off := 3
	copy(out[off:], source)
	off += len(source)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(target)))
	off += 2
	copy(out[off:], target)
	return out
}

// DecodeDropCast decodes a RecordKindDropCast payload.
func DecodeDropCast(payload []byte) (source, target string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: drop-cast payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropCast {
		return "", "", fmt.Errorf("wal: record kind %d is not drop-cast", payload[0])
	}
	sourceLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+sourceLen+2 {
		return "", "", fmt.Errorf("wal: drop-cast payload truncated (need %d bytes)", off+sourceLen+2)
	}
	source = string(payload[off : off+sourceLen])
	off += sourceLen
	targetLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+targetLen {
		return "", "", fmt.Errorf("wal: drop-cast payload truncated (need %d bytes)", off+targetLen)
	}
	target = string(payload[off : off+targetLen])
	return source, target, nil
}

// EncodeCreateConversion encodes a CREATE [DEFAULT] CONVERSION event (DU-002
// restart-persistence follow-up to M0119-0004). The OID and resolved function
// OID are carried so recovery re-registers the conversion identically to the
// live server. forEncoding/toEncoding are pg_enc IDs, which are small
// non-negative values in practice but wire-encoded as signed int32 to match
// catalog.UserConversion's field types exactly.
// Format: kind(1) | oid(4) | ownerOID(4) | funcOID(4) | forEncoding(4) |
// toEncoding(4) | defaultFlag(1) | nameLen(2) | name(nameLen bytes) |
// schemaLen(2) | schema(schemaLen bytes) | procSchemaLen(2) |
// procSchema(procSchemaLen bytes) | procNameLen(2) | procName(procNameLen bytes).
func EncodeCreateConversion(name, schema, procSchema, procName string, oid, ownerOID, funcOID uint32, forEncoding, toEncoding int32, isDefault bool) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(procSchema) > 0xFFFF {
		procSchema = procSchema[:0xFFFF]
	}
	if len(procName) > 0xFFFF {
		procName = procName[:0xFFFF]
	}
	// 22-byte fixed header + 4 length-prefixed strings (2-byte length each).
	out := make([]byte, 22+8+len(name)+len(schema)+len(procSchema)+len(procName))
	out[0] = RecordKindCreateConversion
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], ownerOID)
	binary.LittleEndian.PutUint32(out[9:13], funcOID)
	binary.LittleEndian.PutUint32(out[13:17], uint32(forEncoding))
	binary.LittleEndian.PutUint32(out[17:21], uint32(toEncoding))
	if isDefault {
		out[21] = 1
	}
	off := 22
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(procSchema)))
	off += 2
	copy(out[off:], procSchema)
	off += len(procSchema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(procName)))
	off += 2
	copy(out[off:], procName)
	return out
}

// DecodeCreateConversion decodes a RecordKindCreateConversion payload.
func DecodeCreateConversion(payload []byte) (name, schema, procSchema, procName string, oid, ownerOID, funcOID uint32, forEncoding, toEncoding int32, isDefault bool, err error) {
	if len(payload) < 22 {
		return "", "", "", "", 0, 0, 0, 0, 0, false, fmt.Errorf("wal: create-conversion payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateConversion {
		return "", "", "", "", 0, 0, 0, 0, 0, false, fmt.Errorf("wal: record kind %d is not create-conversion", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	ownerOID = binary.LittleEndian.Uint32(payload[5:9])
	funcOID = binary.LittleEndian.Uint32(payload[9:13])
	forEncoding = int32(binary.LittleEndian.Uint32(payload[13:17]))
	toEncoding = int32(binary.LittleEndian.Uint32(payload[17:21]))
	isDefault = payload[21] != 0
	off := 22
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-conversion payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-conversion payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", "", 0, 0, 0, 0, 0, false, err
	}
	if schema, err = readStr(); err != nil {
		return "", "", "", "", 0, 0, 0, 0, 0, false, err
	}
	if procSchema, err = readStr(); err != nil {
		return "", "", "", "", 0, 0, 0, 0, 0, false, err
	}
	if procName, err = readStr(); err != nil {
		return "", "", "", "", 0, 0, 0, 0, 0, false, err
	}
	return name, schema, procSchema, procName, oid, ownerOID, funcOID, forEncoding, toEncoding, isDefault, nil
}

// EncodeDropConversion encodes a DROP CONVERSION event (DU-002
// restart-persistence follow-up to M0119-0004). Format: kind(1) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes).
func EncodeDropConversion(name, schema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(schema))
	out[0] = RecordKindDropConversion
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	return out
}

// DecodeDropConversion decodes a RecordKindDropConversion payload.
func DecodeDropConversion(payload []byte) (name, schema string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: drop-conversion payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropConversion {
		return "", "", fmt.Errorf("wal: record kind %d is not drop-conversion", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: drop-conversion payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen {
		return "", "", fmt.Errorf("wal: drop-conversion payload truncated (need %d bytes)", off+schemaLen)
	}
	schema = string(payload[off : off+schemaLen])
	return name, schema, nil
}

// EncodeCreateTSDict encodes a CREATE TEXT SEARCH DICTIONARY event (DU-002
// restart-persistence follow-up to slice 437, M0119-0004). The OID and
// resolved template OID are carried so recovery re-registers the dictionary
// identically to the live server.
// Format: kind(1) | oid(4) | ownerOID(4) | template(4) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
// initOptionLen(2) | initOption(initOptionLen bytes).
func EncodeCreateTSDict(name, schema, initOption string, oid, ownerOID, template uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(initOption) > 0xFFFF {
		initOption = initOption[:0xFFFF]
	}
	out := make([]byte, 13+6+len(name)+len(schema)+len(initOption))
	out[0] = RecordKindCreateTSDict
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], ownerOID)
	binary.LittleEndian.PutUint32(out[9:13], template)
	off := 13
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(initOption)))
	off += 2
	copy(out[off:], initOption)
	return out
}

// DecodeCreateTSDict decodes a RecordKindCreateTSDict payload.
func DecodeCreateTSDict(payload []byte) (name, schema, initOption string, oid, ownerOID, template uint32, err error) {
	if len(payload) < 13 {
		return "", "", "", 0, 0, 0, fmt.Errorf("wal: create-tsdict payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateTSDict {
		return "", "", "", 0, 0, 0, fmt.Errorf("wal: record kind %d is not create-tsdict", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	ownerOID = binary.LittleEndian.Uint32(payload[5:9])
	template = binary.LittleEndian.Uint32(payload[9:13])
	off := 13
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-tsdict payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-tsdict payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", 0, 0, 0, err
	}
	if schema, err = readStr(); err != nil {
		return "", "", "", 0, 0, 0, err
	}
	if initOption, err = readStr(); err != nil {
		return "", "", "", 0, 0, 0, err
	}
	return name, schema, initOption, oid, ownerOID, template, nil
}

// EncodeDropTSDict encodes a DROP TEXT SEARCH DICTIONARY event. Format:
// kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes).
func EncodeDropTSDict(name, schema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(schema))
	out[0] = RecordKindDropTSDict
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	return out
}

// DecodeDropTSDict decodes a RecordKindDropTSDict payload.
func DecodeDropTSDict(payload []byte) (name, schema string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: drop-tsdict payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropTSDict {
		return "", "", fmt.Errorf("wal: record kind %d is not drop-tsdict", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: drop-tsdict payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen {
		return "", "", fmt.Errorf("wal: drop-tsdict payload truncated (need %d bytes)", off+schemaLen)
	}
	schema = string(payload[off : off+schemaLen])
	return name, schema, nil
}

// EncodeCreateTSConfig encodes a CREATE TEXT SEARCH CONFIGURATION event
// (DU-002 restart-persistence follow-up to slice 446, M0119-0004).
// Format: kind(1) | oid(4) | ownerOID(4) | parser(4) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes).
func EncodeCreateTSConfig(name, schema string, oid, ownerOID, parser uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	out := make([]byte, 13+4+len(name)+len(schema))
	out[0] = RecordKindCreateTSConfig
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], ownerOID)
	binary.LittleEndian.PutUint32(out[9:13], parser)
	off := 13
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	return out
}

// DecodeCreateTSConfig decodes a RecordKindCreateTSConfig payload.
func DecodeCreateTSConfig(payload []byte) (name, schema string, oid, ownerOID, parser uint32, err error) {
	if len(payload) < 13 {
		return "", "", 0, 0, 0, fmt.Errorf("wal: create-tsconfig payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateTSConfig {
		return "", "", 0, 0, 0, fmt.Errorf("wal: record kind %d is not create-tsconfig", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	ownerOID = binary.LittleEndian.Uint32(payload[5:9])
	parser = binary.LittleEndian.Uint32(payload[9:13])
	off := 13
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-tsconfig payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-tsconfig payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", 0, 0, 0, err
	}
	if schema, err = readStr(); err != nil {
		return "", "", 0, 0, 0, err
	}
	return name, schema, oid, ownerOID, parser, nil
}

// EncodeAddTSConfigMapping encodes an ALTER TEXT SEARCH CONFIGURATION name
// ADD MAPPING event. Format: kind(1) | nameLen(2) | name(nameLen bytes) |
// schemaLen(2) | schema(schemaLen bytes) | tokenTypeLen(2) |
// tokenType(tokenTypeLen bytes) | dictCount(2) | dictOID(4) * dictCount.
func EncodeAddTSConfigMapping(name, schema, tokenType string, dictOIDs []uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(tokenType) > 0xFFFF {
		tokenType = tokenType[:0xFFFF]
	}
	if len(dictOIDs) > 0xFFFF {
		dictOIDs = dictOIDs[:0xFFFF]
	}
	out := make([]byte, 1+6+len(name)+len(schema)+len(tokenType)+2+4*len(dictOIDs))
	out[0] = RecordKindAddTSConfigMapping
	off := 1
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(tokenType)))
	off += 2
	copy(out[off:], tokenType)
	off += len(tokenType)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(dictOIDs)))
	off += 2
	for _, d := range dictOIDs {
		binary.LittleEndian.PutUint32(out[off:off+4], d)
		off += 4
	}
	return out
}

// DecodeAddTSConfigMapping decodes a RecordKindAddTSConfigMapping payload.
func DecodeAddTSConfigMapping(payload []byte) (name, schema, tokenType string, dictOIDs []uint32, err error) {
	if len(payload) < 1 {
		return "", "", "", nil, fmt.Errorf("wal: add-tsconfig-mapping payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAddTSConfigMapping {
		return "", "", "", nil, fmt.Errorf("wal: record kind %d is not add-tsconfig-mapping", payload[0])
	}
	off := 1
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: add-tsconfig-mapping payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: add-tsconfig-mapping payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", nil, err
	}
	if schema, err = readStr(); err != nil {
		return "", "", "", nil, err
	}
	if tokenType, err = readStr(); err != nil {
		return "", "", "", nil, err
	}
	if len(payload) < off+2 {
		return "", "", "", nil, fmt.Errorf("wal: add-tsconfig-mapping payload truncated (need %d bytes)", off+2)
	}
	count := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+4*count {
		return "", "", "", nil, fmt.Errorf("wal: add-tsconfig-mapping payload truncated (need %d bytes)", off+4*count)
	}
	dictOIDs = make([]uint32, count)
	for i := 0; i < count; i++ {
		dictOIDs[i] = binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
	}
	return name, schema, tokenType, dictOIDs, nil
}

// EncodeAlterTSConfigMapping encodes an ALTER TEXT SEARCH CONFIGURATION name
// ALTER MAPPING FOR tok WITH dict [, ...] override event for one token type.
// Same wire shape as EncodeAddTSConfigMapping (see RecordKindAlterTSConfigMapping).
func EncodeAlterTSConfigMapping(name, schema, tokenType string, dictOIDs []uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(tokenType) > 0xFFFF {
		tokenType = tokenType[:0xFFFF]
	}
	if len(dictOIDs) > 0xFFFF {
		dictOIDs = dictOIDs[:0xFFFF]
	}
	out := make([]byte, 1+6+len(name)+len(schema)+len(tokenType)+2+4*len(dictOIDs))
	out[0] = RecordKindAlterTSConfigMapping
	off := 1
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(tokenType)))
	off += 2
	copy(out[off:], tokenType)
	off += len(tokenType)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(dictOIDs)))
	off += 2
	for _, d := range dictOIDs {
		binary.LittleEndian.PutUint32(out[off:off+4], d)
		off += 4
	}
	return out
}

// DecodeAlterTSConfigMapping decodes a RecordKindAlterTSConfigMapping payload.
func DecodeAlterTSConfigMapping(payload []byte) (name, schema, tokenType string, dictOIDs []uint32, err error) {
	if len(payload) < 1 {
		return "", "", "", nil, fmt.Errorf("wal: alter-tsconfig-mapping payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterTSConfigMapping {
		return "", "", "", nil, fmt.Errorf("wal: record kind %d is not alter-tsconfig-mapping", payload[0])
	}
	off := 1
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: alter-tsconfig-mapping payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: alter-tsconfig-mapping payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", nil, err
	}
	if schema, err = readStr(); err != nil {
		return "", "", "", nil, err
	}
	if tokenType, err = readStr(); err != nil {
		return "", "", "", nil, err
	}
	if len(payload) < off+2 {
		return "", "", "", nil, fmt.Errorf("wal: alter-tsconfig-mapping payload truncated (need %d bytes)", off+2)
	}
	count := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+4*count {
		return "", "", "", nil, fmt.Errorf("wal: alter-tsconfig-mapping payload truncated (need %d bytes)", off+4*count)
	}
	dictOIDs = make([]uint32, count)
	for i := 0; i < count; i++ {
		dictOIDs[i] = binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
	}
	return name, schema, tokenType, dictOIDs, nil
}

// EncodeDropTSConfig encodes a DROP TEXT SEARCH CONFIGURATION event. Format:
// kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes).
func EncodeDropTSConfig(name, schema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(schema))
	out[0] = RecordKindDropTSConfig
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	return out
}

// DecodeDropTSConfig decodes a RecordKindDropTSConfig payload.
func DecodeDropTSConfig(payload []byte) (name, schema string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: drop-tsconfig payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropTSConfig {
		return "", "", fmt.Errorf("wal: record kind %d is not drop-tsconfig", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: drop-tsconfig payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen {
		return "", "", fmt.Errorf("wal: drop-tsconfig payload truncated (need %d bytes)", off+schemaLen)
	}
	schema = string(payload[off : off+schemaLen])
	return name, schema, nil
}

// EncodeDropTSConfigMapping encodes an ALTER TEXT SEARCH CONFIGURATION name
// DROP MAPPING FOR tokenType event (DU-002 restart-persistence follow-up to
// the slice 446 RENAME/SET SCHEMA/DROP MAPPING follow-up, M0119-0004).
// Format: kind(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
// schema(schemaLen bytes) | tokenTypeLen(2) | tokenType(tokenTypeLen bytes).
func EncodeDropTSConfigMapping(name, schema, tokenType string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(tokenType) > 0xFFFF {
		tokenType = tokenType[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(tokenType))
	out[0] = RecordKindDropTSConfigMapping
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(tokenType)))
	off += 2
	copy(out[off:], tokenType)
	return out
}

// DecodeDropTSConfigMapping decodes a RecordKindDropTSConfigMapping payload.
func DecodeDropTSConfigMapping(payload []byte) (name, schema, tokenType string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: drop-tsconfig-mapping payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropTSConfigMapping {
		return "", "", "", fmt.Errorf("wal: record kind %d is not drop-tsconfig-mapping", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: drop-tsconfig-mapping payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: drop-tsconfig-mapping payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	tokenTypeLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+tokenTypeLen {
		return "", "", "", fmt.Errorf("wal: drop-tsconfig-mapping payload truncated (need %d bytes)", off+tokenTypeLen)
	}
	tokenType = string(payload[off : off+tokenTypeLen])
	return name, schema, tokenType, nil
}

// EncodeRenameTSConfig encodes an ALTER TEXT SEARCH CONFIGURATION name
// RENAME TO newName event, mirroring EncodeAlterCollationRename. DU-002
// restart-persistence follow-up to the slice 446 RENAME/SET SCHEMA/DROP
// MAPPING follow-up (M0119-0004). Format: kind(1) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
// newNameLen(2) | newName(newNameLen bytes).
func EncodeRenameTSConfig(name, schema, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(newName))
	out[0] = RecordKindRenameTSConfig
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeRenameTSConfig decodes a RecordKindRenameTSConfig payload.
func DecodeRenameTSConfig(payload []byte) (name, schema, newName string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: rename-tsconfig payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindRenameTSConfig {
		return "", "", "", fmt.Errorf("wal: record kind %d is not rename-tsconfig", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: rename-tsconfig payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: rename-tsconfig payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", "", fmt.Errorf("wal: rename-tsconfig payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, schema, newName, nil
}

// EncodeSetTSConfigSchema encodes an ALTER TEXT SEARCH CONFIGURATION name
// SET SCHEMA newSchema event, mirroring EncodeAlterCollationSetSchema.
// DU-002 restart-persistence follow-up to the slice 446 RENAME/SET
// SCHEMA/DROP MAPPING follow-up (M0119-0004). Format: kind(1) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
// newSchemaLen(2) | newSchema(newSchemaLen bytes).
func EncodeSetTSConfigSchema(name, schema, newSchema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(newSchema) > 0xFFFF {
		newSchema = newSchema[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(newSchema))
	out[0] = RecordKindSetTSConfigSchema
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newSchema)))
	off += 2
	copy(out[off:], newSchema)
	return out
}

// DecodeSetTSConfigSchema decodes a RecordKindSetTSConfigSchema payload.
func DecodeSetTSConfigSchema(payload []byte) (name, schema, newSchema string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: set-tsconfig-schema payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindSetTSConfigSchema {
		return "", "", "", fmt.Errorf("wal: record kind %d is not set-tsconfig-schema", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: set-tsconfig-schema payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: set-tsconfig-schema payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	newSchemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newSchemaLen {
		return "", "", "", fmt.Errorf("wal: set-tsconfig-schema payload truncated (need %d bytes)", off+newSchemaLen)
	}
	newSchema = string(payload[off : off+newSchemaLen])
	return name, schema, newSchema, nil
}

// EncodeRenameTSDict encodes an ALTER TEXT SEARCH DICTIONARY name RENAME TO
// newName event, mirroring EncodeRenameTSConfig. DU-002 ALTER TEXT SEARCH
// DICTIONARY follow-up (M0119-0004). Format: kind(1) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
// newNameLen(2) | newName(newNameLen bytes).
func EncodeRenameTSDict(name, schema, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(newName))
	out[0] = RecordKindRenameTSDict
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeRenameTSDict decodes a RecordKindRenameTSDict payload.
func DecodeRenameTSDict(payload []byte) (name, schema, newName string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: rename-tsdict payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindRenameTSDict {
		return "", "", "", fmt.Errorf("wal: record kind %d is not rename-tsdict", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: rename-tsdict payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: rename-tsdict payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", "", fmt.Errorf("wal: rename-tsdict payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, schema, newName, nil
}

// EncodeSetTSDictSchema encodes an ALTER TEXT SEARCH DICTIONARY name SET
// SCHEMA newSchema event, mirroring EncodeSetTSConfigSchema. DU-002 ALTER
// TEXT SEARCH DICTIONARY follow-up (M0119-0004). Format: kind(1) |
// nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen
// bytes) | newSchemaLen(2) | newSchema(newSchemaLen bytes).
func EncodeSetTSDictSchema(name, schema, newSchema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(newSchema) > 0xFFFF {
		newSchema = newSchema[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(newSchema))
	out[0] = RecordKindSetTSDictSchema
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newSchema)))
	off += 2
	copy(out[off:], newSchema)
	return out
}

// DecodeSetTSDictSchema decodes a RecordKindSetTSDictSchema payload.
func DecodeSetTSDictSchema(payload []byte) (name, schema, newSchema string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: set-tsdict-schema payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindSetTSDictSchema {
		return "", "", "", fmt.Errorf("wal: record kind %d is not set-tsdict-schema", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: set-tsdict-schema payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: set-tsdict-schema payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	newSchemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newSchemaLen {
		return "", "", "", fmt.Errorf("wal: set-tsdict-schema payload truncated (need %d bytes)", off+newSchemaLen)
	}
	newSchema = string(payload[off : off+newSchemaLen])
	return name, schema, newSchema, nil
}

// EncodeAlterTSDictOptions encodes an ALTER TEXT SEARCH DICTIONARY name
// ( key [= value] [, ...] ) event. Carries the already-merged final
// dictinitoption text (computed once by catalog.InMemory.AlterTSDictOptions
// at original-execution time), not the raw option-list directives — replay
// (DecodeAlterTSDictOptions + AlterTSDictOptionsDuringRecovery) is a plain
// overwrite, mirroring how RecordKindCreateTSDict itself stores a
// pre-serialized string. DU-002 ALTER TEXT SEARCH DICTIONARY follow-up
// (M0119-0004). Format: kind(1) | nameLen(2) | name(nameLen bytes) |
// schemaLen(2) | schema(schemaLen bytes) | initOptionLen(2) |
// initOption(initOptionLen bytes).
func EncodeAlterTSDictOptions(name, schema, initOption string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(initOption) > 0xFFFF {
		initOption = initOption[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(initOption))
	out[0] = RecordKindAlterTSDictOptions
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(initOption)))
	off += 2
	copy(out[off:], initOption)
	return out
}

// DecodeAlterTSDictOptions decodes a RecordKindAlterTSDictOptions payload.
func DecodeAlterTSDictOptions(payload []byte) (name, schema, initOption string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: alter-tsdict-options payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterTSDictOptions {
		return "", "", "", fmt.Errorf("wal: record kind %d is not alter-tsdict-options", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-tsdict-options payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-tsdict-options payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	initOptionLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+initOptionLen {
		return "", "", "", fmt.Errorf("wal: alter-tsdict-options payload truncated (need %d bytes)", off+initOptionLen)
	}
	initOption = string(payload[off : off+initOptionLen])
	return name, schema, initOption, nil
}

// EncodeReplaceTSConfigMappingDict encodes an ALTER TEXT SEARCH CONFIGURATION
// name ALTER MAPPING [FOR tok [, ...]] REPLACE olddict WITH newdict event.
// An empty tokenTypes encodes the bare REPLACE form. DU-002 replacedict
// follow-up (M0119-0004).
func EncodeReplaceTSConfigMappingDict(name, schema string, tokenTypes []string, oldOID, newOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(tokenTypes) > 0xFFFF {
		tokenTypes = tokenTypes[:0xFFFF]
	}
	total := 1 + 2 + len(name) + 2 + len(schema) + 2
	for _, tt := range tokenTypes {
		if len(tt) > 0xFFFF {
			tt = tt[:0xFFFF]
		}
		total += 2 + len(tt)
	}
	total += 8
	out := make([]byte, total)
	out[0] = RecordKindReplaceTSConfigMappingDict
	off := 1
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(tokenTypes)))
	off += 2
	for _, tt := range tokenTypes {
		if len(tt) > 0xFFFF {
			tt = tt[:0xFFFF]
		}
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(tt)))
		off += 2
		copy(out[off:], tt)
		off += len(tt)
	}
	binary.LittleEndian.PutUint32(out[off:off+4], oldOID)
	off += 4
	binary.LittleEndian.PutUint32(out[off:off+4], newOID)
	return out
}

// DecodeReplaceTSConfigMappingDict decodes a
// RecordKindReplaceTSConfigMappingDict payload.
func DecodeReplaceTSConfigMappingDict(payload []byte) (name, schema string, tokenTypes []string, oldOID, newOID uint32, err error) {
	if len(payload) < 1 {
		return "", "", nil, 0, 0, fmt.Errorf("wal: replace-tsconfig-mapping-dict payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindReplaceTSConfigMappingDict {
		return "", "", nil, 0, 0, fmt.Errorf("wal: record kind %d is not replace-tsconfig-mapping-dict", payload[0])
	}
	off := 1
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: replace-tsconfig-mapping-dict payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: replace-tsconfig-mapping-dict payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", nil, 0, 0, err
	}
	if schema, err = readStr(); err != nil {
		return "", "", nil, 0, 0, err
	}
	if len(payload) < off+2 {
		return "", "", nil, 0, 0, fmt.Errorf("wal: replace-tsconfig-mapping-dict payload truncated (need %d bytes)", off+2)
	}
	count := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	tokenTypes = make([]string, count)
	for i := 0; i < count; i++ {
		if len(payload) < off+2 {
			return "", "", nil, 0, 0, fmt.Errorf("wal: replace-tsconfig-mapping-dict payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", "", nil, 0, 0, fmt.Errorf("wal: replace-tsconfig-mapping-dict payload truncated (need %d bytes)", off+l)
		}
		tokenTypes[i] = string(payload[off : off+l])
		off += l
	}
	if len(payload) < off+8 {
		return "", "", nil, 0, 0, fmt.Errorf("wal: replace-tsconfig-mapping-dict payload truncated (need %d bytes)", off+8)
	}
	oldOID = binary.LittleEndian.Uint32(payload[off : off+4])
	off += 4
	newOID = binary.LittleEndian.Uint32(payload[off : off+4])
	return name, schema, tokenTypes, oldOID, newOID, nil
}

// EncodeCreateCollation encodes a CREATE COLLATION event (DU-002
// restart-persistence follow-up to M0119-0004). The OID is carried so
// recovery re-registers the collation identically to the live server.
// encoding is a pg_enc ID (-1 = encoding-independent), wire-encoded as
// signed int32 to match catalog.UserCollation's field type exactly.
// Format: kind(1) | oid(4) | ownerOID(4) | encoding(4) | provider(1) |
// deterministicFlag(1) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
// schema(schemaLen bytes) | collateLen(2) | collate(collateLen bytes) |
// ctypeLen(2) | ctype(ctypeLen bytes) | localeLen(2) | locale(localeLen bytes) |
// rulesLen(2) | rules(rulesLen bytes).
func EncodeCreateCollation(name, schema, collate, ctype, locale, rules string, oid, ownerOID uint32, encoding int32, provider byte, deterministic bool) []byte {
	strs := []string{name, schema, collate, ctype, locale, rules}
	total := 0
	for i, s := range strs {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
			strs[i] = s
		}
		total += 2 + len(s)
	}
	// 15-byte fixed header + 6 length-prefixed strings (2-byte length each).
	out := make([]byte, 15+total)
	out[0] = RecordKindCreateCollation
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], ownerOID)
	binary.LittleEndian.PutUint32(out[9:13], uint32(encoding))
	out[13] = provider
	if deterministic {
		out[14] = 1
	}
	off := 15
	for _, s := range strs {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(s)))
		off += 2
		copy(out[off:], s)
		off += len(s)
	}
	return out
}

// DecodeCreateCollation decodes a RecordKindCreateCollation payload.
func DecodeCreateCollation(payload []byte) (name, schema, collate, ctype, locale, rules string, oid, ownerOID uint32, encoding int32, provider byte, deterministic bool, err error) {
	if len(payload) < 15 {
		return "", "", "", "", "", "", 0, 0, 0, 0, false, fmt.Errorf("wal: create-collation payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateCollation {
		return "", "", "", "", "", "", 0, 0, 0, 0, false, fmt.Errorf("wal: record kind %d is not create-collation", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	ownerOID = binary.LittleEndian.Uint32(payload[5:9])
	encoding = int32(binary.LittleEndian.Uint32(payload[9:13]))
	provider = payload[13]
	deterministic = payload[14] != 0
	off := 15
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-collation payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-collation payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", "", "", "", 0, 0, 0, 0, false, err
	}
	if schema, err = readStr(); err != nil {
		return "", "", "", "", "", "", 0, 0, 0, 0, false, err
	}
	if collate, err = readStr(); err != nil {
		return "", "", "", "", "", "", 0, 0, 0, 0, false, err
	}
	if ctype, err = readStr(); err != nil {
		return "", "", "", "", "", "", 0, 0, 0, 0, false, err
	}
	if locale, err = readStr(); err != nil {
		return "", "", "", "", "", "", 0, 0, 0, 0, false, err
	}
	if rules, err = readStr(); err != nil {
		return "", "", "", "", "", "", 0, 0, 0, 0, false, err
	}
	return name, schema, collate, ctype, locale, rules, oid, ownerOID, encoding, provider, deterministic, nil
}

// EncodeDropCollation encodes a DROP COLLATION event (DU-002
// restart-persistence follow-up to M0119-0004). Format: kind(1) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes).
func EncodeDropCollation(name, schema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(schema))
	out[0] = RecordKindDropCollation
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	return out
}

// DecodeDropCollation decodes a RecordKindDropCollation payload.
func DecodeDropCollation(payload []byte) (name, schema string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: drop-collation payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropCollation {
		return "", "", fmt.Errorf("wal: record kind %d is not drop-collation", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: drop-collation payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen {
		return "", "", fmt.Errorf("wal: drop-collation payload truncated (need %d bytes)", off+schemaLen)
	}
	schema = string(payload[off : off+schemaLen])
	return name, schema, nil
}

// EncodeAlterCollationRename encodes an ALTER COLLATION ... RENAME TO event
// (DU-002 restart-persistence follow-up to M0119-0004). Format: kind(1) |
// nameLen(2) | name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
// newNameLen(2) | newName(newNameLen bytes).
func EncodeAlterCollationRename(name, schema, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(newName))
	out[0] = RecordKindAlterCollationRename
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeAlterCollationRename decodes a RecordKindAlterCollationRename payload.
func DecodeAlterCollationRename(payload []byte) (name, schema, newName string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: alter-collation-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterCollationRename {
		return "", "", "", fmt.Errorf("wal: record kind %d is not alter-collation-rename", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-collation-rename payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-collation-rename payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", "", fmt.Errorf("wal: alter-collation-rename payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, schema, newName, nil
}

// EncodeAlterCollationSetSchema encodes an ALTER COLLATION ... SET SCHEMA
// event (DU-002 slice 442). Format: kind(1) | nameLen(2) | name(nameLen
// bytes) | schemaLen(2) | schema(schemaLen bytes) | newSchemaLen(2) |
// newSchema(newSchemaLen bytes).
func EncodeAlterCollationSetSchema(name, schema, newSchema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(newSchema) > 0xFFFF {
		newSchema = newSchema[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(newSchema))
	out[0] = RecordKindAlterCollationSetSchema
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newSchema)))
	off += 2
	copy(out[off:], newSchema)
	return out
}

// DecodeAlterCollationSetSchema decodes a RecordKindAlterCollationSetSchema
// payload.
func DecodeAlterCollationSetSchema(payload []byte) (name, schema, newSchema string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: alter-collation-set-schema payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterCollationSetSchema {
		return "", "", "", fmt.Errorf("wal: record kind %d is not alter-collation-set-schema", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-collation-set-schema payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-collation-set-schema payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	newSchemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newSchemaLen {
		return "", "", "", fmt.Errorf("wal: alter-collation-set-schema payload truncated (need %d bytes)", off+newSchemaLen)
	}
	newSchema = string(payload[off : off+newSchemaLen])
	return name, schema, newSchema, nil
}

// EncodeRenameIndex encodes an `ALTER INDEX name RENAME TO newname` event
// (DU-002 slice 443). Format: kind(1) | schemaLen(2) | schema(schemaLen
// bytes) | oldNameLen(2) | oldName(oldNameLen bytes) | newNameLen(2) |
// newName(newNameLen bytes).
func EncodeRenameIndex(schema, oldName, newName string) []byte {
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(oldName) > 0xFFFF {
		oldName = oldName[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 7+len(schema)+len(oldName)+len(newName))
	out[0] = RecordKindRenameIndex
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(schema)))
	off := 3
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(oldName)))
	off += 2
	copy(out[off:], oldName)
	off += len(oldName)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeRenameIndex decodes a RecordKindRenameIndex payload.
func DecodeRenameIndex(payload []byte) (schema, oldName, newName string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: rename-index payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindRenameIndex {
		return "", "", "", fmt.Errorf("wal: record kind %d is not rename-index", payload[0])
	}
	schemaLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: rename-index payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	oldNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+oldNameLen+2 {
		return "", "", "", fmt.Errorf("wal: rename-index payload truncated (need %d bytes)", off+oldNameLen+2)
	}
	oldName = string(payload[off : off+oldNameLen])
	off += oldNameLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", "", fmt.Errorf("wal: rename-index payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return schema, oldName, newName, nil
}

// EncodeAlterCollationOwner encodes an ALTER COLLATION ... OWNER TO event
// (DU-002 restart-persistence follow-up to M0119-0004). Format: kind(1) |
// ownerOID(4) | nameLen(2) | name(nameLen bytes) | schemaLen(2) |
// schema(schemaLen bytes).
func EncodeAlterCollationOwner(name, schema string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	out := make([]byte, 9+len(name)+len(schema))
	out[0] = RecordKindAlterCollationOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	off := 5
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	return out
}

// DecodeAlterCollationOwner decodes a RecordKindAlterCollationOwner payload.
func DecodeAlterCollationOwner(payload []byte) (name, schema string, ownerOID uint32, err error) {
	if len(payload) < 9 {
		return "", "", 0, fmt.Errorf("wal: alter-collation-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterCollationOwner {
		return "", "", 0, fmt.Errorf("wal: record kind %d is not alter-collation-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	off := 5
	nameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+nameLen+2 {
		return "", "", 0, fmt.Errorf("wal: alter-collation-owner payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen {
		return "", "", 0, fmt.Errorf("wal: alter-collation-owner payload truncated (need %d bytes)", off+schemaLen)
	}
	schema = string(payload[off : off+schemaLen])
	return name, schema, ownerOID, nil
}

// EncodeAlterConversionRename encodes an ALTER CONVERSION ... RENAME TO
// event, mirroring EncodeAlterCollationRename. M0122-0007 4e follow-up
// (DU-002 round-trip probe unblock). Format: kind(1) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
// newNameLen(2) | newName(newNameLen bytes).
func EncodeAlterConversionRename(name, schema, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(newName))
	out[0] = RecordKindAlterConversionRename
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeAlterConversionRename decodes a RecordKindAlterConversionRename
// payload.
func DecodeAlterConversionRename(payload []byte) (name, schema, newName string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: alter-conversion-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterConversionRename {
		return "", "", "", fmt.Errorf("wal: record kind %d is not alter-conversion-rename", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-conversion-rename payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-conversion-rename payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", "", fmt.Errorf("wal: alter-conversion-rename payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, schema, newName, nil
}

// EncodeAlterConversionSetSchema encodes an ALTER CONVERSION ... SET SCHEMA
// event, mirroring EncodeAlterCollationSetSchema. M0122-0007 4e follow-up
// (DU-002 round-trip probe unblock). Format: kind(1) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes) |
// newSchemaLen(2) | newSchema(newSchemaLen bytes).
func EncodeAlterConversionSetSchema(name, schema, newSchema string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	if len(newSchema) > 0xFFFF {
		newSchema = newSchema[:0xFFFF]
	}
	out := make([]byte, 7+len(name)+len(schema)+len(newSchema))
	out[0] = RecordKindAlterConversionSetSchema
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	off += len(schema)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newSchema)))
	off += 2
	copy(out[off:], newSchema)
	return out
}

// DecodeAlterConversionSetSchema decodes a RecordKindAlterConversionSetSchema
// payload.
func DecodeAlterConversionSetSchema(payload []byte) (name, schema, newSchema string, err error) {
	if len(payload) < 7 {
		return "", "", "", fmt.Errorf("wal: alter-conversion-set-schema payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterConversionSetSchema {
		return "", "", "", fmt.Errorf("wal: record kind %d is not alter-conversion-set-schema", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-conversion-set-schema payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen+2 {
		return "", "", "", fmt.Errorf("wal: alter-conversion-set-schema payload truncated (need %d bytes)", off+schemaLen+2)
	}
	schema = string(payload[off : off+schemaLen])
	off += schemaLen
	newSchemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newSchemaLen {
		return "", "", "", fmt.Errorf("wal: alter-conversion-set-schema payload truncated (need %d bytes)", off+newSchemaLen)
	}
	newSchema = string(payload[off : off+newSchemaLen])
	return name, schema, newSchema, nil
}

// EncodeAlterConversionOwner encodes an ALTER CONVERSION ... OWNER TO event,
// mirroring EncodeAlterCollationOwner. M0122-0007 4e follow-up (DU-002
// round-trip probe unblock). Format: kind(1) | ownerOID(4) | nameLen(2) |
// name(nameLen bytes) | schemaLen(2) | schema(schemaLen bytes).
func EncodeAlterConversionOwner(name, schema string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(schema) > 0xFFFF {
		schema = schema[:0xFFFF]
	}
	out := make([]byte, 9+len(name)+len(schema))
	out[0] = RecordKindAlterConversionOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	off := 5
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(schema)))
	off += 2
	copy(out[off:], schema)
	return out
}

// DecodeAlterConversionOwner decodes a RecordKindAlterConversionOwner
// payload.
func DecodeAlterConversionOwner(payload []byte) (name, schema string, ownerOID uint32, err error) {
	if len(payload) < 9 {
		return "", "", 0, fmt.Errorf("wal: alter-conversion-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterConversionOwner {
		return "", "", 0, fmt.Errorf("wal: record kind %d is not alter-conversion-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	off := 5
	nameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+nameLen+2 {
		return "", "", 0, fmt.Errorf("wal: alter-conversion-owner payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	schemaLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+schemaLen {
		return "", "", 0, fmt.Errorf("wal: alter-conversion-owner payload truncated (need %d bytes)", off+schemaLen)
	}
	schema = string(payload[off : off+schemaLen])
	return name, schema, ownerOID, nil
}

// EncodeCreateAggregate encodes a CREATE AGGREGATE event (DU-002
// restart-persistence follow-up to M0119-0004, slice 405 resume point (c)).
// The OID is carried so recovery re-registers the aggregate identically to
// the live server. `schema` carries the aggregate's namespace name (slice
// 405 resume point (a)) so recovery can re-resolve pronamespace the same
// way CreateCollation's WAL record does. Format documented at the
// RecordKindCreateAggregate constant.
func EncodeCreateAggregate(name, schema, sType, sFunc, finalFunc, combineFunc, initCond, finalFuncModify string, argTypes []string, oid uint32, sFuncStrict, variadic bool) []byte {
	strs := []string{name, schema, sType, sFunc, finalFunc, combineFunc, initCond, finalFuncModify}
	total := 0
	for i, s := range strs {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
			strs[i] = s
		}
		total += 2 + len(s)
	}
	total += 2
	for _, a := range argTypes {
		if len(a) > 0xFFFF {
			a = a[:0xFFFF]
		}
		total += 2 + len(a)
	}
	// 7-byte fixed header (kind + oid + 2 flag bytes) + 7 length-prefixed
	// strings (2-byte length each) + arg-type count + arg-type strings.
	out := make([]byte, 7+total)
	out[0] = RecordKindCreateAggregate
	binary.LittleEndian.PutUint32(out[1:5], oid)
	if sFuncStrict {
		out[5] = 1
	}
	if variadic {
		out[6] = 1
	}
	off := 7
	for _, s := range strs {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(s)))
		off += 2
		copy(out[off:], s)
		off += len(s)
	}
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(argTypes)))
	off += 2
	for _, a := range argTypes {
		if len(a) > 0xFFFF {
			a = a[:0xFFFF]
		}
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(a)))
		off += 2
		copy(out[off:], a)
		off += len(a)
	}
	return out
}

// DecodeCreateAggregate decodes a RecordKindCreateAggregate payload.
func DecodeCreateAggregate(payload []byte) (name, schema, sType, sFunc, finalFunc, combineFunc, initCond, finalFuncModify string, argTypes []string, oid uint32, sFuncStrict, variadic bool, err error) {
	if len(payload) < 7 {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, fmt.Errorf("wal: create-aggregate payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateAggregate {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, fmt.Errorf("wal: record kind %d is not create-aggregate", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	sFuncStrict = payload[5] != 0
	variadic = payload[6] != 0
	off := 7
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-aggregate payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-aggregate payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, err
	}
	if schema, err = readStr(); err != nil {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, err
	}
	if sType, err = readStr(); err != nil {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, err
	}
	if sFunc, err = readStr(); err != nil {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, err
	}
	if finalFunc, err = readStr(); err != nil {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, err
	}
	if combineFunc, err = readStr(); err != nil {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, err
	}
	if initCond, err = readStr(); err != nil {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, err
	}
	if finalFuncModify, err = readStr(); err != nil {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, err
	}
	if len(payload) < off+2 {
		return "", "", "", "", "", "", "", "", nil, 0, false, false, fmt.Errorf("wal: create-aggregate payload truncated (need %d bytes)", off+2)
	}
	argCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	argTypes = make([]string, 0, argCount)
	for i := 0; i < argCount; i++ {
		a, aerr := readStr()
		if aerr != nil {
			return "", "", "", "", "", "", "", "", nil, 0, false, false, aerr
		}
		argTypes = append(argTypes, a)
	}
	return name, schema, sType, sFunc, finalFunc, combineFunc, initCond, finalFuncModify, argTypes, oid, sFuncStrict, variadic, nil
}

// EncodeAlterAggregateRename encodes an ALTER AGGREGATE ... RENAME TO event
// (DU-002 restart-persistence follow-up to M0119-0004, slice 405 resume
// point (c)). Format: kind(1) | nameLen(2) | name(nameLen bytes) |
// newNameLen(2) | newName(newNameLen bytes).
func EncodeAlterAggregateRename(name, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(newName))
	out[0] = RecordKindAlterAggregateRename
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	off := 3
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeAlterAggregateRename decodes a RecordKindAlterAggregateRename payload.
func DecodeAlterAggregateRename(payload []byte) (name, newName string, err error) {
	if len(payload) < 5 {
		return "", "", fmt.Errorf("wal: alter-aggregate-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterAggregateRename {
		return "", "", fmt.Errorf("wal: record kind %d is not alter-aggregate-rename", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: alter-aggregate-rename payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", fmt.Errorf("wal: alter-aggregate-rename payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, newName, nil
}

// EncodeDropAggregate encodes a DROP AGGREGATE event (DU-002
// restart-persistence follow-up to M0119-0004, loop #56 ledger resume
// point). Format: kind(1) | nameLen(2) | name(nameLen bytes).
func EncodeDropAggregate(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropAggregate
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropAggregate decodes a RecordKindDropAggregate payload.
func DecodeDropAggregate(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-aggregate payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropAggregate {
		return "", fmt.Errorf("wal: record kind %d is not drop-aggregate", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-aggregate payload truncated (need %d bytes)", 3+nameLen)
	}
	name = string(payload[3 : 3+nameLen])
	return name, nil
}

// EncodeAlterAggregateOwner encodes an ALTER AGGREGATE ... OWNER TO event
// (M0119-0004, loop #57 ledger follow-up). Unlike
// EncodeAlterCollationOwner, aggregates have no Schema field yet (slice 405
// ledger resume point (a)), so this format omits the schema component.
// Format: kind(1) | ownerOID(4) | nameLen(2) | name(nameLen bytes).
func EncodeAlterAggregateOwner(name string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 7+len(name))
	out[0] = RecordKindAlterAggregateOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(name)))
	copy(out[7:], name)
	return out
}

// DecodeAlterAggregateOwner decodes a RecordKindAlterAggregateOwner payload.
func DecodeAlterAggregateOwner(payload []byte) (name string, ownerOID uint32, err error) {
	if len(payload) < 7 {
		return "", 0, fmt.Errorf("wal: alter-aggregate-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterAggregateOwner {
		return "", 0, fmt.Errorf("wal: record kind %d is not alter-aggregate-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return "", 0, fmt.Errorf("wal: alter-aggregate-owner payload truncated (need %d bytes)", 7+nameLen)
	}
	name = string(payload[7 : 7+nameLen])
	return name, ownerOID, nil
}

// publicationFlags packs the four Publication boolean fields into a single
// byte for EncodeCreatePublication (bit0=AllTables, bit1=PublishInsert,
// bit2=PublishUpdate, bit3=PublishDelete).
func publicationFlags(allTables, publishInsert, publishUpdate, publishDelete bool) byte {
	var f byte
	if allTables {
		f |= 1 << 0
	}
	if publishInsert {
		f |= 1 << 1
	}
	if publishUpdate {
		f |= 1 << 2
	}
	if publishDelete {
		f |= 1 << 3
	}
	return f
}

// EncodeCreatePublication encodes a CREATE PUBLICATION event (DU-002
// restart-persistence follow-up to M0119-0004, loop #67 ledger resume
// point). The OID is carried so recovery re-registers the publication
// identically to the live server. Format documented at the
// RecordKindCreatePublication constant.
func EncodeCreatePublication(name string, tables []string, oid, ownerOID uint32, allTables, publishInsert, publishUpdate, publishDelete bool) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	total := 10 + 2 + len(name) + 2
	for _, t := range tables {
		if len(t) > 0xFFFF {
			t = t[:0xFFFF]
		}
		total += 2 + len(t)
	}
	out := make([]byte, total)
	out[0] = RecordKindCreatePublication
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], ownerOID)
	out[9] = publicationFlags(allTables, publishInsert, publishUpdate, publishDelete)
	off := 10
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(tables)))
	off += 2
	for _, t := range tables {
		if len(t) > 0xFFFF {
			t = t[:0xFFFF]
		}
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(t)))
		off += 2
		copy(out[off:], t)
		off += len(t)
	}
	return out
}

// DecodeCreatePublication decodes a RecordKindCreatePublication payload.
func DecodeCreatePublication(payload []byte) (name string, tables []string, oid, ownerOID uint32, allTables, publishInsert, publishUpdate, publishDelete bool, err error) {
	if len(payload) < 10 {
		return "", nil, 0, 0, false, false, false, false, fmt.Errorf("wal: create-publication payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreatePublication {
		return "", nil, 0, 0, false, false, false, false, fmt.Errorf("wal: record kind %d is not create-publication", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	ownerOID = binary.LittleEndian.Uint32(payload[5:9])
	flags := payload[9]
	allTables = flags&(1<<0) != 0
	publishInsert = flags&(1<<1) != 0
	publishUpdate = flags&(1<<2) != 0
	publishDelete = flags&(1<<3) != 0
	off := 10
	if len(payload) < off+2 {
		return "", nil, 0, 0, false, false, false, false, fmt.Errorf("wal: create-publication payload truncated (need %d bytes)", off+2)
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+nameLen+2 {
		return "", nil, 0, 0, false, false, false, false, fmt.Errorf("wal: create-publication payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	tablesCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	tables = make([]string, 0, tablesCount)
	for i := 0; i < tablesCount; i++ {
		if len(payload) < off+2 {
			return "", nil, 0, 0, false, false, false, false, fmt.Errorf("wal: create-publication payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", nil, 0, 0, false, false, false, false, fmt.Errorf("wal: create-publication payload truncated (need %d bytes)", off+l)
		}
		tables = append(tables, string(payload[off:off+l]))
		off += l
	}
	return name, tables, oid, ownerOID, allTables, publishInsert, publishUpdate, publishDelete, nil
}

// EncodeDropPublication encodes a DROP PUBLICATION event (DU-002
// restart-persistence follow-up to M0119-0004, loop #67 ledger resume
// point). Format documented at the RecordKindDropPublication constant.
func EncodeDropPublication(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropPublication
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropPublication decodes a RecordKindDropPublication payload.
func DecodeDropPublication(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-publication payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropPublication {
		return "", fmt.Errorf("wal: record kind %d is not drop-publication", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-publication payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeAlterPublicationOwner encodes an ALTER PUBLICATION ... OWNER TO
// event (DU-002 restart-persistence follow-up to M0119-0004, loop #67
// ledger resume point). Format documented at the
// RecordKindAlterPublicationOwner constant.
func EncodeAlterPublicationOwner(name string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 7+len(name))
	out[0] = RecordKindAlterPublicationOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(name)))
	copy(out[7:], name)
	return out
}

// DecodeAlterPublicationOwner decodes a RecordKindAlterPublicationOwner
// payload.
func DecodeAlterPublicationOwner(payload []byte) (name string, ownerOID uint32, err error) {
	if len(payload) < 7 {
		return "", 0, fmt.Errorf("wal: alter-publication-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterPublicationOwner {
		return "", 0, fmt.Errorf("wal: record kind %d is not alter-publication-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return "", 0, fmt.Errorf("wal: alter-publication-owner payload truncated (need %d bytes)", 7+nameLen)
	}
	return string(payload[7 : 7+nameLen]), ownerOID, nil
}

// EncodeCreateSubscription encodes a CREATE SUBSCRIPTION event (DU-002
// restart-persistence follow-up to M0119-0004, loop #67 ledger resume
// point). The OID is carried so recovery re-registers the subscription
// identically to the live server. Format documented at the
// RecordKindCreateSubscription constant. dbOid is appended as a trailing
// 4-byte field (M0122-0007 4d-ii-part-2b item 1) so a pre-existing WAL
// file with no trailer still decodes — DecodeCreateSubscription treats a
// short read of just that trailer as dbOid 0 (DefaultDBOid via
// NamespaceDBOid), matching the pre-item-1 single-database default.
func EncodeCreateSubscription(name, conninfo, slotName string, publications []string, oid, ownerOID uint32, enabled bool, dbOid uint32) []byte {
	strs := []string{name, conninfo, slotName}
	for i, s := range strs {
		if len(s) > 0xFFFF {
			strs[i] = s[:0xFFFF]
		}
	}
	name, conninfo, slotName = strs[0], strs[1], strs[2]
	total := 10 + 2 + len(name) + 2 + len(conninfo) + 2 + len(slotName) + 2
	for _, p := range publications {
		if len(p) > 0xFFFF {
			p = p[:0xFFFF]
		}
		total += 2 + len(p)
	}
	total += 4
	out := make([]byte, total)
	out[0] = RecordKindCreateSubscription
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], ownerOID)
	if enabled {
		out[9] = 1
	}
	off := 10
	writeStr := func(s string) {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(s)))
		off += 2
		copy(out[off:], s)
		off += len(s)
	}
	writeStr(name)
	writeStr(conninfo)
	writeStr(slotName)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(publications)))
	off += 2
	for _, p := range publications {
		if len(p) > 0xFFFF {
			p = p[:0xFFFF]
		}
		writeStr(p)
	}
	binary.LittleEndian.PutUint32(out[off:off+4], dbOid)
	off += 4
	return out
}

// DecodeCreateSubscription decodes a RecordKindCreateSubscription payload.
// dbOid is 0 (DefaultDBOid via NamespaceDBOid) for a pre-item-1 payload
// that predates the trailing dbOid field.
func DecodeCreateSubscription(payload []byte) (name, conninfo, slotName string, publications []string, oid, ownerOID uint32, enabled bool, dbOid uint32, err error) {
	if len(payload) < 10 {
		return "", "", "", nil, 0, 0, false, 0, fmt.Errorf("wal: create-subscription payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateSubscription {
		return "", "", "", nil, 0, 0, false, 0, fmt.Errorf("wal: record kind %d is not create-subscription", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	ownerOID = binary.LittleEndian.Uint32(payload[5:9])
	enabled = payload[9] != 0
	off := 10
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-subscription payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-subscription payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if name, err = readStr(); err != nil {
		return "", "", "", nil, 0, 0, false, 0, err
	}
	if conninfo, err = readStr(); err != nil {
		return "", "", "", nil, 0, 0, false, 0, err
	}
	if slotName, err = readStr(); err != nil {
		return "", "", "", nil, 0, 0, false, 0, err
	}
	if len(payload) < off+2 {
		return "", "", "", nil, 0, 0, false, 0, fmt.Errorf("wal: create-subscription payload truncated (need %d bytes)", off+2)
	}
	pubCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	publications = make([]string, 0, pubCount)
	for i := 0; i < pubCount; i++ {
		s, serr := readStr()
		if serr != nil {
			return "", "", "", nil, 0, 0, false, 0, serr
		}
		publications = append(publications, s)
	}
	if len(payload) >= off+4 {
		dbOid = binary.LittleEndian.Uint32(payload[off : off+4])
		off += 4
	}
	return name, conninfo, slotName, publications, oid, ownerOID, enabled, dbOid, nil
}

// EncodeDropSubscription encodes a DROP SUBSCRIPTION event (DU-002
// restart-persistence follow-up to M0119-0004, loop #67 ledger resume
// point). Format documented at the RecordKindDropSubscription constant.
func EncodeDropSubscription(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropSubscription
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropSubscription decodes a RecordKindDropSubscription payload.
func DecodeDropSubscription(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-subscription payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropSubscription {
		return "", fmt.Errorf("wal: record kind %d is not drop-subscription", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-subscription payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeAlterSubscriptionOwner encodes an ALTER SUBSCRIPTION ... OWNER TO
// event (DU-002 restart-persistence follow-up to M0119-0004, loop #67
// ledger resume point). Format documented at the
// RecordKindAlterSubscriptionOwner constant.
func EncodeAlterSubscriptionOwner(name string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 7+len(name))
	out[0] = RecordKindAlterSubscriptionOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(name)))
	copy(out[7:], name)
	return out
}

// DecodeAlterSubscriptionOwner decodes a RecordKindAlterSubscriptionOwner
// payload.
func DecodeAlterSubscriptionOwner(payload []byte) (name string, ownerOID uint32, err error) {
	if len(payload) < 7 {
		return "", 0, fmt.Errorf("wal: alter-subscription-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterSubscriptionOwner {
		return "", 0, fmt.Errorf("wal: record kind %d is not alter-subscription-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return "", 0, fmt.Errorf("wal: alter-subscription-owner payload truncated (need %d bytes)", 7+nameLen)
	}
	return string(payload[7 : 7+nameLen]), ownerOID, nil
}

// EncodeCreateEventTrigger encodes a CREATE EVENT TRIGGER event (DU-002
// restart-persistence follow-up to M0119-0004, loop #70 ledger resume
// point). The OID is carried so recovery re-registers the event trigger
// identically to the live server. Format documented at the
// RecordKindCreateEventTrigger constant.
func EncodeCreateEventTrigger(name, event string, tags []string, oid, ownerOID, funcOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(event) > 0xFFFF {
		event = event[:0xFFFF]
	}
	total := 14 + 2 + len(event) + 2 + len(name) + 2
	for _, t := range tags {
		if len(t) > 0xFFFF {
			t = t[:0xFFFF]
		}
		total += 2 + len(t)
	}
	out := make([]byte, total)
	out[0] = RecordKindCreateEventTrigger
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint32(out[5:9], ownerOID)
	binary.LittleEndian.PutUint32(out[9:13], funcOID)
	off := 13
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(event)))
	off += 2
	copy(out[off:], event)
	off += len(event)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(name)))
	off += 2
	copy(out[off:], name)
	off += len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(tags)))
	off += 2
	for _, t := range tags {
		if len(t) > 0xFFFF {
			t = t[:0xFFFF]
		}
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(t)))
		off += 2
		copy(out[off:], t)
		off += len(t)
	}
	return out
}

// DecodeCreateEventTrigger decodes a RecordKindCreateEventTrigger payload.
func DecodeCreateEventTrigger(payload []byte) (name, event string, tags []string, oid, ownerOID, funcOID uint32, err error) {
	if len(payload) < 13 {
		return "", "", nil, 0, 0, 0, fmt.Errorf("wal: create-event-trigger payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateEventTrigger {
		return "", "", nil, 0, 0, 0, fmt.Errorf("wal: record kind %d is not create-event-trigger", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	ownerOID = binary.LittleEndian.Uint32(payload[5:9])
	funcOID = binary.LittleEndian.Uint32(payload[9:13])
	off := 13
	readStr := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-event-trigger payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-event-trigger payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	if event, err = readStr(); err != nil {
		return "", "", nil, 0, 0, 0, err
	}
	if name, err = readStr(); err != nil {
		return "", "", nil, 0, 0, 0, err
	}
	if len(payload) < off+2 {
		return "", "", nil, 0, 0, 0, fmt.Errorf("wal: create-event-trigger payload truncated (need %d bytes)", off+2)
	}
	tagsCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	tags = make([]string, 0, tagsCount)
	for i := 0; i < tagsCount; i++ {
		s, serr := readStr()
		if serr != nil {
			return "", "", nil, 0, 0, 0, serr
		}
		tags = append(tags, s)
	}
	return name, event, tags, oid, ownerOID, funcOID, nil
}

// EncodeDropEventTrigger encodes a DROP EVENT TRIGGER event (DU-002
// restart-persistence follow-up to M0119-0004, loop #70 ledger resume
// point). Format documented at the RecordKindDropEventTrigger constant.
func EncodeDropEventTrigger(name string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 3+len(name))
	out[0] = RecordKindDropEventTrigger
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	return out
}

// DecodeDropEventTrigger decodes a RecordKindDropEventTrigger payload.
func DecodeDropEventTrigger(payload []byte) (name string, err error) {
	if len(payload) < 3 {
		return "", fmt.Errorf("wal: drop-event-trigger payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropEventTrigger {
		return "", fmt.Errorf("wal: record kind %d is not drop-event-trigger", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if len(payload) < 3+nameLen {
		return "", fmt.Errorf("wal: drop-event-trigger payload truncated (need %d bytes)", 3+nameLen)
	}
	return string(payload[3 : 3+nameLen]), nil
}

// EncodeAlterEventTriggerEnabled encodes an ALTER EVENT TRIGGER name
// {DISABLE|ENABLE [REPLICA|ALWAYS]} event (DU-002 restart-persistence
// follow-up to M0119-0004, loop #70 ledger resume point). code is the raw
// pg_event_trigger.evtenabled value ('D'/'O'/'R'/'A'). Format documented at
// the RecordKindAlterEventTriggerEnabled constant.
func EncodeAlterEventTriggerEnabled(name string, code byte) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 4+len(name))
	out[0] = RecordKindAlterEventTriggerEnabled
	out[1] = code
	binary.LittleEndian.PutUint16(out[2:4], uint16(len(name)))
	copy(out[4:], name)
	return out
}

// DecodeAlterEventTriggerEnabled decodes a
// RecordKindAlterEventTriggerEnabled payload.
func DecodeAlterEventTriggerEnabled(payload []byte) (name string, code byte, err error) {
	if len(payload) < 4 {
		return "", 0, fmt.Errorf("wal: alter-event-trigger-enabled payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterEventTriggerEnabled {
		return "", 0, fmt.Errorf("wal: record kind %d is not alter-event-trigger-enabled", payload[0])
	}
	code = payload[1]
	nameLen := int(binary.LittleEndian.Uint16(payload[2:4]))
	if len(payload) < 4+nameLen {
		return "", 0, fmt.Errorf("wal: alter-event-trigger-enabled payload truncated (need %d bytes)", 4+nameLen)
	}
	return string(payload[4 : 4+nameLen]), code, nil
}

// EncodeAlterEventTriggerRename encodes an ALTER EVENT TRIGGER name RENAME
// TO newname event (DU-002 restart-persistence follow-up to M0119-0004,
// loop #70 ledger resume point). Format documented at the
// RecordKindAlterEventTriggerRename constant.
func EncodeAlterEventTriggerRename(name, newName string) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 5+len(name)+len(newName))
	out[0] = RecordKindAlterEventTriggerRename
	binary.LittleEndian.PutUint16(out[1:3], uint16(len(name)))
	copy(out[3:], name)
	off := 3 + len(name)
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(newName)))
	off += 2
	copy(out[off:], newName)
	return out
}

// DecodeAlterEventTriggerRename decodes a RecordKindAlterEventTriggerRename
// payload.
func DecodeAlterEventTriggerRename(payload []byte) (name, newName string, err error) {
	if len(payload) < 3 {
		return "", "", fmt.Errorf("wal: alter-event-trigger-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterEventTriggerRename {
		return "", "", fmt.Errorf("wal: record kind %d is not alter-event-trigger-rename", payload[0])
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	off := 3
	if len(payload) < off+nameLen+2 {
		return "", "", fmt.Errorf("wal: alter-event-trigger-rename payload truncated (need %d bytes)", off+nameLen+2)
	}
	name = string(payload[off : off+nameLen])
	off += nameLen
	newNameLen := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	if len(payload) < off+newNameLen {
		return "", "", fmt.Errorf("wal: alter-event-trigger-rename payload truncated (need %d bytes)", off+newNameLen)
	}
	newName = string(payload[off : off+newNameLen])
	return name, newName, nil
}

// EncodeAlterEventTriggerOwner encodes an ALTER EVENT TRIGGER name OWNER TO
// event (DU-002 restart-persistence follow-up to M0119-0004, loop #70
// ledger resume point). Format documented at the
// RecordKindAlterEventTriggerOwner constant.
func EncodeAlterEventTriggerOwner(name string, ownerOID uint32) []byte {
	if len(name) > 0xFFFF {
		name = name[:0xFFFF]
	}
	out := make([]byte, 7+len(name))
	out[0] = RecordKindAlterEventTriggerOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(name)))
	copy(out[7:], name)
	return out
}

// DecodeAlterEventTriggerOwner decodes a RecordKindAlterEventTriggerOwner
// payload.
func DecodeAlterEventTriggerOwner(payload []byte) (name string, ownerOID uint32, err error) {
	if len(payload) < 7 {
		return "", 0, fmt.Errorf("wal: alter-event-trigger-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterEventTriggerOwner {
		return "", 0, fmt.Errorf("wal: record kind %d is not alter-event-trigger-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return "", 0, fmt.Errorf("wal: alter-event-trigger-owner payload truncated (need %d bytes)", 7+nameLen)
	}
	return string(payload[7 : 7+nameLen]), ownerOID, nil
}

// FunctionArgPayload is one CREATE FUNCTION/PROCEDURE argument, mirroring
// the parallel ArgNames/ArgTypes/ArgModes/ArgDefaults slices on
// catalog.Routine. TypeArgs carries a type's numeric modifiers (e.g.
// numeric(10,2)'s precision/scale) — these do not affect overload
// resolution (PG's proargtypes is an OID list, not typmods) but do affect
// pg_dump fidelity, so they are round-tripped like everything else.
type FunctionArgPayload struct {
	Name     string
	TypeName string
	TypeArgs []int64
	Mode     string // "i"/"o"/"b"/"v", "" defaults to "i"
	Default  string
}

// CreateFunctionPayload carries the metadata needed to fully reconstruct a
// catalog.Routine during WAL replay. Dependency-tracking fields
// (SequenceDeps/RoutineCallOIDs/TableDeps/ColumnDeps) are deliberately NOT
// carried — the recovery driver recomputes them from Body/ArgDefaults via
// executor.ExtractRoutineDeps after registering, the same way the live
// CREATE FUNCTION path derives them, rather than serializing derived state.
type CreateFunctionPayload struct {
	OID             uint32
	Schema          string
	Name            string
	Args            []FunctionArgPayload
	ReturnTypeName  string
	ReturnTypeArgs  []int64
	ReturnsSet      bool
	ReturnsTable    bool
	Language        string
	Body            string
	Strict          bool
	Volatile        string
	Parallel        string
	Cost            string
	Rows            string
	SecurityDefiner bool
	Leakproof       bool
	IsProcedure     bool
	IsWindow        bool
	BeginAtomic     bool
	IsReturnForm    bool
	KindChar        string
	// Config is pg_proc.proconfig ("name=value" entries set via CREATE
	// FUNCTION's SET clause). Encoded as an optional trailing extension
	// block, omitted entirely when empty for byte-identical output vs.
	// pre-existing records with no SET clause (same pattern as
	// CreateIndexPayload's predicate/INCLUDE-column extension block).
	// DU-002 proconfig follow-up to M0097-0150.
	Config []string
}

// EncodeCreateFunction encodes a CREATE [OR REPLACE] FUNCTION/PROCEDURE
// event (DU-002 restart-persistence follow-up to M0119-0004, loop #71
// ledger resume point). Format documented at the RecordKindCreateFunction
// constant; uses 2-byte length-prefixed strings throughout except Body,
// which gets a 4-byte prefix since a plpgsql routine body can plausibly
// exceed 65535 bytes (unlike every other DU-002 WAL-persisted DDL family's
// string fields).
func EncodeCreateFunction(p CreateFunctionPayload) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindCreateFunction)
	var oidBuf [4]byte
	binary.LittleEndian.PutUint32(oidBuf[:], p.OID)
	buf.Write(oidBuf[:])
	var flags, flags2 byte
	if p.ReturnsSet {
		flags |= 1 << 0
	}
	if p.ReturnsTable {
		flags |= 1 << 1
	}
	if p.Strict {
		flags |= 1 << 2
	}
	if p.SecurityDefiner {
		flags |= 1 << 3
	}
	if p.Leakproof {
		flags |= 1 << 4
	}
	if p.IsProcedure {
		flags |= 1 << 5
	}
	if p.IsWindow {
		flags |= 1 << 6
	}
	if p.BeginAtomic {
		flags |= 1 << 7
	}
	if p.IsReturnForm {
		flags2 |= 1 << 0
	}
	buf.WriteByte(flags)
	buf.WriteByte(flags2)
	writeWALStr16 := func(s string) {
		if len(s) > 0xFFFF {
			s = s[:0xFFFF]
		}
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(s)))
		buf.Write(l[:])
		buf.WriteString(s)
	}
	writeWALStr32 := func(s string) {
		if uint64(len(s)) > 0xFFFFFFFF {
			s = s[:0xFFFFFFFF]
		}
		var l [4]byte
		binary.LittleEndian.PutUint32(l[:], uint32(len(s)))
		buf.Write(l[:])
		buf.WriteString(s)
	}
	writeI64s := func(a []int64) {
		var l [2]byte
		if len(a) > 0xFFFF {
			a = a[:0xFFFF]
		}
		binary.LittleEndian.PutUint16(l[:], uint16(len(a)))
		buf.Write(l[:])
		var v [8]byte
		for _, x := range a {
			binary.LittleEndian.PutUint64(v[:], uint64(x))
			buf.Write(v[:])
		}
	}
	writeWALStr16(p.Schema)
	writeWALStr16(p.Name)
	writeWALStr16(p.Language)
	writeWALStr16(p.Volatile)
	writeWALStr16(p.Parallel)
	writeWALStr16(p.Cost)
	writeWALStr16(p.Rows)
	writeWALStr16(p.KindChar)
	writeWALStr32(p.Body)
	writeWALStr16(p.ReturnTypeName)
	writeI64s(p.ReturnTypeArgs)
	var argCount [2]byte
	args := p.Args
	if len(args) > 0xFFFF {
		args = args[:0xFFFF]
	}
	binary.LittleEndian.PutUint16(argCount[:], uint16(len(args)))
	buf.Write(argCount[:])
	for _, a := range args {
		writeWALStr16(a.Name)
		writeWALStr16(a.TypeName)
		writeI64s(a.TypeArgs)
		mode := byte(0)
		if len(a.Mode) > 0 {
			mode = a.Mode[0]
		}
		buf.WriteByte(mode)
		writeWALStr16(a.Default)
	}
	// Optional Config extension block (DU-002 proconfig follow-up), omitted
	// entirely when empty — byte-identical to a pre-Config record for the
	// (overwhelmingly common) case of no SET clause.
	if len(p.Config) > 0 {
		cfg := p.Config
		if len(cfg) > 0xFFFF {
			cfg = cfg[:0xFFFF]
		}
		var cfgCount [2]byte
		binary.LittleEndian.PutUint16(cfgCount[:], uint16(len(cfg)))
		buf.Write(cfgCount[:])
		for _, c := range cfg {
			writeWALStr16(c)
		}
	}
	return buf.Bytes()
}

// DecodeCreateFunction decodes a RecordKindCreateFunction payload.
func DecodeCreateFunction(payload []byte) (CreateFunctionPayload, error) {
	var p CreateFunctionPayload
	if len(payload) < 7 {
		return p, fmt.Errorf("wal: create-function payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindCreateFunction {
		return p, fmt.Errorf("wal: record kind %d is not create-function", payload[0])
	}
	p.OID = binary.LittleEndian.Uint32(payload[1:5])
	flags := payload[5]
	flags2 := payload[6]
	p.ReturnsSet = flags&(1<<0) != 0
	p.ReturnsTable = flags&(1<<1) != 0
	p.Strict = flags&(1<<2) != 0
	p.SecurityDefiner = flags&(1<<3) != 0
	p.Leakproof = flags&(1<<4) != 0
	p.IsProcedure = flags&(1<<5) != 0
	p.IsWindow = flags&(1<<6) != 0
	p.BeginAtomic = flags&(1<<7) != 0
	p.IsReturnForm = flags2&(1<<0) != 0
	off := 7
	readStr16 := func() (string, error) {
		if len(payload) < off+2 {
			return "", fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	readStr32 := func() (string, error) {
		if len(payload) < off+4 {
			return "", fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+4)
		}
		l := int(binary.LittleEndian.Uint32(payload[off : off+4]))
		off += 4
		if len(payload) < off+l {
			return "", fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+l)
		}
		s := string(payload[off : off+l])
		off += l
		return s, nil
	}
	readI64s := func() ([]int64, error) {
		if len(payload) < off+2 {
			return nil, fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+2)
		}
		count := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+count*8 {
			return nil, fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+count*8)
		}
		out := make([]int64, count)
		for i := 0; i < count; i++ {
			out[i] = int64(binary.LittleEndian.Uint64(payload[off : off+8]))
			off += 8
		}
		return out, nil
	}
	var err error
	if p.Schema, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.Name, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.Language, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.Volatile, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.Parallel, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.Cost, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.Rows, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.KindChar, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.Body, err = readStr32(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.ReturnTypeName, err = readStr16(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if p.ReturnTypeArgs, err = readI64s(); err != nil {
		return CreateFunctionPayload{}, err
	}
	if len(payload) < off+2 {
		return CreateFunctionPayload{}, fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+2)
	}
	argCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
	off += 2
	p.Args = make([]FunctionArgPayload, 0, argCount)
	for i := 0; i < argCount; i++ {
		var a FunctionArgPayload
		if a.Name, err = readStr16(); err != nil {
			return CreateFunctionPayload{}, err
		}
		if a.TypeName, err = readStr16(); err != nil {
			return CreateFunctionPayload{}, err
		}
		if a.TypeArgs, err = readI64s(); err != nil {
			return CreateFunctionPayload{}, err
		}
		if len(payload) < off+1 {
			return CreateFunctionPayload{}, fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+1)
		}
		if payload[off] != 0 {
			a.Mode = string(payload[off])
		}
		off++
		if a.Default, err = readStr16(); err != nil {
			return CreateFunctionPayload{}, err
		}
		p.Args = append(p.Args, a)
	}
	// Optional Config extension block (DU-002 proconfig follow-up). Absent
	// entirely for a pre-existing record with no SET clause — backward
	// compatible, mirrors CreateIndexPayload's extension-block pattern.
	if off < len(payload) {
		if len(payload) < off+2 {
			return CreateFunctionPayload{}, fmt.Errorf("wal: create-function payload truncated (need %d bytes)", off+2)
		}
		cfgCount := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		p.Config = make([]string, 0, cfgCount)
		for i := 0; i < cfgCount; i++ {
			s, err := readStr16()
			if err != nil {
				return CreateFunctionPayload{}, err
			}
			p.Config = append(p.Config, s)
		}
	}
	return p, nil
}

// EncodeDropFunction encodes a DROP FUNCTION/PROCEDURE removal by OID
// (DU-002 restart-persistence follow-up to M0119-0004, loop #71 ledger
// resume point). Format documented at the RecordKindDropFunction constant.
func EncodeDropFunction(oid uint32) []byte {
	out := make([]byte, 5)
	out[0] = RecordKindDropFunction
	binary.LittleEndian.PutUint32(out[1:5], oid)
	return out
}

// DecodeDropFunction decodes a RecordKindDropFunction payload.
func DecodeDropFunction(payload []byte) (oid uint32, err error) {
	if len(payload) < 5 {
		return 0, fmt.Errorf("wal: drop-function payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindDropFunction {
		return 0, fmt.Errorf("wal: record kind %d is not drop-function", payload[0])
	}
	return binary.LittleEndian.Uint32(payload[1:5]), nil
}

// EncodeAlterFunctionRename encodes an ALTER FUNCTION/PROCEDURE/ROUTINE
// RENAME TO event (DU-002 restart-persistence follow-up to M0119-0004,
// loop #71 ledger resume point). Format documented at the
// RecordKindAlterFunctionRename constant.
func EncodeAlterFunctionRename(oid uint32, newName string) []byte {
	if len(newName) > 0xFFFF {
		newName = newName[:0xFFFF]
	}
	out := make([]byte, 7+len(newName))
	out[0] = RecordKindAlterFunctionRename
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(newName)))
	copy(out[7:], newName)
	return out
}

// DecodeAlterFunctionRename decodes a RecordKindAlterFunctionRename
// payload.
func DecodeAlterFunctionRename(payload []byte) (oid uint32, newName string, err error) {
	if len(payload) < 7 {
		return 0, "", fmt.Errorf("wal: alter-function-rename payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterFunctionRename {
		return 0, "", fmt.Errorf("wal: record kind %d is not alter-function-rename", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	nameLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+nameLen {
		return 0, "", fmt.Errorf("wal: alter-function-rename payload truncated (need %d bytes)", 7+nameLen)
	}
	return oid, string(payload[7 : 7+nameLen]), nil
}

// EncodeAlterFunctionFlags encodes an ALTER FUNCTION/PROCEDURE/ROUTINE
// attribute change as a full post-mutation snapshot of the four mutable
// attributes (DU-002 restart-persistence follow-up to M0119-0004, loop #71
// ledger resume point). Format documented at the RecordKindAlterFunctionFlags
// constant.
func EncodeAlterFunctionFlags(oid uint32, volatile string, securityDefiner, leakproof, strict bool) []byte {
	if len(volatile) > 0xFFFF {
		volatile = volatile[:0xFFFF]
	}
	out := make([]byte, 8+len(volatile))
	out[0] = RecordKindAlterFunctionFlags
	binary.LittleEndian.PutUint32(out[1:5], oid)
	var flags byte
	if securityDefiner {
		flags |= 1 << 0
	}
	if leakproof {
		flags |= 1 << 1
	}
	if strict {
		flags |= 1 << 2
	}
	out[5] = flags
	binary.LittleEndian.PutUint16(out[6:8], uint16(len(volatile)))
	copy(out[8:], volatile)
	return out
}

// DecodeAlterFunctionFlags decodes a RecordKindAlterFunctionFlags payload.
func DecodeAlterFunctionFlags(payload []byte) (oid uint32, volatile string, securityDefiner, leakproof, strict bool, err error) {
	if len(payload) < 8 {
		return 0, "", false, false, false, fmt.Errorf("wal: alter-function-flags payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterFunctionFlags {
		return 0, "", false, false, false, fmt.Errorf("wal: record kind %d is not alter-function-flags", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	flags := payload[5]
	securityDefiner = flags&(1<<0) != 0
	leakproof = flags&(1<<1) != 0
	strict = flags&(1<<2) != 0
	volLen := int(binary.LittleEndian.Uint16(payload[6:8]))
	if len(payload) < 8+volLen {
		return 0, "", false, false, false, fmt.Errorf("wal: alter-function-flags payload truncated (need %d bytes)", 8+volLen)
	}
	return oid, string(payload[8 : 8+volLen]), securityDefiner, leakproof, strict, nil
}

// EncodeAlterFunctionOwner encodes an ALTER FUNCTION/PROCEDURE/ROUTINE
// OWNER TO event (M0097-0150). Format documented at the
// RecordKindAlterFunctionOwner constant.
func EncodeAlterFunctionOwner(oid, ownerOID uint32) []byte {
	out := make([]byte, 9)
	out[0] = RecordKindAlterFunctionOwner
	binary.LittleEndian.PutUint32(out[1:5], ownerOID)
	binary.LittleEndian.PutUint32(out[5:9], oid)
	return out
}

// DecodeAlterFunctionOwner decodes a RecordKindAlterFunctionOwner payload.
func DecodeAlterFunctionOwner(payload []byte) (oid, ownerOID uint32, err error) {
	if len(payload) < 9 {
		return 0, 0, fmt.Errorf("wal: alter-function-owner payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterFunctionOwner {
		return 0, 0, fmt.Errorf("wal: record kind %d is not alter-function-owner", payload[0])
	}
	ownerOID = binary.LittleEndian.Uint32(payload[1:5])
	oid = binary.LittleEndian.Uint32(payload[5:9])
	return oid, ownerOID, nil
}

// EncodeAlterFunctionSetSchema encodes an ALTER FUNCTION/PROCEDURE/ROUTINE
// SET SCHEMA event (M0097-0150). Format documented at the
// RecordKindAlterFunctionSetSchema constant.
func EncodeAlterFunctionSetSchema(oid uint32, newSchema string) []byte {
	if len(newSchema) > 0xFFFF {
		newSchema = newSchema[:0xFFFF]
	}
	out := make([]byte, 7+len(newSchema))
	out[0] = RecordKindAlterFunctionSetSchema
	binary.LittleEndian.PutUint32(out[1:5], oid)
	binary.LittleEndian.PutUint16(out[5:7], uint16(len(newSchema)))
	copy(out[7:], newSchema)
	return out
}

// DecodeAlterFunctionSetSchema decodes a RecordKindAlterFunctionSetSchema
// payload.
func DecodeAlterFunctionSetSchema(payload []byte) (oid uint32, newSchema string, err error) {
	if len(payload) < 7 {
		return 0, "", fmt.Errorf("wal: alter-function-set-schema payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterFunctionSetSchema {
		return 0, "", fmt.Errorf("wal: record kind %d is not alter-function-set-schema", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	schemaLen := int(binary.LittleEndian.Uint16(payload[5:7]))
	if len(payload) < 7+schemaLen {
		return 0, "", fmt.Errorf("wal: alter-function-set-schema payload truncated (need %d bytes)", 7+schemaLen)
	}
	return oid, string(payload[7 : 7+schemaLen]), nil
}

// EncodeAlterFunctionConfig encodes the post-mutation snapshot of a
// routine's pg_proc.proconfig array after an ALTER FUNCTION/PROCEDURE/
// ROUTINE ... SET/RESET clause (DU-002 proconfig follow-up to M0097-0150).
// Format documented at the RecordKindAlterFunctionConfig constant:
//
//	kind(1) | oid(4) | count(2) | [entryLen(2) | entry(entryLen bytes)]*count
func EncodeAlterFunctionConfig(oid uint32, config []string) []byte {
	var buf bytes.Buffer
	buf.WriteByte(RecordKindAlterFunctionConfig)
	var oidBuf [4]byte
	binary.LittleEndian.PutUint32(oidBuf[:], oid)
	buf.Write(oidBuf[:])
	if len(config) > 0xFFFF {
		config = config[:0xFFFF]
	}
	var countBuf [2]byte
	binary.LittleEndian.PutUint16(countBuf[:], uint16(len(config)))
	buf.Write(countBuf[:])
	for _, entry := range config {
		if len(entry) > 0xFFFF {
			entry = entry[:0xFFFF]
		}
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(entry)))
		buf.Write(l[:])
		buf.WriteString(entry)
	}
	return buf.Bytes()
}

// DecodeAlterFunctionConfig decodes a RecordKindAlterFunctionConfig payload.
func DecodeAlterFunctionConfig(payload []byte) (oid uint32, config []string, err error) {
	if len(payload) < 7 {
		return 0, nil, fmt.Errorf("wal: alter-function-config payload too short (%d bytes)", len(payload))
	}
	if payload[0] != RecordKindAlterFunctionConfig {
		return 0, nil, fmt.Errorf("wal: record kind %d is not alter-function-config", payload[0])
	}
	oid = binary.LittleEndian.Uint32(payload[1:5])
	count := int(binary.LittleEndian.Uint16(payload[5:7]))
	off := 7
	config = make([]string, 0, count)
	for i := 0; i < count; i++ {
		if len(payload) < off+2 {
			return 0, nil, fmt.Errorf("wal: alter-function-config payload truncated (need %d bytes)", off+2)
		}
		l := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if len(payload) < off+l {
			return 0, nil, fmt.Errorf("wal: alter-function-config payload truncated (need %d bytes)", off+l)
		}
		config = append(config, string(payload[off:off+l]))
		off += l
	}
	return oid, config, nil
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
	// ColDescending / ColNullsFirst carry the per-key-column ASC/DESC +
	// NULLS FIRST/LAST ordering (mirrors upstream's pg_index.indoption
	// bitmask) so a restart-triggered replay of this record restores the
	// exact same catalog.Index.ColDescending/ColNullsFirst state the live
	// CREATE INDEX produced, instead of silently defaulting every column
	// to ascending/NULLS LAST. Parallel to Columns; index i describes
	// Columns[i].
	ColDescending []bool
	ColNullsFirst []bool
	// --- M0122-0006 follow-up: index properties beyond column ordering.
	// Carried in a self-describing, OPTIONAL trailing "extension" block
	// (see encodeCreateIndexExtension/decodeCreateIndexExtension) appended
	// after the ColDescending/ColNullsFirst blocks. Omitted entirely when
	// every field below is at its zero value, so a plain index's WAL record
	// stays byte-identical to a pre-this-follow-up record — mirroring
	// catalog.Index's own "zero means unset, dumps byte-identically"
	// discipline for these same fields.
	HasPredicate     bool
	PredicateString  string
	IncludeColumns   []string
	ColOpClasses     []string // parallel to Columns; "" = default opclass
	ColCollations    []string // parallel to Columns; "" = default collation
	Fillfactor       int
	DeduplicateItems *bool
	NullsNotDistinct bool
	// Tablespace carries pg_class.reltablespace (0 = database default) so a
	// CREATE INDEX ... TABLESPACE / ALTER INDEX ... SET TABLESPACE survives an
	// uncheckpointed crash restart replayed purely from WAL. M0122-0007
	// tablespace-restart-durability follow-up.
	Tablespace uint32
}

// EncodeCreateIndex encodes a CREATE INDEX event (M0079-0001).
// Format documented at the RecordKindCreateIndex constant.
//
// ColDescending/ColNullsFirst (M0122-0006) are appended as two trailing
// numCols-byte blocks AFTER the column name list, deliberately NOT
// interleaved with each column's bytes: this keeps the format
// append-only, so DecodeCreateIndex can still read a pre-M0122-0006 WAL
// record (which simply lacks the trailing blocks — e.g. an existing
// on-disk cluster's history) without misparsing column name bytes as
// order flags.
//
// A further optional "extension" block (M0122-0006 follow-up) may follow
// the order blocks — see hasCreateIndexExtension/encodeCreateIndexExtension.
func EncodeCreateIndex(p CreateIndexPayload) []byte {
	const headerSize = 1 + 4 + 4 + 1 + 1 + 2 + 2 + 2 + 2
	totalLen := headerSize + len(p.Schema) + len(p.Name) + len(p.Method) + 2*len(p.Columns)
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
	for i := range p.Columns {
		if i < len(p.ColDescending) && p.ColDescending[i] {
			out[off] = 1
		}
		off++
	}
	for i := range p.Columns {
		if i < len(p.ColNullsFirst) && p.ColNullsFirst[i] {
			out[off] = 1
		}
		off++
	}
	if hasCreateIndexExtension(p) {
		out = append(out, encodeCreateIndexExtension(p)...)
	}
	return out
}

// Bit layout for encodeCreateIndexExtension's leading flags byte.
const (
	ciExtHasPredicate     = 1 << 0
	ciExtDedupSpecified   = 1 << 1
	ciExtDedupValue       = 1 << 2
	ciExtHasOpClasses     = 1 << 3
	ciExtHasCollations    = 1 << 4
	ciExtNullsNotDistinct = 1 << 5
	ciExtHasTablespace    = 1 << 6
)

// hasCreateIndexExtension reports whether p carries any of the M0122-0006
// follow-up fields at a non-zero value. When false, EncodeCreateIndex omits
// the extension block entirely so a plain index's WAL record is unaffected
// by this follow-up.
func hasCreateIndexExtension(p CreateIndexPayload) bool {
	if p.HasPredicate || len(p.IncludeColumns) > 0 || p.Fillfactor != 0 ||
		p.DeduplicateItems != nil || p.NullsNotDistinct || p.Tablespace != 0 {
		return true
	}
	for _, s := range p.ColOpClasses {
		if s != "" {
			return true
		}
	}
	for _, s := range p.ColCollations {
		if s != "" {
			return true
		}
	}
	return false
}

// encodeCreateIndexExtension encodes the M0122-0006 follow-up "index
// properties" block: a leading flags byte (ciExt* bits), a fillfactor
// int32, the partial-index predicate string (when present), the INCLUDE
// column list, and — parallel to p.Columns — per-column opclass/collation
// overrides (only when at least one column has a non-default value, mirroring
// the ColOpClasses/ColCollations "empty means all default" catalog contract).
// Self-terminating: DecodeCreateIndex parses it to end-of-payload and
// verifies nothing is left over.
func encodeCreateIndexExtension(p CreateIndexPayload) []byte {
	var flags byte
	if p.HasPredicate {
		flags |= ciExtHasPredicate
	}
	if p.DeduplicateItems != nil {
		flags |= ciExtDedupSpecified
		if *p.DeduplicateItems {
			flags |= ciExtDedupValue
		}
	}
	if p.NullsNotDistinct {
		flags |= ciExtNullsNotDistinct
	}
	if p.Tablespace != 0 {
		flags |= ciExtHasTablespace
	}
	numCols := len(p.Columns)
	hasOpClasses := false
	for _, s := range p.ColOpClasses {
		if s != "" {
			hasOpClasses = true
			break
		}
	}
	hasCollations := false
	for _, s := range p.ColCollations {
		if s != "" {
			hasCollations = true
			break
		}
	}
	if hasOpClasses {
		flags |= ciExtHasOpClasses
	}
	if hasCollations {
		flags |= ciExtHasCollations
	}

	size := 1 + 4 // flags + fillfactor
	if p.HasPredicate {
		size += 2 + len(p.PredicateString)
	}
	size += 2 // numInclude
	for _, c := range p.IncludeColumns {
		size += 2 + len(c)
	}
	if hasOpClasses {
		for i := 0; i < numCols; i++ {
			size += 2 + len(stringAt(p.ColOpClasses, i))
		}
	}
	if hasCollations {
		for i := 0; i < numCols; i++ {
			size += 2 + len(stringAt(p.ColCollations, i))
		}
	}
	if p.Tablespace != 0 {
		size += 4
	}

	out := make([]byte, size)
	off := 0
	out[off] = flags
	off++
	binary.LittleEndian.PutUint32(out[off:off+4], uint32(int32(p.Fillfactor)))
	off += 4
	if p.HasPredicate {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(p.PredicateString)))
		off += 2
		off += copy(out[off:], p.PredicateString)
	}
	binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(p.IncludeColumns)))
	off += 2
	for _, c := range p.IncludeColumns {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(c)))
		off += 2
		off += copy(out[off:], c)
	}
	if hasOpClasses {
		for i := 0; i < numCols; i++ {
			c := stringAt(p.ColOpClasses, i)
			binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(c)))
			off += 2
			off += copy(out[off:], c)
		}
	}
	if hasCollations {
		for i := 0; i < numCols; i++ {
			c := stringAt(p.ColCollations, i)
			binary.LittleEndian.PutUint16(out[off:off+2], uint16(len(c)))
			off += 2
			off += copy(out[off:], c)
		}
	}
	if p.Tablespace != 0 {
		binary.LittleEndian.PutUint32(out[off:off+4], p.Tablespace)
		off += 4
	}
	return out
}

func stringAt(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

// decodeCreateIndexExtension parses the M0122-0006 follow-up block written by
// encodeCreateIndexExtension, starting at *off, and advances *off past it.
// Callers must verify *off == len(payload) afterward (nothing left over).
func decodeCreateIndexExtension(payload []byte, off *int, numCols int, p *CreateIndexPayload) error {
	if len(payload) < *off+5 {
		return fmt.Errorf("wal: create-index payload truncated in extension header")
	}
	flags := payload[*off]
	*off++
	p.Fillfactor = int(int32(binary.LittleEndian.Uint32(payload[*off : *off+4])))
	*off += 4
	if flags&ciExtHasPredicate != 0 {
		if len(payload) < *off+2 {
			return fmt.Errorf("wal: create-index payload truncated in predicate length")
		}
		predLen := int(binary.LittleEndian.Uint16(payload[*off : *off+2]))
		*off += 2
		if len(payload) < *off+predLen {
			return fmt.Errorf("wal: create-index payload truncated in predicate body")
		}
		p.HasPredicate = true
		p.PredicateString = string(payload[*off : *off+predLen])
		*off += predLen
	}
	if len(payload) < *off+2 {
		return fmt.Errorf("wal: create-index payload truncated in include-columns count")
	}
	numInclude := int(binary.LittleEndian.Uint16(payload[*off : *off+2]))
	*off += 2
	p.IncludeColumns = make([]string, 0, numInclude)
	for i := 0; i < numInclude; i++ {
		if len(payload) < *off+2 {
			return fmt.Errorf("wal: create-index payload truncated at include column %d header", i)
		}
		l := int(binary.LittleEndian.Uint16(payload[*off : *off+2]))
		*off += 2
		if len(payload) < *off+l {
			return fmt.Errorf("wal: create-index payload truncated at include column %d body", i)
		}
		p.IncludeColumns = append(p.IncludeColumns, string(payload[*off:*off+l]))
		*off += l
	}
	if flags&ciExtHasOpClasses != 0 {
		p.ColOpClasses = make([]string, numCols)
		for i := 0; i < numCols; i++ {
			if len(payload) < *off+2 {
				return fmt.Errorf("wal: create-index payload truncated at opclass %d header", i)
			}
			l := int(binary.LittleEndian.Uint16(payload[*off : *off+2]))
			*off += 2
			if len(payload) < *off+l {
				return fmt.Errorf("wal: create-index payload truncated at opclass %d body", i)
			}
			p.ColOpClasses[i] = string(payload[*off : *off+l])
			*off += l
		}
	}
	if flags&ciExtHasCollations != 0 {
		p.ColCollations = make([]string, numCols)
		for i := 0; i < numCols; i++ {
			if len(payload) < *off+2 {
				return fmt.Errorf("wal: create-index payload truncated at collation %d header", i)
			}
			l := int(binary.LittleEndian.Uint16(payload[*off : *off+2]))
			*off += 2
			if len(payload) < *off+l {
				return fmt.Errorf("wal: create-index payload truncated at collation %d body", i)
			}
			p.ColCollations[i] = string(payload[*off : *off+l])
			*off += l
		}
	}
	if flags&ciExtDedupSpecified != 0 {
		v := flags&ciExtDedupValue != 0
		p.DeduplicateItems = &v
	}
	p.NullsNotDistinct = flags&ciExtNullsNotDistinct != 0
	if flags&ciExtHasTablespace != 0 {
		if len(payload) < *off+4 {
			return fmt.Errorf("wal: create-index payload truncated in tablespace")
		}
		p.Tablespace = binary.LittleEndian.Uint32(payload[*off : *off+4])
		*off += 4
	}
	return nil
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
	// ColDescending/ColNullsFirst (M0122-0006) are two trailing numCols-byte
	// blocks appended AFTER the column list — append-only so a pre-existing
	// on-disk WAL record predating this field (which simply ends here, with
	// no trailing blocks) still decodes: every column defaults to
	// ascending/NULLS LAST, matching its actual pre-M0122-0006 behavior.
	p.ColDescending = make([]bool, numCols)
	p.ColNullsFirst = make([]bool, numCols)
	switch remaining := len(payload) - off; {
	case remaining == 0:
		// Pre-M0122-0006 record: no trailing blocks at all. Valid — every
		// column defaults to ascending/NULLS LAST, matching its actual
		// pre-M0122-0006 behavior.
	case remaining < 2*numCols:
		return CreateIndexPayload{}, fmt.Errorf("wal: create-index payload truncated in trailing order blocks (have %d trailing bytes, want 0 or at least %d)", remaining, 2*numCols)
	default:
		for i := 0; i < numCols; i++ {
			p.ColDescending[i] = payload[off] != 0
			off++
		}
		for i := 0; i < numCols; i++ {
			p.ColNullsFirst[i] = payload[off] != 0
			off++
		}
		// M0122-0006 follow-up: an optional "extension" block (predicate/
		// INCLUDE/opclass/collation/fillfactor/dedup/NULLS NOT DISTINCT) may
		// follow the order blocks — present iff bytes remain. A record with
		// remaining == 2*numCols exactly (no extension) is the prior
		// M0122-0006 format and needs no further parsing here.
		if off < len(payload) {
			if err := decodeCreateIndexExtension(payload, &off, numCols, &p); err != nil {
				return CreateIndexPayload{}, err
			}
			if off != len(payload) {
				return CreateIndexPayload{}, fmt.Errorf("wal: create-index payload has %d unexpected trailing bytes after extension block", len(payload)-off)
			}
		}
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

// EncodeClogTruncate encodes a CLOG_TRUNCATE record carrying oldestXid — the
// oldest XID whose commit status is still retained after the truncation.
// Wire format: "kind(1) | oldestXid(4)" = 5 bytes. Mirrors PG's
// WriteTruncateXlogRec (postgres/src/backend/access/transam/clog.c:1029).
func EncodeClogTruncate(xid storage.TransactionID) []byte {
	out := make([]byte, xactRecordSize)
	out[0] = RecordKindClogTruncate
	binary.LittleEndian.PutUint32(out[1:5], uint32(xid))
	return out
}

// DecodeClogTruncate decodes a RecordKindClogTruncate payload, returning the
// retained oldestXid.
func DecodeClogTruncate(payload []byte) (storage.TransactionID, error) {
	if len(payload) != xactRecordSize {
		return 0, fmt.Errorf("wal: clog-truncate payload len %d (want %d)", len(payload), xactRecordSize)
	}
	if payload[0] != RecordKindClogTruncate {
		return 0, fmt.Errorf("wal: record kind %d is not clog-truncate", payload[0])
	}
	return storage.TransactionID(binary.LittleEndian.Uint32(payload[1:5])), nil
}

// ProcessCommittedInvalidationMessages unlinks both pg_internal.init files
// (global/pg_internal.init and base/<dboid>/pg_internal.init) so the next
// backend reloads fresh relcache descriptors. ENOENT is silently ignored.
//
// On the primary, this is called by the xact-marker hook inside open.go.
// On the standby, this is called by ApplyRecord when it processes a
// RecordKindXactCommitInval WAL record. Mirrors PG's
// inval.c:ProcessCommittedInvalidationMessages (standby-side redo path).
func ProcessCommittedInvalidationMessages(dataDir string, dboid uint32) error {
	paths := [2]string{
		filepath.Join(dataDir, "global", "pg_internal.init"),
		filepath.Join(dataDir, "base", fmt.Sprintf("%d", dboid), "pg_internal.init"),
	}
	var firstErr error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			if firstErr == nil {
				firstErr = fmt.Errorf("relcache init unlink %s: %w", p, err)
			}
		}
	}
	return firstErr
}

// DecodeXactMarker returns the xid carried by a commit or abort
// marker payload. The caller already knows the kind from the
// payload's first byte; this helper just unpacks the xid.
func DecodeXactMarker(payload []byte) (storage.TransactionID, error) {
	if len(payload) != xactRecordSize {
		return 0, fmt.Errorf("wal: invalid xact-marker payload len %d (want %d)", len(payload), xactRecordSize)
	}
	switch payload[0] {
	case RecordKindXactCommit, RecordKindXactAbort:
		// valid xact-marker kinds
	default:
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
func EncodeCheckpointCompat(redoLSN0 uint64, tli uint32, nextXid uint64, nextOid uint32) []byte {
	if nextXid < 3 {
		nextXid = 3
	}
	return encodeCheckPointStruct(redoLSN0, tli, nextXid, nextOid, uint32(nextXid))
}

// encodeCheckPointStruct builds the raw 88-byte PG18 CheckPoint struct.
// oldestActiveXid is parameterised (A9-checkpoint-opcode): PG stamps
// InvalidTransactionId (0) on shutdown checkpoints and
// GetOldestActiveTransactionId() on online ones (xlog.c CreateCheckPoint);
// EncodeCheckpointCompat keeps its historical nextXid mirror.
func encodeCheckPointStruct(redoLSN0 uint64, tli uint32, nextXid uint64, nextOid uint32, oldestActiveXid uint32) []byte {
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
	//
	// M0106-0010 batched-47: nextXid is now parameterised (was hardcoded
	// to 3 = FirstNormalTransactionId). PG's InitWalRecovery decodes the
	// checkpoint record from the basebackup-shipped WAL and seeds
	// ShmemVariableCache->nextXid + latestCompletedXid from it. With the
	// old hardcoded 3, every tuple goopg wrote with xmin >= 3 was
	// invisible to the standby's recovery snapshot (snapshot.Xmax =
	// latestCompletedXid + 1 = 3, so xid 3 was treated as "future" and
	// pg_class scans returned no row → 42P01). The runtime checkpointer
	// now passes mvcc.Manager.NextXID() through.
	// oldestActiveXid mirrors nextXid because the IMMEDIATE checkpoint
	// runs while no user xact is in-flight (CheckpointNow blocks
	// dirty-page flush; the prior xact-marker WAL is already durable).
	//
	// M0106-0013: nextOid is now parameterised (was hardcoded to 16384 =
	// FirstNormalObjectId). PG's InitWalRecovery seeds
	// ShmemVariableCache->nextOid from this field so after a crash the
	// OID counter is restored from the last checkpoint rather than from
	// the pg_catalog.json snapshot.
	const checkPointSize = 88
	if nextXid < 3 {
		nextXid = 3
	}
	const firstNormalOID = uint32(16384) // FirstNormalObjectId
	if nextOid < firstNormalOID {
		nextOid = firstNormalOID
	}
	payload := make([]byte, checkPointSize)
	le := binary.LittleEndian
	now := time.Now()

	le.PutUint64(payload[0:8], redoLSN0)  // redo
	le.PutUint32(payload[8:12], tli)      // ThisTimeLineID
	le.PutUint32(payload[20:24], 1)       // wal_level (replica)
	le.PutUint64(payload[24:32], nextXid) // nextXid (>= FirstNormalTxnId)
	le.PutUint32(payload[32:36], nextOid) // nextOid (>= FirstNormalObjectId)
	le.PutUint32(payload[36:40], 1)       // nextMulti
	le.PutUint32(payload[44:48], 3)       // oldestXid
	le.PutUint32(payload[52:56], 1)       // oldestMulti
	// time (pg_time_t=int64, 8-byte aligned → starts at offset 64)
	le.PutUint64(payload[64:72], uint64(now.Unix())) // time
	// After time (offset 72): oldestCommitTsXid, newestCommitTsXid,
	// oldestActiveXid. Each is TransactionId (uint32, 4 bytes).
	// NOTE: pg_time_t alignment forces 4-byte pad before time, pushing
	// offsets: time=64, oldestCommitTsXid=72, newestCommitTsXid=76,
	// oldestActiveXid=80, sizeof(CheckPoint)=88.
	le.PutUint32(payload[72:76], 3)               // oldestCommitTsXid
	le.PutUint32(payload[76:80], 3)               // newestCommitTsXid
	le.PutUint32(payload[80:84], oldestActiveXid) // oldestActiveXid

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

// EncodeBtreeSplit encodes one atomic B-tree split record. The left
// and right pages must be exactly storage.BlockSize bytes; the
// record embeds them in left-then-right order so replay applies the
// new right page before any reader could follow left's right-link to
// it.
//
// On a NON-rightmost split the page that used to be left's right
// sibling has its btpo_prev relinked from leftBlk to rightBlk;
// callers pass that block as sibBlk with its post-relink image as
// sibPage, and the record carries a third page so the relink is
// crash-atomic with the split (mirrors PostgreSQL _bt_split, which
// locks and stamps the original right sibling under the same WAL
// record — nbtxlog.c xl_btree_split + the SPLIT redo applying the
// rnext page). On a RIGHTMOST split sibBlk is
// storage.InvalidBlockNumber and sibPage must be nil; no third page
// follows.
func EncodeBtreeSplit(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, sibBlk storage.BlockNumber, sibPage storage.Page) ([]byte, error) {
	if len(leftPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split left page is %d bytes, want %d", len(leftPage), storage.BlockSize)
	}
	if len(rightPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split right page is %d bytes, want %d", len(rightPage), storage.BlockSize)
	}
	hasSib := sibBlk != storage.InvalidBlockNumber
	if hasSib && len(sibPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split sibling page is %d bytes, want %d", len(sibPage), storage.BlockSize)
	}
	if !hasSib && sibPage != nil {
		return nil, fmt.Errorf("wal: btree-split rightmost split must not carry a sibling page")
	}
	nPages := 2
	if hasSib {
		nPages = 3
	}
	out := make([]byte, btreeSplitHeaderSize+nPages*storage.BlockSize)
	out[0] = RecordKindBtreeSplit
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(leftBlk))
	binary.LittleEndian.PutUint32(out[14:18], uint32(rightBlk))
	binary.LittleEndian.PutUint32(out[18:22], uint32(sibBlk))
	copy(out[btreeSplitHeaderSize:btreeSplitHeaderSize+storage.BlockSize], leftPage)
	copy(out[btreeSplitHeaderSize+storage.BlockSize:btreeSplitHeaderSize+2*storage.BlockSize], rightPage)
	if hasSib {
		copy(out[btreeSplitHeaderSize+2*storage.BlockSize:], sibPage)
	}
	return out, nil
}

// DecodeBtreeSplit returns the rel + (left,right) blocks + page
// images carried by a BtreeSplit record payload. On a non-rightmost
// split sibBlk is the old right sibling and sibPage its relinked
// image; on a rightmost split sibBlk is storage.InvalidBlockNumber
// and sibPage is nil.
func DecodeBtreeSplit(payload []byte) (rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, sibBlk storage.BlockNumber, sibPage storage.Page, err error) {
	want2 := btreeSplitHeaderSize + 2*storage.BlockSize
	want3 := btreeSplitHeaderSize + 3*storage.BlockSize
	if len(payload) != want2 && len(payload) != want3 {
		err = fmt.Errorf("wal: invalid btree-split payload len %d (want %d or %d)", len(payload), want2, want3)
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
	sibBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[18:22]))
	hasSib := len(payload) == want3
	if hasSib != (sibBlk != storage.InvalidBlockNumber) {
		err = fmt.Errorf("wal: btree-split sibBlk/payload-length mismatch (sibBlk=%d len=%d)", sibBlk, len(payload))
		return
	}
	leftPage = make(storage.Page, storage.BlockSize)
	copy(leftPage, payload[btreeSplitHeaderSize:btreeSplitHeaderSize+storage.BlockSize])
	rightPage = make(storage.Page, storage.BlockSize)
	copy(rightPage, payload[btreeSplitHeaderSize+storage.BlockSize:btreeSplitHeaderSize+2*storage.BlockSize])
	if hasSib {
		sibPage = make(storage.Page, storage.BlockSize)
		copy(sibPage, payload[btreeSplitHeaderSize+2*storage.BlockSize:])
	}
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

// ExportedReplayStart returns the start index (first post-checkpoint record)
// and the checkpoint LSN from a slice of WAL records. Used by the initdb
// crash-recovery xact-stamp pass to skip pre-checkpoint records. (M0106-0013)
func ExportedReplayStart(records []Record) (int, uint64) {
	return replayStart(records)
}

// isGoopgOwnedRmgr reports whether rmid is one of the resource managers
// classifyXLogRecord/recordKindToRmgrInfo (doc 04 §3) assigns to a native
// goopg RecordKind — either a real PG analog (Heap/Heap2/Btree/Xact/Storage/
// CLOG) or goopg's private custom rmgr (GoopgCatalog, §3.2). A record
// classified into any of these still carries its goopg RecordKind tag at
// Payload[0] (nativeHeaderMatchesMainData, pg_xlog_decode.go, populates it
// symmetrically regardless of rmid) and can be dispatched through the
// native payload[0] switch. RmgrXLog is deliberately excluded: it is PG's
// own rmgr (checkpoints, XLOG_FPI, parameter-change) plus the legacy
// pre-doc-04 0xF0 catch-all, neither of which is "goopg-owned" in this sense.
func isGoopgOwnedRmgr(rmid Rmgr) bool {
	switch rmid {
	case RmgrHeap, RmgrHeap2, RmgrBtree, RmgrXact, RmgrStorage, RmgrCLOG, RmgrGoopgCatalog:
		return true
	default:
		return false
	}
}

// IsGoopgNativeRecord reports whether r.Payload[0] is a trustworthy goopg
// RecordKind tag byte that a caller may safely switch on. A record whose XLog
// header does NOT match the (xl_rmid, xl_info) recordKindToRmgrInfo assigns to
// Payload[0] is a structurally real PG record (e.g. an XLOG_CHECKPOINT_SHUTDOWN)
// whose MainData is raw PG struct bytes with no goopg kind tag at all — Payload[0]
// in that case is arbitrary data (e.g. a checkpoint's redo-LSN low byte) that
// can coincidentally collide with a real RecordKind constant, exactly the
// M0106-0011 collision ApplyRecord below guards against. The catalog-only
// DDL-recovery scanners (internal/initdb/*_ddl_recovery.go — schema,
// transform, cast, conversion, ...) share this same hazard because they
// walk the same `wal.ReadAll` records and switch on Payload[0]; they must
// call this before trusting the byte. DU-002 restart-persistence follow-up.
func IsGoopgNativeRecord(r Record) bool {
	return headerMatchesEmittedKind(r)
}

// headerMatchesEmittedKind reports whether r's XLog header carries exactly the
// (xl_rmid, xl_info) that recordKindToRmgrInfo assigns to payload[0] — i.e. r is
// a record goopg emitted, as opposed to a structured PG record (e.g. an 88-byte
// XLOG_CHECKPOINT_SHUTDOWN) whose arbitrary payload[0] byte can collide with a
// RecordKind constant. This replaces the historical RM_XLOG/0xF0 skip-tag test
// now that goopg classifies each record with its real PG-compatible (rmid,info)
// (see recordKindToRmgrInfo / classifyXLogRecord). The partition is identical to
// the old test: every goopg-emitted non-checkpoint record matches; structured PG
// records (checkpoint) do not.
func headerMatchesEmittedKind(r Record) bool {
	if r.XLog == nil {
		return true
	}
	if len(r.Payload) == 0 {
		return false
	}
	wantRmid, wantInfo := recordKindToRmgrInfo(r.Payload[0])
	return r.XLog.Header.Rmid == wantRmid && r.XLog.Header.Info == wantInfo
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
		// (M0106-0011) A structured PG record (e.g. an 88-byte
		// XLOG_CHECKPOINT_SHUTDOWN whose redo-LSN low byte happened to
		// collide with a goopg RecordKind) carries a header that does
		// NOT match the (rmid,info) recordKindToRmgrInfo assigns to
		// payload[0]; route it to the decoded path. A goopg-emitted
		// record's header matches its kind, so it falls through to the
		// native payload[0] switch below (the real replayX functions),
		// never the FPI-only decoded arms.
		if !headerMatchesEmittedKind(r) {
			return replayDecodedXLogRecord(mgr, r)
		}
	}
	if len(r.Payload) == 0 {
		return false, errors.New("wal: empty record payload")
	}
	// PG-compat checkpoint (88 bytes) in legacy WAL: treat as no-op.
	if isCheckpointRecord(r) && len(r.Payload) == 88 {
		return false, nil
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
	case RecordKindClogTruncate:
		// CLOG truncation marker (G9). Physical page recovery is a no-op: the
		// clog (pg_xact) is a write-behind cache whose authoritative state is
		// the WAL. The recovery driver in internal/initdb scans the WAL for
		// this record after physical replay and re-applies the (idempotent)
		// truncation to the CLog, which has no access to mvcc.Manager from
		// here. Mirrors PG clog_redo's CLOG_TRUNCATE branch
		// (postgres/src/backend/access/transam/clog.c:1131).
		return false, nil
	case RecordKindCreateDatabase, RecordKindDropDatabase:
		// CREATE/DROP DATABASE records (M0054-0001) carry only a database
		// name; goopg v0 has no per-database file namespacing, so the
		// physical replay path has nothing to do. The recovery driver in
		// internal/initdb/open.go scans the WAL for these records after
		// physical replay and re-applies them to the catalog's database
		// list.
		return false, nil
	case RecordKindCreateTablespace, RecordKindDropTablespace:
		// CREATE/DROP TABLESPACE records (M0122-0007 tablespace-registry
		// restart-durability follow-up) carry only pg_tablespace registry
		// metadata; goopg's tablespace registry has no backing heap
		// relation, so the physical replay path has nothing to do. The
		// recovery driver in internal/initdb/tablespace_ddl_recovery.go
		// scans the WAL for these records after physical replay and
		// re-applies them to the catalog's tablespace registry.
		return false, nil
	case RecordKindCreateForeignServer, RecordKindDropForeignServer:
		// CREATE/DROP SERVER records (M0122-0007 foreign-server registry
		// restart-durability follow-up) carry only pg_foreign_server
		// registry metadata; goopg's foreign-server registry has no backing
		// heap relation, so the physical replay path has nothing to do. The
		// recovery driver in internal/initdb/foreignserver_ddl_recovery.go
		// scans the WAL for these records after physical replay and
		// re-applies them to the catalog's foreign-server registry.
		return false, nil
	case RecordKindCreateUserMapping, RecordKindDropUserMapping:
		// CREATE/DROP USER MAPPING records (M0122-0007 user-mapping
		// restart-durability follow-up) carry only pg_user_mapping registry
		// metadata; goopg's user-mapping registry has no backing heap
		// relation, so the physical replay path has nothing to do. The
		// recovery driver in internal/initdb/usermapping_ddl_recovery.go
		// scans the WAL for these records after physical replay and
		// re-applies them to the catalog's user-mapping registry.
		return false, nil
	case RecordKindCreateTransform, RecordKindDropTransform:
		// CREATE/DROP TRANSFORM records (M0119-0004 restart persistence)
		// carry only pg_transform metadata; goopg has no per-transform file
		// namespace, so the physical replay path has nothing to do. The
		// recovery driver in internal/initdb/transform_ddl_recovery.go scans
		// the WAL for these records after physical replay and re-applies
		// them to the catalog's transform registry.
		return false, nil
	case RecordKindCreateCast, RecordKindDropCast:
		// CREATE/DROP CAST records (DU-002 restart-persistence follow-up)
		// carry only pg_cast metadata; goopg has no per-cast file namespace,
		// so the physical replay path has nothing to do. The recovery
		// driver in internal/initdb/cast_ddl_recovery.go scans the WAL for
		// these records after physical replay and re-applies them to the
		// catalog's cast registry.
		return false, nil
	case RecordKindSequenceState, RecordKindDropSequence:
		// Sequence state / removal records (SERIAL restart persistence)
		// carry only the executor's in-memory sequence registry state; a
		// sequence has no physical relation file in goopg, so the physical
		// replay path has nothing to do. The recovery driver in
		// internal/initdb/sequence_ddl_recovery.go scans the WAL for these
		// records after physical replay (and after loadUserTablesFromHeap)
		// and re-applies them to the sequence registry + the owning
		// column's serial/identity catalog markers.
		return false, nil
	case RecordKindRoleState, RecordKindDropRole, RecordKindAlterRoleRename:
		// Role state / removal / rename records (CREATE/ALTER/DROP ROLE
		// restart persistence) carry only the in-memory role registry state +
		// credential; runtime role DDL never writes the pg_authid heap
		// (initdb-only, like all on-disk shared catalogs), so the physical
		// replay path has nothing to do. The recovery driver in
		// internal/initdb/role_ddl_recovery.go scans the WAL for these
		// records after physical replay and re-applies them to the catalog
		// role registry; cmd/goopg seeds the auth UserStore from it.
		return false, nil
	case RecordKindColumnDefaults:
		// Column DEFAULT snapshots (root-0020 follow-up) carry only the
		// in-memory catalog's DefaultExpr ASTs as SQL text; no physical
		// page state. The recovery driver in
		// internal/initdb/column_defaults_recovery.go re-parses them after
		// loadUserTablesFromHeap.
		return false, nil
	case RecordKindCreateMatView:
		// Materialized-view query snapshots carry only the in-memory
		// catalog's View AST as SQL text + IsPopulated; no physical page
		// state (the matview's own heap data is ordinary pg_class-tracked
		// storage, already covered by loadUserTablesFromHeap). The recovery
		// driver in internal/initdb/matview_ddl_recovery.go re-parses the
		// query after loadUserTablesFromHeap.
		return false, nil
	case RecordKindCreateView:
		// Plain-view query snapshots carry only the in-memory catalog's View
		// AST as SQL text; a view has no physical page state (Virtual=true,
		// no heap file at all). The recovery driver in
		// internal/initdb/view_ddl_recovery.go re-parses the query after
		// loadUserTablesFromHeap.
		return false, nil
	case RecordKindCreateConversion, RecordKindDropConversion, RecordKindAlterConversionRename, RecordKindAlterConversionOwner, RecordKindAlterConversionSetSchema:
		// CREATE/DROP/ALTER CONVERSION records (DU-002 restart-persistence
		// follow-up; ALTER added M0122-0007 4e follow-up) carry only
		// pg_conversion metadata; goopg has no per-conversion file
		// namespace, so the physical replay path has nothing to do. The
		// recovery driver in internal/initdb/conversion_ddl_recovery.go
		// scans the WAL for these records after physical replay and
		// re-applies them to the catalog's conversion registry.
		return false, nil
	case RecordKindCreateTSDict, RecordKindDropTSDict, RecordKindCreateTSConfig, RecordKindAddTSConfigMapping, RecordKindDropTSConfig,
		RecordKindDropTSConfigMapping, RecordKindRenameTSConfig, RecordKindSetTSConfigSchema, RecordKindReplaceTSConfigMappingDict,
		RecordKindAlterTSConfigMapping, RecordKindRenameTSDict, RecordKindSetTSDictSchema, RecordKindAlterTSDictOptions:
		// CREATE/DROP TEXT SEARCH DICTIONARY and CREATE/ADD MAPPING/DROP TEXT
		// SEARCH CONFIGURATION records (DU-002 restart-persistence follow-up
		// to slices 437/446, M0119-0004) carry only pg_ts_dict/pg_ts_config
		// metadata; goopg has no per-dictionary/per-configuration file
		// namespace, so the physical replay path has nothing to do. The
		// recovery drivers in internal/initdb/tsdict_ddl_recovery.go and
		// internal/initdb/tsconfig_ddl_recovery.go scan the WAL for these
		// records after physical replay and re-apply them to the catalog's
		// text-search registries.
		return false, nil
	case RecordKindCreateCollation, RecordKindDropCollation, RecordKindAlterCollationRename, RecordKindAlterCollationOwner, RecordKindAlterCollationSetSchema:
		// CREATE/DROP/ALTER COLLATION records (DU-002 restart-persistence
		// follow-up) carry only pg_collation metadata; goopg has no
		// per-collation file namespace, so the physical replay path has
		// nothing to do. The recovery driver in
		// internal/initdb/collation_ddl_recovery.go scans the WAL for
		// these records after physical replay and re-applies them to the
		// catalog's collation registry.
		return false, nil
	case RecordKindCreateStatistics, RecordKindDropStatistics, RecordKindAlterStatisticsRename, RecordKindAlterStatisticsOwner, RecordKindAlterStatisticsSetSchema:
		// CREATE/DROP/ALTER STATISTICS records (DU-002 restart-persistence
		// follow-up) carry only pg_statistic_ext metadata; goopg has no
		// per-statistics-object file namespace, so the physical replay path
		// has nothing to do. The recovery driver in
		// internal/initdb/statistics_ddl_recovery.go scans the WAL for these
		// records after physical replay and re-applies them to the catalog's
		// statistics registry. Mirrors the RecordKindCreateCollation case
		// above; CREATE/DROP STATISTICS (kinds 95/96) were missing this case
		// entirely until this fix — falling to the switch's default
		// "unsupported kind" error whenever ApplyRecord ran on a
		// non-XLog-wrapped native record (the standby streaming-replication
		// path, internal/wal/stream_replayer.go), even though startup
		// crash-recovery on the primary never called ApplyRecord for these
		// (internal/initdb/open.go invokes replayStatisticsDDLRecords
		// directly) and so never observed the gap.
		return false, nil
	case RecordKindCreatePublication, RecordKindDropPublication, RecordKindAlterPublicationOwner,
		RecordKindCreateSubscription, RecordKindDropSubscription, RecordKindAlterSubscriptionOwner:
		// CREATE/DROP/ALTER PUBLICATION/SUBSCRIPTION records (DU-002
		// restart-persistence follow-up, M0119-0004 loop #67 ledger resume
		// point) carry only catalog.PubSub metadata; goopg has no
		// per-publication/subscription file namespace, so the physical
		// replay path has nothing to do. The recovery driver in
		// internal/initdb/pubsub_ddl_recovery.go scans the WAL for these
		// records after physical replay and re-applies them to the PubSub
		// registry.
		return false, nil
	case RecordKindCreateEventTrigger, RecordKindDropEventTrigger, RecordKindAlterEventTriggerEnabled,
		RecordKindAlterEventTriggerRename, RecordKindAlterEventTriggerOwner:
		// CREATE/DROP/ALTER EVENT TRIGGER records (DU-002 restart-persistence
		// follow-up, M0119-0004 loop #70 ledger resume point) carry only
		// catalog.InMemory's eventTriggers registry metadata; goopg has no
		// per-event-trigger file namespace, so the physical replay path has
		// nothing to do. The recovery driver in
		// internal/initdb/event_trigger_ddl_recovery.go scans the WAL for
		// these records after physical replay and re-applies them to the
		// event trigger registry.
		return false, nil
	case RecordKindCreateAccessMethod, RecordKindDropAccessMethod:
		// CREATE/DROP ACCESS METHOD records (DU-002 restart-persistence
		// follow-up, M0119-0004 DU-002 slice 426 ledger resume point) carry
		// only catalog.InMemory's accessMethods registry metadata; goopg has
		// no per-access-method file namespace (no pluggable storage engine),
		// so the physical replay path has nothing to do. The recovery driver
		// in internal/initdb/access_method_ddl_recovery.go scans the WAL for
		// these records after physical replay and re-applies them to the
		// access method registry.
		return false, nil
	case RecordKindCreateRangeType, RecordKindDropRangeType, RecordKindAlterRangeTypeRename, RecordKindAlterRangeTypeOwner:
		// CREATE/DROP TYPE ... AS RANGE records (DU-002 restart-persistence
		// follow-up, M0110-0001 DU-002 slice 429 ledger resume point,
		// sub-item (c)) and the ALTER TYPE ... RENAME TO/OWNER TO follow-up
		// (M0122-0005 restart-persistence follow-up) carry only
		// catalog.InMemory's rangeTypes registry metadata; goopg has no
		// per-range-type file namespace, so the physical replay path has
		// nothing to do. The recovery driver in
		// internal/initdb/range_type_ddl_recovery.go scans the WAL for these
		// records after physical replay and re-applies them to the range
		// type registry.
		return false, nil
	case RecordKindCreateDomain, RecordKindDropDomain:
		// CREATE/DROP DOMAIN records (M0122-0005 restart-persistence
		// follow-up, deferral ledger 2026-07-06 row) carry only
		// catalog.InMemory's domains registry metadata; goopg has no
		// per-domain file namespace, so the physical replay path has nothing
		// to do. The recovery driver in
		// internal/initdb/domain_ddl_recovery.go scans the WAL for these
		// records after physical replay and re-applies them to the domain
		// registry.
		return false, nil
	case RecordKindCreateOperator, RecordKindDropOperator, RecordKindGrantRoleMembership, RecordKindRevokeRoleMembership:
		// CREATE/DROP OPERATOR (DU-002 restart-persistence follow-up,
		// M0119-0004/M0110-0001 loop #65/#66) and GRANT/REVOKE ROLE
		// membership (M0119-0004-ACLHEAP) records carry only in-memory
		// registry state (userOperators / roleMembers); goopg has no
		// per-operator or per-role-membership file namespace, so the
		// physical replay path has nothing to do. **Bug fix (this loop):**
		// these four kinds previously had NO case in this switch at all —
		// on a data dir where the last checkpoint predates one of these
		// records (i.e. no shutdown checkpoint ran between the DDL and the
		// restart, such as a crash restart), ReplayRecords/ApplyRecord
		// would hit the `default` branch below and fail the ENTIRE replay
		// with "unsupported kind N", aborting startup outright — not a
		// silent data-loss bug, but a crash-recovery availability bug that
		// a graceful restart (which always takes a shutdown checkpoint,
		// trimming these pre-checkpoint records out of ReplayRecords'
		// input before they ever reach ApplyRecord) could never surface.
		// Discovered while auditing this switch for the CREATE/DROP
		// OPERATOR CLASS/FAMILY kinds added below — see the deferral
		// ledger. The recovery drivers in
		// internal/initdb/operator_ddl_recovery.go and
		// internal/initdb/role_membership_recovery.go already scan the WAL
		// unconditionally from LSN 0 (not trimmed to the checkpoint) and
		// re-apply these records to the catalog, independent of whatever
		// ApplyRecord does here.
		return false, nil
	case RecordKindCreateOperatorFamily, RecordKindCreateOperatorClass, RecordKindDropOperatorClass,
		RecordKindCreateAmOpMember, RecordKindDropAmOpMember, RecordKindCreateAmProcMember, RecordKindDropAmProcMember,
		RecordKindDropOperatorFamily:
		// CREATE OPERATOR FAMILY / CREATE OPERATOR CLASS (+ its pg_amop/
		// pg_amproc AS-list members) / DROP OPERATOR CLASS / ALTER OPERATOR
		// FAMILY ... ADD|DROP records (DU-002 restart-persistence
		// follow-up, M0119-0004/M0110-0001, closing the loop #65/#66 ledger
		// row's "still open" item (1)) carry only catalog.InMemory's
		// userOperatorFamilies/userOperatorClasses/amOpMembers/amProcMembers
		// registry state; goopg has no per-opclass/opfamily file namespace,
		// so the physical replay path has nothing to do. The recovery
		// driver in internal/initdb/operator_class_ddl_recovery.go scans
		// the WAL for these records after physical replay and re-applies
		// them to the catalog.
		return false, nil
	case RecordKindCreateAggregate, RecordKindAlterAggregateRename, RecordKindDropAggregate, RecordKindAlterAggregateOwner:
		// CREATE/ALTER/DROP AGGREGATE records (DU-002 restart-persistence
		// follow-up, slice 405 resume point (c)) carry only pg_aggregate/
		// pg_proc metadata; goopg has no per-aggregate file namespace, so
		// the physical replay path has nothing to do. The recovery driver
		// in internal/initdb/aggregate_ddl_recovery.go scans the WAL for
		// these records after physical replay and re-applies them to the
		// catalog's user-aggregate registry.
		return false, nil
	case RecordKindAlterDatabaseSetConfig, RecordKindAlterDatabaseResetConfig, RecordKindAlterDatabaseResetAllConfig:
		// ALTER DATABASE ... SET/RESET records (M0119-0004-ACLHEAP ALTER
		// DATABASE ... SET follow-up) carry only catalog.InMemory's
		// dbRoleSettings registry state; goopg has no per-database file
		// namespace, so the physical replay path has nothing to do. The
		// recovery driver in internal/initdb/database_config_recovery.go
		// scans the WAL for these records after physical replay and
		// re-applies them to the registry.
		return false, nil
	case RecordKindAlterRoleSetConfig, RecordKindAlterRoleResetConfig, RecordKindAlterRoleResetAllConfig:
		// ALTER ROLE ... SET/RESET records (M0119-0004-ACLHEAP ALTER ROLE
		// ... SET follow-up) carry only catalog.InMemory's roleSettings
		// registry state; same no-op physical replay path as the ALTER
		// DATABASE ... SET/RESET records above. The recovery driver in
		// internal/initdb/role_config_recovery.go scans the WAL for these
		// records after physical replay and re-applies them to the
		// registry.
		return false, nil
	case RecordKindCreateIndex, RecordKindDropIndex, RecordKindRenameIndex:
		// CREATE/DROP/RENAME INDEX records (M0079-0001; RENAME added
		// DU-002 slice 443) carry the catalog metadata that goopg's heap
		// representation cannot fully store (no pg_index relation). The
		// on-disk btree pages and the index relfile are restored by
		// RecordKindBtreeInsert / RecordKindSmgrCreate — a rename touches
		// neither — so the in-memory catalog state is reconstructed by
		// `internal/initdb.replayIndexDDLRecords` after physical replay
		// finishes.
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
		RecordKindClogTruncate,
		RecordKindCreateDatabase,
		RecordKindDropDatabase,
		RecordKindCreateIndex,
		RecordKindDropIndex,
		RecordKindRenameIndex,
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
	case RmgrGoopgCatalog:
		// doc 04 §5.4 point 3: catalog/DDL RecordKinds with no PG analog
		// (§3.2 default) fall through nativeApplyRecordKindKnown's
		// allow-list gate in ApplyRecord (they carry no physical page
		// state, e.g. RecordKindCreateTransform) and land here. They are
		// intentionally a no-op: the recovery driver in
		// internal/initdb/*_ddl_recovery.go re-scans the raw WAL and
		// re-applies them to the catalog directly (see
		// wal.IsGoopgNativeRecord / isGoopgOwnedRmgr, which those scanners
		// rely on to trust r.Payload[0] for this same rmgr).
		return false, nil
	case RmgrXLog:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogXLogParameterChange:
			return replayXLogParameterChange(mgr, xlog)
		case xlogXLogFPI:
			// A9: goopg's standalone first-touch FPI (Pool.maybeEmitFPI) is now
			// emitted as a real PG XLOG_FPI carrying the page as a block-0
			// apply-image (EncodePageImagePG) — an empty native Payload. Restore
			// it via the shared FPI-block replay (identical to the A8 btree FPI
			// flips), which reconstructs the hole and stamps pd_lsn to the record
			// LSN. A native (no-block-refs) RecordKindPageImage record still keeps
			// its populated Payload — recomputed symmetrically at decode time by
			// nativeHeaderMatchesMainData — and replays via replayPageImage (dead
			// fallback once the emit path is fully flipped).
			if len(r.Payload) == 0 {
				if err := replayDecodedXLogHeapFPIBlocks(mgr, r, xlog); err != nil {
					return false, err
				}
				return true, nil
			}
			if err := replayPageImage(mgr, r.Payload); err != nil {
				return false, err
			}
			return true, nil
		default:
			// Other RmgrXLog opcodes (checkpoint, noop, switch, …) need no
			// physical replay action on the standby.
			return false, nil
		}
	case RmgrXact:
		switch xlog.Header.Info & xlogXactOpMask {
		case xlogXactCommit:
			// CLOG commit-stamping is done by the initdb xact-recovery pass, not
			// here (physical replay is a page-wise no-op for xact markers). But a
			// commit carrying relcache invalidations (XLOG_XACT_HAS_INFO + xinfo
			// HAS_INVALS) must unlink the standby's pg_internal.init files before
			// the transaction's heap writes become visible — the A6 replacement
			// for the native RecordKindXactCommitInval redo path.
			if xactCommitCarriesInvals(xlog.Header.Info, xlog.MainData) {
				_ = ProcessCommittedInvalidationMessages(mgr.DataDir(), defaultRecoveryDBOid)
			}
			return false, nil
		case xlogXactAbort:
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
	case RmgrStorage:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogSmgrCreate:
			// A9: goopg's relation-file creation is now a PG xl_smgr_create
			// (RelFileLocator + forkNum main-data, no block ref); recreate the
			// relfile's first block, identical to native replaySmgrCreate.
			rel, err := decodeXLogSmgrCreate(xlog.MainData)
			if err != nil {
				return false, err
			}
			if err := applySmgrCreate(mgr, rel); err != nil {
				return false, err
			}
			return true, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrCLOG:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogClogTruncate:
			// A9: goopg's clog truncation is now a PG xl_clog_truncate. Physical
			// replay is a page-wise no-op (matching the native RecordKindClogTruncate
			// case); the actual CLOG truncation is re-applied by the initdb
			// clog-recovery scan (replayCLogFromWAL), which decodes the PG body.
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
		case xlogHeapDelete:
			// A3: goopg emits xl_heap_delete with block-0 main-data (xmax +
			// offnum), no FPI — replay stamps xmax at the slot. A real-PG
			// delete carrying a full-page image is restored via the FPI branch
			// inside replayDecodedXLogHeapDelete.
			if err := replayDecodedXLogHeapDelete(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeapHotUpdate:
			// A4: goopg emits xl_heap_update (HOT) with block-0 main-data (new
			// tuple + old/new offnums), no FPI — replay adds the new tuple and
			// stamps the old. A real-PG HOT update carrying a full-page image is
			// restored via the FPI branch inside replayDecodedXLogHeapUpdate.
			if err := replayDecodedXLogHeapUpdate(mgr, r, xlog, true); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeapUpdate:
			// B0.2: goopg now emits non-HOT xl_heap_update for catalog ALTERs
			// (EncodeHeapUpdatePG — block 0 new page + tuple, optional block 1
			// old page). Tuple-carrying records replay logically (add new
			// version + stamp old WITHOUT HOT bits); records with only FPIs
			// (a real-PG update) restore the images.
			if xlogHeapUpdateCarriesTuple(xlog) {
				if err := replayDecodedXLogHeapUpdate(mgr, r, xlog, false); err != nil {
					return false, err
				}
				return true, nil
			}
			if err := replayDecodedXLogHeapFPIBlocks(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeapInplace:
			// xlogHeapInplace (M0117-0008 Part B, datfrozenxid-advanced
			// pg_database) is emitted with full-page images; restore each
			// block from its FPI.
			if err := replayDecodedXLogHeapFPIBlocks(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrHeap2:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogHeap2PruneOnAccess, xlogHeap2PruneVacuumScan, xlogHeap2PruneVacuumClean:
			// A7: goopg's opportunistic prune / VACUUM prune / freeze all emit
			// xl_heap_prune (block-0 sub-records: redirects, now-unused, freeze
			// plans). Replay applies them to the page; a real-PG record carrying
			// a full-page image is restored via the FPI branch.
			if err := replayDecodedXLogHeapPrune(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrBtree:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogBtreeInsertLeaf:
			// A5: goopg emits xl_btree_insert with the IndexTuple as block-0
			// data, no FPI — replay re-inserts by key. A real-PG leaf insert
			// carrying a full-page image is restored via the FPI branch inside
			// replayDecodedXLogBtreeInsert.
			if err := replayDecodedXLogBtreeInsert(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		default:
			// Other btree records (split / newroot / vacuum / unlink / …) are
			// not flipped yet and are emitted with full-page images; restore
			// each block from its FPI.
			if err := replayDecodedXLogHeapFPIBlocks(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		}
	default:
		return false, unsupportedDecodedXLogRecord(r)
	}
}

// replayDecodedXLogHeapFPIBlocks restores all block references that carry a
// full-page image with ImageApply set. Used for XLOG_HEAP_DELETE,
// XLOG_HEAP_UPDATE, and XLOG_HEAP_HOT_UPDATE records emitted with FPI on
// every modified block. The FPI already encodes the complete post-mutation
// page state, so no tuple-level main-data parsing is required.
func replayDecodedXLogHeapFPIBlocks(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	for i, block := range xlog.Blocks {
		if !block.HasImage || !block.ImageApply {
			continue
		}
		if err := restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN)); err != nil {
			return fmt.Errorf("wal: xlog heap FPI block %d: %w", i, err)
		}
	}
	return nil
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
	// PG's xl_heap_insert block-0 data is xl_heap_header (t_infomask2,
	// t_infomask, t_hoff) followed by the tuple bytes past the fixed 23-byte
	// header — the null bitmap + alignment + column data — verbatim. Rebuild the
	// marshaled tuple by reconstructing the fixed header (t_xmin from the record;
	// a fresh insert is not deleted and self-points t_ctid at (block, offnum),
	// which A2-pre made the primary store too) and concatenating that data
	// verbatim. Verbatim concatenation preserves the null bitmap; the previous
	// prefix-stripping reconstruction only handled bitmap-less tuples (and
	// rejected a non-zero bitmap outright).
	dataPortion := block.Data[sizeOfXLogHeapHeaderData:]
	out := make([]byte, storage.SizeOfHeapTupleHeaderData+len(dataPortion))
	binary.LittleEndian.PutUint32(out[0:4], uint32(xid))                           // t_xmin
	binary.LittleEndian.PutUint32(out[4:8], uint32(storage.InvalidTransactionID))  // t_xmax
	binary.LittleEndian.PutUint32(out[8:12], uint32(storage.InvalidTransactionID)) // t_field3 (xvac)
	binary.LittleEndian.PutUint32(out[12:16], uint32(block.Block))                 // t_ctid.block (self)
	binary.LittleEndian.PutUint16(out[16:18], offnum)                              // t_ctid.offset (self)
	copy(out[18:22], block.Data[0:4])                                              // t_infomask2 + t_infomask
	out[22] = block.Data[4]                                                        // t_hoff
	copy(out[storage.SizeOfHeapTupleHeaderData:], dataPortion)
	return out, nil
}

// decodeXLogHeapDeleteMainData parses the fixed xl_heap_delete struct from a
// PG-format heap-delete record's main data (xmax, offnum, infobits_set, flags).
// Any old-tuple bytes past the struct (XLH_DELETE_CONTAINS_OLD_TUPLE) are for
// logical decoding and are not needed by redo.
func decodeXLogHeapDeleteMainData(mainData []byte) (xmax uint32, offnum uint16, infobits, flags uint8, err error) {
	if len(mainData) < sizeOfXLogHeapDeleteData {
		return 0, 0, 0, 0, fmt.Errorf("wal: invalid xlog heap-delete main-data len %d (want >= %d)", len(mainData), sizeOfXLogHeapDeleteData)
	}
	xmax = binary.LittleEndian.Uint32(mainData[0:4])
	offnum = binary.LittleEndian.Uint16(mainData[4:6])
	infobits = mainData[6]
	flags = mainData[7]
	return xmax, offnum, infobits, flags, nil
}

// reconstructMarshaledTupleFromHeader rebuilds a marshaled HeapTuple from a
// PG xl_heap_header (t_infomask2, t_infomask, t_hoff) + the tuple bytes past the
// fixed header — the form the old key/tuple rides in for heap-delete/update. The
// fixed-header transaction fields (xmin/xmax/xvac/ctid) are left zero: this is
// consumed only by logical decoding, which reads column values (via t_infomask +
// t_hoff), not the header xacts.
func reconstructMarshaledTupleFromHeader(headerAndData []byte) ([]byte, error) {
	if len(headerAndData) < sizeOfXLogHeapHeaderData {
		return nil, fmt.Errorf("wal: old-tuple header %d bytes < %d", len(headerAndData), sizeOfXLogHeapHeaderData)
	}
	data := headerAndData[sizeOfXLogHeapHeaderData:]
	out := make([]byte, storage.SizeOfHeapTupleHeaderData+len(data))
	copy(out[18:22], headerAndData[0:4]) // t_infomask2 + t_infomask
	out[22] = headerAndData[4]           // t_hoff
	copy(out[storage.SizeOfHeapTupleHeaderData:], data)
	return out, nil
}

// replayDecodedXLogHeapDelete applies a PG-format xl_heap_delete: stamp the
// deleted tuple's xmax at offnum on block 0's page. Mirrors the native
// replayHeapDelete (PageSetHeapTupleXmax), so goopg↔goopg replay is identical;
// a full-page image on the block (real-PG WAL) is restored instead. Idempotent
// via pd_lsn.
func replayDecodedXLogHeapDelete(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap-delete missing block 0")
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	xmax, offnum, _, _, err := decodeXLogHeapDeleteMainData(xlog.MainData)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return err
	}
	if block.Block >= nblocks {
		return fmt.Errorf("wal: xlog heap-delete: block %d does not exist (nblocks=%d)", block.Block, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: xlog heap-delete: block %d is uninitialised", block.Block)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := storage.PageSetHeapTupleXmax(page, offnum, storage.TransactionID(xmax)); err != nil {
		return fmt.Errorf("wal: xlog heap-delete apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// decodeXLogHeapUpdateMainData parses the fixed xl_heap_update struct from a
// PG-format heap-update record's main data.
func decodeXLogHeapUpdateMainData(mainData []byte) (oldXmax uint32, oldOffnum uint16, oldInfobits, flags uint8, newXmax uint32, newOffnum uint16, err error) {
	if len(mainData) < sizeOfXLogHeapUpdateData {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("wal: invalid xlog heap-update main-data len %d (want >= %d)", len(mainData), sizeOfXLogHeapUpdateData)
	}
	oldXmax = binary.LittleEndian.Uint32(mainData[0:4])
	oldOffnum = binary.LittleEndian.Uint16(mainData[4:6])
	oldInfobits = mainData[6]
	flags = mainData[7]
	newXmax = binary.LittleEndian.Uint32(mainData[8:12])
	newOffnum = binary.LittleEndian.Uint16(mainData[12:14])
	return oldXmax, oldOffnum, oldInfobits, flags, newXmax, newOffnum, nil
}

// replayDecodedXLogHeapUpdate applies a PG-format xl_heap_update for a HOT
// (same-page) update: add the new tuple from block 0 at new_offnum, then stamp
// the old tuple (old_offnum) with old_xmax + t_ctid->new + HEAP_HOT_UPDATED.
// Mirrors the native replayHeapHotUpdate (PageAddHeapTuple + PageStampHotOldTuple),
// so goopg↔goopg replay is identical; a full-page image is restored instead.
// Idempotent via pd_lsn.
// xlogHeapUpdateCarriesTuple reports whether block 0 carries the new tuple's
// bytes (goopg's logical emit) rather than only full-page images (a real-PG
// record). B0.2 dispatch helper.
func xlogHeapUpdateCarriesTuple(xlog *XLogDecodedRecord) bool {
	block, ok := xlogBlockRefByID(xlog, 0)
	return ok && len(block.Data) > 0 && !(block.HasImage && block.ImageApply)
}

// replayDecodedXLogHeapUpdate applies a tuple-carrying xl_heap_update. hot
// selects the old-tuple stamp: HOT (same-page chain link + HeapHotUpdated)
// vs plain non-HOT (B0.2 catalog ALTERs — xmax + forward ctid, possibly
// cross-page via block 1, no HOT bits).
func replayDecodedXLogHeapUpdate(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord, hot bool) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap-update missing block 0")
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	oldXmax, oldOffnum, _, _, _, newOffnum, err := decodeXLogHeapUpdateMainData(xlog.MainData)
	if err != nil {
		return err
	}
	newTupleBytes, err := decodeXLogHeapInsertTuple(block, storage.TransactionID(xlog.Header.XID), newOffnum)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return err
	}
	if block.Block >= nblocks {
		return fmt.Errorf("wal: xlog heap-update: block %d does not exist (nblocks=%d)", block.Block, nblocks)
	}
	// Old-tuple page: block 1 when the versions live on different pages
	// (non-HOT cross-page form), else the shared block 0 page.
	oldBlock := block
	if ob, ok := xlogBlockRefByID(xlog, 1); ok {
		if hot {
			return fmt.Errorf("wal: xlog heap-hot-update with cross-page block 1")
		}
		oldBlock = ob
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: xlog heap-update: block %d is uninitialised", block.Block)
	}
	samePage := oldBlock.Block == block.Block
	stampOld := func(p storage.Page) error {
		if hot {
			return storage.PageStampHotOldTuple(p, oldOffnum, storage.TransactionID(oldXmax), block.Block, newOffnum)
		}
		return storage.PageStampUpdatedOldTuple(p, oldOffnum, storage.TransactionID(oldXmax), block.Block, newOffnum)
	}
	if storage.MustHeader(page).LSN() < storage.LSN(r.EndLSN) {
		tup, err := storage.ParseHeapTuple(newTupleBytes)
		if err != nil {
			return fmt.Errorf("wal: xlog heap-update parse new tuple: %w", err)
		}
		gotSlot, err := storage.PageAddHeapTuple(page, tup)
		if err != nil {
			return fmt.Errorf("wal: xlog heap-update add new tuple: %w", err)
		}
		if gotSlot != newOffnum {
			return fmt.Errorf("wal: xlog heap-update new-slot drift: got %d, want %d", gotSlot, newOffnum)
		}
		if samePage {
			if err := stampOld(page); err != nil {
				return fmt.Errorf("wal: xlog heap-update stamp old tuple: %w", err)
			}
		}
		storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
		if err := mgr.WriteBlock(block.Rel, block.Block, page); err != nil {
			return err
		}
	}
	if samePage {
		return nil
	}
	// Cross-page: stamp the old version on its own page, with its own
	// pd_lsn idempotency (mirrors PG's per-buffer redo).
	if oldBlock.Block >= nblocks {
		return fmt.Errorf("wal: xlog heap-update: old block %d does not exist (nblocks=%d)", oldBlock.Block, nblocks)
	}
	oldPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(block.Rel, oldBlock.Block, oldPage); err != nil {
		return err
	}
	if storage.IsNew(oldPage) {
		return fmt.Errorf("wal: xlog heap-update: old block %d is uninitialised", oldBlock.Block)
	}
	if storage.MustHeader(oldPage).LSN() >= storage.LSN(r.EndLSN) {
		return nil
	}
	if err := stampOld(oldPage); err != nil {
		return fmt.Errorf("wal: xlog heap-update stamp old tuple: %w", err)
	}
	storage.MustHeader(oldPage).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, oldBlock.Block, oldPage)
}

// replayDecodedXLogBtreeInsert applies a PG-format xl_btree_insert: insert the
// IndexTuple carried in block 0's data into the leaf page. Mirrors the native
// replayBtreeInsert (btree.ApplyInsertRecord re-inserts by key), so goopg↔goopg
// replay is identical; a full-page image is restored instead. Idempotent via pd_lsn.
func replayDecodedXLogBtreeInsert(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-insert missing block 0")
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return err
	}
	if block.Block >= nblocks {
		return fmt.Errorf("wal: xlog btree-insert: block %d does not exist (nblocks=%d)", block.Block, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: xlog btree-insert: block %d is uninitialised", block.Block)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if err := btree.ApplyInsertRecord(page, block.Data); err != nil {
		return fmt.Errorf("wal: xlog btree-insert apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// sizeOfXLHPFreezePlan is one xlhp_freeze_plan: xmax(4) + t_infomask2(2) +
// t_infomask(2) + frzflags(1) + ntuples(2).
const sizeOfXLHPFreezePlan = 11

// decodeXLogHeapPrune parses a PG xl_heap_prune record's main data + block-0
// sub-records into goopg's page-mutation inputs: the HOT redirect pairs, the
// now-unused (reclaimed) slots, and the frozen slots (from the freeze plans'
// trailing offset array). Sub-records appear in flag order — freeze plans,
// redirections, dead items (skipped; goopg has none), now-unused — with the
// freeze offset array trailing after all of them.
func decodeXLogHeapPrune(mainData, blockData []byte) (redirects [][2]uint16, unused, frozenSlots []uint16, err error) {
	if len(mainData) < sizeOfXLogHeapPruneData {
		return nil, nil, nil, fmt.Errorf("wal: invalid xlog heap-prune main-data len %d (want >= %d)", len(mainData), sizeOfXLogHeapPruneData)
	}
	flags := mainData[1]
	off := 0
	read16 := func() (uint16, error) {
		if off+2 > len(blockData) {
			return 0, fmt.Errorf("wal: truncated xlog heap-prune block data at %d", off)
		}
		v := binary.LittleEndian.Uint16(blockData[off : off+2])
		off += 2
		return v, nil
	}

	nFreezeTuples := 0
	if flags&xlhpHasFreezePlans != 0 {
		nplans, e := read16()
		if e != nil {
			return nil, nil, nil, e
		}
		if _, e = read16(); e != nil { // pad2
			return nil, nil, nil, e
		}
		for i := 0; i < int(nplans); i++ {
			if off+sizeOfXLHPFreezePlan > len(blockData) {
				return nil, nil, nil, fmt.Errorf("wal: truncated xlog heap-prune freeze plan")
			}
			ntuples := binary.LittleEndian.Uint16(blockData[off+9 : off+11])
			nFreezeTuples += int(ntuples)
			off += sizeOfXLHPFreezePlan
		}
	}
	if flags&xlhpHasRedirections != 0 {
		n, e := read16()
		if e != nil {
			return nil, nil, nil, e
		}
		redirects = make([][2]uint16, n)
		for i := range redirects {
			a, e := read16()
			if e != nil {
				return nil, nil, nil, e
			}
			b, e := read16()
			if e != nil {
				return nil, nil, nil, e
			}
			redirects[i] = [2]uint16{a, b}
		}
	}
	if flags&xlhpHasDeadItems != 0 {
		n, e := read16()
		if e != nil {
			return nil, nil, nil, e
		}
		for i := 0; i < int(n); i++ { // goopg reclaims directly — nothing to do with LP_DEAD
			if _, e := read16(); e != nil {
				return nil, nil, nil, e
			}
		}
	}
	if flags&xlhpHasNowUnusedItems != 0 {
		n, e := read16()
		if e != nil {
			return nil, nil, nil, e
		}
		unused = make([]uint16, n)
		for i := range unused {
			v, e := read16()
			if e != nil {
				return nil, nil, nil, e
			}
			unused[i] = v
		}
	}
	if nFreezeTuples > 0 {
		frozenSlots = make([]uint16, nFreezeTuples)
		for i := range frozenSlots {
			v, e := read16()
			if e != nil {
				return nil, nil, nil, e
			}
			frozenSlots[i] = v
		}
	}
	return redirects, unused, frozenSlots, nil
}

// replayDecodedXLogHeapPrune applies a PG xl_heap_prune to block 0's page:
// freeze the frozen slots, set the HOT redirect line pointers, and compact away
// the now-unused slots. Mirrors the native replayHeapPruneOpt / replayHeapFreeze
// (goopg emits prune and freeze as separate xl_heap_prune records, so each has
// only one kind of sub-record), so goopg↔goopg replay is identical; a full-page
// image is restored instead. Idempotent via pd_lsn.
func replayDecodedXLogHeapPrune(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap-prune missing block 0")
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	redirects, unused, frozenSlots, err := decodeXLogHeapPrune(xlog.MainData, block.Data)
	if err != nil {
		return err
	}
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return err
	}
	if block.Block >= nblocks {
		return fmt.Errorf("wal: xlog heap-prune: block %d does not exist (nblocks=%d)", block.Block, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: xlog heap-prune: block %d is uninitialised", block.Block)
	}
	if storage.MustHeader(page).LSN() >= storage.LSN(r.EndLSN) {
		return nil // already applied
	}
	if len(frozenSlots) > 0 {
		if err := storage.PageFreezeBySlots(page, frozenSlots); err != nil {
			return fmt.Errorf("wal: xlog heap-prune freeze: %w", err)
		}
	}
	for _, rd := range redirects {
		if err := storage.PageSetItemIDRedirect(page, rd[0], rd[1]); err != nil {
			return fmt.Errorf("wal: xlog heap-prune redirect: %w", err)
		}
	}
	if len(unused) > 0 {
		if _, err := storage.VacuumHeapPageBySlots(page, unused); err != nil {
			return fmt.Errorf("wal: xlog heap-prune compact: %w", err)
		}
	}
	if len(redirects) > 0 || len(unused) > 0 {
		storage.MustHeader(page).SetPruneXID(0)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// ReplayFromDir reads records from <dataDir>/pg_wal and replays them.
func ReplayFromDir(dataDir string, segmentSize int64) (ReplayStats, error) {
	records, err := ReadAll(filepath.Join(dataDir, "pg_wal"), segmentSize)
	if err != nil {
		return ReplayStats{}, err
	}
	// Honor the cluster's data_checksum_version so FPI replay rewrites
	// pages with valid checksums on a --data-checksums cluster (0 ⇒ the
	// checksum-less default). The runtime path uses ReplayFromDirWithMgr
	// with its already-configured Manager; this standalone entry point
	// reads pg_control itself.
	checksums := false
	if pgCtrl, pce := control.ReadControlFile(dataDir); pce == nil && pgCtrl != nil {
		checksums = pgCtrl.DataChecksumVersion != 0
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir, ChecksumsEnabled: checksums})
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
	rel, leftBlk, rightBlk, leftPage, rightPage, sibBlk, sibPage, err := DecodeBtreeSplit(payload)
	if err != nil {
		return err
	}
	if err := writeBlockOrExtend(mgr, rel, leftBlk, leftPage); err != nil {
		return fmt.Errorf("apply left block %d: %w", leftBlk, err)
	}
	if err := writeBlockOrExtend(mgr, rel, rightBlk, rightPage); err != nil {
		return fmt.Errorf("apply right block %d: %w", rightBlk, err)
	}
	// Non-rightmost split: relink the old right sibling's btpo_prev
	// to the new right page. Applied last; the sibling already
	// exists on disk (it predates the split) so this is always a
	// WriteBlock, never an Extend.
	if sibBlk != storage.InvalidBlockNumber {
		if err := writeBlockOrExtend(mgr, rel, sibBlk, sibPage); err != nil {
			return fmt.Errorf("apply old-right-sibling block %d: %w", sibBlk, err)
		}
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

// replayStart returns the replay start index plus the last checkpoint
// record's EndLSN. Crash recovery replays from the last checkpoint's REDO
// position — NOT from the checkpoint record itself (C2-S3 review MUST-FIX):
// the checkpoint's dirty-page/CLOG flush phase runs BEFORE its record is
// appended, so records in the (redo, checkpoint-record] window cover state
// the flush may not have captured (a commit acked during the flush phase
// leaves its CLOG lane dirty in memory with its record before the
// checkpoint record — anchoring at the record skipped it and the startup
// implicit-abort sweep then stamped the ACKED commit aborted). Mirrors PG:
// InitWalRecovery starts redo at checkPoint.redo. Replaying the extra
// (redo, record] span is idempotent — pages carry pd_lsn guards and CLOG
// stamps are terminal-state writes.
//
// The redo pointer is decoded from the PG-compat 88-byte CheckPoint payload
// (offset 0, 0-based LSN); legacy 1-byte checkpoint records carry no redo
// and keep the historical record-anchored behavior.
//
// If no checkpoint is found, returns (0, 0) — replay all records
// from the beginning (correct for fresh clusters or early startup).
func replayStart(records []Record) (int, uint64) {
	ckptIdx := -1
	var checkpointLSN uint64
	for i, r := range records {
		if isCheckpointRecord(r) {
			ckptIdx = i
			checkpointLSN = r.EndLSN
		}
	}
	if ckptIdx < 0 {
		return 0, 0
	}
	startIdx := ckptIdx
	if p := checkpointStructOf(records[ckptIdx]); len(p) == 88 {
		redo0 := binary.LittleEndian.Uint64(p[0:8])
		// Walk back to the first record whose span ends beyond redo.
		// Record LSNs are 1-based absolute positions; redo0 is 0-based
		// (EncodeCheckpointCompat), so EndLSN > redo0+? — comparing
		// EndLSN (1-based) > redo0 (0-based position) errs toward
		// replaying one extra byte-adjacent record, which is idempotent.
		for startIdx > 0 && records[startIdx-1].EndLSN > redo0 {
			startIdx--
		}
	}
	return startIdx, checkpointLSN
}

// isCheckpointRecord returns true if r is a checkpoint WAL record in
// either the legacy 1-byte format (RecordKindCheckpoint) or the
// PG-compat 88-byte CheckPoint struct format.
func isCheckpointRecord(r Record) bool {
	if len(r.Payload) == 1 && r.Payload[0] == RecordKindCheckpoint {
		return true
	}
	// Pre-A9 PG-compat checkpoint: 88-byte CheckPoint struct whose header was
	// stamped by the retired classify-by-len==88 rule, so the read side
	// re-matched it and populated Payload.
	if len(r.Payload) == 88 {
		return true
	}
	// A9-checkpoint-opcode: explicit-opcode checkpoint (EncodeCheckpointPG).
	// Header-driven — the record routes to the decoded path (Payload nil,
	// struct in XLog.MainData). This arm also recognises pre-A9 88-byte
	// records once classify no longer re-matches them on read.
	if r.XLog != nil && r.XLog.Header.Rmid == RmgrXLog && len(r.XLog.MainData) == 88 {
		switch r.XLog.Header.Info & XLRRmgrInfoMask {
		case xlogCheckpointShutdown, xlogCheckpointOnline:
			return true
		}
	}
	return false
}

// checkpointStructOf returns the 88-byte CheckPoint struct carried by a
// checkpoint record, regardless of which era framed it (native-classified
// Payload vs decoded-path XLog.MainData), or nil for the legacy 1-byte marker.
func checkpointStructOf(r Record) []byte {
	if len(r.Payload) == 88 {
		return r.Payload
	}
	if r.XLog != nil && len(r.XLog.MainData) == 88 {
		return r.XLog.MainData
	}
	return nil
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
		if isCheckpointRecord(r) {
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
	return applySmgrCreate(mgr, rel)
}

// decodeXLogSmgrCreate parses a PG xl_smgr_create main-data body
// (RelFileLocator{spcOid,dbOid,relNumber} + ForkNumber, 16 bytes) into a goopg
// RelFileNode. The default-tablespace spcOid (pgDefaultTableSpaceOID) is mapped
// back to goopg's TblOid=0 convention so the decoded rel matches the on-disk
// relation the emitter used (EncodeSmgrCreatePG encodes 0 → pgDefaultTableSpaceOID).
func decodeXLogSmgrCreate(mainData []byte) (storage.RelFileNode, error) {
	if len(mainData) < 16 {
		return storage.RelFileNode{}, fmt.Errorf("wal: xl_smgr_create main-data len %d (want 16)", len(mainData))
	}
	rel := storage.RelFileNode{
		TblOid: binary.LittleEndian.Uint32(mainData[0:4]),
		DBOid:  binary.LittleEndian.Uint32(mainData[4:8]),
		RelOid: binary.LittleEndian.Uint32(mainData[8:12]),
		Fork:   storage.ForkNumber(binary.LittleEndian.Uint32(mainData[12:16])),
	}
	if rel.TblOid == pgDefaultTableSpaceOID {
		rel.TblOid = 0
	}
	return rel, nil
}

// applySmgrCreate ensures the relation file identified by rel has at least one
// initialised block (idempotent: a no-op if the file already has blocks).
// Shared by the native replaySmgrCreate and the A9 decoded
// RmgrStorage/XLOG_SMGR_CREATE arm.
func applySmgrCreate(mgr *storage.Manager, rel storage.RelFileNode) error {
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

// DiscoverLastWALTLI returns the highest timeline ID found in PG-compat
// WAL segment filenames (format: <TLI:8hex><LogNo:8hex><SegNo:8hex>) under
// walDir. Returns 0 if walDir is empty, does not exist, or contains only
// legacy (non-PG-compat) segment files.
func DiscoverLastWALTLI(walDir string) (uint32, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("wal: list %s: %w", walDir, err)
	}
	var maxTLI uint32
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		tli, _, ok := ParseXLogFileName(e.Name(), 0)
		if !ok {
			continue
		}
		if tli > maxTLI {
			maxTLI = tli
		}
	}
	return maxTLI, nil
}

// WriteHistoryAfterRecovery checks whether the WAL directory contains
// segments on a timeline higher than persistedTLI and, if so, writes the
// missing <newTLI>.history file.  switchLSN is used as the end-of-life LSN
// for the old timeline entry (0 is valid — means "unknown at crash time").
//
// Returns (persistedTLI, false, nil) when no bump is needed (walTLI ≤
// persistedTLI or walDir is empty).  Returns (newTLI, true, nil) when a
// history file was written.
//
// Callers should update the persisted timeline_id to newTLI after a
// successful bump (wrote == true).
func WriteHistoryAfterRecovery(walDir string, persistedTLI uint32, switchLSN uint64) (newTLI uint32, wrote bool, err error) {
	walTLI, err := DiscoverLastWALTLI(walDir)
	if err != nil {
		return persistedTLI, false, err
	}
	if walTLI == 0 || walTLI <= persistedTLI {
		return persistedTLI, false, nil
	}
	// WAL segments carry a higher TLI than the timeline_id file. This
	// can happen when the server crashed after receiving a streaming
	// timeline switch but before the timeline_id file was updated (e.g.
	// crash between WriteHistory and WriteTimelineID in finalizePromotion).
	// Reconstruct the history chain and write the missing file.
	prev, err := ReadHistory(walDir, persistedTLI)
	if err != nil {
		return persistedTLI, false, fmt.Errorf("wal: read history for TLI %d: %w", persistedTLI, err)
	}
	entries := append(prev, TimelineHistoryEntry{
		TLI:       persistedTLI,
		SwitchLSN: switchLSN,
		Reason:    "recovered after primary promotion",
	})
	if werr := WriteHistory(walDir, walTLI, entries); werr != nil {
		return persistedTLI, false, fmt.Errorf("wal: write history for TLI %d: %w", walTLI, werr)
	}
	return walTLI, true, nil
}
