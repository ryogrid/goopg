# Design 0093-0002 — pgbench re-measurement target post-M0093

**Status:** draft (filed 2026-05-11).
**Milestone:** [M0093](../../milestones/0093-read-only-commit-skip-wal-emission.md).

## Goal

Define the re-measurement methodology, expected TPS lift,
and acceptance threshold for M0093-0003 (pgbench
re-measurement post-read-only-commit-skip).

## Baseline (today, 2026-05-11)

Per
`bench/pgbench-compare/results/20260511_goopg_select-only_m0092_followup_summary.md`:

| measurement | TPS |
|---|---:|
| M0092 baseline (commit `82d7acf`, re-measured today) | 317.47 |
| M0092 follow-up tip (commit `1916109`, 3-run average) | ~317 (range 283-342) |

CPU utilisation during the run: **0.17 %** (server-side).
WAL writer flushes: **19,684 per 60 s**, matching the
transaction rate.

## Expected lift

The dominant per-query cost is the synchronous
`walWriter.FlushUpTo(endLSN)` on every commit. With fsync
latency on the order of 100 µs and ~10 outstanding clients
serialised through the WAL writer, the per-query critical
path is roughly:

```
~100 µs fsync × per-client serialisation = ~ms latency floor
```

After M0093-0002 lands, read-only transactions skip the
fsync entirely. The new critical path is:

- Parse + plan + execute (~100-300 µs per query)
- Network round-trip (~10-100 µs LAN)
- Snapshot + transaction begin/end (~µs)

That puts the latency floor in the sub-ms range and TPS
ceiling well into the thousands range per client.

## Acceptance thresholds

- **Primary:** TPS ≥ 1,000 (M0091's original acceptance
  bar). This is the structural goal.
- **Secondary:** zero failed transactions.
- **Tertiary:** WAL writer flushes during a 60 s run drop
  from ~19,600 to < 100 (the background
  `wal_writer_delay` periodic flush rate is 5 / s ×
  60 s = 300; in a pure-select workload there's nothing
  for it to flush, so the actual number should be far
  lower).
- **Quaternary:** pgbench standard / simple-update TPS
  unchanged within noise vs the M0092 baseline (zero
  read-write path regression).

## Methodology

Use the existing harness in `/tmp/pgbench_goopg_m0092_followup.sh`
(or its successor `/tmp/pgbench_goopg_m0093.sh`):

1. Build the M0093 tip with `make build`.
2. Cold-start `bin/goopg start -D bench/pgbench-compare/goopg-data`.
3. Wait for ready probe (`SELECT 1`).
4. Run `pgbench -h 127.0.0.1 -p 5433 -U postgres -c 10 -j 10 -T 180 -P 10 -S postgres`.
5. Capture pprof during the 60-s window via
   `curl http://127.0.0.1:6060/debug/pprof/profile?seconds=30`.
6. Tail the server log for `walwriter flush` count.
7. Repeat 3 runs back-to-back. Report median TPS.
8. Run pgbench standard / simple-update once each to
   confirm no read-write regression.

Server config: identical to M0091 / M0092 summaries
(`shared_buffers=2560MB`, `wal_buffers=100MB`,
`checkpoint_timeout=24h`, `max_wal_size=1024GB`).
`track_io_timing` defaults off.

## Sanity checks

- Before declaring M0093 done: capture a CPU pprof. If the
  server is still at < 1 % CPU utilisation, the TPS lift
  is bounded by something else (network, pgbench client,
  Go runtime scheduling). Document that finding and file
  the next bottleneck for M0094.
- Run with `-c 1` and `-c 50` for one shot each to
  confirm the scaling shape matches the historical post-
  M0026 baseline (`3,224 TPS @ -c 1`, `6,403 TPS @ -c 4`,
  `5,900 TPS @ -c 16`). Significant divergence at any
  client count indicates a residual bottleneck we haven't
  diagnosed.

## Out-of-scope

- pgbench `-M prepared` measurement. Extended-protocol plan
  reuse is a separate optimisation (M0092-0008 deferral);
  measure it after that lands.
- TPC-H runtime impact. Read-only commit skip helps OLAP
  too (every Q1..Q22 is read-only) but the per-query cost
  there is dominated by the query engine, not commit
  fsync. Note any unexpected TPC-H improvement / regression
  in the M0093 closeout but don't gate acceptance on it.
