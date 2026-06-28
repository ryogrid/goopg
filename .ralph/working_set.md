Task: M0117-0006 Part B (live CLOG store swap) — BLOCKED on dedicated full-gate
session. This loop delivered the code-grounded Part B implementation blueprint
(docs/design/0117-0006-clog-slru-buffer-pool.md, new section) instead of forcing
the swap, per Hard-won Rule #1 (silent CLOG visibility/durability regression is
the project's most expensive failure mode).

Findings (grounded in code read this loop):
- `clogBufferPool` (clog_bufferpool.go) is complete + standalone, NO caller. Pool
  is SLRU-segment-backed, has getStatus/setStatus/setStatusWithLSN/flushDirty +
  group-LSN + injectable flushWAL barrier. Encoding proven byte-equivalent.
- Live store = `banks` (clog.go); durability = M0117-0005 group-commit leader
  `applyGroupBatchLocked` → flushDirtyPagesLocked (flat file) +
  mirrorGroupToSLRULocked (SLRU, OR semantics, reads banks). GetStatus reads banks.
- Production ALWAYS sets slruDir (initdb.Open + initdb both EnablePGSLRUMirror),
  so the dual-path (pool when slruDir set, banks for no-mirror unit tests) is
  clean — see blueprint §1.
- THE risk: OR (mirror, M0117-0004 durability invariant) vs clear-then-set (pool).
  Resolution in blueprint §3: single store ⇒ clear-then-set correct, but the
  loadFromSLRU/MarkUnknownAsAborted repair path must be re-verified on the pool.

Next step (dedicated session): implement the swap per blueprint §Resolutions 1-7;
gate with `go test -race ./internal/mvcc/... ./internal/wal/...` + xlog_replay +
heterogeneous PG-standby E2E + fresh-server TPC-H Q12/Q13 on a populated data dir
+ pgbench smoke. M0117-0007 Part B (async commit) joins at blueprint §2
(pool.flushWAL = wal.Writer.FlushUpTo).

Alternative if human unpauses M0110: M0110-0001 pg_dump TAP advances one catalog
gap at a time (safe, incremental, self-promoting guard) — but currently PAUSED
per the 2026-06-20 directive (resume only after M0117+M0118 complete).

Gates run this loop: doc-only change; `go test ./internal/mvcc/...` PASS;
make ralph-state-guard (see status block).
