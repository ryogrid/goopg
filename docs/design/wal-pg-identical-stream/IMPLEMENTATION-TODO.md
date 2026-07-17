# Implementation TODO — PG-identical WAL stream

Fine-grained execution tracker for the design bundle in this directory
([README](README.md), [01 Section A](01-record-content-parity.md),
[02 Section B](02-catalog-heap-journaling.md)). Mark `[x]` as each item lands green + committed.

**Sequencing: Part A first, then Part B.** Section B's catalog DML emits the same
`XLOG_HEAP_*` / `XLOG_BTREE_*` / `XLOG_XACT_COMMIT` records Part A makes byte-correct, so B rides on
already-correct bodies.

**Central discipline (Part A, doc 01 §3):** every record after A0 is an *atomic encode↔replay↔classifier
flip* — rewrite `Encode*`→PG body (block refs via A0's assembler), delete the native `Decode*`/`replay*`,
add a per-rmgr handler to `replayDecodedXLogRecord`, update pgoutput `classifier.go`. Never land a PG body
without its replay (a half-flip silently breaks recovery).

**Constraints:** implemented on branch `wal-pg-stream-impl` (worktree, off Ralph's baseline `344470fe`);
no `gofmt -w` (go1.25/1.26 mismatch); re-init data dirs after on-disk format changes; commits end with the
`Co-Authored-By: Claude Opus 4.8 (1M context)` trailer.

---

## Phase 0 — Setup
- [x] 0.1 Stop the Ralph loop (respawner-first kill order)
- [x] 0.2 Stash Ralph's main-tree WIP (SHA `8d8a32da`, tag `ralph-wip-pause-walpgstream-7d204969`) — restore at wrap
- [x] 0.3 Create this tracker

## Phase A — Record content parity (doc 01)
- [x] **A0** Block-ref/FPI encoder — `internal/wal/xlog_assemble.go`: `BlockRef`, `FullPageImage`,
  `assembleXLogRecord(mainData, blocks)` with pd_lower/pd_upper hole detection; round-trip test vs
  `pg_xlog_decode.go` (`xlog_assemble_test.go`, 6 cases). *Keystone; additive (flips no record).*
  Gate green: `go build ./...`, `go vet`, `go test`, `go test -race ./internal/wal/`.
  (Dropped the doc's illustrative `xid` param — it's a header field, threaded in A1 — and the
  caller-set `ForkFlags`/hole fields — derived from block contents / page header instead.)
- [ ] **A1** `xl_xid` threading through `Append`/`appendPGCompat`/`encodeRecordXLog`; stamp live xid at emit sites.
  > ⚠️ **Not standalone-additive (found 2026-07-15).** `nativeHeaderMatchesMainData` (`pg_xlog_decode.go:276`)
  > gates the native-replay fast-path on `header.XID == classifyXLogRecord(...)`, which returns **0**.
  > Stamping a *real* xid into the header while a record body is still native fails that check →
  > `decoded.Payload` goes nil → the record routes to FPI-only `replayDecodedXLogRecord` → silent recovery
  > corruption. So xid can only be stamped for records *already flipped* to PG bodies (blocks>0 bypasses the
  > fast-path). **⇒ Fold A1 into A2** (do the xid-stamp per record as it flips). The API plumbing exists
  > across **145 `.Append(` call sites** — thread it, but pass the live xid only from flipped emit sites; all
  > others keep 0 until they flip. Retire `nativeHeaderMatchesMainData` when the last native record is gone.
- [x] **A2-pre** t_ctid convention change (**prerequisite**, found 2026-07-15): PG `xl_heap_insert` carries no
  t_ctid; replay reconstructs self-pointing `{block,offnum}`. But goopg stores `{InvalidBlockNumber,0}` for
  fresh inserts and MVCC sites read that (`isChainTailCTID` handles both; `operators_fk.go:46`,
  `operators_lockrows.go:1967`, `operators_storage.go:255` epqSerializationErr, prune/visibility check
  `{Invalid,0}` specifically). Adopt PG self-pointing t_ctid on the insert path (stamp `{blk,lineSlot}` after
  `PageAddHeapTuple`) + route all convention consumers through a shared "no-successor = {Invalid,0} OR self"
  predicate. Gate: **full regress + `internal/testport` isolation + -race** (MVCC blast radius). Landed
  independently before the WAL flip. *(User-chosen 2026-07-15: do the full change now.)*
- [x] **A2** HeapInsert flip (rides on A2-pre) — **LANDED**. `xl_heap_insert{offnum,flags}` + blk0
  `xl_heap_header`+tuple, xl_xid=t_xmin. Null-safe `decodeXLogHeapInsertTuple` (verbatim concat). Live wiring:
  `logHeapInsert`→`EncodeHeapInsertPG` (open.go); `classifier.go` decoded-path (`classifyDecodedXLog`). Native
  `EncodeHeapInsert`/`replayHeapInsert`/ApplyRecord case left as dead fallback (retire later). **Gates all
  green:** wal unit+`-race`, executor, **initdb crash-recovery**, e2e **native replication+promotion**,
  **physical**, **logical** (classifier). Separate FPI kept (unification deferred). (doc 01 §5/§6)
- [x] **A3** HeapDelete flip — **LANDED**. `xl_heap_delete{xmax,offnum,infobits_set=0,flags}` + block-0 page
  ref + optional old tuple (logical). Built the net-new decoded replay (`replayDecodedXLogHeapDelete` reuses
  `PageSetHeapTupleXmax` = native parity; split the FPI-only dispatch). Live: `logHeapDelete`→`EncodeHeapDeletePG`;
  classifier decoded delete-path (reconstructs old tuple). **Gates:** wal+`-race`, executor, initdb
  crash-recovery (234s), e2e native/physical/logical repl, **full isolation + regress**. infobits_set=0 (no
  HEAP_KEYS_UPDATED — native delta) and ALL_VISIBLE_CLEARED not set = known PG-standby parity gaps.
- [x] **A4** HeapHotUpdate flip — **LANDED (HOT path)**. `xl_heap_update` (HOT opcode 0x40): main-data
  {old_xmax,old_offnum,old_infobits=0,flags=CONTAINS_NEW_TUPLE,new_xmax=0,new_offnum} + block-0 new tuple
  (same page; no block 1; prefix/suffix skipped). Built decoded `replayDecodedXLogHeapUpdate`
  (PageAddHeapTuple new + PageStampHotOldTuple old). Threaded `new_offnum` from the executor; extended the
  A2-pre self-`t_ctid` stamp to `markHeapHotUpdateDirty` (HOT new version). classifier decoded update-path.
  **Gates:** wal+`-race`, executor, server, initdb crash-recovery (228s), e2e native/physical/logical repl,
  full isolation (485s) + regress (288s). **Non-HOT update DEFERRED**: it already emits a PG-format
  Delete+Insert pair (A2/A3), not a single `xl_heap_update` — single-record conversion is an executor
  restructure, left as a parity gap (`RecordKindHeapUpdate`=27 is dead code).
- [x] **A5** BtreeInsert flip — **LANDED**. `xl_btree_insert{offnum=0}` + blk0 IndexTuple (as block data).
  Built decoded `replayDecodedXLogBtreeInsert` (reuses `btree.ApplyInsertRecord` = re-insert by key = native
  parity; FPI fallback); RmgrBtree dispatch now switches on opcode (INSERT_LEAF → new handler, split/newroot/…
  stay FPI). Live: `logBtreeInsert`→`EncodeBtreeInsertPG`. No classifier (index changes aren't logical
  user-data). **Gates:** wal+`-race`, executor, initdb crash-recovery (227s), e2e native/physical/logical
  repl, full isolation + regress. offnum=0 is a documented PG-standby parity gap (goopg replays by key).
- [x] **A6** XactCommit / Abort / CommitInval flip — **LANDED**. `xl_xact_commit`/`xl_xact_abort`
  (xact_time=0; **xid in the header xl_xid**, not the body). CommitInval → HAS_INFO + xinfo{HAS_INVALS} +
  empty invals array; decoded RmgrXact replay unlinks standby init files on HAS_INVALS (replaces native
  RecordKindXactCommitInval redo). No block ref → routes to decoded path (header.XID≠0 vs classifyXLogRecord's
  0). Most consumers were already header-ready (initdb xact-recovery CLOG stamp, stream_replayer); the one gap
  was `classifyDecodedXLog` (added RmgrXact branch → ApplyCommit/ApplyAbort by xl_xid). Fixed
  `wal_durability_test.go` (header-based commit detection). **Gates:** wal+`-race`(*), initdb crash-recovery
  (commit visibility), executor, server, e2e ×3, isolation+regress. *(-race: full-package flake in the
  pre-existing `TestDrainSafetyStress` WAL-drain concurrency test — passes isolated 3/3; A6 adds no
  concurrency.)* Deferred: xact_time, real subxact/inval arrays (not available at the commit emitter).
- [ ] **A2–A6 cross-cutting**: FPI↔logical unification (doc 01 §5); `predictXLogRecordLen` assembled-length
  fix; audit record-count / LSN-delta consumers (stream replayer, recovery-pass WAL-decode memoization,
  FPI-count test assertions).
- [x] **A7** heap2 composite — **LANDED**. HeapPruneOpt + HeapFreeze → PG `xl_heap_prune` (RM_HEAP2).
  Prune (PRUNE_ON_ACCESS): XLHP_HAS_REDIRECTIONS + XLHP_HAS_NOW_UNUSED_ITEMS. Freeze (VACUUM_CLEANUP):
  XLHP_HAS_FREEZE_PLANS (one plan + offset array = frozen slots). Shared composite `decodeXLogHeapPrune` +
  `replayDecodedXLogHeapPrune` (PageSetItemIDRedirect / VacuumHeapPageBySlots / PageFreezeBySlots) + `case
  RmgrHeap2` dispatch. **HeapVacuum SKIPPED** (dormant — no runtime producer). Not classifier-relevant
  (non-logical). **Gates:** wal round-trip+replay, executor, vacuum, initdb crash-recovery, e2e ×3, isolation
  + regress. Parity gaps: no conflict horizon; freeze frzflags/infomask=0 (goopg freezes via xmin→FrozenXID,
  not PG's infomask bit — a real-PG-standby freeze representation gap).
- [~] **A8** btree structural — **Split + Vacuum + NewRoot LANDED (FPI)**; UnlinkPage stays incremental (by
  design), MarkHalfDead dormant. All FPI-flippable records now PG-format; only UnlinkPage remains native.
  - [x] **A8-split** — `EncodeBtreeSplitPG` (RM_BTREE/SPLIT_L) carries post-split left/right/sib pages as
    apply-FPIs; reuses A0 FPI encoder + existing RmgrBtree default→`replayDecodedXLogHeapFPIBlocks` (no new
    replay). Emit closure already had the pages (no signature change). Gates: wal round-trip+FPI-replay,
    executor, access/btree, initdb crash-recovery (220s), e2e ×3, isolation (479s) + regress (289s).
  - [x] **A8-vacuum** — `EncodeBtreeVacuumPG` (RM_BTREE/VACUUM 0xC0) carries the post-vacuum leaf as a
    block-0 apply-FPI. `LogBtreeVacuumFunc` narrowed `(rel, blk, kept, flags)`→`(rel, blk, page)`; both emit
    sites (`btree_vacuum.go`, `btree.go` dedup-recovery) pass `slot.Page()`. No new replay (RmgrBtree default
    arm). Native `EncodeBtreeVacuum`/`replayBtreeVacuum` kept as dead fallback.
  - [x] **A8-newroot** — `EncodeBtreeNewRootPG` (RM_BTREE/NEWROOT 0xA0) carries the new root (block 0,
    WILL_INIT) and the updated metapage (block 2) as apply-FPIs. `LogBtreeNewRootFunc` `(rel, rootBlk, level,
    items)`→`(rel, rootBlk, rootPage, metaBlk, metaPage)`; both emit sites now update the metapage in memory
    under `splitMu` BEFORE emit so its bytes ride the same record (retired `updateRootMetaWithLSN`). Metapage
    FPI is hole-safe (meta struct at [24:48] sits below pd_lower=48). No new replay.
  - [ ] **A8-unlinkpage** — DELIBERATELY kept native/incremental (not a deferral of convenience): at emit the
    sibling pages are unmutated and their btpo_prev/btpo_next are re-derived at apply under a fresh pin to
    survive a concurrent split on another connection's `*BTree` for the same rel (splitMu is per-instance). An
    emit-time FPI snapshot would be stale and stomp a racing relink. A PG-format flip must emit PG's real
    incremental `xl_btree_unlink_page` main-data (link patches), a distinct effort → deferral ledger.
  - **A8-markhalfdead** — dormant (no production emit site; the half-dead transition rides the vacuum record's
    opaque flags). No flip needed.
  > Note: btree structural records already replay correctly for goopg↔goopg via their native bodies; the flip
  > is for PG-parseability / real-PG-standby. FPI-based records carry no incremental main-data (PG deviation).
- [x] **A9** smgr/clog/standalone-FPI/checkpoint-opcode/xact chunks; retire legacy native frame — **COMPLETE**
  (checkpoint-opcode was the last item; landed 115121c7).
  - [x] **A9-fpi** — standalone first-touch FPI (`Pool.maybeEmitFPI`) → PG `RM_XLOG`/`XLOG_FPI` via
    `EncodePageImagePG` (block-0 apply-image, hole removed). Replay routes the empty-payload case to
    `replayDecodedXLogHeapFPIBlocks` (the arm previously NO-OPed it — fixed together). `predictXLogRecordLen`
    assembled branch already reserves the shrunken size. Native `EncodePageImage` kept as dead fallback.
    Gates: crash-recovery (218s), e2e ×3, isolation (real, 26s), regress (280s).
  - [x] **A9-smgr-create** — LANDED. `EncodeSmgrCreatePG` (RM_SMGR/XLOG_SMGR_CREATE): 16-byte
    RelFileLocator{spcOid,dbOid,relNumber}+ForkNumber main-data, no block ref, with the CREATING xid in the
    header (PG stamps it via log_smgrcreate). The real xid is both PG-faithful and the routing guarantee — a
    non-zero header xid mismatches `classifyXLogRecord`'s always-0 xid, so the record reaches the decoded path
    regardless of the RelFileLocator's leading byte (test proves the tablespace-OID-16395 = 16384+11 collision
    is resolved by the xid). New `RmgrStorage`/`XLOG_SMGR_CREATE` decoded arm → `applySmgrCreate` (shared with
    native replay). xid plumbing: `Pool.PinNewWithXID`/`ExtendRelationBatchWithXID` (plain `PinNew` stays,
    xid=0 for bootstrap/catalog in default tablespace = routing-safe); wired heap/TOAST/mirror + index build
    (`btree.Options.CreateXID`, `BulkCreateWithXID`/`CreateWithXID`) to `ctx.Tx.XID`. Gates: crash-recovery
    (230s), e2e ×3, isolation (real), regress (284s).
  - [x] **A9-clog-truncate** — LANDED. `EncodeClogTruncatePG` (RM_CLOG/CLOG_TRUNCATE): 16-byte
    `xl_clog_truncate{pageno,oldestXact,oldestXactDb}`, `xl_xid=0` (PG's `WriteTruncateXlogRec` carries none).
    Since xid=0 can't drive routing, `nativeHeaderMatchesMainData` gained a **native-size guard**
    (`nativeFixedRecordSize`): a same-classified main-data-only record whose length ≠ the native fixed size
    (ClogTruncate=5, SmgrCreate=10) routes to the decoded path — resolving the pageno-byte-≡-33 collision
    (test-proven). New `RmgrCLOG` decoded arm = physical no-op; the initdb `replayCLogFromWAL` scan got a
    PG-format branch (`DecodeXLogClogTruncate`) that re-applies the idempotent truncation. `oldestXactDb`
    stamped 0 (datoid threading through `SetTruncateLogger` is a follow-up — goopg redo uses only
    pageno+oldestXact). Gates: crash-recovery (217s), e2e ×3, isolation, regress (278s).
  - [x] **A9-checkpoint-opcode** — LANDED (115121c7). Checkpoints route via
    `framePGAssembled(RmgrXLog, online|shutdown)` (`EncodeCheckpointPG`); the `len==88` classify hack is
    deleted. The hot-standby entanglement resolved PG-faithfully instead of being dodged: an ONLINE
    checkpoint emits the full upstream chain — `XLOG_CHECKPOINT_REDO` at the redo point (appended INSIDE
    the FPI redo barrier so redo==marker-start is atomic; PG17+ recovery demands this record at
    `CheckPoint.redo`), then `XLOG_RUNNING_XACTS` (new `RunningXactsFn` ← mvcc snapshot; conservative
    subxid_overflow under load, exact when idle), then the checkpoint record with real `oldestActiveXid`
    (0 on shutdown, like PG). BASE_BACKUP fallout: backup_label CHECKPOINT LOCATION + pg_control CheckPoint
    now carry the checkpoint RECORD's start (`LastCheckpointRecordLSN`), no longer == redo. Read side
    header-driven (`isCheckpointRecord`/`replayStart` read `XLog.MainData`). Real-PG failover 5s (was 228s
    of retries). Gates: wal+race, crash-recovery, pg_waldump ×2, isolation, regress, e2e ×7 GREEN.
  - [x] **A9-xact-inval-fold** — LANDED (d02f7d91). Standalone `RecordKindXactCommitInval` (byte 32) deleted;
    invals ride A6's `HAS_INVALS` chunk on `xl_xact_commit` (`xactCommitCarriesInvals` was already the live
    path — the standalone record was dead weight). Native replay case, `DecodeXactMarker` arm, native-kind-set
    entry, initdb scanner arms and their tests all removed;
    `TestApplyRecordXactCommitInvalUnlinksInitFiles` migrated to `EncodeXactCommitPG(xid, true)`.
  - [x] **A9-legacy-frame-retire** — LANDED (81d850bc). `encodeRecord`/`decodeRecord` (IEEE-CRC 8-byte frame)
    deleted; Config normalization forces `PageHeaders=true` (+`TimelineID=1` default) so every writer emits the
    PG frame. Legacy dirs rejected by NewWriter (`ErrWALFormatMismatch`) and ReadAll — re-init required.
    `waitInsertionsToFinish` gained a written-frontier guard (atomic `writeLSNMirror` only; a plain
    `s.writeLSN` read raced appendPGCompat under `-race`) so `FlushUpTo(unwritten)` returns
    `ErrLSNNotWritten` instead of spinning. Legacy-sized tests migrated: retention fills segments by observed
    end-LSN (512-byte segs), discover-checkpoint uses page-aligned segs + stripe path (records never straddle
    retained-segment starts), buffer round-trip reads decoded-path bodies from `XLog.MainData`
    (payload[0]=11 ≡ SmgrCreate fails the native-size guard by design), format-mismatch seeds legacy bytes by
    hand. Gates: wal unit+race, initdb crash-recovery, pg_waldump ×2, real-PG failover async+sync GREEN.
  - [x] **A9-INIT_PAGE** — first heap insert on an empty page now stamps `XLOG_HEAP_INIT_PAGE` (0x80) +
    `REGBUF_WILL_INIT` (`markHeapInsertDirty` computes `initPage = pageLinePointerCount==1`;
    `EncodeHeapInsertPG` sets the info bit + block WILL_INIT). This is what lets a **real PG18 standby build
    page 0 during redo** instead of PANICking `references to invalid pages` — it was the actual blocker for the
    replay gate, NOT the FPI↔logical fold (doc 01 §5), which turned out to be unnecessary. goopg's own replay
    ignores the flag (opcode masked by 0x70). btree first-insert-on-new-leaf equivalent = follow-up (not gate-
    tested; failover uses an index-less table).
- [x] **A-gate** Phase-A exit — **GREEN** (2026-07-16): `pg_waldump` structural+rmgr parses all §A records
  (`TestPGWaldumpParsesEmittedWAL`, `TestPort_WALPgWaldumpCompat` — real CREATE TABLE + 100-insert + CHECKPOINT
  cluster workload); a **real PG 18 standby fully replays goopg WAL** (`TestE2E_FailoverGoopgToPG`, both async +
  sync_remote_apply, re-enabled); goopg↔goopg crash-recovery + isolation + regress green. The final blocker was
  A9-INIT_PAGE (above). Byte-for-byte segment diff vs PG for a pgbench run remains a manual/tooling follow-up
  (no automated WAL byte-diff exists), but structural parse + real-PG replay both pass.

## Phase B — Catalog heap journaling (doc 02; detailed designs 02a–02d)
- [ ] **B0** Enabler (doc 02a) — four slices, each landed + gated separately:
  - [x] **B0.1** LANDED — generic per-catalog heap-reload framework in new
    `internal/initdb/catalog_heap_reload.go`: `catalogRowLive` (THE unified visibility filter, transcribed
    rules incl. basebackup pass-through + legacy committed-xmin branch, pinned by a 9-case unit test),
    `scanCatalogHeapRows` (generic block-walk + decode), `catalogReloadDesc`/`simpleCatalogReload`/
    `runCatalogReloads` (Slot-ordered registry with Fatal/warn semantics, pinned by an order/fatality test).
    `loadUserTablesFromHeapForDB`'s two scan passes re-expressed over `scanCatalogHeapRows` (decode returns
    the requireCommittedXmin verdict: `!physicalRow` for pg_class, always-false for pg_attribute); the
    pass-3 join, M0114 cache fast path, and all call sites untouched — zero behavior change. Gate: initdb
    (241s incl. crash recovery) + executor + catalog + wal units, FULL regress (295s), isolation (22s),
    replication smoke — all green.
  - [x] **B0.2** LANDED — non-HOT catalog `XLOG_HEAP_UPDATE` end-to-end machinery:
    `EncodeHeapUpdatePG` (same-page single block-ref / cross-page block 0 new + block 1 old, upstream
    log_heap_update shape), `replayDecodedXLogHeapUpdate` extended with the non-HOT arm
    (`PageStampUpdatedOldTuple` — xmax + forward ctid, NO HOT bits; per-page pd_lsn idempotency for the
    cross-page form; dispatch: tuple-carrying → logical, FPI-only → image restore),
    `Pool.LogHeapUpdate` hook wired in open.go, and the producer `updateHeapRowCanonicalPG`
    (verify-old-live per R-B0-3, same-page fast path, block-ordered two-page slow path,
    `buildCatalogPGHeapTuple` factored shared with the INSERT twin). Pinned by same-page + cross-page
    replay round-trip tests; the record is in the real-pg_waldump structural gate. Gate: storage/wal/
    executor units, wal -race, initdb crash recovery (235s), e2e failover — green.
    NOTE: first real caller + the TID-carrying cache land with B1.1 pg_namespace (no emit site exists
    until a catalog converts; the helper is additive and unused in production this landing).
  - [x] **B0.3** LANDED — per-DB catalog heap+index bootstrap at CREATE DATABASE + index-skip lift.
    `CreatePerDatabaseScaffolding` (server CREATE DATABASE + WAL-replay recovery share it) now copies
    template0's pristine bootstrap catalog image (`copyBootstrapCatalogImage`: base/4 → base/<newDb>,
    copy-if-missing per file for replay idempotency, write-temp+fsync+rename against torn copies; system
    DBs + pre-B0.3 dirs no-op). Source is base/4 NOT the named template — goopg clones template user
    tables under fresh OIDs, so the named template's pg_class heap would carry phantom rows (documented
    PG deviation). `insertCanonicalSysBtreeLeaf` routes by `tableCatalogHeapDBOid` (same dbOid as the
    heap write); the syncTableToCatalogHeap DefaultDBOid-only skips (follow-up-39) are lifted — pre-B0.3
    dirs still no-op gracefully via the missing-block-1 bail. Pinned by image-copy/idempotency/heal unit
    tests + the existing multi-DB restart suite. Gate: initdb 235s, executor/server/catalog, regress 292s,
    isolation, e2e failover — green.
  - [x] **B0.4** LANDED (implemented after B1 per user directive 2026-07-16). `internal/wal/relmap.go`:
    `EncodeRelMapFile` (524-byte RelMapFile, magic 0x592717 + CRC32C — now THE single encoder; initdb's
    makeRelMapFile delegates), `ValidateRelMapFile`, `EncodeRelmapUpdatePG` (RM_RELMAP=7, opcode 0x00,
    xl_relmap_update{dbid,tsid,nbytes,image}), decoded replay arm = CRC-verified atomic file rewrite
    (goopg hardening over upstream's length-only check). Emit: CREATE DATABASE journals the new DB's map
    (upstream WAL_LOG RelationMapCopy analog) — a PG standby reconstructs base/<db>/pg_filenode.map from
    WAL. Round-trip + replay + corruption tests; record added to the real-pg_waldump structural gate.
    Gate: wal (+race — one pre-existing stripe-flake retried green), initdb 227s, server/executor,
    multi-DB + durability suites, pg_waldump, e2e failover — green.
- [ ] **B1** (doc 02c) pg_namespace → pg_proc → pg_sequence, one catalog per landing per the 02b recipe:
  - [x] **B1.1 pg_namespace** — LANDED, the exemplar conversion. Schema DDL journals REAL pg_namespace
    heap rows: CREATE = heap INSERT + 2684/2685 index entries; RENAME/OWNER = non-HOT xl_heap_update
    (B0.2 producer, first caller) + fresh index entries; DROP = xl_heap_delete (markHeapDeleteDirty).
    TID-carrying cache landed on the schema registry (catalog.SchemaHeapTID + Set/Delete/Rename);
    reload = reloadUserSchemasFromHeap (generic scan, oid ≥ FirstUserOID, seeds TIDs) replacing
    replaySchemaDDLRecords at the same pass slot. Mirror set extended with 2615/2684/2685 (BLOCKER-3).
    Parse-recovery CREATE SCHEMA fallback rides SyncCompatSchemaToCatalogHeap (frozen-xid variant;
    MaterializeWriterXID reordered to accept pre-stamped xids). DELETED: kinds 34/35/100/101,
    Encode/Decode ×4, schema_ddl_recovery.go, wal/schema_alter_ddl.go, dispatch no-op case,
    native-kind-list entries, 3 wal test files; recovery-test schema seeds rewritten to the heap path;
    new TestPort_AlterSchemaSurvivesRestart. Gate: all units, initdb 231s, regress 298s, isolation,
    schema durability ×2, pg_waldump ×2, e2e failover — green; grep-zero confirmed.
  - [x] **B1.2 pg_proc** — LANDED. Function/procedure DDL journals REAL pg_proc heap rows (30-col
    PG18-physical builder; CREATE = INSERT, OR-REPLACE + every ALTER variant = non-HOT xl_heap_update
    of the total row, DROP = xl_heap_delete). Kinds 61-64/121-123 + codecs (recovery.go) +
    function_ddl_recovery.go + function_ddl_test.go DELETED (no ApplyRecord/native-list/rmgr-map
    changes needed — functions were fall-through no-ops). Routine metadata beyond the physical
    columns rides a JSON blob in proargdefaults (PG reads that column only when pronargdefaults>0 —
    no new PG-side deviation for default-less functions); reload = reloadUserRoutinesFromHeap.
    TID cache = OID-keyed map on the Routines store. Mirror += 1255. RESIDUAL (ledger): runtime
    maintenance of multi-level btrees 2690/2691 (needs catalog-btree descent insert; bootstrap
    indexes never carried user functions — unchanged behavior); proallargtypes/proargmodes OUT-arg
    columns. New TestPort_FunctionSurvivesRestart (create/replace/alter/drop across restart).
    Gate: initdb 230s, server, regress 278s, isolation, durability ×3, pg_waldump + e2e failover —
    green; grep-zero.
  - [x] **B1.3 pg_sequence (catalog row only)** — LANDED. The DEFINITION journals as a real
    Form_pg_sequence heap row in base/<db>/2224 + index 5002 entry: CREATE = INSERT, ALTER = non-HOT
    xl_heap_update (fingerprint-gated inside WALLogSequenceState so counter-only snapshots — setval,
    TRUNCATE RESTART pre-logs — skip, PG parity), all 9 DropSequence emit sites paired with a heap
    DELETE. TID cache = seqKey-keyed map with definition fingerprint; startup TID reseed
    (reloadSequenceHeapTIDs) after the kind-65 replay. Kinds 65/66 SURVIVE per the 02c scope decision
    (counter + goopg replay source until B1.3b). pg_waldump workload extended with schema/function/
    sequence DDL (real pg_waldump parses all B1 records). New
    TestPort_SequenceCatalogRowSurvivesRestart (post-restart ALTER updates in place). Gate: all units,
    initdb 227s, regress 298s, isolation, durability ×4, e2e failover — green.
  - [ ] **B1.3b** (optional / ledger) `XLOG_SEQ_LOG` flip for the sequence-relation counter page.
- [x] **B2-prep** descent-aware catalog-btree insert — LANDED. The multi-level machinery
  (readSysBtreeMeta/descendSysBtreeToLeaf/insertIntoExistingLeaf/rebuildSysBtreeWithNewEntry,
  M0106-0010 batched-41) already existed; the real gaps were (1) `keyMetaForSysBtree` registration —
  2684/2685 were MISSING too (latent B1.1 split-path hole), now registered alongside 2690/2691 —
  and (2) variable-length IndexTuple support (2691's proargtypes oidvector): `btreeIndexKeyMeta.variable`
  + variable-tolerant collect/split/rebuild + `buildBulkSysBtreeLayoutVariable` (executor twin of
  initdb's pgBuildBtreeBulkLoadVariable). pg_proc DDL now maintains BOTH indexes at every heap write
  (fresh entries at the new TID on updates, PG-style; old entries die with their heap versions);
  mirror set += 2690/2691. Fixed latent B1.2 hazard the new e2e gate would have exposed:
  buildPGProcRow stuffed NON-NULL empty strings into prosqlbody/proallargtypes/proargmodes/
  protrftypes/probin/proconfig/proacl — PG branches on attisnull (stringToNode("") on every SQL
  function call); now genuinely NULL (pg_class builder convention). e2e failover extended: goopg
  CREATE FUNCTION over WAL → promoted PG resolves by name (2691) AND executes it (2690 + prosrc).
- [ ] **B2** type/operator families (pg_type/enum/range, pg_operator, pg_opclass/opfamily/amop/amproc,
  pg_cast, pg_conversion, pg_collation, pg_aggregate).
  - [x] **B2.1a pg_type index maintenance (2703/2704)** — LANDED. All ten pg_type row writes
    (enum/composite/domain/range/multirange + arrays, ACL resync) funnel through
    `writeTypeHeapRowWithIndexes`, which derives the index keys from the built row itself
    (oid/typname/typnamespace = cols 0-2) and maintains 2703 (oid, 16B) + 2704 (name+nsp, 80B —
    same shape as 2663). Two structural gaps closed along the way: (1) **empty-placeholder lazy
    rooting** — 2704 shipped as a metapage-only placeholder that `insertCanonicalSysBtreeLeaf`
    silently skipped (nBlocks≤1); now `allocateEmptySysBtreeLeafRoot` mirrors PG's `_bt_getroot`
    write path (needed for pre-existing data dirs + any future placeholder index); (2) **2704
    bootstrap population** — the empty placeholder meant a real PG standby could not resolve ANY
    type by name (`SELECT 1::int4` → `type "int4" does not exist`; TYPENAMENSP probes 2704 for
    builtins too); `bootstrapPgTypeTypnameNspIndex` now bulk-loads one entry per bootstrap heap
    row (shared entry map with the heap writer). e2e failover: runtime `CREATE TYPE AS ENUM`
    (heap-insert/FPI records ONLY — no bespoke WAL) resolves on the promoted PG via
    `'public.b2prep_mood'::regtype::text` — exercising runtime AND bootstrap 2704 entries.
    **Empirical pin from the failed first attempt**: a surviving RmgrGoopgCatalog(128) record in
    streamed WAL hard-kills a real PG standby (`FATAL: resource manager with ID 128 not
    registered`, unrecoverable startup loop) — sequences (65/66), ranges (81/82/117/118), domains
    (119/120) are live landmines for mixed replication until B1.3b/B2.1b/B2.1c land.
  - [x] **B2.1b domains** — LANDED. Kinds 119/120 + codecs + dispatch arm + domain_ddl_recovery.go
    DELETED. NO JSON sidecar needed: the domain reconstructs fully physically — skeleton from its
    pg_type row (typtypmod→Base.Args via `pgTypeArgsFromTypmod`, the decode twin of pgAttTypmod;
    typdefaultbin raw SQL → ParseExpr, the pre-existing deviation), CHECKs from NEW narrow
    pg_constraint heap rows (28-col PG18 builder in sys_pg_constraint.go; contype='c',
    contypid=domain; conbin=raw expr text; the pg_constraint VIEW stays virtual — pg_class
    precedent; NO index maintenance, 2664-2667 stay empty → ledgered). InValues (the ONLY thing
    runtime enforcement reads, expr.go:871) re-derive from conbin's synthesized
    `VALUE = ANY (ARRAY[...])` text (`domainInValuesFromConbin`). ALTER DOMAIN all 7 arms now
    durable (were NOT durable at all): pg_type mutations = non-HOT xl_heap_update via the new
    generic TypeHeapTID cache (catalog.SetTypeHeapTID, seeded by every type-row write AND the
    reload for ALL user pg_type rows); constraint mutations = pg_constraint INSERT/DELETE.
    **Bug found by the new test**: RegisterDomainDuringRecovery force-keyed DefaultDBOid(1) while
    sessions look up under their resolved DB OID (postgres=5) — post-restart CHECK enforcement
    was silently DISABLED (pre-existing in the WAL-scanner era, never caught because no test
    asserted post-restart enforcement); reload now keys cat.DBOID(). Known non-goal pinned in the
    test: goopg never applies domain DEFAULTs at INSERT time (verified pre-restart too — separate
    pre-existing gap). e2e: CREATE DOMAIN streams as pure heap records; promoted PG casts
    42::public.b2prep_dom. New TestPort_DomainSurvivesRestart (CHECK enforcement + RENAME +
    SET DEFAULT across restart); pg_waldump workload += domain create/alter/drop.
  - [x] **B2.1c ranges** — LANDED. Kinds 81/82/117/118 + codecs + dispatch arm +
    range_type_ddl_recovery.go DELETED. CREATE TYPE AS RANGE journals the 4 pg_type rows (existing)
    + a NEW pg_range heap row (sys_pg_range.go, 7-col Form_pg_range, keyed rngtypid — no oid col)
    with 3542/2228 index entries; DROP stamps it; ALTER RENAME/OWNER = non-HOT pg_type updates of
    all four rows via the TypeHeapTID cache (resyncRangeHeapAfterAlter). Reload
    (reloadUserRangeTypesFromHeap) is fully physical: the pg_range row carries the linkage
    (subtype/multirange/opclass/collation), joined with the range + multirange pg_type rows
    (names/array peers/owner); subtype name via pgTypeCanonical. RegisterRangeTypeDuringRecovery
    got the same DBOid-keying fix as domains. ALL nine type builders now carry real I/O proc OIDs
    (enum_in 3506-, record_in 2290-, range_in 3834-, multirange_in 4231-, array_in 750- —
    pg_proc.dat-verified). e2e failover: goopg CREATE TYPE AS RANGE → promoted PG executes
    '[1,5)'::public.b2prep_rng (RANGETYPE syscache → runtime 3542 entry → pg_range row).
    New TestPort_RangeTypeSurvivesRestart; waldump workload += range create/rename/drop.
  - [x] **B2.1d enum labels** — LANDED. Enums are restart-durable for the FIRST time. Every label
    owns a real pg_enum heap row (sys_pg_enum.go) + entries in 3502/3503/3534 (all lazily rooted
    from empty placeholders); EnumValue gained a persisted OID (the virtual pg_enum view now
    renders real label OIDs instead of synthetic 20000+i). CREATE = one INSERT per label; ADD
    VALUE = INSERT; RENAME VALUE = delete+insert (stable OID); enum RENAME/OWNER resync pg_type
    via TypeHeapTID; DROP stamps all label rows. Reload = reloadUserEnumsFromHeap (pg_type
    typtype='e' + pg_enum grouped by enumtypid, sorted by sortorder) + RegisterEnumDuringRecovery.
    **enumsortorder encoding pin**: goopg's shared float4 encoder writes TEXT VARLENA
    (M0111-0002), which shifted enumlabel under PG's attlen=4 TupleDesc (e2e read "ppy" for
    "happy"; pg_proc's float4s only survive by pad-back-to-4 luck before 4-aligned columns) —
    pg_enum encodes sortorder via the "xid" encode-hint carrying IEEE-754 float32 BITS =
    byte-identical to a real PG float4 on disk. e2e failover: promoted PG casts
    'happy'::public.b2prep_mood (enum_in → 3503 → goopg's runtime rows). New
    TestPort_EnumSurvivesRestart (create/add/rename/drop across restart); waldump += enum DDL.
- [ ] **B3** extension/config (pg_ts_*, pg_transform, pg_event_trigger, pg_publication*/subscription*,
  pg_statistic_ext, pg_constraint/attrdef/depend).
- [ ] **B4** shared catalogs in `global/` (pg_database, pg_authid/auth_members, pg_tablespace,
  pg_foreign_*/user_mapping) + retire the postgres-DB mirror shim.
- [ ] **B5** Retire `RmgrGoopgCatalog=128` (now unused) — header-side parity complete.
- [ ] **B-gate**: per-catalog full regress + `internal/testport` isolation; `psql \d`/`\df`/`\dn` +
  `information_schema` parity vs PG 18.3; crash-after-DDL recovery via generic reload; re-init data dir.

---

## Log (A9 — checkpoint-opcode landed; A9 COMPLETE, Part A record work done)
- 2026-07-16: **A9-checkpoint-opcode landed (115121c7) — A9 is complete.** The "deferred: hot-standby
  entanglement" turned out to have a clean PG-faithful resolution: emit what PG emits. Three findings worth
  keeping: (1) PG17+ recovery **validates the record at CheckPoint.redo is XLOG_CHECKPOINT_REDO** whenever
  redo < the checkpoint record — an ONLINE flip without that marker fails `unexpected record type found at
  redo point`; goopg appends the marker inside `PublishRedoBarrier`'s critical section so the marker start
  IS the redo (lock order fpiPublishMu→WAL-stripe matches the FPI sections; a no-block-ref record makes no
  FPI decision → no deadlock). (2) The standby locates the checkpoint via backup_label CHECKPOINT LOCATION /
  pg_control CheckPoint — those must name the checkpoint RECORD's start, which is no longer the redo point
  (`invalid resource manager ID in checkpoint record` otherwise). (3) A hot-standby needs
  `XLOG_RUNNING_XACTS` after an online checkpoint to reach STANDBY_SNAPSHOT_READY — wired from the mvcc
  snapshot (InProgress = top-level xids; Xmin/Xmax-1 map onto oldestRunningXid/latestCompletedXid).
  Bonus: the failover e2e dropped 228s→5s (the standby had been retry-looping on the old always-SHUTDOWN
  chain's basebackup edge cases). Remaining Part-A followups (btree INIT analog, byte-diff tooling,
  clog-truncate datoid) are unchanged.

## Log (A9 — legacy frame retired; A9 complete except checkpoint-opcode)
- 2026-07-16: **A9-legacy-frame-retire landed (81d850bc)** — goopg WAL now has exactly ONE on-disk format:
  the PG-compat page-headered XLogRecord stream. −730/+256 lines. Two behavioural pins worth remembering:
  (1) the stripe path confines each record to one segment (pads + relocates at boundaries) while the slow
  path emits true cross-segment contrecords — post-retention reads therefore need stripe-path writers and
  page-aligned segment sizes in tests; (2) the PG frame's conservative ring reservation is
  2*(paddedLen+64), so WALBuffers caps below ~1 KiB force every append onto the direct-write bypass.
  A `-race`-only regression was caught by the gate: the first version of the FlushUpTo unwritten-LSN guard
  read `s.writeLSN` (state-loop-owned) — rewritten to consult only the atomic `writeLSNMirror`.
  **A9 status: everything landed except checkpoint-opcode (deferred, hot-standby-entangled, byte-identical
  no-op for the stream).**

## Log (A9 — clog-truncate landed via native-size routing)
- 2026-07-16: **A9-clog-truncate landed — CLOG truncation is now a PG `RM_CLOG`/`CLOG_TRUNCATE` record.**
  PG's `xl_clog_truncate` carries `xl_xid=0`, so (unlike smgr-create) the xid can't disambiguate routing;
  instead `nativeHeaderMatchesMainData` grew a `nativeFixedRecordSize` guard that rejects a same-classified
  main-data-only record whose length differs from the native fixed size (ClogTruncate=5 vs PG's 16,
  SmgrCreate=10 vs 16) — blast radius exactly those two kinds. New `RmgrCLOG` decoded arm (physical no-op) +
  a PG-format branch in `replayCLogFromWAL` (initdb) that decodes the body and re-applies the idempotent
  truncation. `oldestXactDb` = 0 stopgap (datoid plumb is a follow-up). Gates green (crash-recovery 217s,
  regress 278s). **A9 status: fpi + smgr-create + clog-truncate LANDED; remaining = checkpoint-opcode
  (hot-standby-entangled), xact-inval-fold, legacy-frame-retire.**

## Log (Phase-A exit gate GREEN — real PG replays goopg WAL)
- 2026-07-16: **Phase-A exit gate is GREEN.** Un-skipped the three gate tests (deferred 2026-07-15 pending
  exactly this native→PG rewrite): `pg_waldump` structurally parses goopg WAL (both structural tests pass),
  and — after the final fix — a **real PG 18 standby fully replays goopg WAL** (`TestE2E_FailoverGoopgToPG`,
  async + sync_remote_apply). The replay gate first PANICked `WAL contains references to invalid pages` at a
  Heap/INSERT for a fresh page; a pg_waldump dump of a CREATE-TABLE-then-INSERT workload showed goopg emits
  `Heap INSERT (blk 0)` then a SEPARATE `XLOG FPI (blk 0)`. The fix was NOT the FPI↔logical fold (doc 01 §5,
  which I'd initially scoped as the blocker) — it was the much smaller **A9-INIT_PAGE**: stamp
  `XLOG_HEAP_INIT_PAGE` + `WILL_INIT` on the first insert into an empty page so PG PageInit's the page during
  redo (its own first-insert-on-a-new-page behaviour). Full gate suite green (crash-recovery 221s, e2e incl.
  real-PG failover 31s, pg_waldump 1.4s, isolation 30s, regress 286s). Remaining A9 cleanup (checkpoint-opcode
  = byte-identical no-op; xact-inval-fold + legacy-frame-retire = dead-code removals) does not affect the gate.

## Log (A9 — smgr-create landed via xid-plumbing)
- 2026-07-16: **A9-smgr-create landed — relation-file creation is now a PG `RM_SMGR`/`XLOG_SMGR_CREATE`
  record carrying the creating transaction's xid.** The real xid (PG-faithful) is also the routing guarantee:
  a main-data-only record only reaches the decoded replay path when `nativeHeaderMatchesMainData` is false,
  and a non-zero header xid always mismatches `classifyXLogRecord`'s xid=0 — so it routes correctly no matter
  what the RelFileLocator's leading byte is (resolving the tablespace-OID-≡-11 = `RecordKindSmgrCreate`
  collision that xid=0 would misroute). Plumbed `ctx.Tx.XID` to every user-relation create via
  `Pool.PinNewWithXID`/`ExtendRelationBatchWithXID` + btree `Options.CreateXID` (`BulkCreateWithXID`/
  `CreateWithXID`); plain `PinNew` stays for bootstrap/catalog (xid=0, default tablespace, routing-safe). New
  `RmgrStorage` decoded arm → `applySmgrCreate`. Gates: crash-recovery 230s, e2e ×3, isolation (real), regress
  284s. FF to main needed the shared-stash dance (Ralph WIP overlapped `operators_ddl.go` — disjoint regions,
  clean stash/FF/apply/drop). **Remaining A9: clog-truncate (needs size-check routing since PG uses xid=0),
  checkpoint-opcode, xact-inval-fold, legacy-frame-retire.**

## Log (A9 — standalone FPI landed)
- 2026-07-16: **A9-fpi landed — goopg's hot first-touch FPI is now a real PG `RM_XLOG`/`XLOG_FPI` record.**
  `EncodePageImagePG` emits the page as a block-0 apply-image (A0 encoder; free-space hole removed → smaller
  than the native 8 KiB body). The `xlogXLogFPI` decoded arm now routes the empty-payload (block-ref) case to
  `replayDecodedXLogHeapFPIBlocks` — it previously NO-OPed that shape, so leaving it would have silently
  dropped every FPI on replay; the replay fix lands with the emit flip. Full gate suite (this is the hottest
  WAL path): crash-recovery 218s, e2e ×3, isolation 26s (now real coverage after the harness fix), regress
  280s — all green. **Rest of A9 deferred with resume points (see checklist + deferral ledger):** smgr-create
  / clog-truncate need a real header xid for robust decoded-path routing (main-data-only, tablespace-OID
  collision risk); checkpoint-opcode is hot-standby-entangled; xact-inval-fold + legacy-frame-retire are
  follow-ups. **Note:** also fixed `TestPort_IsolationSuite`'s false-green (parent-defer vs t.Parallel) this
  session, so the isolation gate now gives real coverage.

## Log (A8 — vacuum + newroot landed)
- 2026-07-16: **A8-vacuum + A8-newroot landed — the remaining FPI-flippable btree structural records are now
  PG-format.** `EncodeBtreeVacuumPG` (RM_BTREE/VACUUM) carries the post-vacuum leaf as a block-0 apply-FPI;
  `EncodeBtreeNewRootPG` (RM_BTREE/NEWROOT) carries the new root (block 0, WILL_INIT) + updated metapage
  (block 2) as apply-FPIs. Both reuse the A0 encoder + the RmgrBtree default replay arm — zero new replay
  code, exactly like split. `LogBtreeVacuumFunc`/`LogBtreeNewRootFunc` were narrowed to pass the mutated
  page(s); newroot now updates the metapage in memory under `splitMu` before emit (retiring
  `updateRootMetaWithLSN`), co-holding root+meta race-free (each newroot holds its private root then the
  shared metapage — no lock cycle). Metapage FPI is hole-safe (meta struct below pd_lower). Test mocks in
  `btree_vacuum_wal_test.go` + `lpdead_kill_test.go` migrated from kept-items to page/`PageItemKeys`. New
  `internal/wal/btree_vacuum_newroot_pg_test.go` round-trips both. **UnlinkPage stays native by design**
  (concurrent-split relink re-derivation); MarkHalfDead dormant. **A8 is functionally complete — only
  UnlinkPage remains non-PG-format, with a documented reason. Next: A9, then Part B.**

## Log (A8 partial — split landed)
- 2026-07-16: **A8-split landed — BtreeSplit flip is LIVE (FPI-based).** `EncodeBtreeSplitPG` emits a PG
  RM_BTREE SPLIT_L record carrying the post-split left/right/sib pages as apply-FPIs; the existing RmgrBtree
  default arm (`replayDecodedXLogHeapFPIBlocks`) restores them (no new replay code). The emit closure already
  received the pages, so no signature change. Gates all green (executor, access/btree, initdb crash-recovery
  220s, e2e ×3, isolation 479s, regress 289s). **Deferred (in deferral ledger):** A8-vacuum + A8-newroot
  (FPI-feasible but need the post-op page threaded through the emit closures); A8-unlinkpage (keep incremental
  — concurrency-critical apply-time relink re-derivation, needs PG's real xl_btree_unlink_page main-data);
  A8-markhalfdead (dormant). **Next: finish A8 (vacuum/newroot/unlinkpage), then A9, then Part B.**

## Log (A7 complete)
- 2026-07-16: **A7-freeze landed — HeapFreeze flip is LIVE.** `EncodeHeapFreezePG` (xl_heap_prune,
  VACUUM_CLEANUP; one XLHP_HAS_FREEZE_PLANS plan + offset array = frozen slots). Reuses the A7-prune composite
  decoder/replay (PageFreezeBySlots). Wired `logHeapFreeze`. `heap_freeze_pg_test.go`: decode round-trip +
  insert→freeze replay (xmin→FrozenTransactionID). **A7 DONE** (prune + freeze; vacuum dormant).
  **Next: A8 btree structural, A9 smgr/clog/FPI/legacy-frame; then Part B.**

- 2026-07-16: **A7-prune landed — HeapPruneOpt flip is LIVE.** `EncodeHeapPruneOptPG` (xl_heap_prune,
  RM_HEAP2/PRUNE_ON_ACCESS; block-0 XLHP_HAS_REDIRECTIONS pairs + XLHP_HAS_NOW_UNUSED_ITEMS); built the
  composite `decodeXLogHeapPrune` (handles freeze plans too, for A7-freeze) + `replayDecodedXLogHeapPrune`
  (PageSetItemIDRedirect + VacuumHeapPageBySlots + PageFreezeBySlots) + `case RmgrHeap2` dispatch (opcode
  switch). Wired `logHeapPruneOpt` (covers opportunistic + VACUUM prune). Gates: wal+build, executor, vacuum,
  initdb crash-recovery (221s), e2e ×3. **A7-vacuum:** SKIPPED — `RecordKindHeapVacuum` is dormant (no runtime
  producer; VACUUM emits HeapPruneOpt). **A7-freeze next.** No conflict-horizon persisted (parity gap).

## Log
- 2026-07-15: Phase 0 complete (Ralph paused, WIP stashed `8d8a32da`, tracker created). Implementing on
  branch `wal-pg-stream-impl` off `344470fe`. Starting A0.
- 2026-07-15: **A0 landed** (commit `83c04364`) — `internal/wal/xlog_assemble.go` (+`_test.go`). 6
  round-trip cases green; full `internal/wal` suite + `-race` green; `go build ./...` green.
- 2026-07-15: Found A1 is **not** standalone-additive (see A1 ⚠️ note); folded into A2. Session boundary
  taken at A0 (clean committed keystone). **Next session: A2** (HeapInsert flip incl. per-record xid stamp,
  replay dispatch, FPI/logical unification) — its own focused session with full crash-recovery gates.
- 2026-07-16: A2 investigation (agent-mapped emit path). Findings: HeapInsert emit = native `EncodeHeapInsert`
  → `Append`→`wrapXLogMainData`; a separate conditional FPI follows (post-insert page) via
  `MarkDirtyLogicalChange` — so a **minimal flip keeps that FPI** (no unification needed yet). Routing to the
  already-built `replayDecodedXLogHeapInsert` is automatic (`r.Payload==nil` when a block ref is present).
  **`classifier.go` must be taught the decoded form** (it gates on native `r.Payload`).
- 2026-07-16: **Discovered the t_ctid-convention dependency** (see A2-pre) → user chose the full CTID change.
- 2026-07-16: **A2a landed** — `internal/wal/pg_assembled_emit.go` (envelope + non-wrapping `encodeAssembledXLog`)
  + branches in `encodeRecordXLog`/`predictXLogRecordLen` + `pg_assembled_emit_test.go` (3 cases). Additive
  (wired to nothing). Gates green: build/vet/test/-race ./internal/wal/. Next: A2-pre (CTID), then A2b/c.
- 2026-07-16: **A2-pre landed** (audit-mapped, agent-verified). Stamp self-`t_ctid` in `markHeapInsertDirty`
  (page via existing `PageSetHeapTupleCtid` + `tupleBytes[12:18]` for redo consistency); swap the 4
  `Block==InvalidBlockNumber`-only delete-detectors (`operators_storage.go:259/4051/4581/4803`) to
  `isChainTailCTID`. New `insert_self_ctid_test.go` (plain + NULL rows self-pointing). **Gates all green:**
  storage + executor unit + `-race`; **full isolation 121/121**; **full regress ok**. (Isolation/regress need
  the `postgres` submodule — symlinked the worktree's empty `postgres/` to the main tree's REL_18_3 checkout.)
- 2026-07-16: **A2b-core landed** (dormant) — `EncodeHeapInsertPG` (`pg_assembled_emit.go`) builds the PG
  `xl_heap_insert` (main-data offnum/flags + blk0 `xl_heap_header`+tuple, xid from t_xmin) via A0/A2a; fixed
  `decodeXLogHeapInsertTuple` to reconstruct the tuple by **verbatim concat** (null-bitmap-safe; the old
  prefix-strip rejected non-zero bitmaps). `heap_insert_pg_test.go` round-trips plain + NULL tuples
  byte-for-byte through the real encode/decode path; existing decoded-replay tests stay green. Gates:
  build/vet/test/-race ./internal/wal/. **Remaining A2c** (final, live-stream): wire the `logHeapInsert`
  closure (open.go) to `EncodeHeapInsertPG` + non-wrapping Append, teach `classifier.go` the decoded form,
  gate on G-crash + goopg↔goopg.
- 2026-07-16: **A2c landed — HeapInsert flip is LIVE.** `logHeapInsert` (open.go) now emits
  `EncodeHeapInsertPG`; `classifier.go` gained `classifyDecodedXLog` (routes the PG record by xl_xid) +
  `heap_insert_pg_classify_test.go`. Every INSERT now writes a PostgreSQL `xl_heap_insert`. **Gates:** wal
  unit+`-race`, executor, initdb crash-recovery (237s), e2e native-replication+promotion / physical /
  logical — all green. **A2 DONE.** Next record: A3 HeapDelete (same pattern, no t_ctid landmine).
- 2026-07-16: **A3a landed** (dormant). Unlike HeapInsert, **no** decoded delete replay existed → built it:
  `EncodeHeapDeletePG` (main-data `xl_heap_delete{xmax,offnum,infobits=0,flags}` + optional old tuple as
  `xl_heap_header`+data for logical; block-0 page ref; xl_xid=xmax), `decodeXLogHeapDeleteMainData`,
  `replayDecodedXLogHeapDelete` (reuses `PageSetHeapTupleXmax` = native parity; FPI fallback). Split the
  decoded dispatch so `xlogHeapDelete` gets the new handler (update/hotupdate/inplace stay FPI-only).
  `heap_delete_pg_test.go`: insert→delete replay stamps xmax (xmin/data intact) + old-tuple carried. Gates:
  build/vet/test/-race ./internal/wal/.
- 2026-07-16: **A3b landed — HeapDelete flip is LIVE.** `logHeapDelete`→`EncodeHeapDeletePG` (open.go);
  `classifyDecodedXLog` gained the delete branch (reconstructs old tuple via `reconstructMarshaledTupleFromHeader`)
  + `heap_delete_pg_classify_test.go`. Every DELETE now writes a PostgreSQL `xl_heap_delete`. **Gates all green:**
  wal+`-race`, executor, initdb crash-recovery (234s), e2e native/physical/logical replication, **full isolation
  + full regress**. **A3 DONE.** Next: A5 BtreeInsert or A4 HeapHotUpdate (update needs 2 block refs + t_ctid chain).
- 2026-07-16: **A4 landed (HOT path).** A4a (encoder+decoded replay, dormant) + A4b (live wiring). Threaded
  `new_offnum` (bufpool `LogHeapHotUpdateFunc` + open.go closure + markHeapHotUpdateDirty + tryApplyHOTUpdate);
  self-`t_ctid` stamp extended to the HOT new version; classifier decoded HOT-update path. Every HOT UPDATE
  now writes a PG `xl_heap_update`. Gates all green (wal+race, executor, server, initdb 228s, e2e ×3,
  isolation 485s, regress 288s). Non-HOT single-record conversion deferred (already PG-format Delete+Insert).
  **Next: A5 BtreeInsert.**
- 2026-07-16: **A5 landed — BtreeInsert flip is LIVE.** A5a (encoder + `replayDecodedXLogBtreeInsert` via
  `btree.ApplyInsertRecord`, RmgrBtree opcode switch, dormant) + A5b (`logBtreeInsert`→`EncodeBtreeInsertPG`).
  Every leaf index insert now writes a PG `xl_btree_insert`. Gates all green (wal+race, executor, initdb
  crash-recovery 227s, e2e ×3, full isolation + regress — fresh `-count=1`). **A5 DONE.** offnum=0 parity gap
  documented.
- 2026-07-16: **A6 landed — Xact commit/abort flip is LIVE.** A6a (EncodeXactCommitPG/AbortPG, xid→header,
  HAS_INVALS redo, dormant) + A6b (wire SetXactMarkerLogger hook, classifyDecodedXLog RmgrXact branch,
  header-based `wal_durability_test`). Key insight: the xid moves body→header (xl_xid), but 3 of 4 consumers
  were already header-ready — only the logical-decoder classifier needed the RmgrXact branch. Every COMMIT/
  ABORT now writes a PG `xl_xact_commit`/`xl_xact_abort`. **A6 DONE.** **Part-A hot set (A2–A6) COMPLETE;
  remaining: A7 heap2, A8 btree structural, A9 smgr/clog/FPI/legacy-frame; then Part B.**
