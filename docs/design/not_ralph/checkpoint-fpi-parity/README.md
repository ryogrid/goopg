# CHECKPOINT / FPI parity audit — goopg vs PostgreSQL 18.3

Status: audit complete, no code changes made (design bundle only).
Worktree: `.claude/waitevent-impl` (branch `waitevent-impl`).
Oracle: PG 18.3 sources under `postgres/` (read-only).

## Scope

Whether goopg's CHECKPOINT behavior — manual and automatic — matches vanilla
PG 18.3, with the central question being the reviewer's concern:

> Does the FIRST page modification after a checkpoint include a full-page
> image (FPI), exactly like upstream?

Everything is traced on both sides with file:line citations:

* upstream: `postgres/src/backend/access/transam/{xlog.c,xloginsert.c}`,
  `postmaster/checkpointer.c`, `storage/sync/sync.c`, `tcop/utility.c`,
  `utils/misc/guc_tables.c`
* goopg: `internal/storage/bufpool.go`,
  `internal/access/transam/xlog/checkpointer.go`,
  `internal/executor/operators_checkpoint.go`,
  `internal/postmaster/server.go`, `internal/initdb/{open,recovery_state}.go`,
  `cmd/goopg/main.go`, `internal/utils/misc/defaults.go`

Method: one real DML (heap insert) is traced end-to-end from executor →
bufpool → WAL record in [02](02-goopg-current-state.md) to establish exactly
when an FPI is emitted relative to checkpoints.

## Headline answer

**YES — the first-touch-after-checkpoint case emits an FPI.** goopg publishes
the redo pointer at checkpoint START (`PublishRedoBarrier`,
internal/access/transam/xlog/checkpointer.go:784-793) BEFORE the dirty-page
flush, and every page whose recorded image watermark is `<= redo` gets a full
page image on its first post-publication modification
(internal/storage/bufpool.go:1081-1098, 2335-2444). This matches upstream's
`PageGetLSN(page) <= RedoRecPtr` rule
(postgres/src/backend/access/transam/xloginsert.c:618-626), including PG 18's
`<=` comparison.

## Verdict table

| # | Area | Verdict | One-line reason |
|---|------|---------|-----------------|
| 1 | Manual SQL `CHECKPOINT` statement | MATCH | Sync IMMEDIATE-speed checkpoint both sides (utility.c:955-956 vs checkpointer.go:633-640, operators_checkpoint.go:27) |
| 2 | FPI decision rule (first touch after checkpoint start) | MATCH | `watermark <= publishedRedo` at checkpoint-start publication; barrier prevents decision/append tearing |
| 3 | Redo pointer published at checkpoint START (not END) | MATCH | PublishRedoBarrier before flushDirty (checkpointer.go:728-799); old post-record epoch reset is gone (checkpointer.go:990-993) |
| 4 | FPW-off window between checkpoint end/start | MATCH | Published redo persists between checkpoints; no image-less window exists (perf-optimize3-dash/03 closed it) |
| 5 | `full_page_writes=off` suppresses images | MATCH | All three emit paths gate on `FullPageWrites()` (bufpool.go:2346, 2416, 2449) like xloginsert.c:609-610 |
| 6 | Shutdown checkpoint exists | MATCH (quirk) | Runtime.Close → CheckpointShutdown (open.go:3379-3394); but graceful STOP runs TWO checkpoints (see D-4) |
| 7 | Checkpoint phases/durability order | MATCH (notes) | publish-redo → flush → fdatasync → record → pg_control LAST (checkpointer.go:717-984 ≈ xlog.c:6927-7307) |
| 8 | `checkpoint_timeout` trigger (300 s default) | PARTIAL | Value+default match (defaults.go:358 = guc_tables.c:2982-2991) but frozen at startup; no SIGHUP live reload (D-2) |
| 9 | `max_wal_size` volume trigger | MATCH (approx) | Distance math mirrors CalculateCheckpointSegments incl /(1+cct) fix (checkpointer.go:561-567 = xlog.c:2169-2198); evaluated by 1 s poll instead of at segment open (D-5) |
| 10 | `checkpoint_completion_target` spreading | MATCH | buildPacer spreads writeback over Interval*cct; IMMEDIATE bypasses (checkpointer.go:1050-1078 ≈ checkpointer.c:759-900, 993) |
| 11 | Idle-skip of timed checkpoints | **DIVERGE** | goopg checkpoints unconditionally every Interval; upstream skips when no important WAL since last checkpoint (xlog.c:7005-7019) → empty-checkpoint noise + num_timed inflation (D-1) |
| 12 | Runtime FPW toggle mechanics | **DIVERGE** (minor) | No `XLOG_FPW_CHANGE` emission, GUC applied once at startup only (main.go:777-779); upstream writes FPW_CHANGE under insert locks (xlog.c:8211-8268) (D-3) |
| 13 | smgr close phase after checkpoint | **DIVERGE** (minor) | No post-checkpoint smgrdestroyall analog (checkpointer.c:488-490); fds released lazily (D-6) |
| 14 | delayChkpt quiesce windows around flush | PARTIAL | Upstream waits out DELAY_CHKPT_START/COMPLETE xacts (xlog.c:7197-7232); goopg has no analog, covered indirectly by redo-anchored retention (checkpointer.go:1000-1008) (D-7) |
| 15 | Recovery REDO-start handling | MATCH | beginRecovery seeds from pg_control CheckPointCopyRedo + online-vs-shutdown discrimination (recovery_state.go:75-116); pool epoch seeded open.go:1249-1258 ≈ xlog.c:5725-5728; FPI replay arms exist (recovery.go:2425-2453) |
| 16 | PG-compat checkpoint records | MATCH | SHUTDOWN(0x00)/ONLINE(0x10)/CHECKPOINT_REDO opcodes + RUNNING_XACTS before online records (pg_xlog_decode.go:202-203, pg_assembled_emit.go:1284-1335, checkpointer.go:769-783, 869-900 ≈ xlog.c:7094-7108, 7242-7243) |

Overall: **the core torn-page/FPI contract matches upstream**. All
divergences found are peripheral (idle-skip, config liveness, FPW_CHANGE
record, double shutdown checkpoint, smgr-close phase, delayChkpt quiesce);
none breaks crash safety. Details per area in [03-design.md](03-design.md).

## Doc index

| File | Content |
|---|---|
| [01-upstream-behavior.md](01-upstream-behavior.md) | PG 18.3 CHECKPOINT + FPI mechanics, fully cited |
| [02-goopg-current-state.md](02-goopg-current-state.md) | Same matrix on the goopg side + heap-insert DML trace |
| [03-design.md](03-design.md) | Per-divergence fix designs D-1…D-7 + accepted deviations |
| [04-execution-plan.md](04-execution-plan.md) | Ordered implementation plan for the designs |
| [TODO.md](TODO.md) | Checkbox checklist |

## Out of scope

Restartpoints on a standby (goopg `standby.go`), online backup
FPW validation via `lastFpwDisableRecPtr` (xlog.c:8978, 9274 — relevant to
BASE_BACKUP, not to primary crash safety), wal_compression of images,
wal_consistency_checking.
