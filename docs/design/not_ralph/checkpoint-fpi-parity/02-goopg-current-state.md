# goopg current state — CHECKPOINT & FPI behavior

All goopg citations are relative to the worktree root
(`.claude/waitevent-impl`). Upstream counterpart citations appear inline as
`[PG: …]`. Line numbers verified on this branch.

## 0. The DML trace — heap insert from executor to WAL

Entry point: `markHeapInsertDirty`
(internal/executor/operators_storage.go:9673-9721), the single choke point
all fresh-insert paths funnel through:

1. Stamps PG's self-pointing `t_ctid` into the page and tuple bytes
   (operators_storage.go:9679-9696).
2. Calls `pool.MarkDirtyLogicalChange(slot, emitter)` (:9718-9720) — NOT
   `MarkDirtyChangeRecord`, because the logical HeapInsert record must always
   be emitted even when an FPI would replace it for redo purposes
   (:9712-9717 rationale; see docs/design/0103-0018).
3. `MarkDirtyLogicalChange` (internal/storage/bufpool.go:2398-2444):
   * takes `fpiPublishMu.RLock` spanning decision + appends (:2403);
   * `needFPI := p.needsImage(s)` (:2405);
   * ALWAYS calls `emitter()` → `logHeap(...)` producing the native/PG heap
     WAL record, stamps page header LSN via `MustHeader(s.page).SetLSN(lsn)`
     (:2410-2414);
   * if `needFPI && logFPI != nil && FullPageWrites()` → emits a standalone
     full-page image record via `p.logFPI(tag.Rel, tag.Block, pageCopy)`
     (:2416-2425), re-stamps pd_lsn and records
     `nativeImageLSN = fpiLSN`.
4. `needsImage` (bufpool.go:1081-1098): `s.nativeImageLSN.Load() <=
   p.redoRecPtr.Load()` (:1097). Deliberately keyed on the per-slot NATIVE
   image watermark rather than pd_lsn so a canonical-family stamp cannot
   suppress a native image ("cross-family poisoning", :1087-1089). The
   1-based-vs-0-based LSN base mismatch errs only toward EXTRA images
   (:1091-1095).
5. `logFPI` is wired in initdb/open.go:470-484: encodes a real PG
   `XLOG_FPI` (RM_XLOG standalone full-page image, block-0 apply-image,
   hole removed) via `xlog.EncodePageImagePG` and appends it through
   `walWriter.Append`; wired into the pool as `LogPageImage` at open.go:765.
   Recovery restores it via the RmgrXLog XLOG_FPI arm
   (`replayDecodedXLogHeapFPIBlocks`, internal/access/transam/xlog/recovery.go:2434).

**Conclusion of trace:** whether the first modification of a page after a
checkpoint carries an image is decided by comparing the slot's image
watermark against the *published* redo pointer. The published pointer is set
at checkpoint START (§2 below), so the first post-checkpoint touch of any
page whose last image predates that redo emits a fresh FPI — same rule, same
timing as upstream [PG: xloginsert.c:618-626, publication xlog.c:7039-7113].

The non-record legacy path (`MarkDirty` → `maybeEmitFPI`, bufpool.go:2282,
2446-2480) applies the same gate for hint-like dirtiers.

## 1. FPI decision rule

| Upstream | goopg |
|---|---|
| `PageGetLSN(page) <= RedoRecPtr`, gated on doPageWrites (xloginsert.c:604-626) | `nativeImageLSN <= publishedRedoRecPtr`, gated on `FullPageWrites()` (bufpool.go:1097, 2346/2416/2449) |

* Comparison operator matches PG 18's `<=`.
* Keying difference (watermark vs pd_lsn) is conservative: extra images only
  (bufpool.go:1087-1095). After eviction the watermark survives via a
  tag-keyed stash (bufpool.go:1040-1074); after process restart it resets to
  0, so every page images once more than upstream would — safe direction,
  WAL-volume-only cost.
* Race protection equivalence: upstream freezes RedoRecPtr/doPageWrites with
  the WAL insert lock across assembly+insert and retries on change
  (xlog.c:809-810, 841-859); goopg holds `fpiPublishMu.RLock` across
  decision→append and publishes under the write side
  (bufpool.go:2336-2338, 1017-1038), which is the same guarantee by
  construction.

## 2. Redo publication timing — checkpoint START ✓

`runCheckpoint` (internal/access/transam/xlog/checkpointer.go:717-1039)
order:

1. **Publish redo BEFORE flushing**: comment :728-733 ("compute AND PUBLISH
   the redo pointer BEFORE the buffer flush (PG CreateCheckPoint order)").
   For PG-compat ONLINE checkpoints the sampled value is the start LSN of an
   inserted `XLOG_CHECKPOINT_REDO` record (:769-783, `startLSN - 1` converts
   to 0-based) [PG: xlog.c:7094-7108]; shutdown/legacy checkpoints use the
   sampled writer frontier (:764-766, computeRedo :734-751) [PG:
   xlog.c:7044-7076].
2. Publication goes through `PublishRedoBarrier(sample)` when the flusher
   implements `redoPublisher` (:784-793; interface :29-39) →
   bufpool.go:1026-1038: exclusive `fpiPublishMu`, sample, atomic store,
   drop stale evicted-watermarks.
3. Then `flushDirty` (:798-801), CLOG drain (:806-810), data-file fdatasync
   M0089 (:811-821).
4. The old post-record epoch reset is gone; NOTE at :990-993 documents that
   the START publication is the sole epoch boundary. There is therefore NO
   FPW-off-style window between checkpoint end and next checkpoint start:
   between checkpoints the published redo is stable and every page's first
   modification after it is imaged once.

Startup seeding: `initdb.Open` reads pg_control and calls
`pool.PublishRedoRecPtr(pgCtrl.CheckPointCopyRedo)`
(internal/initdb/open.go:1249-1258) [PG: xlog.c:5725-5728]. Without the seed
(redo=0 = "image everything") the direction stays safe
(bufpool.go:1076-1079).

## 3. full_page_writes gating

* GUC registered: internal/utils/misc/defaults.go:380-385 (`TypeBool`,
  `BootVal: "on"`, ContextSigHup) [PG: default true xlog.c:123].
* Runtime plumbing: `Pool.SetFullPageWrites/FullPageWrites`
  (bufpool.go:993-997); applied once at startup from the registry in
  cmd/goopg/main.go:777-779; the checkpointer samples the live value into
  every checkpoint record + pg_control via `FullPageWritesFn`
  (checkpointer.go:166-174, :852-863, :943-948).
* Suppression semantics match upstream: with FPW off none of the three emit
  paths produces images (bufpool.go:2346, 2416, 2449) [PG:
  xloginsert.c:609-610].
* **Gap:** toggling the GUC at runtime does not emit an `XLOG_FPW_CHANGE`
  record (upstream writes one whenever wal_level permits, xlog.c:8253-8259;
  replay tracks disables at :8621-8642), and the registry value is applied
  only at server start — `Server.reloadConfig`
  (internal/postmaster/server.go:823) does not re-push
  checkpoint_timeout/max_wal_size/completion_target/full_page_writes into
  the checkpointer/pool. main.go:749-753 says so explicitly ("today the
  registry value is frozen at startup"). Crash safety is unaffected; standby/
  tooling observability differs (D-2/D-3).

## 4. Checkpoint cycle ordering (checkpointer.go:717-1039)

| Step | goopg | Upstream |
|---|---|---|
| pre-checkpoint sync hook | — (no SyncPreCheckpoint analog needed; fsync queue is drained in step 3) | sync.c:177 |
| shutdown state marker | only final DBStateShutdowned at control update (:927-938); no DB_SHUTDOWNING interim | xlog.c:6977-6983 |
| idle skip | **absent** — timed loop always runs (:518-537 ticker case → runCheckpoint :717) | xlog.c:7005-7019 |
| redo publish at START | :784-793 | xlog.c:7039-7113 |
| quiesce delayChkpt xacts | absent (structural; no delayChkpt flags) | xlog.c:7197-7215, 7219-7232 |
| buffer writeback (spread) | `FlushAllPaced(pacer)` via buildPacer (:1041-1078) | CheckPointBuffers + CheckpointWriteDelay |
| SLRU/CLOG flush | FlushCLOGFn before redo-sample-dependent phases (:806-810) | CheckPointCLOG etc. |
| data durability | `SyncAllDataFiles` fdatasync after pwrite, before marker (M0089-0001, :811-821) | ProcessSyncRequests xlog.c:7571 |
| running-xacts record | online PG-compat only, right before the checkpoint payload (:869-890) | xlog.c:7242-7243 |
| checkpoint record + flush | Append + `FlushUpTo(endLSN)` (:901-907); explicit SHUTDOWN/ONLINE opcodes via EncodeCheckpointPGFields (pg_assembled_emit.go:1284-1335) | xlog.c:7245-7256 |
| post-shutdown WAL ban | implicit (Close order: checkpoint → pool/WAL close, open.go:3379-3414) | xlog.c:7265-7279 |
| pg_control LAST | UpdateControlFile after marker durable (:919-984): State, CheckPoint=startLSN-1, CheckPointCopyRedo=redoLSN0, TLI/FPW/multixact/xid/oid copies, MinRecoveryPoint=0 | xlog.c:7285-7307 |
| retention / old-WAL removal | SlotAwareRetainer.Retain from REDO pointer (:1000-1009; C2-S3 MUST-FIX note) | xlog.c:7353-7372 |
| smgr close phase | absent (D-6) | checkpointer.c:488-490 |
| post hooks | pg_internal.init regen, CLOG/SUBTRANS truncation (:1010-1031) | CheckPointTwoPhase etc. inside Guts |

Durability-relevant ordering (flush → fsync → record durable → control file
LAST → retention) matches upstream exactly; the missing pieces are hygiene
and observability, not crash safety.

## 5. Triggers

* **Manual SQL CHECKPOINT**: parser node `optimizer.Checkpoint`
  (internal/optimizer/plan.go:2265-2271, built at planner.go:258) →
  executor dispatch newCheckpointOp (internal/executor/executor.go:314;
  task hint about operators_ddl ~26932 was stale — it lives in
  internal/executor/operators_checkpoint.go) → `ctx.Checkpointer.CheckpointNow()`
  (operators_checkpoint.go:19-36; interface internal/executor/context.go:1795).
  CheckpointNow is synchronous IMMEDIATE-speed and returns after the marker
  is durable (checkpointer.go:633-640) — equivalent to upstream's
  `CHECKPOINT_IMMEDIATE | CHECKPOINT_WAIT | CHECKPOINT_FORCE`
  (utility.c:955-956). Note: goopg has no `pg_checkpoint` role gate here
  (utility.c:945-953) — out-of-band observation, listed as adjacent note.
* **CLI/control-plane**: `goopg ctl checkpoint` subcommand (main.go:53,72)
  and control listener OnCheckpoint (server.go:773-780) both call
  CheckpointNow.
* **Timeout**: `checkpoint_timeout` GUC BootVal 300 s (defaults.go:356-360 =
  guc_tables.c:2982-2991) wired at startup into `SetInterval`
  (main.go:749-763); Run arms a ticker (checkpointer.go:484). Config
  fallback if unset is 10 s (withDefaults :271-274) — production always
  overrides from the GUC.
* **Volume (max_wal_size)**: BootVal 1024 MB (defaults.go:368-372) →
  SetMaxWALBytes (main.go:764-770). Trigger distance mirrors
  CalculateCheckpointSegments including the /(1+completion_target) division
  and floor (checkpointSegments, checkpointer.go:561-567 = xlog.c:2171-2198;
  M0131-S30.4 fixed the raw-bytes bug documented at :556-560). Evaluation:
  1 s poll of WrittenLSN vs segment-number test anchored at the last
  checkpoint REDO (volumeTriggerFires :583-613 ≈ xlog.c:2280-2289, evaluated
  upstream at segment-open xlog.c:2491-2504); deliberate floor deviation
  documented at elapsedSegmentsNeeded (:615-631). Pre-first-checkpoint anchor
  seeded from writer position (:487-500).
* **completion_target**: BootVal 0.9 (defaults.go:362-366 = guc_tables.c:4123-4129)
  → SetCompletionTarget (main.go:772-776); spreading implemented by
  buildPacer over Interval×target (:1050-1078); volume-/SQL-triggered
  checkpoints bypass spreading (:110-115 doc, :530-535) matching IMMEDIATE
  semantics (checkpointer.c:993).
* **Stats classification**: num_timed (spread=true) vs num_requested
  (:664-668, 985-989) rendered by pg_stat_checkpointer.
* **Shutdown checkpoint EXISTS**: graceful STOP runs an OnStop CheckpointNow
  (DB_IN_PRODUCTION, server.go:722-739) and then Runtime.Close runs the real
  `CheckpointShutdown` stamping DB_SHUTDOWNED (open.go:3379-3394;
  checkpointer.go:642-655) — mirroring ShutdownXLOG →
  CreateCheckPoint(IS_SHUTDOWN|IMMEDIATE) (xlog.c:6640-6680). Immediate stop
  skips both, leaving DB_IN_PRODUCTION for recovery-on-next-start
  (open.go:3364-3378, server.go:748-767, main.go:1064). Quirk: two
  back-to-back checkpoints on graceful stop where upstream performs one
  (D-4). BASE_BACKUP also takes a CheckpointNow first
  (internal/backup/basebackup.go:264).
* **No checkpoint_warning/"too frequently" logic** exists anywhere in
  goopg (grep clean) — cosmetic gap vs checkpointer.c:444-456 region.

## 6. Recovery REDO-start handling

* `beginRecovery` (internal/initdb/recovery_state.go:75-116): reads
  pg_control; `redoLSN = cd.CheckPointCopyRedo` (:86); classifies
  online-vs-shutdown checkpoint via `CheckPointCopyRedo < cd.CheckPoint`
  (:90); crash recovery required unless State==SHUTDOWNED and the last
  checkpoint was a shutdown one (:91-93); logs upstream-verbatim messages
  (:96-103) [PG: xlogrecovery.c equivalents].
* Replay starts at the seeded redo; embedded page images are restored by
  dedicated arms — native RecordKindPageImage and PG XLOG_FPI both route to
  `replayDecodedXLogHeapFPIBlocks` (recovery.go:2425-2453, :2815);
  XLOG_CHECKPOINT_ONLINE/SHUTDOWN handled at :2457 and :6284.
* The buffer-pool FPI epoch is re-seeded from the same control-file redo
  (open.go:1257), so post-restart first-touch imaging keys off the same
  point crash recovery replays from.

## 7. Divergence summary (details → 03-design.md)

| ID | Area | Severity | goopg | PG 18.3 |
|----|------|----------|-------|---------|
| D-1 | Idle-skip of timed checkpoints | LOW | checkpointer.go:518-537, 717 (no skip) | xlog.c:7005-7019 |
| D-2 | GUC liveness (timeout/volume/cct/fpw frozen at startup; reload no-op) | MED-LOW | main.go:749-779, server.go:823 | guc_tables.c SIGHUP assigns |
| D-3 | No XLOG_FPW_CHANGE emission / no lastFpwDisableRecPtr tracking | LOW | decode-only constant pg_xlog_decode.go:209 | xlog.c:8211-8268, 8621-8642 |
| D-4 | Graceful stop runs TWO checkpoints (OnStop + Close) | LOW | server.go:731-739 + open.go:3379-3394 | xlog.c:6640-6680 (one) |
| D-5 | Volume trigger evaluated by poll, not at segment open (+floor) | LOW | checkpointer.go:583-631 | xlog.c:2491-2504 |
| D-6 | No smgr close-all phase after checkpoint | LOW | absent (storage/smgr.go has no checkpoint hook) | checkpointer.c:488-490 |
| D-7 | No DELAY_CHKPT quiesce windows around flush | LOW-MED | absent | xlog.c:7197-7232 |
