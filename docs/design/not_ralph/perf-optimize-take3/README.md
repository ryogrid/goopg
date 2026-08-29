# perf-optimize-take3 — pgbench c=50 profiling study

Scope: a fresh, evidence-first profile of `pgbench` **simple-update (`-N`)** and
**select-only (`-S`)** at **50 clients** against goopg at HEAD, with PostgreSQL
18.3 measured side-by-side as the oracle under identical settings, turned into a
ranked improvement plan.

- **Base commit**: `9ecc840b5` (branch `after-parser-refac`)
- **Oracle**: PostgreSQL 18.3, `./postgres/local_install`
- **Date**: 2026-08-29
- **Raw artifacts**: `tmp/take3/runs/<run-tag>/` (not committed — see
  [00-methodology.md](00-methodology.md) for the reproduction commands)

## Why this study exists

The previous full attribution (`analysis/perf-optimize3/`, 2026-07-13) and its
successor (`analysis/perf-optimize3-dash/`, 2026-07-14) are the newest
goopg-vs-PG pgbench numbers in the repository. **Three things have changed the
ground truth since, and none had been re-measured:**

1. **The WAL system was rewritten.** The canonical `0xFE` record family was
   deleted outright on 2026-07-15 and goopg now emits real PG-format records
   (`docs/design/wal-native-pg-format/`, `wal-pg-identical-stream/`). Every
   write-path number predates it.
2. **The parser was migrated to goyacc** (`after-parser-refac`). Per-query setup
   was already ~30 % of read CPU *in the prior study*, so the hot statement path
   moved wholesale. (This study measures it at ~26 %; see
   [02 §3](02-cpu-and-allocation.md).)
3. **Wait-event instrumentation landed** (PR #96). The perf-optimize3 study got
   **empty wait columns on all 28,425 goopg samples**; this is the first study
   that can attribute goopg's commit wait the same way PG's is attributed.

> **Update (2026-08-30, `ac0fd1267`): candidates A, B and C are implemented.**
> `-S` select-only went **92,191 → 111,927 tps (+21.4 %)**, closing the read gap
> against PostgreSQL from 1.23× to **1.02×**; `-N` is unchanged, as predicted for
> a commit-flush-bound workload already at WAL parity. Allocation per query fell
> 68.7 % and `Lock:relation` went from 19.9 % of all backend samples to zero.
> Full results and the re-profile: [06-post-implementation-results.md](06-post-implementation-results.md).
> Chapters 01–05 below describe the state *before* those changes.

## Verdict up front

**The write path has reached PostgreSQL parity, and the WAL-persistence problem
that motivated three prior design bundles is solved.** The remaining gap is
small and is no longer about durability — it is per-statement overhead, split
between allocation volume in the btree descent and one unpartitioned lock.

| workload | goopg | PG 18.3 | gap |
|---|---:|---:|---:|
| `-N` simple-update | 10,786 TPS / 4.635 ms | 11,994 TPS / 4.168 ms | **1.11×** |
| `-S` select-only | 93,083 TPS / 0.537 ms | 114,388 TPS / 0.436 ms | **1.23×** |

Trajectory of the write gap: **7.38× → 1.47× → 1.11×**.

> **Read the read gap as a lower bound.** goopg's `pgbench_accounts` heap is
> **3.08× smaller** than PostgreSQL's after an identical load, because pgbench's
> `filler char(84)` is blank-padded and goopg stores `bpchar` trimmed. That
> favours goopg on `-S`. See [00 §5](00-methodology.md).

Five findings carry the report:

1. **`END` (commit + WAL flush) is at parity: 3.215 ms vs PG's 3.190 ms
   (1.008×).** goopg's wait profile is now PG's canonical group-commit shape —
   60.1 % `LWLock:WALWriteLock` vs PG's 63.5 % `LWLock:WALWrite`. WAL volume is
   1,157 B/txn vs 905 (1.28×), down from 33 KB/txn (18×) in the original study.
   **Do not spend further effort here.**
2. **Go's garbage collector is no longer a bottleneck.** `gcBgMarkWorker` is
   0.09 % and `scanObject` 0.06 % — roughly **700× below** the 63.3 %/54.9 %
   baseline the earlier design series set out to fix. The Go cost has moved
   entirely from *collection* to *allocation*: ~20 % of read-path user cycles are
   inside the allocator.
3. **Allocator cost tracks allocation _count_, not bytes — and the count is
   dominated by the btree descent.** `nbtree.DeformPGIndexTuple` performs **276
   allocations per single-row index lookup** (55.9 % of all allocated objects)
   and costs **11.5 % of read CPU**, 8.7 % of it in `runtime.makeslice` alone.
   This is the largest actionable CPU item in the study and had no candidate in
   any prior bundle.
4. **Every statement parse heap-allocates a 26,664-byte struct** — 54 % of all
   allocated *bytes*; `BEGIN` costs 26 KB. PostgreSQL's equivalent is 1,600 bytes
   **on the C stack** with zero heap traffic, because a C `union` is
   max-of-members while a Go struct is sum-of-members. Striking as this is, it is
   only **1.94 % of CPU** — a caution against reading byte shares as cycle shares.
5. **The unpartitioned `LockManager` global mutex** is **90.8 % of `-S` mutex
   delay** (65.1 % acquire + 25.7 % release) and 54 % of `-N`, surfacing as
   19.9 % of all `-S` backend samples in `Lock:relation` — a wait PostgreSQL
   records **zero** of, because it fast-paths weak relation locks out of the
   shared hash table entirely.

## Documents

| file | content |
|---|---|
| [00-methodology.md](00-methodology.md) | Run matrix, exact commands, configuration, what is headline vs perturbed, threats to validity |
| [01-results.md](01-results.md) | Headline TPS/latency, per-statement decomposition, wait-event distributions, WAL volume, relation growth, load times |
| [02-cpu-and-allocation.md](02-cpu-and-allocation.md) | CPU profiles, the GC verdict, the allocation accounting, `perf` hardware counters — the Go-specific cost chapter |
| [03-contention.md](03-contention.md) | Block and mutex profiles, ranked and mapped to source symbols |
| [04-wal-persistence.md](04-wal-persistence.md) | goopg's flush path vs PG's `XLogFlush` group commit; why this is done |
| [05-improvement-plan.md](05-improvement-plan.md) | Ranked candidates with measured ceilings, effort, risk, gates, sequencing, and the do-not-re-land list |
| [06-post-implementation-results.md](06-post-implementation-results.md) | **Candidates A, B, C landed** — measured outcome, re-profile, and the next bottleneck |

## Review

Agent-reviewed on 2026-08-29; see [05-improvement-plan.md §8](05-improvement-plan.md#8-review-record).
