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
			// Other btree records (dedup / delete / reuse-page / …) are
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
// IndexTuple carried in block 0's data into the leaf page at the offset number
// carried in the record's main data, exactly as upstream btree_xlog_insert
// does. Mirrors the native replayBtreeInsert, so goopg↔goopg replay is
// identical; a full-page image is restored instead. Idempotent via pd_lsn.
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
	if len(xlog.MainData) < sizeOfXLogBtreeInsertData {
		return fmt.Errorf("wal: xlog btree-insert: main data len %d (want >= %d)", len(xlog.MainData), sizeOfXLogBtreeInsertData)
	}
	offnum := binary.LittleEndian.Uint16(xlog.MainData[0:2])
	if err := btree.ApplyInsertRecordAt(page, block.Data, offnum); err != nil {
		return fmt.Errorf("wal: xlog btree-insert apply: %w", err)
	}
	storage.MustHeader(page).SetLSN(storage.LSN(r.EndLSN))
	return mgr.WriteBlock(block.Rel, block.Block, page)
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

	meta, ok := xlogBlockRefByID(xlog, 2)
	if !ok {
		return fmt.Errorf("wal: xlog btree-newroot missing block 2 (metapage)")
	}
	if meta.HasImage && meta.ImageApply {
		if err := restoreDecodedXLogBlockImage(mgr, meta, endLSN); err != nil {
			return fmt.Errorf("wal: xlog btree-newroot meta image: %w", err)
		}
		return nil
	}
	md, err := decodeXLogBtreeMetadata(meta.Data)
	if err != nil {
		return fmt.Errorf("wal: xlog btree-newroot meta: %w", err)
	}
	if err := replayInitedXLogBlock(mgr, meta, endLSN, func(page storage.Page) error {
		return btree.ReplayRestoreMetaPage(page, md)
	}); err != nil {
		return fmt.Errorf("wal: xlog btree-newroot meta apply: %w", err)
	}
	return nil
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
// separate items. Replaying the deletions while dropping the updates would
// silently leave dead TIDs on the page, so it is refused instead.
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
	if nupdated != 0 {
		return fmt.Errorf("wal: xlog btree-vacuum nupdated=%d: PG's posting-list updates (xl_btree_update) are not implemented", nupdated)
	}
	if len(block.Data) < 2*ndeleted {
		return fmt.Errorf("wal: xlog btree-vacuum block data len %d (want >= %d for ndeleted=%d)", len(block.Data), 2*ndeleted, ndeleted)
	}
	deleted := make([]uint16, ndeleted)
	for i := range deleted {
		deleted[i] = binary.LittleEndian.Uint16(block.Data[2*i : 2*i+2])
	}
	return replayExistingXLogBlock(mgr, block, endLSN, func(page storage.Page) error {
		return btree.ReplayVacuumDelete(page, deleted)
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
// initialised: the mutation reads the current contents.
func replayExistingXLogBlock(mgr *storage.Manager, block XLogBlockRef, endLSN storage.LSN, apply func(storage.Page) error) error {
	nblocks, err := mgr.NBlocks(block.Rel)
	if err != nil {
		return err
	}
	if block.Block >= nblocks {
		return fmt.Errorf("wal: block %d does not exist (nblocks=%d)", block.Block, nblocks)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(block.Rel, block.Block, page); err != nil {
		return err
	}
	if storage.IsNew(page) {
		return fmt.Errorf("wal: block %d is uninitialised", block.Block)
	}
	if storage.MustHeader(page).LSN() >= endLSN {
		return nil // already applied
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
