# Execution plan — CHECKPOINT/FPI parity follow-ups

Scope: implement the designs in [03-design.md](03-design.md). Nothing here is
started by this audit; this plan is the handoff artifact. Order chosen to
keep every step independently revertible and testable, highest
risk-adjusted value first.

## Phase 0 — Guardrails (no behavior change)

- [ ] Add characterization tests locking the current FPI contract:
  - first-touch-after-checkpoint emits exactly one XLOG_FPI per page epoch
    (exists in part: page_image_pg_test.go, replay_redo_start_test.go —
    extend with an explicit "checkpoint START publication" case);
  - fpw=off suppresses images (bufpool paths :2346/:2416/:2449);
  - restart re-seeds the pool epoch from pg_control (open.go:1257).
  Gate: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`.
- [ ] Snapshot current WAL-byte volume of a fixed pgbench run for later A/B
  of D-1/D-3 (FPI/record counts via pg_waldump-compatible decoder).

## Phase 1 — D-2 GUC liveness (MED-LOW)

- [ ] Extract `applyCheckpointGUCs(rt, registry)` from cmd/goopg/main.go:754-779.
- [ ] Call it from Server.reloadConfig (internal/postmaster/server.go:823).
- [ ] Make Checkpointer.Run honor live Interval changes (timer-reset or
  intervalChanged channel; checkpointer.go:482-538, SetInterval :431-441).
- [ ] Tests: reload flips each GUC; observable effects asserted.
  Risk: low. Rollback: revert wiring call.

## Phase 2 — D-1 idle-skip (LOW)

- [ ] Add idleSkipAnchor store at end of successful runCheckpoint
  (checkpointer.go:916-918 region).
- [ ] Skip timed ticks when WrittenLSN unchanged; keep FORCE/shutdown/
  volume paths unconditional (mirror xlog.c:7010-7019 exemptions).
- [ ] Log DEBUG "checkpoint skipped because system is idle".
- [ ] Tests per design; verify pg_stat_checkpointer.num_timed stays flat on
  an idle server.
  Risk: low-moderate (never skip when a checkpoint is needed — anchor only
  advances after success; failure path leaves anchor stale → next tick
  retries, matching upstream's retry-at-next-timeout).

## Phase 3 — D-4 single shutdown checkpoint (LOW)

- [ ] Remove OnStop CheckpointNow (server.go:731-739); keep log line noting
  Close will checkpoint.
- [ ] Re-run stop/start matrix: graceful stop → exactly ONE
  XLOG_CHECKPOINT_SHUTDOWN; immediate stop → zero checkpoints,
  DB_IN_PRODUCTION preserved (open.go:3364-3394).
- [ ] Update M0089-0003 references in comments/docs.

## Phase 4 — D-3 FPW_CHANGE record (LOW; depends on Phase 1)

- [ ] Encoder in xlog/pg_assembled_emit.go + emission from reload path with
  upstream ordering (ON: flip→log; OFF: log→flip; under fpiPublishMu).
- [ ] Decoder/replay already tolerates opcode (pg_xlog_decode.go:209,
  recovery.go:2457-2467) — add a replay test that a standby-recovered
  cluster reflects the toggled value.
- [ ] Defer lastFpwDisableRecPtr + BASE_BACKUP validation to backup-parity work.

## Phase 5 — Optional hygiene

- [ ] D-6 smgr ReleaseForgotten hook (03-design.md §D-6) — only on fd-pressure evidence.
- [ ] D-7 delayChkpt quiesce — prefer documenting as accepted deviation in
  the deferral ledger unless review demands it.
- [ ] D-5 keep as documented deviation; revisit only if TPC-H sweep shows
  trigger-timing skew vs PG reference.

## Verification gates (per repo rules)

- Unit/component: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
  (no `-count=1`).
- Planner/executor untouched except executor checkpoint op path → still run
  `scripts/tpch-spotcheck.sh` if any bufpool/checkpointer change lands.
- TPC-DS SF0.5 gate `scripts/tpcds-sf05-regression.sh sweep` after Phases
  1-4 batch.
- Crash-consistency spot check for any phase touching the checkpoint cycle:
  start server, write rows, SIGKILL mid-load, restart, assert row counts —
  plus one kill during a forced checkpoint window.
