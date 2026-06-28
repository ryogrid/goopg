Task: (loop #8 assessment) — autonomous priority band is EXHAUSTED of
safely-completable work. No code change this loop; reporting BLOCKED with a
clean hand-off, ledger row appended.

State of the priority band (M0117 → M0118, per the 2026-06-20 directive):
- M0118: effectively COMPLETE. Isolation tally 120 pass / 1 failed; the lone
  `deadlock-parallel` needs a parallel-query lock-group abstraction goopg has no
  foundation for (infeasible). M0118-0002 group closed 2026-06-29.
- M0117 remaining (0006 Part B/C, 0007 Part B, 0008 Part B): ALL are
  dedicated-full-gate-session Effort-L/M tasks. Blueprint for 0006 Part B is
  complete (design 0117-0006 §"Part B implementation blueprint", loop #7).
- M0110: PAUSED until M0117+M0118 complete (M0117 not complete → stays paused).
- M0095-0003: needs logical decoding (not implemented). deadlock-parallel:
  parallel-query lock groups (not implemented).

Why no swap this loop (the decisive new finding):
- A populated 2.2 G TPC-H data dir EXISTS (bench/tpch/runtime_goopg/data,
  Q12=2/Q13=33 pinned) BUT the spotcheck infra-fails on it — startup SLRU fsync
  backfill exceeds 60s on WSL2 (memory tpch_spotcheck_slru_backfill_startup_hang.md).
  So the mandatory populated-data TPC-H gate cannot run here.
- M0117-0006 Part B is a live CLOG store swap = the project's highest-blast-radius
  change (Hard-won Rule #1). Landing it without the mandatory standby-E2E +
  populated-TPC-H gates is reckless. Loop #7 deliberately deferred for this reason;
  loop #8 found nothing that changes that.

Gates run this loop: `go build ./...` PASS; `go test ./internal/mvcc/ -run
'CLOG|Clog|BufferPool'` PASS; make ralph-state-guard (see status block).

Next step (HUMAN dedicated session): run M0117-0006 Part B per the blueprint
§Resolutions 1-7, gating with race mvcc/wal + xlog_replay + heterogeneous
PG-standby E2E + fresh-server TPC-H Q12/Q13 on populated data + pgbench smoke.
M0117-0007 Part B (async commit) joins at blueprint §2 (pool.flushWAL =
wal.Writer.FlushUpTo). Alternatively the human may unpause M0110 (pg_dump TAP,
incremental/self-promoting) or re-prioritize.
