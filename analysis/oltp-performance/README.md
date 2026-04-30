# OLTP Performance Analysis

## Executive Summary

goopg's OLTP (short-transaction) throughput was measured using
`pgbench` with three workload profiles at scale factor 3.
The key finding is a **400× throughput gap** between read-only
(select-only) and write workloads:

| Workload | Clients | TPS | Latency/trans (ms) |
|----------|---------|-----|-------------------|
| Select-only | 1 | 3 158 | 0.3 |
| Select-only | 4 | 6 454 | 0.6 |
| Select-only | 16 | 6 014 | 2.7 |
| Simple update (-N) | 1 | 13.5 | 74 |
| Simple update (-N) | 4 | 13.8 | 289 |
| Simple update (-N) | 16 | 13.7 | 1 167 |
| Default TPC-B | 1 | 13.2 | 76 |
| Default TPC-B | 4 | 13.5 | 295 |
| Default TPC-B | 16 | 14.0 | 1 145 |

**TPS does not scale with client count for write workloads** —
throughput stays at ~14 TPS regardless of concurrency, indicating
a serial bottleneck in the write path.

## Per-Statement Breakdown

`pgbench -r` (per-statement timing) pinpoints the dominant cost:

### Default TPC-B (scale=3, 1 client)

| Statement | Latency (ms) | Share |
|-----------|-------------|-------|
| `UPDATE pgbench_accounts SET abalance = abalance + :delta WHERE aid = :aid` | **73.9** | **97.7 %** |
| `SELECT abalance FROM pgbench_accounts WHERE aid = :aid` | 0.4 | 0.5 % |
| `UPDATE pgbench_tellers SET tbalance = tbalance + :delta WHERE tid = :tid` | 0.3 | 0.4 % |
| `UPDATE pgbench_branches SET bbalance = bbalance + :delta WHERE bid = :bid` | 0.3 | 0.4 % |
| `INSERT INTO pgbench_history ...` | 0.3 | 0.4 % |
| `BEGIN` / `END` | 0.2 / 0.2 | 0.3 % / 0.3 % |
| **Total** | **75.6** | **100 %** |

### Simple Update (-N, scale=3, 1 client)

| Statement | Latency (ms) | Share |
|-----------|-------------|-------|
| `UPDATE pgbench_accounts SET abalance = abalance + :delta WHERE aid = :aid` | **70.4** | **98.5 %** |
| `SELECT abalance FROM pgbench_accounts WHERE aid = :aid` | 0.3 | 0.5 % |
| `INSERT INTO pgbench_history ...` | 0.3 | 0.4 % |
| `BEGIN` / `END` | 0.2 / 0.2 | 0.3 % / 0.3 % |
| **Total** | **71.4** | **100 %** |

**`UPDATE pgbench_accounts` consumes 97–98 % of every transaction.**
All other statements together take under 2 ms.

## UPDATE pgbench_accounts Code Path

The `UPDATE pgbench_accounts SET abalance = abalance + :delta WHERE aid = :aid`
query goes through:

1. **Parser** — parse the SQL text
2. **Analyzer** — resolve columns, types
3. **Planner** — plan index scan + heap update
4. **Executor** — execute the plan:

   a. **Index scan** — B-tree search for `aid = :aid` (3–4 page reads)
   b. **Heap fetch** — read the heap page containing the tuple
   c. **Write new tuple** — mark old tuple dead, insert new tuple version
   d. **WAL logging** — emit B-tree + heap WAL records
   e. **WAL flush** — `FlushUpTo` + `fdatasync` (the durability barrier)

Step 4e (WAL flush / fdatasync) is the prime suspect for the ~70 ms
latency. An `fdatasync` call on a standard SSD typically takes 1–10 ms.
Repeated for each UPDATE, this alone could account for most of the
observed latency.

## Wait-Event Observations

`pg_catalog.pg_stat_activity` was polled every 5 seconds during a
`pgbench -N` run. The `wait_event_type` and `wait_event` columns
consistently returned empty (non-blocking state). Possible explanations:

- The WAL `fdatasync` call completes quickly (~1 ms) but total
  latency is dominated by CPU work (B-tree traversal, tuple
  formatting, WAL encoding) rather than I/O wait.
- The wait-event hook fires and clears before the snapshot reads it.
- The `fdatasync` in goopg's WAL flush path does not pass through
  the `FlushUpTo` → `OnWALSync` hook (needs verification).

The fact that TPS is independent of client count (13–14 TPS at both
1 and 16 clients) strongly suggests a **single-threaded serialisation
point** — likely the WAL writer's state-loop goroutine, which
processes all WAL operations sequentially.

## Scale-Factor Impact

| Scale | Workload | Clients | TPS | Notes |
|-------|----------|---------|-----|-------|
| 1 | Default | 4 | 42.9 | Smaller table, less B-tree depth |
| 3 | Default | 4 | 13.5 | Larger table, deeper B-tree |
| 10 | Default | — | (init timeout) | scale 10 init took > 600 s |

TPS drops with scale factor, consistent with deeper B-tree index
traversal adding CPU time on every UPDATE.

## Recommendations

### P1: WAL Flush Batching (estimated impact: 2–10×)

The most promising optimisation. Each UPDATE currently flushes WAL
to disk (fdatasync) individually. Batching multiple commits into a
single fsync (e.g. at 1 ms intervals or every N transactions, like
upstream's `commit_delay` / `wal_writer_flush_after`) could reduce
the per-transaction fsync overhead from ~1 ms to near zero.

Investigate: `wal.Writer.FlushUpTo` is called from
`dispatchSimpleQueryViaExecutor` after each statement. Check whether
every UPDATE really issues a separate fdatasync.

### P2: WAL Writer Process Model (estimated impact: 2–5×)

The WAL writer runs in a single state-loop goroutine that processes
all WAL operations serially. If the `fdatasync` call in the state
loop blocks the loop, all concurrent writers must wait. Moving the
fsync to a separate goroutine (or using AIO for WAL writes) would
allow the state loop to process new appends without waiting for
durability.

### P3: Index Path Optimisation (estimated impact: 1.5–3×)

The B-tree index scan for `pgbench_accounts` touches 3–4 pages per
lookup. Caching the most-recently-used index pages in the buffer
pool (which goopg already has) helps, but the index depth increases
with scale factor. Preloading the index into the buffer pool after
`pgbench -i` (`pgbench -i -s 3` already does a vacuum) would reduce
index page faults during the benchmark.

## Measurement Methodology

- **Server**: goopg built from HEAD, single-node, no replication
- **Hardware**: local developer workstation (SSD + sufficient RAM for scale-3 working set)
- **pgbench**: PostgreSQL 18.3 client (from `postgres/local_install/`)
- **Scale factor**: 3 (~ 300 000 rows in `pgbench_accounts`)
- **Shared buffers**: default (16384 × 8 KB = 128 MB)
- **WAL settings**: default (no `wal_buffers`, no `wal_direct_io`)
- **Command**: `pgbench -c N -t M -h 127.0.0.1 -p $PORT -U postgres postgres`

## Data Files

- `quick-bench.sh` — benchmark runner
- `check-waits.sh` — wait-event collector
- `perf-detail.sh` — per-statement timing collector
- `pgbench-all.log` — raw pgbench output from benchmarking runs
- `waits.csv`, `waits2.csv` — wait-event samples (empty; see findings)
