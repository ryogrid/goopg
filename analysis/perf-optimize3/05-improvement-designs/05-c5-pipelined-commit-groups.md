# C5 — Pipelined commit groups (sketch)

status: sketch → **PROMOTED** · date: 2026-07-13 · base: `e453e3f2` · **gate: measure the
residual serialization AFTER C1 + C2 land — do not design further before
that.**

> **Promotion note (2026-07-14):** the decision gate below is satisfied (06/07
> block profiles show 59–66 % of `-N` block delay under
> `walWriteLock.acquireOrWait`). Idea item 2 (drain/fsync split) is now a full
> design at
> [`../../perf-optimize3-dash/08-improvement-designs/01-c5-drain-fsync-split.md`](../../perf-optimize3-dash/08-improvement-designs/01-c5-drain-fsync-split.md).
> Idea item 1 (CLOG staging overlap) remains obsoleted by C2.

## Signal

The `-N` block profile (`../01-results.md`) shows commit waits split across
exactly two serialized points: 43.2 % `runtime.selectgo` (backends parked in
`walWriteLock.acquireOrWait` — WAL flush cycles) and 32.8 %
`sync.(*Mutex).Lock` (the CLOG buffer-pool mutex held across the commit-path
fsync). C2 removes the second entirely. What remains after C1+C2 is the WAL
cycle itself.

## Idea

PostgreSQL overlaps group *formation* with the in-flight flush for free:
`LWLockAcquireOrWait` lets the next leader assemble while the previous fsync
runs, and per-bank SLRU locks decouple CLOG updates. goopg's
wal-backend-flush rewrite already provides the first half (losers park; new
appends proceed during a peer's fsync — writeMu-only flusher). The residual
pipelining opportunities, if measurement still shows them:

1. **CLOG staging overlap** (mostly obsoleted by C2): stage status-bit writes
   for group N+1 while N's write-back runs.
2. **Drain/fsync split**: the writeMu holder currently drains (pwrite) and
   fsyncs under one lock hold; a two-phase hand-off (drain under writeMu,
   fsync outside it with an LSN-ordered completion queue) would let the next
   group's drain start during the previous fsync. This is a substantial
   concurrency redesign of `xlogWrite` — only worth it if post-C1/C2 profiles
   still show long convoy waits on `acquireOrWait`.

## Decision gate

After C1-S5a and C2-S3 land, re-capture the `-N` block profile
(`GOOPG_BLOCK_PROFILE_RATE=1`, `run_rw50.sh`). If `selectgo` under
`CommitTransaction` still dominates block delay AND the emergent width remains
far below PG's (~19), write the full design for (2); otherwise close C5 as
subsumed.
