# TODO — checkpoint-fpi-parity follow-ups

Audit verdict: core FPI contract MATCHES upstream; fix list is peripheral.
Designs: 03-design.md · Plan: 04-execution-plan.md

## Core contract (verification-only — no changes needed)

- [ ] Verify first-touch-after-checkpoint-start emits XLOG_FPI (trace:
      operators_storage.go:9718 → bufpool.go:2405-2425 → open.go:470-484)
- [ ] Verify PublishRedoBarrier fires BEFORE flushDirty (checkpointer.go:784-801)
- [ ] Verify full_page_writes=off suppresses all three emit paths
- [ ] Verify shutdown checkpoint stamps DB_SHUTDOWNED (open.go:3379-3394)
- [ ] Verify recovery replays from CheckPointCopyRedo incl. image arms
      (recovery_state.go:75-116, recovery.go:2425-2453)

## Fixes

### D-1 Idle-skip timed checkpoints (LOW) — xlog.c:7005-7019
- [ ] idleSkipAnchor stored after each successful checkpoint
- [ ] ticker-case skip + DEBUG log; FORCE/volume/shutdown exempt
- [ ] unit: N idle ticks → 0 checkpoint payloads; activity → runs

### D-2 GUC liveness w/o restart (MED-LOW) — main.go:754-779 frozen
- [ ] extract applyCheckpointGUCs(); call from reloadConfig (server.go:823)
- [ ] Run() honors live Interval change (checkpointer.go:431-441, 484)
- [ ] component test: reload shortens timeout; fpw toggle stops images

### D-3 Emit XLOG_FPW_CHANGE on runtime toggle (LOW; after D-2)
- [ ] encoder in pg_assembled_emit.go (RM_XLOG, opcode 0x80 family)
- [ ] ordering ON=flip→log / OFF=log→flip under fpiPublishMu (xlog.c:8233-8267)
- [ ] replay test: recovered cluster sees toggled value
- [ ] defer lastFpwDisableRecPtr + BASE_BACKUP validation (backup parity)

### D-4 Single shutdown checkpoint on graceful STOP (LOW) — server.go:731-739
- [ ] drop OnStop CheckpointNow; rely on Runtime.Close CheckpointShutdown
- [ ] test: graceful stop ⇒ exactly one SHUTDOWN record; immediate ⇒ zero

### Accepted deviations (document only)
- [x] D-5 poll-based volume trigger + floor (checkpointer.go:583-631)
- [x] D-6 (implemented: Manager.ReleaseForgotten closes cached handles for
      relations whose files vanished; wired as Checkpointer PostReleaseFn
      right after the data-file sync phase, count logged when >0) smgr close-all hook — only on fd-pressure evidence
- [x] D-7 (ACCEPTED as documented structural deviation per 03-design:
      redo-anchored retention guarantees replayability without adding
      commit-path delayChkpt atomics) delayChkpt quiesce — ledger note instead of implementation
- [x] extra FPIs post-restart/eviction (bufpool.go:1087-1095) — safe direction
- [ ] no checkpoint_warning log; no log_checkpoints GUC; no pg_checkpoint
      role gate — route to respective audits

## Gates before closing each phase
- [ ] RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh (no -count=1)
- [ ] scripts/tpch-spotcheck.sh (if bufpool/checkpointer touched)
- [ ] scripts/tpcds-sf05-regression.sh sweep (after Phases 1-4)
- [ ] crash-consistency: SIGKILL mid-write and mid-checkpoint, restart, row counts
