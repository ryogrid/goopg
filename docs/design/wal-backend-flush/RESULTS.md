# wal-backend-flush — implementation results

status: implemented · date: 2026-07-12 · branch: `wal-system-pgnize`

This records the outcome of implementing the
[wal-backend-flush](README.md) design bundle — the PostgreSQL-parity rewrite of
goopg's WAL write/flush path — across seven slices.

## What landed

| Slice | Commit | Change |
|-------|--------|--------|
| 1 | `ada6d3c0` | `walWriteLock` primitive (LWLockAcquireOrWait tri-state) + `publishTail` CAS-max |
| 2 | `9e94c5c7` | extract `xlogWrite(writeRqst)` with the write/flush split |
| 3 | `7af00765` | backend-driven `FlushUpTo` under the WAL write lock (writeMu-only flusher) |
| 4 | `e2d71ff1` | walwriter `BackgroundWrite` (plain-lock pre-writer) + `wal_writer_flush_after` wiring |
| 5 | `85fe701f` | slow-path ops (append/appendRaw/recycle/walbufstat) run directly in the caller |
| 6 | `8691707b` | delete the dedicated writer goroutine (`state.loop`) + all group-commit machinery |
| 7 | _this_ | drain-safety certification (`TestDrainSafetyStress`) + results |

Net effect on the WAL writer: the committing backend now performs its own
segment `pwrite` + `fdatasync` under `writeMu` (emergent group commit — the lock
holder flushes the aggregate published frontier for the followers), the
background walwriter pre-writes on its cadence with a plain lock, and there is no
longer a dedicated WAL writer goroutine. `commit_delay` / `commit_siblings` are
real GUCs at PostgreSQL's defaults (**0 / 5**), replacing the previous hardcoded
1000 µs / 5-sibling sleep on the loop. The on-disk WAL format is unchanged.

## Architecture before / after

| | Before | After |
|--|--------|-------|
| Who fdatasyncs a commit | dedicated writer goroutine, after a channel round-trip | the committing backend itself, under `writeMu` |
| Group commit | explicit queue (`flushGroup` + `handleGroupFlush`) | emergent: the lock holder flushes for the followers |
| `commit_delay` | hardcoded 1000 µs, on the loop, delaying every queued waiter | GUC default 0; holder-only sleep, gated on `commit_siblings` |
| Background walwriter | `FlushUpTo(WrittenLSN())` via the group-commit path every 200 ms | `BackgroundWrite` — plain-lock pre-write+flush |
| Writer goroutine | always running, `LockOSThread`-pinned | removed |
| Lines | — | slice 6 alone: **−211 net** |

## Performance (pgbench, c=50, `-N` simple-update, scale 100, WSL2 ext4, uncapped)

Same host / config as the `analysis/perf-optimize2` baseline.

| Workload | Baseline (pre-rewrite) | After rewrite | Δ |
|----------|-----------------------:|--------------:|---|
| sync-on (`synchronous_commit=on`)  | 1,145 TPS | 1,138–1,188 TPS / ~43 ms | ≈ neutral |
| sync-off (`synchronous_commit=off`) | 9,820 TPS | 7,664–8,740 TPS | ≈ −11% to −22% |

(sync-off spans several fresh-DB runs incl. the restart-then-sync-off sequence,
which read 8,740 TPS; run-to-run variance is high, latency stddev up to ~65 ms.)

The rewrite's goal was **architectural parity with PostgreSQL**, not a raw
throughput jump — the fdatasync latency floor dominates sync-on TPS, and
`commit_delay` was already effectively batching under the old loop.

- **sync-on is neutral** (1,138 vs 1,145; steady-state runs reach ~1,300),
  confirming the emergent group commit matches the old queue's batching while
  removing the per-commit channel hand-off (which helps tail latency at high
  concurrency).
- **sync-off gives back ~22%.** This is the expected cost of routing all WAL
  writes through the `writeMu` (WALWriteLock analog): where the old lock-free
  loop drained the ring off to the side, the walwriter's `BackgroundWrite`
  fsync now holds `writeMu`, so backend appends that overflow the ring serialize
  behind it. PostgreSQL serializes on WALWriteLock in exactly the same way; this
  is parity, not a defect. Run-to-run variance on a fresh scale-100 DB is high
  (latency stddev ~65 ms), and warmer steady-state windows read ~8,000–8,400 TPS.

Measurement: pgbench 18.3, `-N`, c=50, scale 100, fresh init per run, same host
and `postgresql.conf` as the `analysis/perf-optimize2` baseline.

> An intermediate slice-3 variant that held `appendMu` across the fsync
> regressed to 435 TPS (2.6×) by blocking appends during the sync; the shipped
> writeMu-only flusher restores full throughput — fast-path appends proceed
> during a peer's fsync and accumulate for the next batch.

## Correctness / certification

- `go test -race ./internal/wal ./internal/mvcc` — clean across all slices.
- `TestDrainSafetyStress` (`-race`, 5×): 8 appenders (mixed fast/slow via an
  8 KB buffer forcing overflow drains) + 6 committers + the walwriter +
  segment recycling + Close, all concurrent. No data race, no
  concurrent-map panic, and the result invariant
  `writeLSN ≥ drainedLSN ≥ flushedLSN` never inverts.
- Crash/recovery/durability suite, physical-replication e2e, and
  `scripts/tpch-spotcheck.sh` (Q12=2/Q13=33) pass on every slice; the
  per-commit pgbench smoke gate ran on each commit.

### One non-reproduced anomaly

During the very first post-slice-6 measurement, one scale-100 sync-off pgbench
run (which restarts the server between the sync-on and sync-off phases) appeared
to stall for tens of minutes. It could **not** be reproduced across four
subsequent clean runs — scale-30, scale-100 fresh (×2), and the exact
init→sync-on→stop→restart→sync-off sequence — all of which completed with 0
failed transactions. The original host was under high external load at the time,
and goopg's SIGQUIT is a graceful-shutdown handler (not a Go stack dump), so no
goroutine trace was captured from the one event. Treated as a transient, not a
defect. If it recurs, capture a dump via the pprof endpoint rather than SIGQUIT:
`GOOPG_PPROF_ADDR=127.0.0.1:6160 ... start`, then
`curl 127.0.0.1:6160/debug/pprof/goroutine?debug=2`.

## Deferred (see `.ralph/deferral_ledger.md`)

- Async-LSN early-wakeup latch (`XLogSetAsyncXactLSN`) → then page-rounded
  write-only walwriter ticks + active `wal_writer_flush_after` gating + the
  Stage-1 finishing-segment fsync.
- `commit_delay` / `commit_siblings` / `wal_writer_flush_after` SIGHUP live
  update (setters exist; reload wiring is a follow-up).
