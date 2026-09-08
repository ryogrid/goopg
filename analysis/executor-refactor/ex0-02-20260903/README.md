# EX0-02 conforming artifact — `EX0-02-q6-serial-scold`

13 §2.3 header + first real use of the EX0-02 protocol. Single-arm,
timing-only shape (no projection/join change — values table is a
correctness check, not a gate).

```
label: EX0-02-q6-serial-scold
date: 2026-09-03 (21:31–21:36 JST)
goopg: 503f12cf7 +dirty (21 dirty paths at run time — foreign WIP, none
  in internal/executor, internal/storage, or cmd/goopg; binary
  tmp/goopg-ex002 built from this tree during the run)
pg: N/A (goopg-only single-arm artifact; no cross-engine claim)
suite: TPC-H SF=1, Q6 only (HammerDB `tpch` database, load pinned
  2026-07-27; spotcheck_expected.env anchors untouched)
regime: stats=S-cold start (fresh server, first touch of lineitem this
  boot), parallel=off (in-session SET max_parallel_workers_per_gather = 0)
timeout: 300 s | statement_timeout: 0
planner-flags: scripts/planner-flags.env (29 lines, sha256:29782d8b07ba52ed…)
host-load: up 7d 19:30, load 1.48/1.15/1.18, 16 CPU, 31 GB mem
GOGC/GOMEMLIMIT: 100 / 12GiB (explicit override of the GOGC=off env
  default in bench/tpch/env_goopg.sh — protocol §3; profiler arms same
  GC state, no separate profiler regime)
cgroup: scope ex002 via scripts/goopg-test-run.sh,
  MemoryHigh=20G MemoryMax=24G SwapMax=0
work_mem / effective_cache_size: 64MB / 2GB (cluster postgresql.conf,
  P0-12 alignment; shared_buffers=2GB deliberately unaligned, permitted
  divergence)
ports: goopg 127.0.0.1:65433, pprof 127.0.0.1:6161
```

## Pin (goopg-vs-goopg; planner P0-05/06/07 still open, so no PG side)

Serial plan, captured each run (identical text all runs):

```
Aggregate  (cost=60042.59..60042.60 rows=1 width=32)
  ->  Seq Scan on lineitem  (cost=0.00..60036.58 rows=2403 width=550)
        Filter: (l_shipdate range AND l_discount 0.04..0.06 AND l_quantity < 24)
```

No Gather arm (serial setting verified honored). No prior goopg-vs-goopg
capture exists to diff against — this artifact IS the first pin record;
EX0-06 diffs against it.

## Timing (headline profiler-detached, `/usr/bin/time` + psql `\timing`)

| run | wall | revenue | note |
|---|---|---|---|
| 1 | 5.880 s | 102513054.4896 | cold pages + first plan |
| 2 | 7.295 s | 102513054.4896 | warmup spread (GC/page settle) |
| 3 | 6.306 s | 102513054.4896 | settling |
| 4 | 5.480 s | 102513054.4896 | stable |
| 5 | 5.480 s | 102513054.4896 | stable |
| 6 | 5.490 s | 102513054.4896 | stable |

Headline: **5.48 s serial S-cold-stabilized** (runs 4–6, spread 0.2%).
NOT comparable to take7's 3.792 s: that ran `GOGC=off`; this protocol
mandates `GOGC=100` on TPC-H timing arms. The delta is a regime change,
not a regression — no code moved between the two (docs-only diff).

## Alloc arm (pprof `-base` before/after over an 8×Q6 window)

- `alloc_space` total 8.48 GB / 8 queries ≈ **1.06 GB per Q6**;
  `storage.(*Pool).Prefetch` 7.03 GB flat (**82.9%**); next:
  `executor.init.5.func1` 0.65 GB (7.7%), `relFile.lockBlock` 0.29 GB.
  `Datum.MaterializeArena` 0.04 GB (0.5%) — clone traffic is NOT the Q6
  story at this associativity.
- `inuse_space` delta ≈ 0 (2.05 MB retained, ±512 KB window noise) —
  Q6 retains nothing; all allocation is transient.
- `/proc/<pid>/io`: `read_bytes` unchanged across the window (fully
  cached after warmup), `write_bytes` +8 KB. Q6 is CPU-bound here.

## Perf arm (`perf stat -p`, `:u` event modifiers, no trailing `--`)

42.7 s window over the same 8 queries: `task-clock:u` 70.0B (1.64 CPUs),
`cycles:u` 179.3B, **`instructions:u` 452.7B → ~56.6B per Q6**, IPC 2.52,
`branch-misses:u` 521M, `cache-misses:u` 3.03B. Same-window SIGPROF caveat
recorded (absolute figures indicative; shares authoritative).

CPU top (50 s pprof, 62.4 samples): `runtime.futex` 21.4% flat,
`evalFastExpr` 7.8% flat / 15.6% cum, `pageChecksumBlock` 7.7%,
`decodeRowRangeInfo` 7.5% / 16.2% cum, `runtime.usleep` 7.4%,
`decodePhysicalPGValueLowered` 2.7% / 8.6% cum,
`NumericInt64FromStoredPayload` 2.5% / 5.1% cum, `compareDatum` 2.5% /
4.8% cum. Decode + expression + checksum dominate; futex/usleep share is
worker sleep, not query work — EX0-04 slices must separate it.

## Anything worse

Nothing moved (first measurement under the protocol; docs-only tree
diff). The only cross-record comparison available (take7 3.792 s) is
void by regime mismatch, stated above — it is not a regression signal.
