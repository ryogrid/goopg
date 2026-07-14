# 06 — Performance model and observability

status: draft · date: 2026-07-12

Baseline (analysis/perf-optimize2, 2026-07-12, WSL2 ext4, uncapped, scale 100):
goopg 1,145 TPS sync-on / 9,820 sync-off; PG 15,556 TPS with 77.8 %
`LWLock:WALWrite` wait share; goopg batch width ≈8.9 txns/fdatasync;
c=1 ≈175 TPS (1 fsync/commit).

## 6.1 What improves

- **Per-commit fixed cost** — removes, on *every* commit: the `fg.mu`
  lock/unlock, the signal-channel send, the `req.done` park + unpark, and the
  loop-goroutine scheduling latency. Each is µs-scale, but they sit on the
  critical path and worst-case under load; the win shows primarily in **p99 at
  c≥8**, not necessarily mean TPS.
- **Head-of-line blocking eliminated** — today a 1000 µs `commit_delay` sleep
  on the loop delays every queued waiter (including LSNs requested *before*
  the sleep) and every subsequent WAL op. With the GUC defaulting to **0**
  there is no deliberate delay at all; if an operator sets it, only the holder
  and its lock-waiters pay — and the waiters are exactly the beneficiaries of
  the widened batch.
- **Pipeline overlap** — a committer arriving during an in-flight fsync
  becomes the next holder the instant `release()` runs; no round-trip through
  a loop iteration.
- **walwriter pre-write** keeps the ring drained to the OS cache, so the
  committing holder's Stage-1 pwrite volume shrinks → shorter `writeMu` hold →
  wider effective batches under load.
- One dedicated `LockOSThread`'d OS thread freed.

## 6.2 What does NOT improve (honest expectations)

- **c=1 is unchanged**: one commit = one fdatasync; the raw WSL2/ext4 fsync
  latency is untouched. Expect ~175 TPS still.
- **sync-off throughput** (9,820) is append-path-bound and already
  backend-parallel; expect at most a small gain (the walwriter no longer
  forces a full group fsync every 200 ms; overflow drains lose the loop
  round-trip).
- **No PG-parity promise.** The 1,145 → 15,556 sync-on gap will close only
  partially from this restructure; the honest target is a large p99 reduction
  plus a batching-efficiency TPS gain at c=8–50, with the fsync floor
  unchanged. The slice-3/4/7 pgbench matrices set the real numbers.
- The write path retains goopg-specific costs PG doesn't have (Go scheduler,
  ring-drain memcpy) that this bundle does not address.

## 6.3 GUCs (all PG-default-faithful)

| GUC | default | status | notes |
|---|---|---|---|
| `commit_delay` | **0** (µs — no delay) | **new** | PG default (`guc_tables.c`). Replaces the hardcoded `commitDelayUs=1000` from M0099-0003; that tuned value was a stopgap for the queue mechanism. Sighup-settable like PG. |
| `commit_siblings` | 5 | **new** | PG default; gate = in-flight `flushWaiters` count (the `MinimumActiveBackends` analog) |
| `wal_writer_flush_after` | 1 MB | **existing, inert → wired** | already registered (`defaults.go`, BootVal 1048576) and in the sample file, but has no behavioral reader today; slice 4 wires it into the walwriter fsync throttle |
| `wal_writer_delay` | 200 ms | existing | unchanged |

The two new GUCs follow the sample-file discipline: `BuildDefaultRegistry` +
`postgresql.conf.sample` in the same commit, `TestSampleConfigCoversRegistry`
green (implementation slice 3).

An explicit pgbench matrix comparing `commit_delay ∈ {0, 1000}` under the
emergent model is part of slice-4 acceptance — to document the
latency-vs-batch-width trade for operators, matching what PG's docs say about
the knob rather than baking a non-PG default in.

## 6.4 Observability changes

- **Wait events become truthful**: `OnWALWrite` (around the pwrite) and
  `OnWALSync` (around the flush wait) now fire on the goroutine actually
  performing/awaiting the I/O — pg_stat_activity shows `IO:WALWrite`/
  `IO:WALSync` on the *committing backend*, like PG, instead of the shared
  `walProcNum` background slot workaround. No plumbing change needed (the
  hooks already run in caller context; the I/O just moves under them).
- **fsync-time attribution** (`track_io_timing`): keep whole-call attribution
  in v1 (matches today's "wall time inside FlushUpTo" semantics); holder-only
  fsync-time split is a noted follow-up.
- Existing counters (`fsyncCount`, walBufferCounters) unchanged; the
  fsyncCount/TPS ratio becomes the standing batch-width health metric
  (baseline ≈8.9 at c=50).
- A new `wal_buffers_full`-style counter for the overflow write-only drain
  (PG's `pgWalUsage.wal_buffers_full`) is recommended in slice 5 —
  observability parity for the eviction path.
