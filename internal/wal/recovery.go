package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

	// Kinds 18/19 (CreateDatabase/DropDatabase) retired in B4.6 Stage 3:
	// CREATE/DROP DATABASE now journal a real pg_database SHARED heap row
	// (global/1262, via XLOG_HEAP_INSERT/DELETE — Stage 1) for the catalog
	// identity + a real RM_DBASE XLOG_DBASE_CREATE_WAL_LOG / XLOG_DBASE_DROP
	// record (RmgrDbase=4) for the base/<oid> physical directory, so a real
	// PG18 standby replays both. A restart reloads the registry from the heap
	// (reloadDatabasesFromHeap) and physical replay recreates the directory.

	// Kinds 20/21 (CreateIndex/DropIndex) retired in B5 Slice A: the "goopg has
	// no pg_index heap" premise is stale — M0113 added a real pg_index heap
	// (2610) written at runtime by syncIndexToCatalogHeap alongside the pg_class
	// (relkind='i') row, and loadUserIndexesFromHeap reconstructs the full
	// in-memory Index (indkey/unique/primary/indrelid/opclass/collation/
	// indoption/predicate) from both. CREATE/DROP INDEX now journal only real
	// heap inserts/deletes on pg_class + pg_index, which a real PG standby
	// replays (no rmid-128 record). See kind 94 (RenameIndex, also retired).

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

	// Kinds 53-55 (Create/Drop/AlterSubscriptionOwner) retired in B4.4:
	// CREATE/DROP/ALTER SUBSCRIPTION OWNER now journal a real pg_subscription
	// heap row (SHARED, global/6100), and a restart reloads the PubSub registry
	// via reloadSubscriptionsFromHeap. (Publications converted in B3.3.)

	// Kinds 124/125 (CreateTablespace/DropTablespace) retired in B4.1e:
	// CREATE/DROP TABLESPACE now journals a real pg_tablespace heap row
	// (global/1213) + pg_shdepend owner dep + RM_TBLSPC XLOG_TBLSPC_CREATE/
	// DROP, and a restart reloads the registry from that heap
	// (reloadUserTablespacesFromHeap).

	// Kinds 67/68 (RoleState/DropRole) retired in B4.5: CREATE/ALTER/DROP ROLE
	// now journals a real pg_authid heap row (SHARED, global/1260) via
	// XLOG_HEAP_INSERT/DELETE + 2676/2677 index maintenance, and a restart
	// reloads the registry from that heap (reloadRolesFromAuthidHeap). See
	// kind 72 below (also retired in B4.5).

	// Kind 69 (ColumnDefaults) retired in B5 Slice B: column DEFAULT
	// expressions now journal as real pg_attrdef HEAP rows (base/<dbOid>/2604,
	// one per defaulted column, adbin = the expression as SQL text) via
	// XLOG_HEAP_INSERT — written by syncTableToCatalogHeap (writeAttrdefRow) and
	// reloaded by loadColumnDefaultsFromHeap. A real PG standby replays the heap
	// inserts (no rmid-128). Upstream analog: pg_attrdef
	// (postgres/src/backend/catalog/heap.c StoreAttrDefault).

	// Kind 72 (AlterRoleRename) retired in B4.5: ALTER ROLE ... RENAME TO is
	// just an attribute change on rolname, so it rides the same per-row
	// pg_authid heap re-sync (stamp by oid + write the row under the new
	// rolname) as CREATE/ALTER — see the kinds 67/68 note above.

	// Kinds 73-78 (AlterDatabase/RoleSetConfig/ResetConfig/ResetAllConfig)
	// retired in B4.2: ALTER DATABASE/ROLE SET/RESET now re-syncs a real
	// pg_db_role_setting heap row (SHARED, global/2964) from the registry, and
	// a restart reloads it via reloadDbRoleSettingsFromHeap.

	// Kinds 79/80 (Grant/RevokeRoleMembership) retired in B4.3: GRANT/REVOKE
	// role membership now journals a real pg_auth_members heap row (SHARED,
	// global/1261) re-synced per (roleid, member, grantor) from the registry,
	// and a restart reloads it via reloadRoleMembershipsFromHeap.

	// Kind 94 (RenameIndex) retired in B5 Slice A: ALTER INDEX RENAME now
	// rewrites the index's pg_class HEAP row with the new relname
	// (resyncIndexClassHeapRow — the index name lives in pg_class, not
	// pg_index), so loadUserIndexesFromHeap restores the rename and a real PG
	// standby replays it as a pg_class heap UPDATE.

	// Kinds 102 (CreateMatView) / 103 (CreateView) retired in B5 Slice C: a user
	// view / materialized view's defining SELECT now journals as a real
	// pg_rewrite _RETURN rule HEAP row (base/<dbOid>/2618, ev_action = the query
	// as SQL text) via XLOG_HEAP_INSERT — written by syncTableToCatalogHeap
	// (writeViewRewriteRow) and reloaded by loadViewsFromHeap. A real PG standby
	// replays the heap insert (no rmid-128). Upstream analog: pg_rewrite
	// (postgres/src/backend/rewrite/rewriteDefine.c). (matview IsPopulated across
	// a restart is a documented deferral — see the deferral ledger.)

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
	// Fork(1) + Block(4) + Offnum(2) = 16. Offnum joined in
	// M0130-S11.4 slice 3b-2c-ii-B2-b-ii (replay places the item at the
	// recorded offset instead of re-deriving the slot by key), matching the
	// heap-insert record, which has carried its line slot from the start.
	btreeInsertHeaderSize = 16
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
	return encodeCheckPointStruct(CheckPointFields{
		RedoLSN0:        redoLSN0,
		ThisTLI:         tli,
		NextXid:         nextXid,
		NextOid:         nextOid,
		OldestActiveXid: uint32(nextXid),
	})
}

// CheckPointFields is the live cluster state stamped into the PG18
// CheckPoint struct (M0131-S18.4). Before it existed, every member past
// `redo`/`ThisTimeLineID`/`nextXid`/`nextOid`/`oldestActiveXid` was a
// literal in the encoder — and two members, PrevTimeLineID and
// fullPageWrites, were never written AT ALL, so every goopg checkpoint
// record claimed `PrevTimeLineID = 0` and `full_page_writes = off`
// (upstream sets PrevTimeLineID = ThisTimeLineID for every checkpoint
// except an end-of-recovery one, xlog.c:7030-7034, and fullPageWrites
// from Insert->fullPageWrites, xlog.c:7041).
//
// A zero value is NOT a valid CheckPoint: withDefaults applies the same
// floors upstream's bootstrap does, so callers only fill what they know.
type CheckPointFields struct {
	// RedoLSN0 is the 0-based byte position of the checkpoint's redo
	// point. PG's xlogreader validates it against ReadRecPtr.
	RedoLSN0 uint64
	ThisTLI  uint32
	// PrevTLI defaults to ThisTLI. Only an end-of-recovery checkpoint
	// differs (it names the timeline forked off from).
	PrevTLI uint32
	// FullPageWrites mirrors the full_page_writes GUC as the buffer pool
	// currently sees it, not the postgresql.conf text: PG samples
	// Insert->fullPageWrites under the WAL insert lock.
	FullPageWrites bool
	WalLevel       uint32
	NextXid        uint64
	NextOid        uint32
	// NextMulti/NextMultiOffset/OldestMulti come from the multixact
	// allocator. A cluster that has never created a multixact reports
	// NextMulti = OldestMulti = FirstMultiXactId (1), NextMultiOffset = 0.
	NextMulti       uint32
	NextMultiOffset uint32
	OldestXid       uint32
	// OldestXidDB / OldestMultiDB name the database holding the horizon.
	// Upstream's bootstrap seeds both with Template1DbOid; a fresh PG 18
	// cluster reports 1 for each, so 0 (goopg's old value) was never a
	// value real PG writes.
	OldestXidDB   uint32
	OldestMulti   uint32
	OldestMultiDB uint32
	// OldestCommitTsXid / NewestCommitTsXid are InvalidTransactionId (0)
	// while track_commit_timestamp is off — which is goopg's only mode
	// today, and what a real PG 18 pg_controldata reports. goopg used to
	// hardcode 3 here, a value PG never writes with commit ts disabled.
	OldestCommitTsXid uint32
	NewestCommitTsXid uint32
	// OldestActiveXid: PG stamps InvalidTransactionId (0) on shutdown
	// checkpoints (recovery derives it from PrescanPreparedTransactions)
	// and GetOldestActiveTransactionId() on online ones.
	OldestActiveXid uint32
}

// firstMultiXactId mirrors FirstMultiXactId (multixact.h) — the lowest
// MultiXactId ever handed out; 0 is InvalidMultiXactId.
const firstMultiXactId = uint32(1)

// withDefaults applies the floors and mirrors upstream establishes so a
// partially-filled CheckPointFields still encodes a struct PG accepts.
func (f CheckPointFields) withDefaults() CheckPointFields {
	if f.ThisTLI == 0 {
		f.ThisTLI = 1
	}
	if f.PrevTLI == 0 {
		f.PrevTLI = f.ThisTLI
	}
	if f.WalLevel == 0 {
		f.WalLevel = 1 // replica
	}
	if f.NextXid < 3 { // FirstNormalTransactionId
		f.NextXid = 3
	}
	if f.NextOid < firstNormalObjectID {
		f.NextOid = firstNormalObjectID
	}
	if f.NextMulti < firstMultiXactId {
		f.NextMulti = firstMultiXactId
	}
	if f.OldestMulti < firstMultiXactId {
		f.OldestMulti = firstMultiXactId
	}
	if f.OldestXid < 3 {
		f.OldestXid = 3
	}
	if f.OldestXidDB == 0 {
		f.OldestXidDB = template1DbOid
	}
	if f.OldestMultiDB == 0 {
		f.OldestMultiDB = template1DbOid
	}
	return f
}

// firstNormalObjectID mirrors FirstNormalObjectId (transam.h).
const firstNormalObjectID = uint32(16384)

// template1DbOid mirrors Template1DbOid (pg_database_d.h) — the database
// upstream's bootstrap names as the holder of the xid/multi horizons.
const template1DbOid = uint32(1)

// encodeCheckPointStruct builds the raw 88-byte PG18 CheckPoint struct.
func encodeCheckPointStruct(f CheckPointFields) []byte {
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
	//
	// M0131-S18.4: every remaining literal is now a CheckPointFields
	// member, and offsets 12 (PrevTimeLineID) and 16 (fullPageWrites) are
	// written for the first time — they were skipped entirely, so a real
	// PG reading a goopg checkpoint saw PrevTimeLineID = 0 (the pad-zero)
	// and full_page_writes = off.
	const checkPointSize = 88
	f = f.withDefaults()
	payload := make([]byte, checkPointSize)
	le := binary.LittleEndian
	now := time.Now()

	le.PutUint64(payload[0:8], f.RedoLSN0)  // redo
	le.PutUint32(payload[8:12], f.ThisTLI)  // ThisTimeLineID
	le.PutUint32(payload[12:16], f.PrevTLI) // PrevTimeLineID
	if f.FullPageWrites {
		payload[16] = 1 // fullPageWrites (bool; offsets 17-19 stay pad)
	}
	le.PutUint32(payload[20:24], f.WalLevel)        // wal_level
	le.PutUint64(payload[24:32], f.NextXid)         // nextXid (>= FirstNormalTxnId)
	le.PutUint32(payload[32:36], f.NextOid)         // nextOid (>= FirstNormalObjectId)
	le.PutUint32(payload[36:40], f.NextMulti)       // nextMulti
	le.PutUint32(payload[40:44], f.NextMultiOffset) // nextMultiOffset
	le.PutUint32(payload[44:48], f.OldestXid)       // oldestXid
	le.PutUint32(payload[48:52], f.OldestXidDB)     // oldestXidDB
	le.PutUint32(payload[52:56], f.OldestMulti)     // oldestMulti
	le.PutUint32(payload[56:60], f.OldestMultiDB)   // oldestMultiDB
	// time (pg_time_t=int64, 8-byte aligned → starts at offset 64)
	le.PutUint64(payload[64:72], uint64(now.Unix())) // time
	// After time (offset 72): oldestCommitTsXid, newestCommitTsXid,
	// oldestActiveXid. Each is TransactionId (uint32, 4 bytes).
	// NOTE: pg_time_t alignment forces 4-byte pad before time, pushing
	// offsets: time=64, oldestCommitTsXid=72, newestCommitTsXid=76,
	// oldestActiveXid=80, sizeof(CheckPoint)=88.
	le.PutUint32(payload[72:76], f.OldestCommitTsXid) // oldestCommitTsXid
	le.PutUint32(payload[76:80], f.NewestCommitTsXid) // newestCommitTsXid
	le.PutUint32(payload[80:84], f.OldestActiveXid)   // oldestActiveXid

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
// caller stored on the page (the index's on-page nbtree tuple —
// internal/access/btree indexFormat.marshal output), and `offnum` is the
// physical 1-based offset number it was stored at, which is where replay puts
// it back (M0130-S11.4 slice 3b-2c-ii-B2-b-ii).
func EncodeBtreeInsert(rel storage.RelFileNode, blk storage.BlockNumber, offnum uint16, item []byte) []byte {
	out := make([]byte, btreeInsertHeaderSize+len(item))
	out[0] = RecordKindBtreeInsert
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	binary.LittleEndian.PutUint16(out[14:16], offnum)
	copy(out[btreeInsertHeaderSize:], item)
	return out
}

// DecodeBtreeInsert returns the rel + block + offset number + raw item bytes
// carried by a BtreeInsert record payload.
func DecodeBtreeInsert(payload []byte) (rel storage.RelFileNode, blk storage.BlockNumber, offnum uint16, item []byte, err error) {
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
	offnum = binary.LittleEndian.Uint16(payload[14:16])
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
	return ReplayRecordsFrom(mgr, records, 0)
}

// ReplayRecordsFrom is ReplayRecords with an explicit redo pointer, which is
// where upstream actually gets its replay start: InitWalRecovery reads
// checkPoint.redo out of the control file's checkPointCopy and hands it to
// PerformWalRecovery, which never searches the stream for a checkpoint
// (postgres/src/backend/access/transam/xlogrecovery.c:597-707). goopg has
// always scanned instead, which works only for WAL goopg itself wrote in a
// shape its own isCheckpointRecord recognises.
//
// redoLSN == 0 means "no control-file redo available" and keeps the
// goopg-authored scan (replayStart) as the fallback: a fresh cluster, a
// hand-assembled directory with no pg_control, or the standalone
// ReplayFromDir entry point. M0131-S20.2.
//
// The pointer is trusted the way upstream trusts it, and the one way it can
// be wrong is safe: goopg's checkpointer appends the checkpoint record first
// and updates pg_control afterwards (checkpointer.go runCheckpoint), so a
// crash between the two leaves an OLDER redo in pg_control, which replays a
// superset. Replay is idempotent — pages carry pd_lsn guards and CLOG stamps
// are terminal-state writes — so replaying too much costs time, never
// correctness. Replaying too little is the direction that loses data, and
// pg_control can never point past the WAL it was written from.
func ReplayRecordsFrom(mgr *storage.Manager, records []Record, redoLSN uint64) (ReplayStats, error) {
	stats := ReplayStats{Records: len(records)}
	startIdx, checkpointLSN := replayStartAt(records, redoLSN)
	stats.CheckpointLSN = checkpointLSN

	// M0131-S25: refuse the whole index-AM boundary up front, before the first
	// page is written. The per-record arm in replayDecodedXLogRecord would
	// catch these anyway, but only one at a time and only after the prefix has
	// already been applied — so a cluster with a GIN and a BRIN index costs two
	// failed starts to diagnose and leaves a half-advanced data directory
	// behind each time. Scanning the replay range (not the whole slice: records
	// before the redo point are already durable in the pages) turns that into
	// one refusal that names every AM present and mutates nothing.
	if err := preflightIndexAMRecords(records[startIdx:]); err != nil {
		return stats, err
	}

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
		// A decoded rmid-128 record carries no physical page state, so replay
		// is a no-op. Phase B5 retired every goopg-private catalog/DDL kind that
		// used to land here (they now journal as real heap/btree records reloaded
		// from the catalog heaps by internal/initdb/catalog_heap_reload.go, not a
		// WAL re-scan), so a fresh B5 cluster never reaches this arm; it survives
		// only to no-op any legacy rmid-128 record in pre-B5 on-disk WAL.
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
		case xlogXLogFPIForHint:
			// M0131-S16.4: upstream xlog_redo handles XLOG_FPI_FOR_HINT on the
			// SAME arm as XLOG_FPI (xlog.c:8748) — both carry nothing but block
			// references and are replayed by restoring their images. The only
			// difference is tolerance: a FOR_HINT block may legitimately carry
			// no image (the hint bit was already durable via a concurrent FPI),
			// where a bare XLOG_FPI without one is an ERROR upstream. goopg
			// never emits FOR_HINT (rmgr_map.go maps PageImage → XLOG_FPI), so
			// this arm exists purely for a real-PG crash tail; before S16.4 it
			// fell into the default no-op and the hint-bit page was DROPPED.
			if err := replayDecodedXLogHeapFPIBlocks(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogXLogCheckpointShutdown, xlogXLogCheckpointOnline,
			xlogXLogNoop, xlogXLogSwitch, xlogXLogBackupEnd,
			xlogXLogRestorePoint, xlogXLogFPWChange, xlogXLogEndOfRecovery,
			xlogXLogOverwriteContrecord, xlogXLogCheckpointRedo,
			xlogInfoDefault:
			// M0131-S16.4: the ENUMERATED benign set. Each of these is a
			// genuine physical no-op for goopg's page-level replay — upstream
			// xlog_redo either does nothing at all (NOOP, SWITCH,
			// RESTORE_POINT, CHECKPOINT_REDO) or updates only shared-memory /
			// recovery bookkeeping goopg does not maintain (the two CHECKPOINT
			// opcodes, BACKUP_END, END_OF_RECOVERY, FPW_CHANGE,
			// OVERWRITE_CONTRECORD). Checkpoint contents ARE consumed by goopg,
			// but through the separate control-file/redo-start path, not here.
			//
			// xlogInfoDefault (0xF0) is the odd one out: it is not a PG opcode
			// at all but goopg's own classifyXLogRecord marker for an
			// EMPTY-payload record (format.go:151-153). It must stay a no-op
			// or goopg refuses to replay its own WAL. Keeping it here rather
			// than in the refused set costs no real-PG coverage, because PG's
			// opcode space is the high nibble only and it defines nothing at
			// 0xF0 — a real PG producer can never emit this value.
			return false, nil
		case xlogXLogNextOid:
			// M0131-S21a: XLOG_NEXTOID is the opcode S16.4 refused rather than
			// dropped, because xlog_redo sets nextOid EXACTLY from it
			// (xlog.c:8292-8308) and losing one lets goopg re-issue OIDs a
			// crashed PG had already allocated after the last checkpoint.
			//
			// It is now APPLIED — but not here. The OID counter lives in the
			// in-memory catalog (catalog.InMemory.AdvanceNextOIDPast), which
			// this pass cannot reach: replayDecodedXLogRecord is handed a
			// *storage.Manager and nothing else. The record is therefore a
			// page-level no-op recognised here and re-applied by
			// replayNextOIDFromWAL in internal/initdb — the same two-pass split
			// that already routes CLOG_TRUNCATE (see the RmgrCLOG arm below)
			// and the xact commit/abort stamps. Physical replay stays a pure
			// page function; the counter recovery rides the initdb scan that
			// seeds nextOid from pg_control in the first place.
			return false, nil
		default:
			// M0131-S16.4: everything else is refused rather than silently
			// skipped. goopg emits no RM_XLOG opcode outside the named arms,
			// so this cannot fire on goopg's own WAL.
			return false, fmt.Errorf("%w: %s", ErrUnsupportedRecord, unsupportedDecodedXLogRecord(r))
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
		case xlogXactAssignment, xlogXactInvalidations:
			// M0131-S21a: two recovery-bookkeeping records goopg never emits
			// but that appear in any real-PG tail. Neither touches a page.
			//
			// XLOG_XACT_ASSIGNMENT reports a batch of subtransaction→parent
			// links so a hot standby can populate KnownAssignedXids;
			// xact_redo's arm calls ProcArrayApplyXidAssignment and nothing
			// else (xact.c:6429-6435), and goopg rebuilds its own subxact map
			// from its native subxact markers (RecordKindXactAssignment). A
			// real-PG assignment record is therefore informational here.
			//
			// XLOG_XACT_INVALIDATIONS carries the invalidation messages of a
			// transaction that has not committed yet, again for standby
			// relcache maintenance — and upstream's arm ignores it outright
			// ("what matters are invalidations written into the commit
			// record", xact.c:6437-6443). goopg's relcache-file
			// unlink rides the COMMIT record's HAS_INVALS payload instead
			// (see the xlogXactCommit arm above), which is the message set
			// that actually became visible.
			return false, nil
		case xlogXactPrepare, xlogXactCommitPrepared, xlogXactAbortPrepared:
			// M0131-S21a: two-phase commit is out of scope — goopg's
			// max_prepared_transactions BootVal is "0", so it can neither
			// produce these records nor rebuild the pg_twophase state a
			// PREPARED transaction needs. Refuse with a message that names
			// the reason: replaying the COMMIT_PREPARED of a transaction
			// whose PREPARE we skipped would stamp an XID committed whose
			// heap changes were never applied.
			return false, fmt.Errorf("%w: %s (two-phase commit recovery is not supported; "+
				"max_prepared_transactions must be 0 on a cluster goopg starts)",
				ErrUnsupportedRecord, unsupportedDecodedXLogRecord(r))
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrSeq:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogSeqLog:
			// B1.3b: rebuild the 1-tuple sequence page from the logged
			// tuple (seq_redo analog; whole-page replace = idempotent).
			if err := replayDecodedXLogSeqLog(mgr, xlog.MainData); err != nil {
				return false, err
			}
			return true, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrLogicalMessage:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogLogicalMessage:
			// M0131-S23. A recognised no-op here is BYTE-EXACT parity, not an
			// approximation: upstream's logicalmsg_redo
			// (postgres/src/backend/replication/logical/message.c:83-97) has an
			// empty body under the comment "Redo is basically just noop for
			// logical decoding messages." The payload exists only for a logical
			// decoder reading the WAL stream; nothing in it reaches a page.
			//
			// It matters because a single `pg_logical_emit_message()` in a
			// crashed PG's tail used to make the whole rest of that tail
			// invisible (S16: rmid 21 exceeded the decoder's structural bound
			// and the reader called it end-of-WAL).
			return false, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrReplicationOrigin:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogReplOriginSet, xlogReplOriginDrop:
			// M0131-S23. replorigin_redo (origin.c) mutates exactly one thing:
			// the in-shmem `replication_states` array — SET calls
			// replorigin_advance(..., wal_log=false) and DROP clears the
			// matching slot. Both are pure memory; the durable copy lives in
			// pg_logical/replorigin_checkpoint, written at checkpoint time.
			//
			// goopg has no logical-replication apply worker, hence no consumer
			// for that state and no replorigin_checkpoint of its own to keep in
			// step, so a documented no-op is the whole correct behaviour here
			// rather than a shortcut. If goopg ever grows an apply worker, this
			// arm has to grow with it (ledger row) — replaying a subscription's
			// tail without advancing its origin would re-apply changes.
			return false, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrGeneric:
		// M0131-S23. RM_GENERIC_ID has no opcode space: GenericXLogFinish
		// inserts with info 0 (generic_xlog.c:399) and generic_redo never reads
		// xl_info, so a non-zero info is a corrupt or future record.
		if xlog.Header.Info&XLRRmgrInfoMask != xlogGenericInfo {
			return false, unsupportedDecodedXLogRecord(r)
		}
		if err := replayDecodedXLogGeneric(mgr, r, xlog); err != nil {
			return false, err
		}
		return true, nil
	case RmgrCommitTs:
		return replayDecodedXLogCommitTs(mgr, r, xlog)
	case RmgrHash, RmgrGin, RmgrGist, RmgrSPGist, RmgrBrin:
		// M0131-S25: the index-AM boundary. These five have no redo in goopg
		// and no shortcut that could stand in for one (index_am_refusal.go
		// documents why an FPI-only replay is provably wrong for them — GiST's
		// NSN is not in the image, and REGBUF_WILL_INIT blocks carry no image
		// at all). The one thing that CAN improve is the refusal itself: name
		// the access method, the opcode, the LSN and the relation, so the
		// message tells an operator which index to drop rather than which
		// rmgrlist.h line to look up.
		return false, indexAMUnsupportedError(r, xlog)
	case RmgrRelMap:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogRelmapUpdate:
			// B0.4: rewrite the target pg_filenode.map from the record's
			// image (CRC-verified; whole-file replace = idempotent).
			if err := replayDecodedXLogRelmapUpdate(mgr.DataDir(), xlog.MainData); err != nil {
				return false, err
			}
			return true, nil
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
		case xlogStandbyLock, xlogStandbyInvalidations:
			// M0131-S21a: the remaining two RM_STANDBY opcodes. Upstream's
			// standby_redo takes the AccessExclusiveLock / processes the
			// invalidation messages only to keep a HOT STANDBY's queries
			// consistent with the primary; every arm is reached through code
			// that returns immediately when standbyState == STANDBY_DISABLED
			// (standby.c:1170-1172), which is what a crash-recovery start
			// always is. goopg runs no concurrent queries during replay and
			// holds no relcache init files open, so both are genuine no-ops
			// here — recognised so a real-PG tail (every DDL emits a
			// STANDBY_LOCK) does not refuse the start.
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
		case xlogSmgrTruncate:
			// M0131-S21a-2 part 6: every TABLE/INDEX TRUNCATE (the physical
			// half — XLOG_HEAP_TRUNCATE above is the logical-decoding-only
			// no-op) and every VACUUM tail truncation. goopg emits its own
			// truncate-to-zero as the native RecordKindSmgrTruncate, so this
			// arm is reached only by a real-PG record — which, unlike
			// goopg's, can truncate the main fork to a NON-zero surviving
			// prefix and independently truncate the vm/fsm forks.
			rel, blkno, flags, err := decodeXLogSmgrTruncate(xlog.MainData)
			if err != nil {
				return false, err
			}
			if err := applySmgrTruncate(mgr, rel, blkno, flags); err != nil {
				return false, err
			}
			return true, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrTblspc:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogTblspcCreate:
			// B4.1d: recreate the pg_tblspc/<oid> directory (idempotent). A
			// real PG standby's tblspc_redo does the same from the ts_path;
			// goopg's own dirs persist on disk across restart, so this is a
			// defensive MkdirAll for the basebackup-restore edge.
			oid, _, err := decodeXLogTblspcCreate(xlog.MainData)
			if err != nil {
				return false, err
			}
			if err := applyTblspcCreate(mgr, oid); err != nil {
				return false, err
			}
			return true, nil
		case xlogTblspcDrop:
			oid, err := decodeXLogTblspcDrop(xlog.MainData)
			if err != nil {
				return false, err
			}
			if err := applyTblspcDrop(mgr, oid); err != nil {
				return false, err
			}
			return true, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrDbase:
		switch xlog.Header.Info & XLRRmgrInfoMask {
		case xlogDbaseCreateWalLog, xlogDbaseCreateFileCopy:
			// B4.6 Stage 3: recreate base/<db_id> (idempotent). A real PG
			// standby's dbase_redo does the same; goopg's own dirs persist
			// across restart, so this is a defensive MkdirAll for the
			// basebackup-restore edge. The copied relation blocks arrive as
			// separate full-page-image records (Stage 3b).
			dbOID, _, err := decodeXLogDbaseCreateWalLog(xlog.MainData)
			if err != nil {
				return false, err
			}
			if err := applyDbaseCreate(mgr, dbOID); err != nil {
				return false, err
			}
			return true, nil
		case xlogDbaseDrop:
			dbOID, err := decodeXLogDbaseDrop(xlog.MainData)
			if err != nil {
				return false, err
			}
			if err := applyDbaseDrop(mgr, dbOID); err != nil {
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
		case xlogClogZeroPage:
			// M0131-S21a-2 part 5: unlike CLOG_TRUNCATE, ZEROPAGE needs no
			// catalog/txn-manager handle — it is pure page-zeroing at a fixed
			// segment offset (WriteZeroPageXlogRec, clog.c:1073-1078; fires
			// once per 32768 XIDs, right before the first commit/abort into a
			// fresh page), so it is re-applied HERE in the physical pass
			// rather than deferred to replayCLogFromWAL. goopg's own
			// clogBufferPool fault-in already treats a missing/short segment
			// as all-zero (readPageFromDisk, clog_bufferpool.go:181-202), so
			// this arm is not needed for goopg's own reads — it is needed
			// because upstream `SimpleLruReadPage` treats a missing segment
			// as a hard error (slru.c), not a lenient zero-fill: a real PG
			// standby cold-starting on this cluster, or any tool that reads
			// pg_xact/ directly (pg_resetwal, amcheck), expects the segment
			// file for every XID range that was ever assigned to physically
			// exist. Without this arm, a crashed PG's post-checkpoint tail
			// that allocated a fresh CLOG page leaves that segment absent.
			if err := replayDecodedXLogClogZeroPage(mgr, xlog); err != nil {
				return false, err
			}
			return true, nil
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
		case xlogHeapLock:
			// M0131-S21a-2: every SELECT ... FOR UPDATE/SHARE, every foreign-key
			// check, and the tuple lock an UPDATE takes before rewriting a row.
			// goopg emits its own row locks as the native RecordKindHeapLock
			// (payload[0] = 10, replayHeapLock), so this arm is reached only by a
			// real-PG record — which, unlike goopg's, can carry a multixact xmax
			// or an updater's key-share lock.
			if err := replayDecodedXLogHeapLock(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeapConfirm:
			// M0131-S21a-2: the second half of every INSERT ... ON CONFLICT.
			if err := replayDecodedXLogHeapConfirm(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeapTruncate:
			// M0131-S21a: recognised, deliberately not implemented. Upstream's
			// heap_redo says it outright — "TRUNCATE is a no-op because the
			// actions are already logged as SMGR WAL records" (heapam_xlog.c:
			// 1201-1208): the physical effect of a TRUNCATE arrives as
			// XLOG_SMGR_TRUNCATE (and, for a table created in the same
			// transaction, as the new relfilenode's XLOG_SMGR_CREATE). The
			// xl_heap_truncate body carries only the relation OIDs, for
			// logical decoding — which is also why PG only emits it at
			// wal_level=logical (tablecmds.c:2303).
			return false, nil
		default:
			return false, unsupportedDecodedXLogRecord(r)
		}
	case RmgrHeap2:
		// M0131-S21a: RM_HEAP2 shares RM_HEAP's XLOG_HEAP_OPMASK (0x70), NOT
		// the generic 0xF0 rmgr-info mask. Upstream ORs XLOG_HEAP_INIT_PAGE
		// (0x80) into the info byte of a MULTI_INSERT onto a freshly extended
		// page (heapam.c:2607-2611), so a COPY arrives as info == 0xD0. Masked
		// with 0xF0 that value matches no case and the record is refused;
		// masked with 0x70 it is the MULTI_INSERT it actually is. Fixing the
		// mask BEFORE the multi-insert redo lands (S21a-2) keeps the two
		// changes from having to be correct simultaneously.
		switch xlog.Header.Info & xlogHeapOpMask {
		case xlogHeap2PruneOnAccess, xlogHeap2PruneVacuumScan, xlogHeap2PruneVacuumClean:
			// A7: goopg's opportunistic prune / VACUUM prune / freeze all emit
			// xl_heap_prune (block-0 sub-records: redirects, now-unused, freeze
			// plans). Replay applies them to the page; a real-PG record carrying
			// a full-page image is restored via the FPI branch.
			if err := replayDecodedXLogHeapPrune(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeap2MultiInsert:
			// M0131-S21a-2: every COPY. heap_multi_insert packs one page's worth
			// of tuples into a single record, so a PG crash tail taken during a
			// bulk load is made almost entirely of these. Note this case is
			// reachable only because the switch masks with xlogHeapOpMask (0x70)
			// — a COPY onto a freshly extended page arrives as 0xD0, MULTI_INSERT
			// OR'd with XLOG_HEAP_INIT_PAGE.
			if err := replayDecodedXLogHeapMultiInsert(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeap2Visible:
			// M0131-S21a-2 part 3: every VACUUM page it marks all-visible, plus
			// the freeze an INSERT does on a page it filled itself. goopg emits
			// its own VM updates as the native RecordKindHeapVisible (payload[0]
			// = 29), whose ApplyRecord arm is a documented no-op, so this arm is
			// reached only by a real-PG record — and unlike the native one it
			// really writes the visibility-map fork.
			if err := replayDecodedXLogHeap2Visible(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeap2LockUpdated:
			// M0131-S21a-2 part 4: XLOG_HEAP_LOCK's near-sibling — a tuple-lock
			// request (FOR UPDATE/SHARE, an FK RI check) that finds its target
			// already updated by a concurrent live transaction re-locks the
			// newest visible version of the chain instead, on RM_HEAP2 because
			// the chain walk can cross multiple row versions.
			if err := replayDecodedXLogHeap2LockUpdated(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogHeap2NewCid:
			// M0131-S21a: XLOG_HEAP2_NEW_CID exists solely so logical decoding
			// can map a catalog tuple to the command id that produced it;
			// heap2_redo's arm is empty apart from an Assert that recovery is
			// not supposed to see it in any consuming form
			// (heapam_xlog.c:1244-1252 — "Nothing to do on a real replay, only
			// used during logical decoding"). Recognised so a wal_level=logical
			// PG tail does not refuse the start.
			return false, nil
		case xlogHeap2Rewrite:
			// M0131-S21a-2 part 7: a loud refusal, deliberately not a redo and
			// deliberately not a no-op. XLOG_HEAP2_REWRITE is emitted while a
			// VACUUM FULL / CLUSTER rewrites a table whose pre-rewrite row
			// versions a logical replication slot may still have to decode
			// (rewriteheap.c:894 — reachable only at wal_level=logical, and
			// only for a relation logical decoding can reach). Its redo writes
			// no relation page: it truncates a pg_logical/mappings/ file to the
			// record's offset and rewrites the mapping tail from
			// old-ctid → new-ctid, then fsyncs it (heap_xlog_logical_rewrite,
			// rewriteheap.c:1073-1160).
			//
			// goopg has no pg_logical/mappings consumer, so there is no honest
			// middle: replaying it would mean maintaining a file nothing reads,
			// and skipping it silently would leave a slot on the resulting
			// cluster decoding the rewritten table against mappings that stop
			// mid-rewrite — the pre-rewrite tuples become undecodable without
			// anything reporting an error. Refusing names the feature instead,
			// so the operator sees "goopg cannot start on this $PGDATA because
			// of a logical-slot table rewrite" rather than a silently divergent
			// slot. ErrUnsupportedRecord (not the bare error) is what keeps the
			// reader from mistaking this for end-of-WAL and overwriting every
			// record after it (format.go, M0131-S16.2).
			return false, fmt.Errorf("%w: %s (logical-decoding rewrite mappings are not supported; "+
				"this cluster ran VACUUM FULL/CLUSTER on a table with a logical replication slot)",
				ErrUnsupportedRecord, unsupportedDecodedXLogRecord(r))
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
			if err := replayDecodedXLogBtreeInsert(mgr, r, xlog, true, false, false); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeInsertPost:
			// M0131-S21b part 2: PG-only — a leaf insert whose heap TID landed
			// inside an existing posting list, so the primary split that
			// posting (nbtinsert.c:_bt_insertonpg, `postingoff > 0`). goopg's
			// own inserts never take this path (they append to a posting), but
			// any real PG index built with deduplication on — the default —
			// emits this opcode routinely. Block 0's data run is
			// {uint16 postingoff, orignewitem}, not a bare item.
			if err := replayDecodedXLogBtreeInsert(mgr, r, xlog, true, false, true); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeDedup:
			// M0131-S21b part 2b: PG-only — the leaf deduplication pass that
			// merges runs of equal-key tuples into posting lists to postpone a
			// split (_bt_dedup_pass, nbtdedup.c). goopg never runs one, but a
			// real PG index built with deduplication on — the default — emits
			// this opcode routinely, and it is the very record S16.3 named when
			// it turned the old silent FPI-only `default:` arm into a refusal.
			// Block 0's data run is an array of BTDedupInterval, and redo
			// rebuilds the whole page from the pre-image under those bounds.
			if err := replayDecodedXLogBtreeDedup(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeDelete:
			// M0131-S21b part 3: PG-only — the LP_DEAD "simple deletion" pass
			// (_bt_delitems_delete, nbtpage.c). Every index scan that lands on
			// a dead heap tuple marks the entry, and the next insert short of
			// room deletes the marked ones, so this is ordinary crash-tail
			// traffic even though goopg has no such pass. Block 0's data run is
			// {deleted offsets, updated offsets, xl_btree_update array} — the
			// updates being the posting-list tuples that lost SOME of their
			// TIDs, which is the half goopg refused until this slice.
			if err := replayDecodedXLogBtreeDelete(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeReusePage:
			// M0131-S21b part 3: PG-only and a genuine NO-OP. Upstream's
			// btree_xlog_reuse_page (nbtxlog.c:1006-1015) has exactly one
			// statement — `if (InHotStandby) ResolveRecoveryConflictWith
			// SnapshotFullXid(...)` — and mutates no page: the record exists
			// only so a standby can cancel queries that might still read the
			// recycled page. Crash recovery is not hot standby, so upstream
			// itself does nothing here. It is named rather than left to the
			// `default:` arm because that arm now REFUSES a record without a
			// full-page image on every block (S16.3), and this record has no
			// blocks at all.
			return true, nil
		case xlogBtreeInsertUpper:
			// M0131-S21b part 1: PG-only — goopg's own downlink inserts ride
			// RecordKindBtreeSplit/NewRoot. An insert into an internal page also
			// finishes the child's split, so redo clears the child's
			// BTP_INCOMPLETE_SPLIT flag off block 1 (nbtxlog.c:160-177).
			if err := replayDecodedXLogBtreeInsert(mgr, r, xlog, false, false, false); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeInsertMeta:
			// M0131-S21b part 1: INSERT_UPPER plus the metapage rewrite PG folds
			// in when the internal insert also moved the fast root
			// (nbtinsert.c:1346-1361).
			if err := replayDecodedXLogBtreeInsert(mgr, r, xlog, false, true, false); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeMetaCleanup:
			// M0131-S21b part 1: upstream's whole redo for this opcode is
			// `_bt_restore_meta(record, 0)` (nbtxlog.c) — VACUUM's
			// _bt_set_cleanup_info stamping btm_last_cleanup_num_delpages on the
			// metapage, with no other page touched. The metapage is block 0 here,
			// not block 2 as in the insert/newroot records.
			if err := replayDecodedXLogBtreeRestoreMeta(mgr, xlog, storage.LSN(r.EndLSN), 0, "btree-meta-cleanup"); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeNewRoot:
			// M0130-S11.5a: goopg emits the real xl_btree_newroot — main data
			// {rootblk, level}, block-0 item area, block-1 left child, block-2
			// xl_btree_metadata — with no FPI. A real-PG newroot carrying one
			// is restored via the FPI branch inside the replay function.
			if err := replayDecodedXLogBtreeNewRoot(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeSplitL, xlogBtreeSplitR:
			// M0130-S11.5b: goopg emits the real xl_btree_split — main data
			// {level, firstrightoff, newitemoff, postingoff}, block 0 the left
			// half (incrementally since S11.5b-2, as an image when the split is
			// not describable), block 1 the right sibling's item area as block
			// data with no image, block 2 the relinked old sibling.
			//
			// _L vs _R is upstream's only record of which half the new item
			// landed on, and block 0's data run is untagged, so the opcode is
			// what tells redo whether a new item precedes the high key.
			if err := replayDecodedXLogBtreeSplit(mgr, r, xlog, xlog.Header.Info&XLRRmgrInfoMask == xlogBtreeSplitL); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeVacuum:
			// M0130-S11.5c: goopg emits the real xl_btree_vacuum — main data
			// {ndeleted, nupdated} plus, when the rewrite is expressible as a
			// deletion, the deleted offset numbers as block-0 data with no
			// image. The image form (ndeleted = 0) is restored inside the
			// replay function.
			if err := replayDecodedXLogBtreeVacuum(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeMarkPageHalfDead:
			// M0130-S11.5d-1: goopg emits the real xl_btree_mark_page_halfdead —
			// main data {poffset, leafblk, leftblk, rightblk, topparent}, block 0
			// the leaf WILL_INIT with no data (redo rebuilds the half-dead page),
			// block 1 the subtree parent whose downlink is removed.
			if err := replayDecodedXLogBtreeMarkPageHalfDead(mgr, r, xlog); err != nil {
				return false, err
			}
			return true, nil
		case xlogBtreeUnlinkPage, xlogBtreeUnlinkPageMeta:
			// M0130-S11.5d-2: goopg emits the real xl_btree_unlink_page — main
			// data {leftsib, rightsib, level, safexid, leafleftsib,
			// leafrightsib, leaftopparent}, block 0 the target WILL_INIT with
			// no data (redo rewrites it as an empty deleted page), blocks 1/2
			// the siblings, block 3 the half-dead leaf for an internal target,
			// block 4 the metapage on the _META variant.
			if err := replayDecodedXLogBtreeUnlinkPage(mgr, r, xlog, xlog.Header.Info&XLRRmgrInfoMask == xlogBtreeUnlinkPageMeta); err != nil {
				return false, err
			}
			return true, nil
		default:
			// Other btree records (dedup / delete / reuse-page / …) have no
			// native redo in goopg yet; the only way to apply one is to restore
			// every block it mutated from a full-page image.
			//
			// M0131-S16.3: that is a CONDITION, not an assumption. This arm
			// used to call replayDecodedXLogHeapFPIBlocks unconditionally,
			// which silently `continue`s past any block lacking an apply-image
			// and then reported applied=true. For goopg's own WAL that was
			// harmless (goopg emits none of these opcodes — every kind
			// rmgr_map.go maps to RmgrBtree has a named arm above). For a real
			// PG crash tail it is data loss: PG emits an FPI only on a page's
			// FIRST touch after a checkpoint, so the second XLOG_BTREE_DEDUP
			// on a page carries block DATA and no image — and we dropped the
			// mutation while telling the caller it had been applied. Refuse
			// instead, so an unreplayable index change stops the start rather
			// than corrupting the index silently.
			if err := requireFullPageImages(r, xlog); err != nil {
				return false, err
			}
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

// replayDecodedXLogGeneric applies an RM_GENERIC_ID record, mirroring
// generic_redo + applyPageRedo (generic_xlog.c:451-533). M0131-S23.
//
// RM_GENERIC is the one missing rmgr that can be implemented COMPLETELY with
// zero access-method knowledge: GenericXLogFinish computes a byte-level diff of
// the before/after page images and logs it as an opaque run of
// (offset uint16, length uint16, bytes[length]) triples, so redo is a
// memcpy loop. Only extensions (contrib/bloom, third-party AMs) emit these
// records, which is why the arm is cheap rather than urgent — but "cheap and
// exactly right" beats a refusal that would stop an otherwise-startable
// cluster.
//
// Two details of upstream's loop are load-bearing and easy to drop:
//
//   - the "hole" between pd_lower and pd_upper is zeroed AFTER the delta is
//     applied. GenericXLogFinish diffs the page with the hole already zeroed
//     (it never logs bytes inside it), so a redo that leaves the pre-image's
//     stale hole bytes in place produces a page that differs from the primary's
//     byte for byte — invisible to queries, fatal to a checksum or a
//     wal_consistency_checking comparison.
//   - a block carrying a full-page image is RESTORED, not deltaed: upstream's
//     XLogReadBufferForRedo returns BLK_RESTORED and skips the apply entirely.
//     Applying a delta computed against the pre-image on top of the restored
//     post-image page would double-apply it.
func replayDecodedXLogGeneric(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	if len(xlog.Blocks) == 0 {
		return fmt.Errorf("wal: xlog generic: record has no block references")
	}
	for i, block := range xlog.Blocks {
		if block.HasImage && block.ImageApply {
			if err := restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN)); err != nil {
				return fmt.Errorf("wal: xlog generic block %d: %w", i, err)
			}
			continue
		}
		page, skip, err := redoExistingHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN))
		if err != nil {
			return fmt.Errorf("wal: xlog generic block %d: %w", i, err)
		}
		if skip {
			continue
		}
		if err := applyGenericPageDelta(page, block.Data); err != nil {
			return fmt.Errorf("wal: xlog generic block %d: %w", i, err)
		}
		storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
		if err := mgr.WriteBlock(block.Rel, block.Block, page); err != nil {
			return err
		}
	}
	return nil
}

// applyGenericPageDelta is upstream's applyPageRedo (generic_xlog.c:451-472)
// plus the hole-zeroing generic_redo does right after it, and the bounds checks
// upstream can omit because it trusts its own writer.
func applyGenericPageDelta(page storage.Page, delta []byte) error {
	for off := 0; off < len(delta); {
		if len(delta)-off < 4 {
			return fmt.Errorf("truncated delta header at byte %d of %d", off, len(delta))
		}
		start := int(binary.LittleEndian.Uint16(delta[off:]))
		length := int(binary.LittleEndian.Uint16(delta[off+2:]))
		off += 4
		if len(delta)-off < length {
			return fmt.Errorf("truncated delta chunk at byte %d: want %d bytes, have %d", off, length, len(delta)-off)
		}
		if start+length > len(page) {
			return fmt.Errorf("delta chunk [%d,%d) is outside the page", start, start+length)
		}
		copy(page[start:start+length], delta[off:off+length])
		off += length
	}
	hdr := storage.MustHeader(page)
	lower, upper := int(hdr.Lower()), int(hdr.Upper())
	if lower > upper || upper > len(page) {
		return fmt.Errorf("delta produced an inconsistent page (pd_lower=%d pd_upper=%d)", lower, upper)
	}
	for i := lower; i < upper; i++ {
		page[i] = 0
	}
	return nil
}

// replayDecodedXLogCommitTs handles RM_COMMIT_TS_ID. M0131-S23.
//
// The rmgr has exactly two opcodes and NEITHER carries a commit timestamp: the
// timestamps themselves ride xact_redo_commit's xl_xact_commit payload. These
// two records only maintain the pg_commit_ts SLRU's physical extent (ZEROPAGE
// allocates the next page; TRUNCATE drops segments below oldestCommitTsXid) —
// state goopg does not have, because it neither stores commit timestamps nor
// even registers the track_commit_timestamp GUC.
//
// So the arm is conditional rather than a flat no-op. With tracking OFF — the
// PG default, and the only configuration goopg's own checkpointer writes into
// pg_control — a cluster cannot have meaningful pg_commit_ts content, any
// straggler record from a since-disabled window is moot, and no-oping is
// correct. With tracking ON in the crashed cluster's pg_control, silently
// skipping would leave a pg_commit_ts directory whose segments do not match the
// XID range the cluster believes is tracked; a later PG restart on the same
// directory would then read a page SimpleLruReadPage expects to exist. Refuse
// loudly and name the GUC instead — a half implementation here is worse than a
// refusal an operator can act on.
func replayDecodedXLogCommitTs(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) (bool, error) {
	switch xlog.Header.Info & XLRRmgrInfoMask {
	case xlogCommitTsZeroPage, xlogCommitTsTruncate:
	default:
		return false, unsupportedDecodedXLogRecord(r)
	}
	if commitTimestampsTracked(mgr) {
		return false, fmt.Errorf("%w: %s (track_commit_timestamp is on in pg_control, but goopg has no "+
			"pg_commit_ts SLRU; restart the cluster under PostgreSQL with track_commit_timestamp=off, "+
			"or start it with PostgreSQL)",
			ErrUnsupportedRecord, unsupportedDecodedXLogRecord(r))
	}
	return false, nil
}

// commitTimestampsTracked reports whether the cluster being recovered has
// track_commit_timestamp on, per its pg_control. A manager without a data
// directory (test stubs) reports false: there is no pg_commit_ts to be
// inconsistent with.
func commitTimestampsTracked(mgr *storage.Manager) bool {
	if mgr == nil || mgr.DataDir() == "" {
		return false
	}
	cd, err := control.ReadControlFile(mgr.DataDir())
	if err != nil || cd == nil {
		return false
	}
	return cd.TrackCommitTimestamp
}

// requireFullPageImages reports an ErrUnsupportedRecord unless EVERY block
// reference of the record carries an applicable full-page image — the
// precondition for replaying a record goopg has no native redo for by simply
// restoring its pages.
//
// M0131-S16.3. A record with zero block references is refused too: an
// image-only replay of a record that mutated nothing recorded here cannot be
// the whole truth, and silently succeeding is precisely the failure mode this
// slice exists to remove. The error names the offending block so a refused
// start says which record and which page could not be replayed, rather than
// "recovery failed".
func requireFullPageImages(r Record, xlog *XLogDecodedRecord) error {
	if len(xlog.Blocks) == 0 {
		return fmt.Errorf("%w: rmid=%d info=0x%02x lsn[%d,%d] has no block references to restore",
			ErrUnsupportedRecord, xlog.Header.Rmid, xlog.Header.Info&XLRRmgrInfoMask,
			r.StartLSN, r.EndLSN)
	}
	for i, block := range xlog.Blocks {
		if block.HasImage && block.ImageApply {
			continue
		}
		return fmt.Errorf("%w: rmid=%d info=0x%02x lsn[%d,%d] block %d (rel %v blk %d) carries no applicable full-page image (has_image=%t apply=%t) and goopg has no native redo for this opcode",
			ErrUnsupportedRecord, xlog.Header.Rmid, xlog.Header.Info&XLRRmgrInfoMask,
			r.StartLSN, r.EndLSN, i, block.Rel, block.Block,
			block.HasImage, block.ImageApply)
	}
	return nil
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
	page, skip, err := redoHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN), xlog.Header.Info&xlogHeapInit != 0)
	if err != nil {
		return fmt.Errorf("wal: xlog heap-insert: %w", err)
	}
	if skip {
		return nil
	}
	// M0131-S21c sibling: heap_xlog_insert uses the same
	// PageAddItemExtended(PAI_OVERWRITE) as heap_xlog_multi_insert, so a single
	// INSERT into a pruned page's LP_UNUSED hole must reuse the pointer in place
	// too. This path used to call PageInsertItemRawAt directly, which SHIFTED
	// the array — the same bug the multi-insert path refused loudly.
	if err := redoHeapPageAddItemOverwrite(page, offnum, tupleRaw); err != nil {
		return fmt.Errorf("wal: xlog heap-insert apply (block %d): %w", block.Block, err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// redoHeapPageForBlock acquires the heap page a PG-format redo routine is about
// to mutate, mirroring upstream XLogReadBufferExtended (xlogutils.c:479-539).
//
// M0131-S21a-2. Two behaviours upstream has that goopg's per-opcode replay
// functions were each open-coding (or getting wrong):
//
//   - **The replay gap is not an error.** A crash tail routinely references a
//     block past the fork's flushed length: the primary extended the relation
//     in shared buffers and logged the insert, but the extension itself is not
//     WAL-logged and the dirty page never reached disk. Upstream's answer is to
//     zero-extend the fork up to the referenced block ("hm, page doesn't exist
//     in file" → ExtendBufferedRelTo with EB_PERFORMING_RECOVERY) and carry on.
//     goopg's heap-insert arm instead returned "replay gap block=N nblocks=M",
//     which refuses the whole start.
//   - **pd_lsn is the idempotency guard**, checked only for a page that already
//     exists; a freshly extended page has no LSN to compare against.
//
// forceInit is the caller's XLOG_HEAP_INIT_PAGE bit: upstream reinitialises the
// page unconditionally in that case (XLogInitBufferForRedo + PageInit) rather
// than reading it, because the record's tuple offsets are relative to an empty
// page. block.WillInit (BKPBLOCK_WILL_INIT) and an all-zero page get the same
// treatment.
//
// Returns skip=true when the page is already at or past this record's LSN, in
// which case the returned page must not be written.
func redoHeapPageForBlock(mgr *storage.Manager, block XLogBlockRef, endLSN storage.LSN, forceInit bool) (storage.Page, bool, error) {
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return nil, false, err
	}
	page := make(storage.Page, storage.BlockSize)
	if block.Block < nblocks {
		if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
			return nil, false, err
		}
		if !storage.IsNew(page) && storage.MustHeader(page).LSN() >= endLSN {
			return nil, true, nil
		}
		if forceInit || block.WillInit || storage.IsNew(page) {
			if err := storage.InitPage(page); err != nil {
				return nil, false, err
			}
		}
		return page, false, nil
	}
	// Zero-extend the fork up to (but not including) the target block, exactly
	// as upstream does — the intervening pages are genuinely empty on the
	// primary too, and any record that fills them arrives later in the stream.
	zero := make(storage.Page, storage.BlockSize)
	for blk := nblocks; blk < block.Block; blk++ {
		got, err := mgr.Extend(block.Rel, zero)
		if err != nil {
			return nil, false, err
		}
		if got != blk {
			return nil, false, fmt.Errorf("zero-extend returned block %d, want %d", got, blk)
		}
	}
	if err := storage.InitPage(page); err != nil {
		return nil, false, err
	}
	got, err := mgr.Extend(block.Rel, page)
	if err != nil {
		return nil, false, err
	}
	if got != block.Block {
		return nil, false, fmt.Errorf("extend returned block %d, want %d", got, block.Block)
	}
	return page, false, nil
}

// redoHeapPageAddItemOverwrite places `raw` at the 1-based offset `offnum` on a
// heap page, mirroring the call every heap redo routine makes:
// PageAddItemExtended(page, item, size, offnum, PAI_OVERWRITE | PAI_IS_HEAP)
// (postgres/src/backend/storage/page/bufpage.c:193-330).
//
// M0131-S21c. Two distinct cases hide behind that one upstream call, and goopg
// needs two different primitives for them:
//
//   - offnum is past the end of the line-pointer array (the common case): the
//     pointer is appended. PageInsertItemRawAt does this, and its [1, count+1]
//     range check is upstream's "invalid max offset number" PANIC.
//   - offnum is INSIDE the array: upstream takes the PAI_OVERWRITE branch and
//     fills the existing line pointer IN PLACE, without shifting anything. A
//     real-PG crash tail reaches this whenever the page was pruned before the
//     insert (a COPY after a VACUUM leaves LP_UNUSED holes), so it is ordinary
//     traffic, not an exotic shape. PageInsertItemRawAt is the WRONG primitive
//     here — it would shift the array right and displace whatever occupies the
//     following slots — so the in-place pair PageReplaceItemRaw (which allocates
//     fresh tuple bytes at pd_upper for a zero-length pointer, leaving pd_lower
//     alone) + PageSetLinePointerNormal (PageReplaceItemRaw deliberately
//     preserves the old flags) is used instead.
//
// Upstream refuses the in-place branch when the target is `ItemIdIsUsed() ||
// ItemIdHasStorage()` — WARNING "will not overwrite a used ItemId", returning
// InvalidOffsetNumber, which every heap redo caller turns into a PANIC
// ("failed to add tuple"). goopg keeps that as a hard refusal too: a used slot
// means the page on disk disagrees with the record, and writing anyway is
// silent corruption that surfaces much later as a wrong ctid.
func redoHeapPageAddItemOverwrite(page storage.Page, offnum uint16, raw []byte) error {
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		return fmt.Errorf("read line-pointer count: %w", err)
	}
	if int(offnum) > count {
		got, err := storage.PageInsertItemRawAt(page, offnum, raw)
		if err != nil {
			return err
		}
		if got != offnum {
			return fmt.Errorf("slot drift: got %d, want %d", got, offnum)
		}
		return nil
	}
	id, err := storage.PageGetItemID(page, offnum)
	if err != nil {
		return fmt.Errorf("read line pointer %d: %w", offnum, err)
	}
	if id.Flags != storage.ItemIDUnused || id.Length != 0 {
		return fmt.Errorf("%w: will not overwrite used line pointer %d (flags=%d len=%d, page has %d) — the page disagrees with the record",
			ErrUnsupportedRecord, offnum, id.Flags, id.Length, count)
	}
	if err := storage.PageReplaceItemRaw(page, offnum, raw); err != nil {
		return fmt.Errorf("reuse line pointer %d: %w", offnum, err)
	}
	return storage.PageSetLinePointerNormal(page, offnum)
}

// replayDecodedXLogHeapMultiInsert applies a real-PG XLOG_HEAP2_MULTI_INSERT,
// mirroring heap_xlog_multi_insert (heapam_xlog.c:600-731). This is every COPY:
// heap_multi_insert batches as many tuples as fit on one page into a single
// record, so a PG crash tail taken during a bulk load is almost entirely made
// of these.
//
// M0131-S21a-2. Wire format: main data is xl_heap_multi_insert
// {uint8 flags; uint16 ntuples; OffsetNumber offsets[ntuples]} — and the
// offsets array is present ONLY when XLOG_HEAP_INIT_PAGE is clear. With the bit
// set the page is reinitialised and the tuples land at FirstOffsetNumber+i, so
// upstream saves the array's bytes (heapam.c:2607-2611 sets the bit exactly
// when the page was freshly extended, which is why the RM_HEAP2 arm must mask
// with XLOG_HEAP_OPMASK — see the mask note there).
//
// Block 0's data run is a sequence of SHORTALIGNed xl_multi_insert_tuple
// headers, each immediately followed by its `datalen` tuple bytes (everything
// past the fixed 23-byte heap-tuple header: null bitmap, alignment padding and
// column data, verbatim).
func replayDecodedXLogHeapMultiInsert(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap-multi-insert missing block 0")
	}
	if block.Rel.Fork != storage.MainFork {
		return fmt.Errorf("wal: xlog heap-multi-insert fork=%d, want main fork", block.Rel.Fork)
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	isInit := xlog.Header.Info&xlogHeapInit != 0
	flags, offsets, err := decodeXLogHeapMultiInsertMainData(xlog.MainData, isInit)
	if err != nil {
		return err
	}
	tuples, err := decodeXLogMultiInsertTuples(block.Data, len(offsets))
	if err != nil {
		return err
	}
	page, skip, err := redoHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN), isInit)
	if err != nil {
		return fmt.Errorf("wal: xlog heap-multi-insert: %w", err)
	}
	if skip {
		return nil
	}
	for i, offnum := range offsets {
		tupleRaw := buildTupleFromXLogHeapHeader(tuples[i].header, tuples[i].data,
			storage.TransactionID(xlog.Header.XID), block.Block, offnum)
		if err := redoHeapPageAddItemOverwrite(page, offnum, tupleRaw); err != nil {
			return fmt.Errorf("wal: xlog heap-multi-insert apply entry %d (block %d): %w", i, block.Block, err)
		}
	}
	// The visibility-map bits upstream mirrors onto the heap page itself. The
	// VM fork update is XLOG_HEAP2_VISIBLE's job (still S21a-2); these two are
	// the page-header half heap_xlog_multi_insert does inline.
	hdr := storage.MustHeader(page)
	if flags&xlogHeapInsertAllVisibleCleared != 0 {
		hdr.SetFlags(hdr.Flags() &^ storage.PDAllVisible)
	} else if flags&xlogHeapInsertAllFrozenSet != 0 {
		hdr.SetFlags(hdr.Flags() | storage.PDAllVisible)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// decodeXLogHeapMultiInsertMainData parses xl_heap_multi_insert. When isInit is
// set the offsets array is absent and the tuples land at FirstOffsetNumber+i;
// the returned slice is synthesised for that case so the caller has one shape.
func decodeXLogHeapMultiInsertMainData(mainData []byte, isInit bool) (flags uint8, offsets []uint16, err error) {
	if len(mainData) < sizeOfXLogHeapMultiInsertData {
		return 0, nil, fmt.Errorf("wal: invalid xlog heap-multi-insert main-data len %d (want >= %d)",
			len(mainData), sizeOfXLogHeapMultiInsertData)
	}
	flags = mainData[0]
	ntuples := int(binary.LittleEndian.Uint16(mainData[2:4]))
	if ntuples == 0 {
		return 0, nil, fmt.Errorf("wal: xlog heap-multi-insert carries zero tuples")
	}
	offsets = make([]uint16, ntuples)
	if isInit {
		// FirstOffsetNumber is 1 in PG and goopg alike (line pointers are
		// 1-based); heap_xlog_multi_insert lands tuple i at
		// FirstOffsetNumber + i on a reinitialised page.
		for i := range offsets {
			offsets[i] = 1 + uint16(i)
		}
		return flags, offsets, nil
	}
	want := sizeOfXLogHeapMultiInsertData + 2*ntuples
	if len(mainData) < want {
		return 0, nil, fmt.Errorf("wal: xlog heap-multi-insert main-data len %d < %d for %d offsets",
			len(mainData), want, ntuples)
	}
	for i := range offsets {
		offsets[i] = binary.LittleEndian.Uint16(mainData[sizeOfXLogHeapMultiInsertData+2*i:])
	}
	return flags, offsets, nil
}

// xlogMultiInsertTuple is one (xl_multi_insert_tuple header, tuple data) pair
// carved out of a multi-insert record's block-0 data run. header is the 5-byte
// xl_heap_header-shaped slice {t_infomask2, t_infomask, t_hoff} the shared
// tuple builder consumes.
type xlogMultiInsertTuple struct {
	header []byte
	data   []byte
}

// decodeXLogMultiInsertTuples splits block 0's data run into ntuples entries.
// Upstream walks it with `xlhdr = (xl_multi_insert_tuple *) SHORTALIGN(tupdata)`
// and PANICs at the end if the walk did not consume the run exactly
// ("total tuple length mismatch") — the same exactness is enforced here, since a
// short walk means the record was mis-parsed and the extra bytes are a tuple
// that would be silently dropped.
func decodeXLogMultiInsertTuples(data []byte, ntuples int) ([]xlogMultiInsertTuple, error) {
	out := make([]xlogMultiInsertTuple, 0, ntuples)
	off := 0
	for i := 0; i < ntuples; i++ {
		off += off & 1 // SHORTALIGN
		if off+sizeOfXLogMultiInsertTuple > len(data) {
			return nil, fmt.Errorf("wal: xlog heap-multi-insert block data truncated at tuple %d (off %d of %d)", i, off, len(data))
		}
		hdr := data[off : off+sizeOfXLogMultiInsertTuple]
		datalen := int(binary.LittleEndian.Uint16(hdr[0:2]))
		off += sizeOfXLogMultiInsertTuple
		if off+datalen > len(data) {
			return nil, fmt.Errorf("wal: xlog heap-multi-insert tuple %d datalen %d overruns block data (off %d of %d)", i, datalen, off, len(data))
		}
		out = append(out, xlogMultiInsertTuple{
			// hdr[2:7] is {t_infomask2, t_infomask, t_hoff} — the same three
			// fields, in the same order, as xl_heap_header.
			header: hdr[2:sizeOfXLogMultiInsertTuple],
			data:   data[off : off+datalen],
		})
		off += datalen
	}
	if off != len(data) {
		return nil, fmt.Errorf("wal: xlog heap-multi-insert total tuple length mismatch: consumed %d of %d block-data bytes", off, len(data))
	}
	return out, nil
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
	return buildTupleFromXLogHeapHeader(block.Data[:sizeOfXLogHeapHeaderData],
		block.Data[sizeOfXLogHeapHeaderData:], xid, block.Block, offnum), nil
}

// buildTupleFromXLogHeapHeader rebuilds a marshaled heap tuple from the
// (t_infomask2, t_infomask, t_hoff) triple a WAL record carries plus the tuple
// bytes past the fixed 23-byte header. Shared by the xl_heap_insert and
// xl_heap_multi_insert redo paths (M0131-S21a-2), whose per-tuple headers
// differ only in the multi-insert's leading `datalen` field — upstream's
// heap_xlog_insert and heap_xlog_multi_insert rebuild the tuple identically
// from that point on.
//
// header must be exactly the 5 bytes {t_infomask2[2], t_infomask[2], t_hoff[1]}.
func buildTupleFromXLogHeapHeader(header, data []byte, xid storage.TransactionID, blk storage.BlockNumber, offnum uint16) []byte {
	out := make([]byte, storage.SizeOfHeapTupleHeaderData+len(data))
	binary.LittleEndian.PutUint32(out[0:4], uint32(xid))                           // t_xmin
	binary.LittleEndian.PutUint32(out[4:8], uint32(storage.InvalidTransactionID))  // t_xmax
	binary.LittleEndian.PutUint32(out[8:12], uint32(storage.InvalidTransactionID)) // t_field3 (xvac / cmin)
	binary.LittleEndian.PutUint32(out[12:16], uint32(blk))                         // t_ctid.block (self)
	binary.LittleEndian.PutUint16(out[16:18], offnum)                              // t_ctid.offset (self)
	copy(out[18:22], header[0:4])                                                  // t_infomask2 + t_infomask
	out[22] = header[4]                                                            // t_hoff
	copy(out[storage.SizeOfHeapTupleHeaderData:], data)
	return out
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
	// M0131-S21f: RBM_NORMAL, like upstream's heap_xlog_delete. An absent or
	// all-zero page is BLK_NOTFOUND and the record does nothing; it used to be
	// a hard error that refused the whole start.
	page, skip, err := redoExistingHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN))
	if err != nil {
		return fmt.Errorf("wal: xlog heap-delete: %w", err)
	}
	if skip {
		return nil
	}
	if err := storage.PageSetHeapTupleXmax(page, offnum, storage.TransactionID(xmax)); err != nil {
		return fmt.Errorf("wal: xlog heap-delete apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// sizeOfXLogHeapLockData is PG's SizeOfHeapLock: xmax(4) + offnum(2) +
// infobits_set(1) + flags(1) (heapam_xlog.h:396-404).
const sizeOfXLogHeapLockData = 8

// sizeOfXLogHeapConfirmData is PG's SizeOfHeapConfirm: offnum(2).
const sizeOfXLogHeapConfirmData = 2

// decodeXLogHeapLockMainData parses the fixed xl_heap_lock struct from a
// PG-format heap-lock record's main data.
func decodeXLogHeapLockMainData(mainData []byte) (xmax uint32, offnum uint16, infobits, flags uint8, err error) {
	if len(mainData) < sizeOfXLogHeapLockData {
		return 0, 0, 0, 0, fmt.Errorf("wal: invalid xlog heap-lock main-data len %d (want >= %d)", len(mainData), sizeOfXLogHeapLockData)
	}
	xmax = binary.LittleEndian.Uint32(mainData[0:4])
	offnum = binary.LittleEndian.Uint16(mainData[4:6])
	infobits = mainData[6]
	flags = mainData[7]
	return xmax, offnum, infobits, flags, nil
}

// xlogHeapLockInfomaskBits is upstream's fix_infomask_from_infobits
// (heapam_xlog.c): translate xl_heap_lock's infobits_set wire byte into the
// t_infomask / t_infomask2 bits redo has to restore. Note HEAP_XMAX_SHR_LOCK is
// deliberately absent — upstream says so in a comment; a share lock arrives as
// the two component bits.
func xlogHeapLockInfomaskBits(infobits uint8) (infomask, infomask2 uint16) {
	if infobits&xlhlXmaxIsMulti != 0 {
		infomask |= storage.HeapXmaxIsMulti
	}
	if infobits&xlhlXmaxLockOnly != 0 {
		infomask |= storage.HeapXmaxLockOnly
	}
	if infobits&xlhlXmaxExclLock != 0 {
		infomask |= storage.HeapXmaxExclLock
	}
	if infobits&xlhlXmaxKeyShrLock != 0 {
		infomask |= storage.HeapXmaxKeyShrLock
	}
	if infobits&xlhlKeysUpdated != 0 {
		infomask2 |= storage.HeapKeysUpdated
	}
	return infomask, infomask2
}

// redoExistingHeapPageForBlock is redoHeapPageForBlock's RBM_NORMAL sibling:
// the page must ALREADY exist and be initialised, and when it does not the
// record is skipped rather than applied to a zero-extended page.
//
// M0131-S21a-2. That asymmetry is upstream's, not an approximation. Insert-like
// records reference a block whose extension was never WAL-logged, so
// XLogReadBufferExtended is called in an RBM_ZERO/WILL_INIT mode and extends.
// A record that only STAMPS an existing tuple (lock, confirm) is read with
// RBM_NORMAL, where a missing or all-zero page returns InvalidBuffer =
// BLK_NOTFOUND and the redo routine does nothing (xlogutils.c:500-540): the
// only way the page can be absent is that the relation is dropped or truncated
// later in the same replay stream, in which case the mutation is moot.
//
// Deviation, ledger row: upstream ALSO records the reference in its
// invalid-page hash and PANICs at the end of recovery if no later record
// dropped or truncated that relation. goopg keeps no such table, so a genuinely
// missing page is silently skipped instead of failing the start. It cannot
// corrupt anything on these two opcodes — a lost row-lock stamp or speculative
// confirmation is bookkeeping about a transaction that the crash ended anyway —
// but it does hide a real inconsistency.
//
// M0131-S21f widened the caller set from those two opcodes (lock, confirm) to
// every PG-format record that edits an existing page: heap delete, heap
// prune/freeze, and — via replayExistingXLogBlock — the btree edit-shaped
// records. They had each hand-rolled the same NBlocks + ReadBlock sequence but
// ended it in a hard error, so a stream whose later records drop or truncate
// the relation refused the whole start. The skip is now shared, and so is the
// deviation note above: it applies to all of them, and it is wider than the
// original two, because losing a prune or a btree deletion on a page that DOES
// survive would be a real divergence. Upstream catches exactly that with the
// invalid-page table; goopg still cannot.
//
// Returns skip=true when there is nothing to do (page absent, uninitialised, or
// already at/past this record's LSN).
func redoExistingHeapPageForBlock(mgr *storage.Manager, block XLogBlockRef, endLSN storage.LSN) (storage.Page, bool, error) {
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return nil, false, err
	}
	if block.Block >= nblocks {
		return nil, true, nil
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
		return nil, false, err
	}
	if storage.IsNew(page) {
		return nil, true, nil
	}
	if storage.MustHeader(page).LSN() >= endLSN {
		return nil, true, nil
	}
	return page, false, nil
}

// replayDecodedXLogHeapLock applies a real-PG XLOG_HEAP_LOCK, mirroring
// heap_xlog_lock (heapam_xlog.c). M0131-S21a-2.
//
// This is one of the most common records in an OLTP tail: every SELECT ... FOR
// UPDATE/SHARE, every foreign-key row check, and the tuple lock an UPDATE takes
// on a row it is about to rewrite all emit one. The mutation is confined to a
// single tuple header — xmax, the xmax-classification infomask bits, cmax, and
// (for a locked-only result) t_ctid and HEAP_HOT_UPDATED — which is why the
// work lives in storage.PageApplyHeapLockRedo, the redo sibling of the runtime
// PageSetHeapTupleLockOnly.
//
// Idempotent via pd_lsn. A record carrying a full-page image restores it
// instead.
func replayDecodedXLogHeapLock(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap-lock missing block 0")
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	xmax, offnum, infobits, flags, err := decodeXLogHeapLockMainData(xlog.MainData)
	if err != nil {
		return err
	}
	// XLH_LOCK_ALL_FROZEN_CLEARED, the flags byte's only bit: the locker had to
	// clear the block's visibility-map ALL_FROZEN bit, because a frozen page may
	// not carry a live locker. Upstream does this BEFORE (and independently of)
	// the heap page redo, with the comment "the visibility map may need to be
	// fixed even if the heap page is already up-to-date" (heapam_xlog.c
	// heap_xlog_lock) — so it deliberately runs even when the pd_lsn interlock
	// skips the tuple stamp below. M0131-S21a-2 part 3 discharges the part-2
	// deferral of this bit.
	if flags&xlhLockAllFrozenCleared != 0 {
		if err := redoClearVMBitsForHeapBlock(mgr, block.Rel, block.Block, storage.VMAllFrozen); err != nil {
			return fmt.Errorf("wal: xlog heap-lock vm: %w", err)
		}
	}
	page, skip, err := redoExistingHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN))
	if err != nil {
		return fmt.Errorf("wal: xlog heap-lock: %w", err)
	}
	if skip {
		return nil
	}
	infomaskBits, infomask2Bits := xlogHeapLockInfomaskBits(infobits)
	if err := storage.PageApplyHeapLockRedo(page, offnum, storage.TransactionID(xmax), infomaskBits, infomask2Bits, block.Block); err != nil {
		return fmt.Errorf("wal: xlog heap-lock apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// replayDecodedXLogHeapConfirm applies a real-PG XLOG_HEAP_CONFIRM, mirroring
// heap_xlog_confirm (heapam_xlog.c). M0131-S21a-2.
//
// It is the second record of every INSERT ... ON CONFLICT. The speculative
// insert wrote the tuple with a *speculative token* in t_ctid rather than a
// self-pointer; confirming it means overwriting that token with the tuple's own
// (block, offset), which is exactly goopg's own fresh-insert convention. Redo
// therefore has to run: replaying only the insert would leave a tuple whose
// t_ctid points at a garbage location, and a chain follower would chase it.
func replayDecodedXLogHeapConfirm(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap-confirm missing block 0")
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	if len(xlog.MainData) < sizeOfXLogHeapConfirmData {
		return fmt.Errorf("wal: invalid xlog heap-confirm main-data len %d (want >= %d)", len(xlog.MainData), sizeOfXLogHeapConfirmData)
	}
	offnum := binary.LittleEndian.Uint16(xlog.MainData[0:2])
	page, skip, err := redoExistingHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN))
	if err != nil {
		return fmt.Errorf("wal: xlog heap-confirm: %w", err)
	}
	if skip {
		return nil
	}
	if err := storage.PageSetHeapTupleCtid(page, offnum, storage.ItemPointer{Block: block.Block, Offset: offnum}); err != nil {
		return fmt.Errorf("wal: xlog heap-confirm apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// replayDecodedXLogHeap2LockUpdated applies a real-PG XLOG_HEAP2_LOCK_UPDATED,
// mirroring heap_xlog_lock_updated (heapam_xlog.c). M0131-S21a-2 part 4.
//
// It is XLOG_HEAP_LOCK's near-sibling on RM_HEAP2: emitted by
// heap_lock_updated_tuple_rec when a tuple-lock request discovers the row it
// locked was concurrently updated and re-locks the newest visible version in
// the update chain instead. The wire struct (xl_heap_lock_updated) is
// byte-identical to xl_heap_lock's — same decode, same infobits translation —
// so this reuses decodeXLogHeapLockMainData and xlogHeapLockInfomaskBits; only
// the tuple mutation differs (storage.PageApplyHeapLockUpdatedRedo omits the
// locked-only t_ctid/HOT_UPDATED fixup and the cmax stamp — see its doc
// comment for why).
func replayDecodedXLogHeap2LockUpdated(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap2-lock-updated missing block 0")
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, storage.LSN(r.EndLSN))
	}
	xmax, offnum, infobits, flags, err := decodeXLogHeapLockMainData(xlog.MainData)
	if err != nil {
		return err
	}
	// XLH_LOCK_ALL_FROZEN_CLEARED runs BEFORE and independently of the heap
	// page redo, same as XLOG_HEAP_LOCK (part 3's rationale applies verbatim:
	// "the visibility map may need to be fixed even if the heap page is
	// already up-to-date", heapam_xlog.c heap_xlog_lock_updated).
	if flags&xlhLockAllFrozenCleared != 0 {
		if err := redoClearVMBitsForHeapBlock(mgr, block.Rel, block.Block, storage.VMAllFrozen); err != nil {
			return fmt.Errorf("wal: xlog heap2-lock-updated vm: %w", err)
		}
	}
	page, skip, err := redoExistingHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN))
	if err != nil {
		return fmt.Errorf("wal: xlog heap2-lock-updated: %w", err)
	}
	if skip {
		return nil
	}
	infomaskBits, infomask2Bits := xlogHeapLockInfomaskBits(infobits)
	if err := storage.PageApplyHeapLockUpdatedRedo(page, offnum, storage.TransactionID(xmax), infomaskBits, infomask2Bits); err != nil {
		return fmt.Errorf("wal: xlog heap2-lock-updated apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
}

// sizeOfXLogHeapVisibleData is PG's SizeOfHeapVisible:
// snapshotConflictHorizon(4) + flags(1) (heapam_xlog.h xl_heap_visible).
const sizeOfXLogHeapVisibleData = 5

// decodeXLogHeapVisibleMainData parses the fixed xl_heap_visible struct from a
// PG-format XLOG_HEAP2_VISIBLE record's main data.
func decodeXLogHeapVisibleMainData(mainData []byte) (snapshotConflictHorizon uint32, flags uint8, err error) {
	if len(mainData) < sizeOfXLogHeapVisibleData {
		return 0, 0, fmt.Errorf("wal: invalid xlog heap-visible main-data len %d (want >= %d)", len(mainData), sizeOfXLogHeapVisibleData)
	}
	return binary.LittleEndian.Uint32(mainData[0:4]), mainData[4], nil
}

// replayDecodedXLogHeap2Visible applies a real-PG XLOG_HEAP2_VISIBLE, mirroring
// heap_xlog_visible (heapam_xlog.c). M0131-S21a-2 part 3.
//
// Every VACUUM emits one per page it marks all-visible/all-frozen, and so does
// an INSERT that freezes a page it filled itself, which makes this the first
// record a bulk-loaded PG cluster's tail hits after the COPY records. It is
// also the FIRST record in goopg's redo whose mutation lands on a fork other
// than `main`: block 0 is the *visibility-map* buffer (fork 2, vm block number),
// block 1 is the heap block whose bits are being set.
//
// Both halves must run and they are independent:
//
//   - **Heap page (block 1).** All redo does is set PD_ALL_VISIBLE. Upstream
//     reads it with XLogReadBufferForRedo, i.e. RBM_NORMAL — a heap file
//     dropped or truncated later in the stream is simply absent, "we don't need
//     to update the page, but we'd better still update the visibility map".
//     That comment is why the block-1 skip must not skip block 0.
//   - **VM page (block 0).** Read with RBM_ZERO_ON_ERROR: a vm fork that does
//     not reach this block yet is not an error, it is initialised on the spot.
//     This is the one place recovery *creates* vm-fork content, and it is what
//     makes the bits survive into the running server: crash recovery replays
//     before Runtime.VM is populated from the forks (internal/initdb/open.go),
//     so a fork page written here is loaded by VMLoadForks a moment later.
//
// Upstream additionally (a) resolves hot-standby snapshot conflicts against
// xlrec->snapshotConflictHorizon and (b) feeds the heap page's free space into
// the FSM so a promoted standby does not inherit a stale map. goopg does
// neither — both are deferral-ledger rows, not silent omissions; neither can
// corrupt the map, and (a) is unreachable while goopg has no hot-standby
// query traffic during replay.
func replayDecodedXLogHeap2Visible(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	vmRef, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog heap-visible missing block 0 (visibility map)")
	}
	heapRef, ok := xlogBlockRefByID(xlog, 1)
	if !ok {
		return fmt.Errorf("wal: xlog heap-visible missing block 1 (heap)")
	}
	_, flags, err := decodeXLogHeapVisibleMainData(xlog.MainData)
	if err != nil {
		return err
	}
	// Upstream asserts the record carries nothing outside
	// VISIBILITYMAP_XLOG_VALID_BITS; goopg refuses instead of asserting,
	// because an unknown bit means the record was written by a PG whose map
	// semantics goopg does not know.
	if flags&^(storage.VMValidBits|xlogVisibilitymapXLogCatalogRel) != 0 {
		return fmt.Errorf("%w: xlog heap-visible flags 0x%02x carry unknown bits", ErrUnsupportedRecord, flags)
	}

	// Half 1: the heap page's PD_ALL_VISIBLE.
	if heapRef.HasImage && heapRef.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, heapRef, storage.LSN(r.EndLSN)); err != nil {
			return err
		}
	} else {
		page, skip, err := redoExistingHeapPageForBlock(mgr, heapRef, storage.LSN(r.EndLSN))
		if err != nil {
			return fmt.Errorf("wal: xlog heap-visible heap page: %w", err)
		}
		if !skip {
			hdr := storage.MustHeader(page)
			hdr.SetFlags(hdr.Flags() | storage.PDAllVisible)
			// Upstream stamps the heap page's LSN only when
			// XLogHintBitIsNeeded() (checksums or wal_log_hints), since without
			// those the record carries no FPI for the heap page and a bumped
			// LSN would lie about what is protected. goopg's redo stamps it
			// unconditionally: the pd_lsn guard in redoExistingHeapPageForBlock
			// is what makes every goopg heap redo idempotent, and the stamp is
			// the conservative direction — it can only cause a later record to
			// skip a page whose content is already at this LSN.
			hdr.SetLSN(storage.LSN(r.EndLSN))
			if err := mgr.WriteBlock(heapRef.Rel, heapRef.Block, page); err != nil {
				return err
			}
		}
	}

	// Half 2: the visibility-map bits. Runs even when the heap half was
	// skipped — see the function comment.
	if vmRef.HasImage && vmRef.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, vmRef, storage.LSN(r.EndLSN))
	}
	vmPage, skip, err := redoVMPageForBlock(mgr, vmRef, storage.LSN(r.EndLSN))
	if err != nil {
		return fmt.Errorf("wal: xlog heap-visible vm page: %w", err)
	}
	if skip {
		return nil
	}
	if storage.VMBlockForHeapBlock(heapRef.Block) != vmRef.Block {
		return fmt.Errorf("wal: xlog heap-visible vm block %d does not cover heap block %d (want vm block %d)",
			vmRef.Block, heapRef.Block, storage.VMBlockForHeapBlock(heapRef.Block))
	}
	changed, err := storage.VMPageSetBits(vmPage, heapRef.Block, flags&storage.VMValidBits)
	if err != nil {
		return fmt.Errorf("wal: xlog heap-visible vm apply: %w", err)
	}
	if !changed {
		return nil
	}
	storage.MustHeader(vmPage).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(vmRef.Rel, vmRef.Block, vmPage)
}

// redoVMPageForBlock is the visibility-map counterpart of
// redoHeapPageForBlock/redoExistingHeapPageForBlock: upstream reads the vm
// buffer with RBM_ZERO_ON_ERROR and PageInits it when it comes back new
// (heap_xlog_visible), so a vm fork shorter than the referenced block is not a
// replay gap — it is the normal state of a fork whose extension was never
// WAL-logged. Intervening pages are zero-extended and then initialised, the
// same shape redoHeapPageForBlock uses, because an all-zero vm page and an
// initialised one mean the same thing (no bits set) and PG's vm_extend writes
// initialised pages.
//
// Returns skip=true when the page is already at or past this record's LSN.
func redoVMPageForBlock(mgr *storage.Manager, block XLogBlockRef, endLSN storage.LSN) (storage.Page, bool, error) {
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return nil, false, err
	}
	page := make(storage.Page, storage.BlockSize)
	if block.Block < nblocks {
		if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
			return nil, false, err
		}
		if storage.IsNew(page) || block.WillInit {
			if err := storage.InitPage(page); err != nil {
				return nil, false, err
			}
			return page, false, nil
		}
		if storage.MustHeader(page).LSN() >= endLSN {
			return nil, true, nil
		}
		return page, false, nil
	}
	zero := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(zero); err != nil {
		return nil, false, err
	}
	for blk := nblocks; blk < block.Block; blk++ {
		got, err := mgr.Extend(block.Rel, zero)
		if err != nil {
			return nil, false, err
		}
		if got != blk {
			return nil, false, fmt.Errorf("vm zero-extend returned block %d, want %d", got, blk)
		}
	}
	if err := storage.InitPage(page); err != nil {
		return nil, false, err
	}
	got, err := mgr.Extend(block.Rel, page)
	if err != nil {
		return nil, false, err
	}
	if got != block.Block {
		return nil, false, fmt.Errorf("vm extend returned block %d, want %d", got, block.Block)
	}
	return page, false, nil
}

// redoClearVMBitsForHeapBlock is upstream's visibilitymap_pin +
// visibilitymap_clear pair as redo uses it (heap_xlog_lock, and every other
// record that reports having cleared a bit): heapRel names the MAIN fork, the
// bits live in that relation's visibility-map fork at
// HEAPBLK_TO_MAPBLOCK(heapBlk).
//
// A vm fork that does not reach the covering block is left alone rather than
// extended. Upstream's visibilitymap_pin does extend it, but only because it
// must hand visibilitymap_clear a valid buffer; the bits it would then clear
// are already zero, so the sole difference is whether an all-zero vm page
// materialises on disk — and materialising one for a *clear* would invent map
// content that the primary may never have had.
func redoClearVMBitsForHeapBlock(mgr *storage.Manager, heapRel storage.RelFileNode, heapBlk storage.BlockNumber, bits uint8) error {
	vmRel := heapRel
	vmRel.Fork = storage.VisibilityMapFork
	vmBlk := storage.VMBlockForHeapBlock(heapBlk)
	nblocks, err := mgr.NBlocks(vmRel)
	if err != nil {
		return err
	}
	if vmBlk >= nblocks {
		return nil
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(vmRel, vmBlk, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return nil
	}
	// No pd_lsn interlock here, deliberately: upstream's visibilitymap_clear
	// neither checks nor stamps the vm page's LSN (visibilitymap.c). Clearing
	// is the conservative direction — a cleared bit only costs an extra heap
	// fetch — so re-running it after the page has moved past this LSN cannot
	// produce a wrong answer, whereas skipping it can.
	changed, err := storage.VMPageClearBits(page, heapBlk, bits)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return mgr.WriteBlock(vmRel, vmBlk, page)
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

// decodeXLogHeapUpdateNewTuple rebuilds an xl_heap_update's new tuple from
// block 0's data, splicing back the prefix/suffix bytes the record left out.
//
// M0131-S21g. Upstream's log_heap_update (postgres/src/backend/access/heap/
// heapam.c:8730-8800) compares the old and new versions byte-for-byte and, when
// they share a leading and/or trailing run inside the data area, logs only
// `uint16 prefixlen` / `uint16 suffixlen` in place of those bytes. heap_xlog_update
// (heapam_xlog.c:933-1005) reverses it: read the two optional lengths, then the
// xl_heap_header, then assemble
//
//	SizeofHeapTupleHeader | bitmap+padding (t_hoff - 23 bytes, from the record)
//	                      | prefixlen bytes from the OLD tuple's data area
//	                      | the rest of the record's tuple bytes
//	                      | suffixlen bytes from the OLD tuple's tail
//
// oldTuple is the marshaled old version read off the page (same page — upstream
// asserts newblk == oldblk whenever either flag is set) and may be nil when
// neither flag is present, which is the shape goopg's own emit always produces.
//
// This is not a rare corner: a catalog UPDATE that flips one flag byte (say
// pg_class.relhasindex after CREATE INDEX) compresses down to a handful of
// bytes, and before this landed such a record replayed as a ~4-byte tuple that
// the pg_class reload could not decode — a real PG's `CREATE TABLE` survived the
// crash tail as an index and toast row with no table row behind them.
func decodeXLogHeapUpdateNewTuple(block XLogBlockRef, flags uint8, oldTuple []byte,
	xid storage.TransactionID, offnum uint16) ([]byte, error) {
	rec := block.Data
	var prefixLen, suffixLen int
	takeLen := func(what string) (int, error) {
		if len(rec) < 2 {
			return 0, fmt.Errorf("wal: xlog heap-update block-data too short for %s length", what)
		}
		v := int(binary.LittleEndian.Uint16(rec[:2]))
		rec = rec[2:]
		return v, nil
	}
	var err error
	if flags&xlhUpdatePrefixFromOld != 0 {
		if prefixLen, err = takeLen("prefix"); err != nil {
			return nil, err
		}
	}
	if flags&xlhUpdateSuffixFromOld != 0 {
		if suffixLen, err = takeLen("suffix"); err != nil {
			return nil, err
		}
	}
	if len(rec) < sizeOfXLogHeapHeaderData {
		return nil, fmt.Errorf("wal: invalid xlog heap-update block-data len %d (want >= %d)",
			len(rec), sizeOfXLogHeapHeaderData)
	}
	header, body := rec[:sizeOfXLogHeapHeaderData], rec[sizeOfXLogHeapHeaderData:]
	if prefixLen == 0 && suffixLen == 0 {
		// Uncompressed: byte-for-byte the xl_heap_insert shape.
		return buildTupleFromXLogHeapHeader(header, body, xid, block.Block, offnum), nil
	}
	// hoffExtra is upstream's `xlhdr.t_hoff - SizeofHeapTupleHeader`: the null
	// bitmap plus its alignment padding, which is logged in full and so must be
	// re-emitted BEFORE the spliced-in prefix.
	hoffExtra := int(header[4]) - storage.SizeOfHeapTupleHeaderData
	if hoffExtra < 0 || hoffExtra > len(body) {
		return nil, fmt.Errorf("wal: xlog heap-update t_hoff %d out of range (block data %d)", header[4], len(body))
	}
	if len(oldTuple) < storage.SizeOfHeapTupleHeaderData {
		return nil, fmt.Errorf("wal: xlog heap-update prefix/suffix splice needs the old tuple (got %d bytes)", len(oldTuple))
	}
	oldHoff := int(oldTuple[22]) // t_hoff of the marshaled old version
	if oldHoff < storage.SizeOfHeapTupleHeaderData || oldHoff > len(oldTuple) {
		return nil, fmt.Errorf("wal: xlog heap-update old tuple t_hoff %d out of range (len %d)", oldHoff, len(oldTuple))
	}
	if prefixLen < 0 || suffixLen < 0 || prefixLen+suffixLen > len(oldTuple)-oldHoff {
		return nil, fmt.Errorf("wal: xlog heap-update prefix %d + suffix %d exceed the old tuple's %d data bytes",
			prefixLen, suffixLen, len(oldTuple)-oldHoff)
	}
	out := make([]byte, 0, len(body)+prefixLen+suffixLen)
	out = append(out, body[:hoffExtra]...)
	out = append(out, oldTuple[oldHoff:oldHoff+prefixLen]...)
	out = append(out, body[hoffExtra:]...)
	out = append(out, oldTuple[len(oldTuple)-suffixLen:]...)
	return buildTupleFromXLogHeapHeader(header, out, xid, block.Block, offnum), nil
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
	oldXmax, oldOffnum, _, flags, _, newOffnum, err := decodeXLogHeapUpdateMainData(xlog.MainData)
	if err != nil {
		return err
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
	// M0131-S21d: acquire block 0 the way every other heap redo routine does.
	// Upstream draws no distinction here — heap_xlog_update takes
	// XLogInitBufferForRedo + PageInit when XLOG_HEAP_INIT_PAGE is set and
	// XLogReadBufferForRedo otherwise (heapam_xlog.c:918-931), i.e. the same
	// XLogReadBufferExtended that zero-extends past the end of the fork
	// (xlogutils.c:479-539). This path used to read the block itself and refuse
	// on `block >= nblocks` / `IsNew(page)`, which is why a real-PG crash tail
	// stopped the start ("block %d is uninitialised"): the tail routinely
	// references a page past the last flushed one, and an UPDATE that moves a
	// row to a freshly extended page arrives with INIT_PAGE set and nothing on
	// disk to read.
	page, skip, err := redoHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN), xlog.Header.Info&xlogHeapInit != 0)
	if err != nil {
		return err
	}
	samePage := oldBlock.Block == block.Block
	stampOld := func(p storage.Page) error {
		if hot {
			return storage.PageStampHotOldTuple(p, oldOffnum, storage.TransactionID(oldXmax), block.Block, newOffnum)
		}
		return storage.PageStampUpdatedOldTuple(p, oldOffnum, storage.TransactionID(oldXmax), block.Block, newOffnum)
	}
	if !skip {
		// M0131-S21g: the new tuple's bytes are only WHOLE in goopg's own
		// emit. A real PG's log_heap_update (heapam.c:8730-8800) strips the
		// prefix and suffix the new version shares with the old one and stores
		// their lengths instead, so redo has to splice them back in from the
		// old version — which upstream can do because those two flags are only
		// ever set when both versions are on the SAME page
		// (heapam_xlog.c:933-945 asserts newblk == oldblk).
		var oldTuple []byte
		if flags&(xlhUpdatePrefixFromOld|xlhUpdateSuffixFromOld) != 0 {
			if oldBlock.Block != block.Block {
				return fmt.Errorf("wal: xlog heap-update prefix/suffix compression with cross-page old tuple (old blk %d, new blk %d)",
					oldBlock.Block, block.Block)
			}
			oldTuple, err = storage.PageGetItemRaw(page, oldOffnum)
			if err != nil {
				return fmt.Errorf("wal: xlog heap-update read old tuple for prefix/suffix splice (slot %d): %w", oldOffnum, err)
			}
		}
		newTupleBytes, err := decodeXLogHeapUpdateNewTuple(block, flags, oldTuple,
			storage.TransactionID(xlog.Header.XID), newOffnum)
		if err != nil {
			return err
		}
		// M0131-S21c: place the new version AT new_offnum, exactly as
		// heap_xlog_update's PageAddItemExtended(..., xlrec->new_offnum,
		// PAI_OVERWRITE | PAI_IS_HEAP) does — the third member of the
		// insert/multi-insert/update sibling family. This used to APPEND
		// (PageAddHeapTuple) and then merely complain when the resulting slot
		// disagreed with the record ("new-slot drift"), which refused the start
		// on every real-PG tail that updates into a pruned page's hole: the
		// append lands at the end of the array while the record says slot N.
		if err := redoHeapPageAddItemOverwrite(page, newOffnum, newTupleBytes); err != nil {
			return fmt.Errorf("wal: xlog heap-update add new tuple: %w", err)
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
	//
	// M0131-S21d: block 1 is upstream's RBM_NORMAL read — XLogReadBufferForRedo
	// with no WILL_INIT — so an absent or all-zero page is BLK_NOTFOUND and the
	// stamp is skipped, not an error (redoExistingHeapPageForBlock, and see its
	// invalid-page-table deviation note). The new-tuple page above is the one
	// that may be extended into existence; the page holding the row's OLD
	// version cannot legitimately be missing unless the relation is dropped or
	// truncated later in the same stream.
	oldPage, skipOld, err := redoExistingHeapPageForBlock(mgr, oldBlock, storage.LSN(r.EndLSN))
	if err != nil {
		return err
	}
	if skipOld {
		return nil
	}
	if err := stampOld(oldPage); err != nil {
		return fmt.Errorf("wal: xlog heap-update stamp old tuple: %w", err)
	}
	storage.MustHeader(oldPage).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, oldBlock.Block, oldPage)
}

// (M0130-S11.4 slice 3b-2c-ii-B2-b-ii retired `redoBlobIndexFormat`, the
// hard-wired blob format the two B-tree redo entry points used to decode and
// re-insert with. It was the one site that could not answer the format question
// honestly — redo holds a relfilenode, and recovery has no catalog to turn one
// into a key descriptor, since the catalog it would read is itself being
// replayed — so instead of resolving the format, both entry points stopped
// needing one: the insert replays at the RECORDED offset number (upstream
// btree_xlog_insert) and the parent-downlink removal works on raw item bytes.
// That unblocks 3b-2c-ii-B2-c, the flip.)

// replayDecodedXLogBtreeInsert applies a PG-format xl_btree_insert: insert the
// IndexTuple carried in block 0's data into the target page at the offset number
// carried in the record's main data, exactly as upstream btree_xlog_insert
// (nbtxlog.c:160-247) does. Mirrors the native replayBtreeInsert, so goopg↔goopg
// replay is identical; a full-page image is restored instead. Idempotent via
// pd_lsn.
//
// `isleaf` / `ismeta` are upstream's own arguments, set from the opcode:
// INSERT_LEAF (true,false,false), INSERT_UPPER (false,false,false),
// INSERT_META (false,true,false), INSERT_POST (true,false,true). goopg only
// ever emits INSERT_LEAF; the other three arrive from a real PG crash tail
// (M0131-S21b parts 1-2) and add three limbs:
//
//   - block 1 (!isleaf) — an insert into an INTERNAL page is what finishes the
//     child's split, so redo clears the child's BTP_INCOMPLETE_SPLIT flag.
//     Upstream does this FIRST, before touching block 0, and unconditionally:
//     _bt_insertonpg always registers cbuf as block 1 on the !isleaf path
//     (nbtinsert.c:1342-1343), so a missing block 1 is a malformed record, not
//     an optional limb.
//   - block 2 (ismeta) — the metapage, rebuilt from the carried
//     xl_btree_metadata. Registered WILL_INIT (nbtinsert.c:1359-1360), so it is
//     re-initialised from scratch rather than read-modify-written.
//   - block 0 (posting) — the data run is {uint16 postingoff, orignewitem}
//     instead of a bare item, and redo re-runs the primary's posting-list
//     split before adding the item (btree.ApplyInsertPostingRecordAt).
//
// Each limb carries its own pd_lsn idempotency, matching the per-buffer
// discipline of upstream's redo (and of replayDecodedXLogBtreeNewRoot): a replay
// interrupted between limbs resumes correctly. Note the block-0 image branch
// does NOT return early — upstream's BLK_RESTORED only skips block 0's manual
// mutation, the metapage limb still runs.
func replayDecodedXLogBtreeInsert(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord, isleaf, ismeta, posting bool) error {
	endLSN := storage.LSN(r.EndLSN)

	if !isleaf {
		child, ok := xlogBlockRefByID(xlog, 1)
		if !ok {
			return fmt.Errorf("wal: xlog btree-insert missing block 1 (child of an internal-page insert)")
		}
		if child.HasImage && child.ImageApply {
			if err := restoreDecodedXLogBlockImage(mgr, child, endLSN); err != nil {
				return fmt.Errorf("wal: xlog btree-insert child image: %w", err)
			}
		} else if err := replayExistingXLogBlock(mgr, child, endLSN, btree.ReplayClearIncompleteSplit); err != nil {
			return fmt.Errorf("wal: xlog btree-insert child apply: %w", err)
		}
	}

	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-insert missing block 0")
	}
	if block.HasImage && block.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, block, endLSN); err != nil {
			return err
		}
	} else {
		if len(xlog.MainData) < sizeOfXLogBtreeInsertData {
			return fmt.Errorf("wal: xlog btree-insert: main data len %d (want >= %d)", len(xlog.MainData), sizeOfXLogBtreeInsertData)
		}
		offnum := binary.LittleEndian.Uint16(xlog.MainData[0:2])
		apply := func(page storage.Page) error {
			return btree.ApplyInsertRecordAt(page, block.Data, offnum)
		}
		if posting {
			apply = func(page storage.Page) error {
				return btree.ApplyInsertPostingRecordAt(page, block.Data, offnum)
			}
		}
		if err := replayExistingXLogBlock(mgr, block, endLSN, apply); err != nil {
			return fmt.Errorf("wal: xlog btree-insert apply: %w", err)
		}
	}

	if !ismeta {
		return nil
	}
	return replayDecodedXLogBtreeRestoreMeta(mgr, xlog, endLSN, 2, "btree-insert")
}

// sizeOfBtreeDedupData is SizeOfBtreeDedup (nbtxlog.h:177): xl_btree_dedup is
// one uint16 nintervals, with the interval array riding block 0's data run
// rather than the main data.
const sizeOfBtreeDedupData = 2

// replayDecodedXLogBtreeDedup applies a PG-format xl_btree_dedup, mirroring
// upstream btree_xlog_dedup (nbtxlog.c:463-553): rebuild block 0 from its own
// pre-image, collapsing each carried {baseoff, nitems} run into one posting
// list (btree.ReplayDedupPage).
//
// The record is REBUILD-shaped rather than edit-shaped, which is why the block
// carries no items: everything the merged tuples are made of is already on the
// page being replayed. That also makes the FPI branch the plain one — an image
// is the finished page, so redo restores it and stops.
//
// M0131-S21b part 2b. Idempotent via pd_lsn, per replayExistingXLogBlock.
func replayDecodedXLogBtreeDedup(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	endLSN := storage.LSN(r.EndLSN)
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-dedup missing block 0")
	}
	if block.HasImage && block.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, block, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-dedup image: %w", err)
		}
		return nil
	}
	if len(xlog.MainData) < sizeOfBtreeDedupData {
		return fmt.Errorf("wal: xlog btree-dedup: main data len %d (want >= %d)", len(xlog.MainData), sizeOfBtreeDedupData)
	}
	nintervals := int(binary.LittleEndian.Uint16(xlog.MainData[0:2]))
	// nintervals is the record's own count, so a short data run means a
	// truncated record; trusting len(block.Data) instead would silently replay
	// a partial dedup pass and leave the page half-merged.
	if want := nintervals * btree.SizeOfDedupInterval; len(block.Data) < want {
		return fmt.Errorf("wal: xlog btree-dedup: block 0 data len %d for %d intervals (want >= %d)",
			len(block.Data), nintervals, want)
	}
	intervals := make([]btree.DedupInterval, nintervals)
	for i := range intervals {
		off := i * btree.SizeOfDedupInterval
		intervals[i] = btree.DedupInterval{
			BaseOff: binary.LittleEndian.Uint16(block.Data[off : off+2]),
			NItems:  binary.LittleEndian.Uint16(block.Data[off+2 : off+4]),
		}
	}
	if err := replayExistingXLogBlock(mgr, block, endLSN, func(page storage.Page) error {
		return btree.ReplayDedupPage(page, intervals)
	}); err != nil {
		return fmt.Errorf("wal: xlog btree-dedup apply: %w", err)
	}
	return nil
}

// replayDecodedXLogBtreeRestoreMeta is upstream's _bt_restore_meta
// (nbtxlog.c:80-127): rebuild the metapage at `blockID` from the carried
// xl_btree_metadata. The buffer is registered WILL_INIT at every call site, so
// the block need not exist yet.
//
// M0131-S21b part 1. Factored out because two opcodes need it on different
// block ids — XLOG_BTREE_INSERT_META on block 2 and XLOG_BTREE_META_CLEANUP on
// block 0 — and XLOG_BTREE_NEWROOT already open-codes the same thing on block 2.
func replayDecodedXLogBtreeRestoreMeta(mgr *storage.Manager, xlog *XLogDecodedRecord, endLSN storage.LSN, blockID byte, what string) error {
	meta, ok := xlogBlockRefByID(xlog, blockID)
	if !ok {
		return fmt.Errorf("wal: xlog %s missing block %d (metapage)", what, blockID)
	}
	if meta.HasImage && meta.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, meta, endLSN); err != nil {
			return fmt.Errorf("wal: xlog %s meta image: %w", what, err)
		}
		return nil
	}
	md, err := decodeXLogBtreeMetadata(meta.Data)
	if err != nil {
		return fmt.Errorf("wal: xlog %s meta: %w", what, err)
	}
	if err := replayInitedXLogBlock(mgr, meta, endLSN, func(page storage.Page) error {
		return btree.ReplayRestoreMetaPage(page, md)
	}); err != nil {
		return fmt.Errorf("wal: xlog %s meta apply: %w", what, err)
	}
	return nil
}

// replayDecodedXLogBtreeNewRoot applies a PG-format xl_btree_newroot, mirroring
// upstream btree_xlog_newroot (nbtxlog.c:764-800):
//
//	block 0 — re-initialise the page as a root at the record's level, then
//	          restore its items from block data (level > 0 only);
//	block 1 — clear the left child's incomplete-split flag (level > 0 only);
//	block 2 — rebuild the metapage from the carried xl_btree_metadata.
//
// Each block is separately idempotent via its own pd_lsn, so a replay
// interrupted between blocks resumes correctly — the same per-limb discipline
// the native replayBtreeNewRoot uses. Block 0 and block 2 are WILL_INIT, so
// neither has to exist yet: they are extended when the record is the first
// thing to touch them (writeBlockOrExtend), matching PG's
// XLogInitBufferForRedo.
func replayDecodedXLogBtreeNewRoot(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	endLSN := storage.LSN(r.EndLSN)
	if len(xlog.MainData) < sizeOfXLogBtreeNewRootData {
		return fmt.Errorf("wal: xlog btree-newroot: main data len %d (want >= %d)", len(xlog.MainData), sizeOfXLogBtreeNewRootData)
	}
	level := binary.LittleEndian.Uint32(xlog.MainData[4:8])

	root, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-newroot missing block 0")
	}
	if root.HasImage && root.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, root, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-newroot root image: %w", err)
		}
	} else {
		var items [][]byte
		if level > 0 {
			parsed, err := btree.PGParseRestorePageData(root.Data)
			if err != nil {
				return fmt.Errorf("wal: xlog btree-newroot root items: %w", err)
			}
			items = parsed
		}
		if err := replayInitedXLogBlock(mgr, root, endLSN, func(page storage.Page) error {
			return btree.ReplayNewRootPage(page, level, items)
		}); err != nil {
			return fmt.Errorf("wal: xlog btree-newroot root apply: %w", err)
		}
	}

	// Block 1 exists only for a level > 0 root; upstream's redo reads it under
	// exactly the same condition.
	if child, ok := xlogBlockRefByID(xlog, 1); ok {
		if child.HasImage && child.ImageApply {
			if err := restoreDecodedXLogBlockImage(mgr, child, endLSN); err != nil {
				return fmt.Errorf("wal: xlog btree-newroot child image: %w", err)
			}
		} else if err := replayExistingXLogBlock(mgr, child, endLSN, btree.ReplayClearIncompleteSplit); err != nil {
			return fmt.Errorf("wal: xlog btree-newroot child apply: %w", err)
		}
	}

	// Shared with XLOG_BTREE_INSERT_META / XLOG_BTREE_META_CLEANUP since
	// M0131-S21b part 1 — all three limbs are upstream's one _bt_restore_meta.
	return replayDecodedXLogBtreeRestoreMeta(mgr, xlog, endLSN, 2, "btree-newroot")
}

// replayDecodedXLogBtreeMarkPageHalfDead applies a PG-format
// xl_btree_mark_page_halfdead, mirroring upstream btree_xlog_mark_page_halfdead
// (nbtxlog.c:762-848) in the same block order:
//
//	block 1 — the to-be-deleted subtree's parent: retarget poffset's downlink at
//	          the right neighbour's child and drop the neighbour's item.
//	          Upstream does this FIRST so it can release the internal page's lock
//	          without coupling it across levels;
//	block 0 — the leaf, recreated from scratch as a half-dead page carrying one
//	          dummy high key whose downlink field holds the top parent.
//
// Each block is separately idempotent via its own pd_lsn, so a replay
// interrupted between the two resumes correctly — the same per-limb discipline
// replayDecodedXLogBtreeNewRoot uses. Block 0 is WILL_INIT.
//
// M0130-S11.5d-1. A real-PG record carrying a full-page image on either block is
// restored from the image instead, exactly as upstream's BLK_RESTORED arm does.
func replayDecodedXLogBtreeMarkPageHalfDead(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	endLSN := storage.LSN(r.EndLSN)
	if len(xlog.MainData) < sizeOfXLogBtreeMarkPageHalfDeadData {
		return fmt.Errorf("wal: xlog btree-mark-halfdead: main data len %d (want >= %d)",
			len(xlog.MainData), sizeOfXLogBtreeMarkPageHalfDeadData)
	}
	le := binary.LittleEndian
	poffset := le.Uint16(xlog.MainData[0:2])
	leftblk := storage.BlockNumber(le.Uint32(xlog.MainData[8:12]))
	rightblk := storage.BlockNumber(le.Uint32(xlog.MainData[12:16]))
	topparent := storage.BlockNumber(le.Uint32(xlog.MainData[16:20]))

	parent, ok := xlogBlockRefByID(xlog, 1)
	if !ok {
		return fmt.Errorf("wal: xlog btree-mark-halfdead missing block 1 (parent)")
	}
	if parent.HasImage && parent.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, parent, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-mark-halfdead parent image: %w", err)
		}
	} else if err := replayExistingXLogBlock(mgr, parent, endLSN, func(page storage.Page) error {
		return btree.ReplayHalfDeadParent(page, poffset)
	}); err != nil {
		return fmt.Errorf("wal: xlog btree-mark-halfdead parent apply: %w", err)
	}

	leaf, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-mark-halfdead missing block 0 (leaf)")
	}
	if leaf.HasImage && leaf.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, leaf, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-mark-halfdead leaf image: %w", err)
		}
		return nil
	}
	if err := replayInitedXLogBlock(mgr, leaf, endLSN, func(page storage.Page) error {
		return btree.ReplayMarkHalfDeadLeaf(page, leftblk, rightblk, topparent)
	}); err != nil {
		return fmt.Errorf("wal: xlog btree-mark-halfdead leaf apply: %w", err)
	}
	return nil
}

// replayDecodedXLogBtreeUnlinkPage applies a PG-format xl_btree_unlink_page,
// mirroring upstream btree_xlog_unlink_page (nbtxlog.c:850-1005) in the same
// block order — which is upstream's LOCK order, left to right, and is preserved
// here because a torn replay must leave the sibling chain traversable in the
// same direction a live reader walks it:
//
//	block 1 — the left sibling, if any: btpo_next skips past the target;
//	block 0 — the target, recreated from scratch as an empty deleted page
//	          carrying only its BTDeletedPageData safexid;
//	block 2 — the right sibling: btpo_prev skips back past the target;
//	block 3 — the half-dead leaf, only when the target was an INTERNAL page:
//	          recreated with a dummy high key naming the next child down, so the
//	          next _bt_unlink_halfdead_page call can find it;
//	block 4 — the metapage, only on XLOG_BTREE_UNLINK_PAGE_META.
//
// Each block is separately idempotent via its own pd_lsn, the same per-limb
// discipline replayDecodedXLogBtreeNewRoot and …MarkPageHalfDead use. Blocks 0
// and 3 are WILL_INIT.
//
// M0130-S11.5d-2. A real-PG record carrying a full-page image on any block is
// restored from the image instead, exactly as upstream's BLK_RESTORED arm does.
func replayDecodedXLogBtreeUnlinkPage(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord, isMeta bool) error {
	endLSN := storage.LSN(r.EndLSN)
	if len(xlog.MainData) < sizeOfXLogBtreeUnlinkPageData {
		return fmt.Errorf("wal: xlog btree-unlink-page: main data len %d (want >= %d)",
			len(xlog.MainData), sizeOfXLogBtreeUnlinkPageData)
	}
	le := binary.LittleEndian
	leftsib := storage.BlockNumber(le.Uint32(xlog.MainData[0:4]))
	rightsib := storage.BlockNumber(le.Uint32(xlog.MainData[4:8]))
	level := le.Uint32(xlog.MainData[8:12])
	safexid := le.Uint64(xlog.MainData[16:24])
	leafleftsib := storage.BlockNumber(le.Uint32(xlog.MainData[24:28]))
	leafrightsib := storage.BlockNumber(le.Uint32(xlog.MainData[28:32]))
	leaftopparent := storage.BlockNumber(le.Uint32(xlog.MainData[32:36]))

	// Block 1 is registered under exactly the condition upstream's redo tests.
	if leftsib != btree.PNone {
		left, ok := xlogBlockRefByID(xlog, 1)
		if !ok {
			return fmt.Errorf("wal: xlog btree-unlink-page leftsib %d but no block 1", leftsib)
		}
		if left.HasImage && left.ImageApply {
			if err := restoreDecodedXLogBlockImage(mgr, left, endLSN); err != nil {
				return fmt.Errorf("wal: xlog btree-unlink-page left image: %w", err)
			}
		} else if err := replayExistingXLogBlock(mgr, left, endLSN, func(page storage.Page) error {
			return btree.ReplayUnlinkLeftSibling(page, rightsib)
		}); err != nil {
			return fmt.Errorf("wal: xlog btree-unlink-page left apply: %w", err)
		}
	}

	target, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-unlink-page missing block 0 (target)")
	}
	if target.HasImage && target.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, target, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-unlink-page target image: %w", err)
		}
	} else if err := replayInitedXLogBlock(mgr, target, endLSN, func(page storage.Page) error {
		return btree.ReplayUnlinkTargetPage(page, leftsib, rightsib, level, safexid)
	}); err != nil {
		return fmt.Errorf("wal: xlog btree-unlink-page target apply: %w", err)
	}

	// Block 2 is unconditional — upstream reads it without testing rightsib,
	// because a rightmost page is never deleted.
	right, ok := xlogBlockRefByID(xlog, 2)
	if !ok {
		return fmt.Errorf("wal: xlog btree-unlink-page missing block 2 (right sibling)")
	}
	if right.HasImage && right.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, right, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-unlink-page right image: %w", err)
		}
	} else if err := replayExistingXLogBlock(mgr, right, endLSN, func(page storage.Page) error {
		return btree.ReplayUnlinkRightSibling(page, leftsib)
	}); err != nil {
		return fmt.Errorf("wal: xlog btree-unlink-page right apply: %w", err)
	}

	// Block 3 exists iff the target was internal; upstream gates on the block
	// reference's presence, not on the level, so this does the same.
	if leaf, ok := xlogBlockRefByID(xlog, 3); ok {
		if leaf.HasImage && leaf.ImageApply {
			if err := restoreDecodedXLogBlockImage(mgr, leaf, endLSN); err != nil {
				return fmt.Errorf("wal: xlog btree-unlink-page leaf image: %w", err)
			}
		} else if err := replayInitedXLogBlock(mgr, leaf, endLSN, func(page storage.Page) error {
			// Byte-identical to phase 1's block 0: upstream builds the
			// half-dead page the same way in both redo routines, which is what
			// makes the two records composable across an arbitrary number of
			// levels.
			return btree.ReplayMarkHalfDeadLeaf(page, leafleftsib, leafrightsib, leaftopparent)
		}); err != nil {
			return fmt.Errorf("wal: xlog btree-unlink-page leaf apply: %w", err)
		}
	}

	if !isMeta {
		return nil
	}
	meta, ok := xlogBlockRefByID(xlog, 4)
	if !ok {
		return fmt.Errorf("wal: xlog btree-unlink-page-meta missing block 4 (metapage)")
	}
	if meta.HasImage && meta.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, meta, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-unlink-page meta image: %w", err)
		}
		return nil
	}
	md, err := decodeXLogBtreeMetadata(meta.Data)
	if err != nil {
		return fmt.Errorf("wal: xlog btree-unlink-page meta: %w", err)
	}
	if err := replayInitedXLogBlock(mgr, meta, endLSN, func(page storage.Page) error {
		return btree.ReplayRestoreMetaPage(page, md)
	}); err != nil {
		return fmt.Errorf("wal: xlog btree-unlink-page meta apply: %w", err)
	}
	return nil
}

// replayDecodedXLogBtreeSplit applies a PG-format xl_btree_split, mirroring
// upstream btree_xlog_split (nbtxlog.c:180-352) in the same block order:
//
//	block 3 — clear the incomplete-split flag on the child one level down whose
//	          split this insertion finishes (INTERNAL split only — upstream
//	          reads block 3 under exactly `if (!isleaf)`, and does it FIRST
//	          because it needs no cross-level lock coupling);
//	block 1 — rebuild the new right sibling from scratch: init, opaque from the
//	          record's level and the record's own block tags, items restored
//	          from block data;
//	block 0 — the left half, restored from its full-page image;
//	block 2 — relink the old right sibling's back-link to the new right page
//	          (non-rightmost split only).
//
// Each block is separately idempotent via its own pd_lsn, so a replay
// interrupted between blocks resumes correctly — the same per-limb discipline
// replayDecodedXLogBtreeNewRoot uses. Block 1 is WILL_INIT and need not exist
// yet; it is extended on first touch, matching XLogInitBufferForRedo.
//
// A block 0 with no image takes upstream's incremental arm (M0130-S11.5b-2):
// the left half is rebuilt from the items already on the page, cut at
// firstrightoff, with the record's new item spliced in at newitemoff when the
// INFO byte says XLOG_BTREE_SPLIT_L, under the new high key the block data
// carries. `newItemOnLeft` therefore comes from the record kind, not from the
// payload — the payload's tuple run is untagged.
func replayDecodedXLogBtreeSplit(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord, newItemOnLeft bool) error {
	endLSN := storage.LSN(r.EndLSN)
	if len(xlog.MainData) < sizeOfXLogBtreeSplitData {
		return fmt.Errorf("wal: xlog btree-split: main data len %d (want >= %d)", len(xlog.MainData), sizeOfXLogBtreeSplitData)
	}
	level := binary.LittleEndian.Uint32(xlog.MainData[0:4])

	left, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-split missing block 0")
	}
	right, ok := xlogBlockRefByID(xlog, 1)
	if !ok {
		return fmt.Errorf("wal: xlog btree-split missing block 1")
	}
	// Upstream reads the sibling's block tag up front and treats an absent
	// block 2 as P_NONE — the right page's forward link comes from it.
	sib, hasSib := xlogBlockRefByID(xlog, 2)
	sibBlk := storage.InvalidBlockNumber
	if hasSib {
		sibBlk = sib.Block
	}

	// Block 3 first, as upstream does: the flag clear needs no cross-level lock
	// coupling, and a level > 0 record that omits it is one a real PG standby
	// would PANIC on (XLogReadBufferForRedo on an unregistered block id), so
	// say so here rather than silently leaving the child marked.
	child, hasChild := xlogBlockRefByID(xlog, 3)
	if level > 0 && !hasChild {
		return fmt.Errorf("wal: xlog btree-split at level %d missing block 3 (child)", level)
	}
	if hasChild {
		if child.HasImage && child.ImageApply {
			if err := restoreDecodedXLogBlockImage(mgr, child, endLSN); err != nil {
				return fmt.Errorf("wal: xlog btree-split child image: %w", err)
			}
		} else if err := replayExistingXLogBlock(mgr, child, endLSN, btree.ReplayClearIncompleteSplit); err != nil {
			return fmt.Errorf("wal: xlog btree-split child apply: %w", err)
		}
	}

	if right.HasImage && right.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, right, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-split right image: %w", err)
		}
	} else {
		items, err := btree.PGParseRestorePageData(right.Data)
		if err != nil {
			return fmt.Errorf("wal: xlog btree-split right items: %w", err)
		}
		if err := replayInitedXLogBlock(mgr, right, endLSN, func(page storage.Page) error {
			return btree.ReplaySplitRightPage(page, level, left.Block, sibBlk, items)
		}); err != nil {
			return fmt.Errorf("wal: xlog btree-split right apply: %w", err)
		}
	}

	if left.HasImage && left.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, left, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-split left image: %w", err)
		}
	} else {
		newItem, highKey, perr := btree.ParseSplitLeftBlockData(left.Data, newItemOnLeft)
		if perr != nil {
			return fmt.Errorf("wal: xlog btree-split left data: %w", perr)
		}
		desc := btree.SplitLeftDescription{
			FirstRightOff: binary.LittleEndian.Uint16(xlog.MainData[4:6]),
			NewItemOff:    binary.LittleEndian.Uint16(xlog.MainData[6:8]),
			NewItemOnLeft: newItemOnLeft,
			NewItem:       newItem,
			HighKey:       highKey,
		}
		if postingOff := binary.LittleEndian.Uint16(xlog.MainData[8:10]); postingOff != 0 {
			// A posting-list split at insert time: upstream's redo repeats it
			// with _bt_swap_posting over the item before newitemoff. goopg's
			// dedup runs over the whole page instead and never emits this, so
			// replaying the record without it would silently leave the pre-split
			// posting tuple on the page next to its own new item.
			return fmt.Errorf("wal: xlog btree-split postingoff=%d: PG's posting-list split (_bt_swap_posting) is not implemented", postingOff)
		}
		if err := replayExistingXLogBlock(mgr, left, endLSN, func(page storage.Page) error {
			return btree.ReplaySplitLeftPage(page, level, right.Block, desc)
		}); err != nil {
			return fmt.Errorf("wal: xlog btree-split left apply: %w", err)
		}
	}

	if hasSib {
		if sib.HasImage && sib.ImageApply {
			if err := restoreDecodedXLogBlockImage(mgr, sib, endLSN); err != nil {
				return fmt.Errorf("wal: xlog btree-split sibling image: %w", err)
			}
		} else if err := replayExistingXLogBlock(mgr, sib, endLSN, func(page storage.Page) error {
			return btree.ReplaySplitSiblingPrev(page, right.Block)
		}); err != nil {
			return fmt.Errorf("wal: xlog btree-split sibling apply: %w", err)
		}
	}
	return nil
}

// sizeOfXLogBtreeDeleteData is PG's SizeOfBtreeDelete (nbtxlog.h:256):
// snapshotConflictHorizon(4) + ndeleted(2) + nupdated(2) + isCatalogRel(1).
// The two conflict-resolution fields are read by hot standby only, which goopg
// does not implement — crash recovery reads the two counts.
const sizeOfXLogBtreeDeleteData = 9

// replayDecodedXLogBtreeDelete applies a PG-format xl_btree_delete, mirroring
// upstream btree_xlog_delete (nbtxlog.c:651-712): rewrite the posting-list
// tuples that lost some of their TIDs, delete the items that went away whole,
// and clear the page's garbage hint.
//
// Only a real PG primary emits this opcode. goopg has no LP_DEAD simple-deletion
// pass at all (its index cleanup rides RecordKindBtreeVacuum), but a PG primary
// runs one whenever an insert is short of room on a page with killed entries —
// so it is ordinary traffic in the crash tail goopg must replay.
//
// M0131-S21b part 3. Idempotent via pd_lsn, per replayExistingXLogBlock.
func replayDecodedXLogBtreeDelete(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	endLSN := storage.LSN(r.EndLSN)
	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-delete missing block 0")
	}
	if block.HasImage && block.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, block, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-delete image: %w", err)
		}
		return nil
	}
	if len(xlog.MainData) < sizeOfXLogBtreeDeleteData {
		return fmt.Errorf("wal: xlog btree-delete: main data len %d (want >= %d)", len(xlog.MainData), sizeOfXLogBtreeDeleteData)
	}
	ndeleted := int(binary.LittleEndian.Uint16(xlog.MainData[4:6]))
	nupdated := int(binary.LittleEndian.Uint16(xlog.MainData[6:8]))
	deleted, updates, err := decodeXLogBtreeDeletePayload(block.Data, ndeleted, nupdated)
	if err != nil {
		return fmt.Errorf("wal: xlog btree-delete: %w", err)
	}
	if err := replayExistingXLogBlock(mgr, block, endLSN, func(page storage.Page) error {
		return btree.ReplayDeletePage(page, deleted, updates)
	}); err != nil {
		return fmt.Errorf("wal: xlog btree-delete apply: %w", err)
	}
	return nil
}

// decodeXLogBtreeDeletePayload parses block 0's data run, which xl_btree_delete
// and xl_btree_vacuum share byte for byte (nbtxlog.h:197-237): the deleted
// offset numbers, then the updated offset numbers, then one variable-length
// xl_btree_update per updated offset.
//
// Every length is checked against the record's OWN counts rather than against
// what the buffer happens to hold: a truncated run must be refused, not
// replayed as a shorter deletion that leaves dead entries behind.
func decodeXLogBtreeDeletePayload(data []byte, ndeleted, nupdated int) ([]uint16, []btree.PostingUpdate, error) {
	want := 2 * (ndeleted + nupdated)
	if len(data) < want {
		return nil, nil, fmt.Errorf("block 0 data len %d (want >= %d for ndeleted=%d nupdated=%d)",
			len(data), want, ndeleted, nupdated)
	}
	deleted := make([]uint16, ndeleted)
	for i := range deleted {
		deleted[i] = binary.LittleEndian.Uint16(data[2*i : 2*i+2])
	}
	if nupdated == 0 {
		return deleted, nil, nil
	}
	updates := make([]btree.PostingUpdate, nupdated)
	for i := range updates {
		off := 2 * (ndeleted + i)
		updates[i].Offset = binary.LittleEndian.Uint16(data[off : off+2])
	}
	// The xl_btree_update array is self-describing (each entry's ndeletedtids
	// states its own length), so it is walked rather than indexed.
	pos := want
	for i := range updates {
		if len(data)-pos < btree.SizeOfBtreeUpdate {
			return nil, nil, fmt.Errorf("block 0 data ends inside xl_btree_update %d of %d", i, nupdated)
		}
		ndeletedtids := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += btree.SizeOfBtreeUpdate
		if len(data)-pos < 2*ndeletedtids {
			return nil, nil, fmt.Errorf("block 0 data holds %d bytes for xl_btree_update %d's %d TID offsets",
				len(data)-pos, i, ndeletedtids)
		}
		tids := make([]uint16, ndeletedtids)
		for j := range tids {
			tids[j] = binary.LittleEndian.Uint16(data[pos+2*j : pos+2*j+2])
		}
		pos += 2 * ndeletedtids
		updates[i].DeleteTIDs = tids
	}
	return deleted, updates, nil
}

// replayDecodedXLogBtreeVacuum applies a PG-format xl_btree_vacuum, mirroring
// upstream btree_xlog_vacuum (nbtxlog.c:479-528): delete the named offset
// numbers from the leaf page, then clear its garbage hint.
//
// Block 0 carrying an apply-image is the other form goopg emits (see
// EncodeBtreeVacuumPG: the rewrite was a consolidation, or touched posting
// lists, or emptied the page) and is upstream's BLK_RESTORED arm — restore it
// and do nothing else, exactly as upstream skips the deletion under a restored
// image.
//
// nupdated > 0 is a record only a real PG primary produces: it rewrites posting
// tuples in place (xl_btree_update) where goopg re-marshals surviving TIDs as
// separate items. It was refused outright until M0131-S21b part 3, which built
// that rewrite for XLOG_BTREE_DELETE; the two records share the payload
// format AND the page work byte for byte (upstream's btree_xlog_vacuum and
// btree_xlog_delete both call btree_xlog_updates then PageIndexMultiDelete),
// so this path shares the decoder and btree.ReplayDeletePage with it rather
// than growing a second, drifting copy.
//
// The one upstream difference — vacuum clears btpo_cycleid, delete deliberately
// does not — is not a difference here: goopg's opaque has no cycle-id field
// (the vacuum cycle id is a concurrency hint for _bt_vacuum_needs_cleanup,
// which goopg does not implement).
func replayDecodedXLogBtreeVacuum(mgr *storage.Manager, r Record, xlog *XLogDecodedRecord) error {
	endLSN := storage.LSN(r.EndLSN)
	if len(xlog.MainData) < sizeOfXLogBtreeVacuumData {
		return fmt.Errorf("wal: xlog btree-vacuum: main data len %d (want >= %d)", len(xlog.MainData), sizeOfXLogBtreeVacuumData)
	}
	ndeleted := int(binary.LittleEndian.Uint16(xlog.MainData[0:2]))
	nupdated := int(binary.LittleEndian.Uint16(xlog.MainData[2:4]))

	block, ok := xlogBlockRefByID(xlog, 0)
	if !ok {
		return fmt.Errorf("wal: xlog btree-vacuum missing block 0")
	}
	if block.HasImage && block.ImageApply {
		return restoreDecodedXLogBlockImage(mgr, block, endLSN)
	}
	deleted, updates, err := decodeXLogBtreeDeletePayload(block.Data, ndeleted, nupdated)
	if err != nil {
		return fmt.Errorf("wal: xlog btree-vacuum: %w", err)
	}
	return replayExistingXLogBlock(mgr, block, endLSN, func(page storage.Page) error {
		return btree.ReplayDeletePage(page, deleted, updates)
	})
}

// replayInitedXLogBlock applies `apply` to a WILL_INIT block: the prior page
// contents are irrelevant (apply rebuilds the page from scratch), so the block
// need not exist yet — it is extended when the record is the first thing to
// touch it. When it does exist and already carries an LSN at or past the
// record's, the mutation is skipped as already applied.
func replayInitedXLogBlock(mgr *storage.Manager, block XLogBlockRef, endLSN storage.LSN, apply func(storage.Page) error) error {
	page := make(storage.Page, storage.BlockSize)
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return err
	}
	if block.Block < nblocks {
		if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
			return err
		}
		if !storage.IsNew(page) && storage.MustHeader(page).LSN() >= endLSN {
			return nil // already applied
		}
	}
	if err := apply(page); err != nil {
		return err
	}
	storage.MustHeader(page).SetLSN(endLSN)
	return writeBlockOrExtend(mgr, block.Rel, block.Block, page)
}

// replayExistingXLogBlock applies `apply` to a block the record mutates in
// place. Unlike replayInitedXLogBlock the page must already exist and be
// initialised: the mutation reads the current contents. When it does not exist,
// the record is SKIPPED (upstream's BLK_NOTFOUND), not refused — see
// redoExistingHeapPageForBlock for why, and for the invalid-page-table
// deviation that skip carries.
func replayExistingXLogBlock(mgr *storage.Manager, block XLogBlockRef, endLSN storage.LSN, apply func(storage.Page) error) error {
	// M0131-S21f: every edit-shaped btree record upstream reads with
	// XLogReadBufferForRedo (RBM_NORMAL) — nbtxlog.c's insert-on-existing-page,
	// delete, vacuum and mark-page-halfdead arms all skip on BLK_NOTFOUND.
	page, skip, err := redoExistingHeapPageForBlock(mgr, block, endLSN)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	if err := apply(page); err != nil {
		return err
	}
	storage.MustHeader(page).SetLSN(endLSN)
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
	// M0131-S21f: RBM_NORMAL, like upstream's heap_xlog_prune_freeze. A pruned
	// page that a later record in this same stream drops or truncates away is
	// BLK_NOTFOUND, and the prune is moot rather than fatal.
	page, skip, err := redoExistingHeapPageForBlock(mgr, block, storage.LSN(r.EndLSN))
	if err != nil {
		return fmt.Errorf("wal: xlog heap-prune: %w", err)
	}
	if skip {
		return nil
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
	// root-0033: compact whenever the record carries ANY prune action, not
	// only when it has now-unused slots. The runtime sibling pagePruneCore
	// (internal/storage/prune.go) runs VacuumHeapPageBySlots on BOTH arms —
	// with the dead set when there are unused slots, and with a nil dead set
	// when the prune produced only redirects — because a redirected chain
	// root becomes ItemIDRedirect and its tuple body must be reclaimed by the
	// repack. Guarding the repack on len(unused) made redo leave that body in
	// place, so the replayed page held LESS free space than the runtime page
	// and the next xl_heap_update redo failed with ErrNoSpaceInPage, leaving
	// the cluster unstartable after a crash under write load. The native
	// replayHeapPruneOpt below always compacts; this PG-format arm (A7) had
	// drifted from both siblings.
	if len(redirects) > 0 || len(unused) > 0 {
		if _, err := storage.VacuumHeapPageBySlots(page, unused); err != nil {
			return fmt.Errorf("wal: xlog heap-prune compact: %w", err)
		}
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
// "nothing to replay" (a freshly initdb'd cluster).
//
// segmentSize of 0 means "the cluster's own wal_segment_size", which ReadAll
// reads back from the stream's long page header (root-0035). It used to be
// coerced to DefaultSegmentSize here, which made startup replay of a cluster
// with a non-default segment size compute every record LSN against the wrong
// base — see readAllUncached for why that silently defeats pd_lsn idempotency.
func ReplayFromDirWithMgr(mgr *storage.Manager, walDir string, segmentSize int64) (ReplayStats, error) {
	return ReplayFromDirWithMgrAt(mgr, walDir, segmentSize, 0)
}

// ReplayFromDirWithMgrAt is ReplayFromDirWithMgr with the control file's redo
// pointer (M0131-S20.2). initdb.Open passes pg_control's checkPointCopy.redo
// here; redoLSN == 0 falls back to the stream scan. See ReplayRecordsFrom.
func ReplayFromDirWithMgrAt(mgr *storage.Manager, walDir string, segmentSize int64, redoLSN uint64) (ReplayStats, error) {
	records, err := ReadAll(walDir, segmentSize)
	if err != nil {
		// Missing pg_wal on a fresh data dir is fine — no records
		// to replay.
		if errors.Is(err, fs.ErrNotExist) {
			return ReplayStats{}, nil
		}
		return ReplayStats{}, err
	}
	return ReplayRecordsFrom(mgr, records, redoLSN)
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
		// M0130-S11.5d-3a: locate the downlink by CHILD BLOCK, ignoring the
		// record's ParentRemoveSlot, and apply upstream's retarget-and-delete
		// — the same `ReplayParentRetargetByChild` the primary runs. The slot
		// index in the record was always advisory (the primary re-located the
		// downlink by identity before writing, M0122-0010), which is precisely
		// what a PG-shaped record may not carry; trusting it here would also
		// let redo delete an unrelated live child's downlink.
		if err := apply(p.ParentBlk, func(page storage.Page) error {
			return btree.ReplayParentRetargetByChild(page, p.LeafBlk)
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
	rel, blk, offnum, item, err := DecodeBtreeInsert(r.Payload)
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
	if err := btree.ApplyInsertRecordAt(page, item, offnum); err != nil {
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
// replayStartAt is replayStart with an authoritative redo pointer from
// pg_control (M0131-S20.2). With redoLSN != 0 the start index comes from the
// pointer alone — the stream scan is not consulted for the anchor, because
// its whole purpose is to reconstruct a redo pointer goopg now has directly,
// and on PG-authored WAL the reconstruction is the part that fails.
//
// The reported CheckpointLSN is still scanned for: it is bookkeeping
// (ReplayStats, the startup xact-stamp pass) rather than a replay anchor, and
// leaving it 0 on a pointer-driven replay would make a recovered cluster look
// checkpoint-less to every caller that reads the stat.
func replayStartAt(records []Record, redoLSN uint64) (int, uint64) {
	if redoLSN == 0 {
		return replayStart(records)
	}
	_, checkpointLSN := replayStart(records)
	// Record LSNs are 1-based absolute positions and redoLSN is a 0-based
	// XLogRecPtr, so "EndLSN > redoLSN" keeps the record the redo pointer
	// sits inside rather than skipping it — the same one-record-early bias
	// replayStart's walk-back has, and idempotent for the same reason.
	startIdx := len(records)
	for i, r := range records {
		if r.EndLSN > redoLSN {
			startIdx = i
			break
		}
	}
	return startIdx, checkpointLSN
}

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
// segmentSize of 0 means "the cluster's own wal_segment_size" (root-0035);
// ReadAll derives it from the stream rather than assuming DefaultSegmentSize.
func DiscoverLastCheckpointLSN(walDir string, segmentSize int64) (uint64, error) {
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
	if rel.TblOid == pgDefaultTableSpaceOID || rel.TblOid == pgGlobalTableSpaceOID {
		// Both the default (1663) and global/shared (1664) tablespaces map
		// back to goopg's TblOid=0; DBOid then selects base/<db> vs global/
		// (sharedOrPerDBRelDir). B4.1a.
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

// SMGR_TRUNCATE_{HEAP,VM,FSM} — flags for xl_smgr_truncate
// (storage_xlog.h:40-44). M0131-S21a-2 part 6.
const (
	smgrTruncateHeap uint32 = 0x0001
	smgrTruncateVM   uint32 = 0x0002
	smgrTruncateFSM  uint32 = 0x0004
)

// decodeXLogSmgrTruncate parses a PG xl_smgr_truncate main-data body
// (BlockNumber blkno + RelFileLocator{spcOid,dbOid,relNumber} + int flags,
// 20 bytes, storage_xlog.h:46-51). The decoded RelFileNode carries no Fork —
// the caller derives it per SMGR_TRUNCATE_* flag, since one record can
// truncate up to three forks to three different lengths. Mirrors
// decodeXLogSmgrCreate's tablespace-OID remap. M0131-S21a-2 part 6.
func decodeXLogSmgrTruncate(mainData []byte) (rel storage.RelFileNode, blkno storage.BlockNumber, flags uint32, err error) {
	if len(mainData) < 20 {
		err = fmt.Errorf("wal: xl_smgr_truncate main-data len %d (want 20)", len(mainData))
		return
	}
	blkno = storage.BlockNumber(binary.LittleEndian.Uint32(mainData[0:4]))
	rel = storage.RelFileNode{
		TblOid: binary.LittleEndian.Uint32(mainData[4:8]),
		DBOid:  binary.LittleEndian.Uint32(mainData[8:12]),
		RelOid: binary.LittleEndian.Uint32(mainData[12:16]),
	}
	flags = binary.LittleEndian.Uint32(mainData[16:20])
	if rel.TblOid == pgDefaultTableSpaceOID || rel.TblOid == pgGlobalTableSpaceOID {
		rel.TblOid = 0
	}
	return rel, blkno, flags, nil
}

// applySmgrTruncate mirrors smgr_redo's XLOG_SMGR_TRUNCATE arm
// (storage.c:997-1094): forcibly recreate the main fork (a later-dropped
// relation still needs its truncate replayed as best-effort, same rationale
// as applySmgrCreate's own idempotent recreate), then truncate each fork the
// flags select — MAIN to blkno (a VACUUM tail truncation, or the surviving
// prefix of a same-transaction TRUNCATE), VM and FSM unconditionally to zero
// (upstream never partially truncates them: FreeSpaceMapTruncateRel's rounds
// only pick the summary level, and the vm fork's truncate always drops to
// zero blocks — visibilitymap.c/freespace.c both re-derive their content
// from a live heap, never from a partial fork). Idempotent via
// TruncateRelationTo's own no-op-if-already-shorter check.
func applySmgrTruncate(mgr *storage.Manager, rel storage.RelFileNode, blkno storage.BlockNumber, flags uint32) error {
	mainRel := rel
	mainRel.Fork = storage.MainFork
	if err := applySmgrCreate(mgr, mainRel); err != nil {
		return err
	}
	if flags&smgrTruncateHeap != 0 {
		if err := mgr.TruncateRelationTo(mainRel, blkno); err != nil {
			return fmt.Errorf("wal: smgr-truncate replay main fork: %w", err)
		}
	}
	if flags&smgrTruncateVM != 0 {
		vmRel := rel
		vmRel.Fork = storage.VisibilityMapFork
		if err := mgr.TruncateRelationTo(vmRel, 0); err != nil {
			return fmt.Errorf("wal: smgr-truncate replay vm fork: %w", err)
		}
	}
	if flags&smgrTruncateFSM != 0 {
		fsmRel := rel
		fsmRel.Fork = storage.FSMFork
		if err := mgr.TruncateRelationTo(fsmRel, 0); err != nil {
			return fmt.Errorf("wal: smgr-truncate replay fsm fork: %w", err)
		}
	}
	return nil
}

// decodeXLogTblspcCreate parses a PG xl_tblspc_create_rec main-data body
// (ts_id Oid + null-terminated ts_path). B4.1d.
func decodeXLogTblspcCreate(mainData []byte) (oid uint32, path string, err error) {
	if len(mainData) < 4 {
		return 0, "", fmt.Errorf("wal: xl_tblspc_create_rec main-data len %d (want >= 4)", len(mainData))
	}
	oid = binary.LittleEndian.Uint32(mainData[0:4])
	rest := mainData[4:]
	if i := bytes.IndexByte(rest, 0); i >= 0 {
		rest = rest[:i]
	}
	return oid, string(rest), nil
}

// decodeXLogTblspcDrop parses a PG xl_tblspc_drop_rec main-data body (ts_id
// Oid). B4.1d.
func decodeXLogTblspcDrop(mainData []byte) (uint32, error) {
	if len(mainData) < 4 {
		return 0, fmt.Errorf("wal: xl_tblspc_drop_rec main-data len %d (want >= 4)", len(mainData))
	}
	return binary.LittleEndian.Uint32(mainData[0:4]), nil
}

// applyTblspcCreate recreates the pg_tblspc/<oid> directory (idempotent).
// goopg's in-place tablespaces are real directories under the data dir; they
// persist on disk across a restart, so this MkdirAll is a defensive no-op in
// the common case and only materializes the directory when replaying onto a
// basebackup that lacks it. B4.1d.
func applyTblspcCreate(mgr *storage.Manager, oid uint32) error {
	dir := filepath.Join(mgr.DataDir(), "pg_tblspc", strconv.FormatUint(uint64(oid), 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("wal: tblspc-create replay MkdirAll %q: %w", dir, err)
	}
	return nil
}

// applyTblspcDrop removes the pg_tblspc/<oid> directory (idempotent). B4.1d.
func applyTblspcDrop(mgr *storage.Manager, oid uint32) error {
	dir := filepath.Join(mgr.DataDir(), "pg_tblspc", strconv.FormatUint(uint64(oid), 10))
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("wal: tblspc-drop replay RemoveAll %q: %w", dir, err)
	}
	return nil
}

// decodeXLogDbaseCreateWalLog parses a PG xl_dbase_create_wal_log_rec main-data
// body {db_id Oid, tablespace_id Oid}. B4.6 Stage 3.
func decodeXLogDbaseCreateWalLog(mainData []byte) (dbOID, tsOID uint32, err error) {
	if len(mainData) < 8 {
		return 0, 0, fmt.Errorf("wal: xl_dbase_create_wal_log_rec main-data len %d (want >= 8)", len(mainData))
	}
	return binary.LittleEndian.Uint32(mainData[0:4]), binary.LittleEndian.Uint32(mainData[4:8]), nil
}

// decodeXLogDbaseDrop parses a PG xl_dbase_drop_rec main-data body {db_id Oid,
// ntablespaces int32, tablespace_ids[] Oid}. Only db_id is needed for goopg's
// single-tablespace replay. B4.6 Stage 3.
func decodeXLogDbaseDrop(mainData []byte) (dbOID uint32, err error) {
	if len(mainData) < 8 {
		return 0, fmt.Errorf("wal: xl_dbase_drop_rec main-data len %d (want >= 8)", len(mainData))
	}
	return binary.LittleEndian.Uint32(mainData[0:4]), nil
}

// applyDbaseCreate recreates the base/<db_id> directory (idempotent). goopg's
// per-database directories persist on disk across a restart, so this MkdirAll is
// a defensive no-op in the common case and only materializes the directory when
// replaying onto a basebackup that lacks it — where the subsequent copied-block
// full-page-image records (B4.6 Stage 3b) then populate its relation files.
// B4.6 Stage 3.
func applyDbaseCreate(mgr *storage.Manager, dbOID uint32) error {
	dir := filepath.Join(mgr.DataDir(), "base", strconv.FormatUint(uint64(dbOID), 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("wal: dbase-create replay MkdirAll %q: %w", dir, err)
	}
	return nil
}

// applyDbaseDrop removes the base/<db_id> directory (idempotent). B4.6 Stage 3.
func applyDbaseDrop(mgr *storage.Manager, dbOID uint32) error {
	dir := filepath.Join(mgr.DataDir(), "base", strconv.FormatUint(uint64(dbOID), 10))
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("wal: dbase-drop replay RemoveAll %q: %w", dir, err)
	}
	return nil
}

// clogSLRUPagesPerSegment mirrors mvcc's slruPagesPerSegment (clog.go:549) —
// PG's SLRU_PAGES_PER_SEGMENT for pg_xact: 32 BLCKSZ pages per 256 KiB segment
// file, named "%04X" of pageno/32. wal cannot import internal/mvcc (mvcc
// imports wal for the async-commit WAL-flush hook — an import cycle), so the
// constant and the segment-path arithmetic below are a deliberate small
// duplication of clogBufferPool.segPathForPage, not a shared helper.
const clogSLRUPagesPerSegment = 32

// replayDecodedXLogClogZeroPage re-applies a PG CLOG_ZEROPAGE record: it
// writes a zero-filled BLCKSZ page into the pg_xact/ segment file at pageno's
// offset, creating the segment (and any zero gap up to the offset) if it does
// not yet exist. This runs in the physical replay pass, BEFORE internal/initdb
// opens the live *mvcc.CLog, so it talks to the segment file directly rather
// than through clogBufferPool — see the case comment at the call site for why
// this differs from CLOG_TRUNCATE's initdb-second-pass treatment.
func replayDecodedXLogClogZeroPage(mgr *storage.Manager, xlog *XLogDecodedRecord) error {
	pageno, err := DecodeXLogClogZeroPage(xlog.MainData)
	if err != nil {
		return fmt.Errorf("wal: clog-zeropage replay decode: %w", err)
	}
	segNo := pageno / clogSLRUPagesPerSegment
	pageInSeg := pageno % clogSLRUPagesPerSegment
	segDir := filepath.Join(mgr.DataDir(), "pg_xact")
	segPath := filepath.Join(segDir, fmt.Sprintf("%04X", segNo))
	off := pageInSeg * int64(storage.BlockSize)

	if err := os.MkdirAll(segDir, 0o700); err != nil {
		return fmt.Errorf("wal: clog-zeropage replay mkdir %q: %w", segDir, err)
	}
	f, err := os.OpenFile(segPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("wal: clog-zeropage replay open %q: %w", segPath, err)
	}
	defer f.Close()
	zero := make([]byte, storage.BlockSize)
	if _, err := f.WriteAt(zero, off); err != nil {
		return fmt.Errorf("wal: clog-zeropage replay write %q@%d: %w", segPath, off, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("wal: clog-zeropage replay sync %q: %w", segPath, err)
	}
	return nil
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
