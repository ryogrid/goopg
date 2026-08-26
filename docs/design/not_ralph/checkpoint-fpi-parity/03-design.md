# Fix designs for found divergences

The core FPI contract needs NO changes (see README verdicts #1-#7, #15, #16).
This file designs minimal fixes for divergences D-1…D-7
(02-goopg-current-state.md §7) and records deviations we recommend accepting.

Guiding constraints: vanilla-PG compatibility is absolute but changes must be
incremental and independently revertible; nothing here may alter the
durability ordering already proven correct (publish-redo → flush → fdatasync
→ record → pg_control).

---

## D-1 — Idle-skip of timed checkpoints (severity LOW)

Problem: `Run`'s ticker fires `runCheckpoint` unconditionally
(checkpointer.go:522-526), so an idle server writes a fresh checkpoint
record + pg_control update every `checkpoint_timeout`. Upstream skips when
nothing important happened since the last checkpoint
(xlog.c:7005-7019). Effects: WAL growth while idle, `num_timed`
inflation vs oracle, pointless fsync churn.

Design:
1. Track "important WAL since last checkpoint": the checkpointer already
   holds `lastCheckpointLSN`/volume anchor. Add a cheap predicate — sample
   `vr.WrittenLSN()` at tick time and compare against the written position
   recorded when the previous checkpoint completed (new atomic field,
   e.g. `idleSkipAnchor atomic.Uint64`, stored at the end of each successful
   `runCheckpoint` next to `lastCheckpointLSN.Store`, checkpointer.go:916-918).
2. In the ticker case only (NOT volume-triggered, NOT CheckpointNow, NOT
   CheckpointShutdown — upstream exempts FORCE/IS_SHUTDOWN too, xlog.c:7010-7011),
   if `WrittenLSN() == idleSkipAnchor` → skip, optionally slog.Debug the
   upstream phrase "checkpoint skipped because system is idle".
3. Caveat: upstream compares against the last checkpoint RECORD location and
   ignores unimportant records (XLOG_MARK_UNIMPORTANT). goopg has no
   unimportant-flag concept; using raw WrittenLSN is strictly conservative
   (skips less often than upstream) — acceptable first cut; refine later by
   classifying RUNNING_XACTS/FPW_CHANGE-only intervals as skippable if
   benchmark evidence demands.
4. Tests: unit — idle server ticks N times, zero checkpoint payloads appended;
   activity between ticks forces one; CheckpointNow during idleness still
   runs and re-arms the anchor.

## D-2 — GUC liveness: apply SIGHUP values without restart (MED-LOW)

Problem: checkpoint_timeout, max_wal_size, checkpoint_completion_target,
full_page_writes are read from the registry once at startup
(main.go:754-779) and `Server.reloadConfig` (server.go:823) does not push
updates anywhere. All four are PGC_SIGHUP upstream (assign hooks run live).

Design:
1. Extract the existing startup wiring block (main.go:754-779) into a
   function `applyCheckpointGUCs(rt, registry)` callable from both startup
   and the reload path.
2. Wire `Server.reloadConfig` (server.go:823) to call it after refreshing
   the registry from postgresql.conf. All four setters already exist and are
   reload-tolerant except `SetInterval`, whose doc admits the armed ticker
   won't pick up changes mid-Run (checkpointer.go:431-441). Extend Run to
   rebuild its ticker when `c.cfg.Interval` changes: either
   * replace `time.NewTicker` with a timer reset at each fire from
     `c.cfg.Interval` (minimal diff), or
   * add an `intervalChanged chan struct{}` that SetInterval signals and the
     select in Run (:518-537) consumes to recreate the ticker.
   Option B keeps timing exactness; option A is fewer lines. Either way the
   volume ticker is unaffected.
3. `SetFullPageWrites` (bufpool.go:994) is a plain atomic store — safe to
   call from the reload goroutine; document that goopg, like upstream, makes
   ON effective immediately and OFF effective for records assembled after
   the store (the fpiPublishMu barrier gives the same decision/append
   atomicity upstream gets from insert locks).
4. Tests: component test flipping each GUC via the reload path and asserting
   observable effect (interval shortened → next timed checkpoint sooner;
   fpw=off → MarkDirtyLogicalChange stops embedding images).

## D-3 — Emit XLOG_FPW_CHANGE on runtime FPW toggle (LOW)

Problem: upstream logs `XLOG_FPW_CHANGE(on/off)` whenever wal_level permits
(xlog.c:8253-8259) so standbys and backup tooling can detect FPW-disabled
WAL spans (lastFpwDisableRecPtr, xlog.c:8621-8642, consumed at :8978/:9274).
goopg only decodes the opcode (pg_xlog_decode.go:209) and never emits it;
the live FPW value reaches standbys only via checkpoint copies
(checkpointer.go:943-948).

Design (only worth doing together with D-2 — otherwise there is no runtime
toggle to log):
1. In the reload path (after step D-2.3), if the FPW value actually changed:
   encode + append an RM_XLOG FPW_CHANGE record (reuse the parameter-change
   encoder family in internal/access/transam/xlog/pg_assembled_emit.go —
   same pattern as EncodeRunningXactsPG; opcode single XLOG_FPW_CHANGE opcode 0x80 (pg_control.h:76; 0x40 is XLOG_SWITCH) already known
   to the decoder).
2. Ordering follows upstream: ON → flip shared flag first, then append
   record; OFF → append record first, then flip (xlog.c:8233-8267 rationale).
   With fpiPublishMu held across the flip+append this is trivially atomic
   w.r.t. FPI decisions.
3. Optional companion: track `lastFpwDisableRecPtr` in recovery replay
   (recovery.go already walks FPW_CHANGE as a no-op at :2459-2467) and
   surface it to BASE_BACKUP validation. Defer until backup parity work;
   primary crash safety never needs it.

## D-4 — Collapse the double shutdown checkpoint (LOW)

Problem: graceful STOP runs `CheckpointNow` (OnStop, server.go:731-739,
stays DB_IN_PRODUCTION) followed by Runtime.Close's `CheckpointShutdown`
(open.go:3379-3394, DB_SHUTDOWNED) — two full flush+fsync cycles and two
checkpoint records where upstream performs exactly one IS_SHUTDOWN
checkpoint (xlog.c:6640-6680).

Design options:
a. **Remove the OnStop checkpoint** and rely solely on Close's shutdown
   checkpoint. Risk: the window comment at open.go:3385-3386 exists because
   a crash between OnStop and Close must still look unclean — but that
   property is preserved WITHOUT the extra checkpoint, since
   DB_SHUTDOWNED is only stamped by the final one. Verify no caller depends
   on OnStop having flushed (M0089-0003 history says it was added so users
   needn't chain `goopg checkpoint && goopg stop`; Close now provides that).
b. Keep OnStop but make it a no-op when teardown is imminent (plumb a flag).
Recommendation: (a), plus a regression test asserting exactly ONE
CHECKPOINT_SHUTDOWN record per graceful stop (scan the tail via
pg_waldump-compatible decoder).

## D-5 — Volume trigger evaluation point (LOW, accept-or-refine)

Problem: upstream evaluates XLogCheckpointNeeded only when opening a NEW WAL
segment (xlog.c:2491-2504); goopg polls WrittenLSN once per second
(checkpointer.go:497-499, 583-613) and compensates with the
`elapsedSegmentsNeeded` floor (:615-631). Post-M0131-S30.4 the distance math
matches; residual differences are sub-segment timing jitter and the
documented floor behavior at tiny max_wal_size.

Recommendation: ACCEPT as documented deviation (comment already cites the
upstream lines). If exactness ever matters, move the check into the WAL
writer's segment-open path via a callback instead of polling; do not attempt
to emulate segment-boundary timing with a poller.

## D-6 — smgr close phase after checkpoint (LOW)

Problem: upstream frees all smgr relations after every checkpoint
(`smgrdestroyall`, checkpointer.c:488-490) so dropped relations release fds;
SyncPostCheckpoint (sync.c:202, called xlog.c:7342) forgets doomed segments.
goopg's storage manager releases lazily (eviction/Close); long-running
servers accumulate fds for dropped/deleted relations until eviction pressure.

Design: add a `PostReleaseFn func() error` hook beside PostCheckpointFn
(checkpointer.go:184-192) wired to a new `Manager.ReleaseForgotten()` that
closes fds for rels with zero live pins and removes pending-unlink files.
Call it immediately after `SyncAllDataFiles` (:817-821) so ordering matches
upstream's "sync, then close". Non-fatal errors logged like sibling hooks.
Low urgency; do only if fd-exhaustion or unlink-latency shows up in soak
runs.

## D-7 — delayChkpt quiesce windows (LOW-MED theoretical)

Problem: upstream waits out transactions in commit critical sections before
(:7197-7215) and after (:7219-7232) CheckPointGuts, so clog updates of
commits straddling the redo point are guaranteed flushed by THIS checkpoint.
goopg has no delayChkpt concept; today's safety net is different but sound:
the checkpoint's own FlushCLOGFn drains clog dirty pages (:806-810), and the
retainer refuses to recycle the `(redo, record]` window
(:1000-1008 C2-S3 MUST-FIX), so replay-from-redo can always reconstruct
acked commits.

Design (only if adversarial review demands upstream-shaped guarantees):
introduce a process-wide `DelayChkptStart/Complete` counter incremented
around the clog-update + WAL-insert section of commit (mirror
RecordTransactionCommit), and have runCheckpoint spin-wait (10 ms sleeps,
like upstream) until zero while absorbing WAL flush work. This adds
commit-path atomics; weigh against the existing retention-based argument.
Default recommendation: DOCUMENT as accepted structural deviation in the
deferral ledger instead of implementing.

---

## Accepted deviations (no action)

* Extra FPIs after restart/eviction-edge due to nativeImageLSN keying
  (bufpool.go:1087-1095) — correctness-safe, WAL-volume-only, self-heals.
* No `checkpoint_warning` / "too frequently" log (checkpointer.c:444-456).
* No `log_checkpoints` GUC — goopg always logs start/complete at Info
  (checkpointer.go:725, 1032-1037). Cosmetic; consider gating behind the GUC
  when adding it to defaults.go.
* No `pg_checkpoint` role privilege gate on the SQL verb
  (utility.c:945-953) — belongs to a privileges audit, not this one.
* bgwriter-less periodic RUNNING_XACTS stream (bgwriter.c:292) — only
  relevant to hot-standby snapshot latency, covered by checkpoint-time
  records today.
