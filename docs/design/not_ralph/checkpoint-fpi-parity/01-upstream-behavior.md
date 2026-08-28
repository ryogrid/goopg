# PG 18.3 CHECKPOINT & FPI mechanics (oracle behavior)

All citations are from the read-only oracle at `postgres/` (PG 18.3).
Paths abbreviated: xlog.c / xloginsert.c =
`src/backend/access/transam/…`; checkpointer.c =
`src/backend/postmaster/checkpointer.c`.

## 1. The FPI decision rule

`XLogInsert(rmid, info)` assembles each registered buffer reference in
`XLogRecordAssemble` (xloginsert.c:474 def; assembly loop xloginsert.c:589-626):

```c
if (regbuf->flags & REGBUF_FORCE_IMAGE)
    needs_backup = true;
else if (regbuf->flags & REGBUF_NO_IMAGE)
    needs_backup = false;
else if (!doPageWrites)
    needs_backup = false;
else
{
    XLogRecPtr page_lsn = PageGetLSN(regbuf->page);
    needs_backup = (page_lsn <= RedoRecPtr);          /* xloginsert.c:618-620 */
    if (!needs_backup && (*fpw_lsn == Invalid || page_lsn < *fpw_lsn))
        *fpw_lsn = page_lsn;                          /* xloginsert.c:623-624 */
}
include_image = needs_backup || (info & XLR_CHECK_CONSISTENCY); /* :647 */
```

Key facts:

* PG 18 uses **`<=`** (an older releases used `<`). A page whose LSN equals
  the redo point still gets imaged.
* `doPageWrites` is `(Insert->fullPageWrites || Insert->runningBackups > 0)`
  sampled under the WAL insert lock (xlog.c:846). When `full_page_writes=off`
  (and no running backup), **no images at all** are produced
  (xloginsert.c:609-610).
* If the redo pointer or doPageWrites moved while the caller was assembling,
  `XLogInsertRecord` detects it under the insert lock and makes the caller
  restart assembly (xlog.c:841-859). Holding any WAL insert lock freezes
  RedoRecPtr/fullPageWrites for the duration of one record (xlog.c:809-810).
* The advisory helper `XLogCheckBufferNeedsBackup` uses the identical test
  (xloginsert.c:1026-1041).

## 2. RedoRecPtr lifecycle — publication happens at checkpoint START

* Backend-local + shared copies: `RedoRecPtr` static, `XLogCtl->RedoRecPtr`
  (info_lck copy), `XLogCtl->Insert.RedoRecPtr` (authoritative). Readers use
  `GetRedoRecPtr()` (xlog.c:6475-6493) and `GetFullPageWriteInfo()`
  (xlog.c:6504-6509).
* **Online checkpoint**: `CreateCheckPoint` acquires all WAL insert locks
  exclusively (xlog.c:7039), snapshots `checkPoint.fullPageWrites` (:7041),
  then inserts a special `XLOG_CHECKPOINT_REDO` record; its start LSN becomes
  the new redo point and is published into `Insert->RedoRecPtr` inside
  `XLogInsertRecord`'s WALINSERT_SPECIAL_CHECKPOINT arm
  (xlog.c:7094-7108, publication at xlog.c:903 under exclusive locks). The
  info_lck copy follows at xlog.c:7110-7113.
* **Shutdown checkpoint**: no concurrent inserts can happen, so the redo
  point is computed directly and published while holding the exclusive
  insert locks (xlog.c:7044-7076, publication at :7076). The checkpoint
  record itself marks the redo point (:7086-7092 comment).
* Publication deliberately happens BEFORE buffer flushing: "We can't
  postpone advancing RedoRecPtr because XLogInserts that happen while we are
  dumping buffers must assume that their buffer changes are not included in
  the checkpoint" (xlog.c:7064-7075). This is exactly what makes the FIRST
  modification of any previously-dirty page carry an FPI after a checkpoint
  starts.
* Restartpoints publish the recovered checkpoint's redo the same way
  (xlog.c:7707-7712). Startup seeds RedoRecPtr/doPageWrites from the
  checkpoint recovery started from (xlog.c:5725-5728, inside `StartupXLOG`
  def at :5467).

Consequence: between checkpoint START and checkpoint END there is **no
image-less window** — pages dirtied in that interval have LSN > redo and
need none; pages whose last change predates redo get imaged on first touch.

## 3. full_page_writes gating and runtime toggles

* Default on: `bool fullPageWrites = true;` (xlog.c:123); GUC registered
  PGC_SIGHUP (guc_tables.c, full_page_writes entry mirrors defaults below).
* The shared authoritative value lives in `XLogCtlInsert.fullPageWrites`;
  backend-local copies may lag by design (xlog.c:421-432, comment block;
  local `doPageWrites` mirror described at :277).
* `UpdateFullPageWrites()` (xlog.c:8211-8268): when turning ON, set shared
  flag first then log `XLOG_FPW_CHANGE(on)`; when turning OFF, log
  `XLOG_FPW_CHANGE(off)` first then clear the flag (:8233-8247 ordering
  rationale "It's always safe to take full page images … not the other
  round"). The record is written when `XLogStandbyInfoActive()`
  (:8253-8259).
* Replay of `XLOG_FPW_CHANGE(off)` advances `XLogCtl->lastFpwDisableRecPtr`
  (field declared xlog.c:549-552; replay update :8621-8642). This LSN is
  consulted ONLY by backup validation paths (xlog.c:8978, 9274) so that
  `pg_backup_start/stop` refuse backups spanning FPW-disabled WAL. It does
  NOT re-enable imaging on the primary: with FPW off, upstream emits no
  images, period. Crash-safety of an FPW=off epoch rests on the next
  checkpoint flushing+fsyncing everything before its redo point becomes
  valid.

## 4. CreateCheckPoint end-to-end ordering (xlog.c:6927-7404)

1. `SyncPreCheckpoint()` (call :6970; def src/backend/storage/sync/sync.c:177).
2. Shutdown flavor sets control state `DB_SHUTDOWNING` immediately (:6977-6983).
3. Idle-skip: non-forced, non-shutdown checkpoints bail out when no
   "important" WAL was generated since the last checkpoint — "checkpoint
   skipped because system is idle" (:7005-7019).
4. Redo determination + publication (see §2; :7039-7113).
5. `LogCheckpointStart` if `log_checkpoints` (:7119-7120).
6. Wait out transactions in `DELAY_CHKPT_START` critical sections
   (:7197-7215) so clog updates of commits straddling the redo point are
   flushed by this checkpoint.
7. `CheckPointGuts(checkPoint.redo, flags)` (:7217; body :7550-7577):
   relation map, replication slots, snapbuild, logical rewrite, replorigin;
   SLRU checkpoints (CLOG, CommitTs, SUBTRANS, MultiXact, predicate);
   `CheckPointBuffers(flags)` — spread writeback of the shared buffer pool;
   `ProcessSyncRequests()` — fsync queue drain (data-file durability);
   `CheckPointTwoPhase`.
8. Wait out `DELAY_CHKPT_COMPLETE` transactions (:7219-7232).
9. `LogStandbySnapshot()` (XLOG_RUNNING_XACTS) for hot standby when enabled
   (:7242-7243). (bgwriter also logs these periodically, bgwriter.c:292.)
10. Insert the checkpoint record (`XLOG_CHECKPOINT_SHUTDOWN` or
    `_ONLINE`) and `XLogFlush(recptr)` — the record must be durable before
    the checkpoint is considered done (:7245-7256).
11. After a shutdown checkpoint, forbid further WAL writes; PANIC if any
    concurrent activity was detected (:7265-7279).
12. Update pg_control LAST: `ControlFile->state = DB_SHUTDOWNED` (shutdown),
    `checkPoint = ProcLastRecPtr`, `checkPointCopy = checkPoint`,
    `UpdateControlFile()` (:7285-7307). Only now does the new checkpoint /
    redo pair become the official restart state.
13. `SyncPostCheckpoint()` (call :7342; def sync.c:202) — unlink doomed
    segments etc.; `RemoveOldXlogFiles` recycles pre-redo segments
    (:7353-7372).
14. The checkpointer process closes all smgr relations after every
    checkpoint: `smgrdestroyall()` (checkpointer.c:488-490, and :1021).

## 5. Triggers

| Trigger | Mechanism | Citation |
|---|---|---|
| Manual SQL `CHECKPOINT` | `RequestCheckpoint(CHECKPOINT_IMMEDIATE \| CHECKPOINT_WAIT \| CHECKPOINT_FORCE)`; requires `pg_checkpoint` role | utility.c:955-956, privilege check :945-953 |
| Timeout | `CheckPointTimeout = 300` s default (checkpointer.c:144; GUC default 300 s, range 30..86400, guc_tables.c:2982-2991); loop computes next deadline from elapsed time (checkpointer.c:390, 569-571) | |
| Volume (`max_wal_size`) | `assign_max_wal_size` → `CalculateCheckpointSegments`: distance = `ConvertToXSegs(max_wal_size)/(1+completion_target)`, floor 1 (xlog.c:2171-2198, math at :2189-2193). `XLogCheckpointNeeded(new_segno)` compares segment numbers vs RedoRecPtr (xlog.c:2280-2289); evaluated in `XLogWrite` right after opening a NEW segment, double-checked with a refreshed RedoRecPtr (xlog.c:2491-2504) | |
| Shutdown | `ShutdownXLOG` → `CreateCheckPoint(CHECKPOINT_IS_SHUTDOWN \| CHECKPOINT_IMMEDIATE)` (xlog.c:6640-6680, call at :6679) | |
| Spread vs immediate | `CheckpointWriteDelay` paces writeback to finish within `checkpoint_completion_target × timeout` (checkpointer.c:759-900, scaling at :851, deadline checks :880-897); IMMEDIATE checkpoints bypass pacing (:993 comment). GUC default 0.9 (guc_tables.c:4123-4129) | |
| Stats | num_timed vs num_requested counted per flags (checkpointer.c:420-437) | |
| Warning | CAUSE_XLOG checkpoints sooner than `checkpoint_warning` log "checkpoints are occurring too frequently" (checkpointer.c:444-456 region) | |

## 6. Recovery REDO-start handling

Startup reads pg_control, validates/replays from `checkPointCopy.redo`
(xlogrecovery.c), then `StartupXLOG` seeds `RedoRecPtr` and
`lastFullPageWrites`/`doPageWrites` from that checkpoint so the first
post-restart modifications apply the same FPI rule against the correct epoch
(xlog.c:5725-5728). FPIs encountered in WAL are applied unconditionally
(`BKPIMAGE_APPLY`, set for needs_backup images at xloginsert.c:720-721).
