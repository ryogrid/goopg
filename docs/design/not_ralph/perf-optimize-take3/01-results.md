# 01 — Results

All figures: scale 100, `-c 50 -j 50`, 180 s, simple protocol, `fsync=on`,
`synchronous_commit=on`, engines run sequentially. Provenance for every table is
named inline.

## 1. Headline

`tmp/take3/runs/{R1-goopg-N,R2-goopg-S,P1-pg-N,P2-pg-S}/pgbench.out`

| workload | goopg | PG 18.3 | gap |
|---|---:|---:|---:|
| `-N` simple-update | **10,786 TPS** / 4.635 ms | **11,994 TPS** / 4.168 ms | **1.11×** |
| `-S` select-only | **93,083 TPS** / 0.537 ms | **114,388 TPS** / 0.436 ms | **1.23×** |

Zero failed transactions on all four runs.

### Trajectory

| study | date | goopg `-N` | gap | goopg `-S` | gap |
|---|---|---:|---:|---:|---:|
| `analysis/perf-optimize3` | 2026-07-13 | 2,141 | 7.38× | 91,783 | 1.96× |
| `analysis/perf-optimize3-dash/06` | 2026-07-14 | 9,898 | 1.47× | 89,955 | 2.03× |
| **this study** | **2026-08-29** | **10,786** | **1.11×** | **93,083** | **1.23×** |

The write gap has closed from 7.38× to 1.11×. The read gap improved from 2.03×
to 1.23×, though the two studies ran on differently-loaded hosts and PG's own
absolute read number differs (182 k then, 114 k here at `-j 50`), so the read
comparison across studies is indicative rather than exact. **The within-study
goopg-vs-PG ratios are the reliable figures**, since both engines were measured
on the same host, back to back, under identical configuration.

## 2. Per-statement decomposition — the decisive table

`pgbench -r`, from the same two `-N` runs. This is where the study's main
conclusion comes from.

| statement | goopg (ms) | PG (ms) | ratio |
|---|---:|---:|---:|
| `BEGIN` | 0.234 | 0.228 | 1.03× |
| `UPDATE pgbench_accounts` | 0.511 | 0.299 | **1.71×** |
| `SELECT abalance` | 0.343 | 0.256 | 1.34× |
| `INSERT pgbench_history` | 0.335 | 0.197 | **1.70×** |
| **`END` (commit + WAL flush)** | **3.215** | **3.190** | **1.008×** |
| total | 4.635 | 4.168 | 1.11× |

**`END` is at parity.** It is 69.4 % of goopg's transaction latency and 76.5 %
of PG's — both engines are commit-flush-bound at 50 clients, which is the
expected shape for this workload, and goopg's flush cycle is now as fast as
PostgreSQL's. Compare the same row in `analysis/perf-optimize3`: 21.20 ms vs
2.619 ms, an 8.1× deficit.

Every remaining millisecond of the gap is in **statement execution**, and it is
concentrated in the two write statements (1.71× and 1.70×) rather than spread
uniformly — the read statement inside the very same transaction is 1.34×.

## 3. Wait events (500 ms sampling)

~17,500 client-backend samples per run. Shares are of all client-backend samples.

### `-N` simple-update — goopg now has PostgreSQL's shape

| goopg (`R1`) | % | | PG 18.3 (`P1`) | % |
|---|---:|---|---|---:|
| `active` / `LWLock:WALWriteLock` | **60.1** | | `active` / `LWLock:WALWrite` | **63.5** |
| `active` / (on-CPU) | 11.0 | | `active` / (on-CPU) | 9.6 |
| `idle in transaction` / `Client:ClientRead` | 7.6 | | `idle in transaction` / `Client:ClientRead` | 5.8 |
| `idle in transaction` / (none) | 7.4 | | `active` / `Client:ClientRead` | 5.6 |
| **`active` / `Lock:relation`** | **3.2** | | `idle in transaction` / (none) | 5.5 |
| `idle in transaction` / `Client:ClientWrite` | 2.6 | | `idle` / `Client:ClientRead` | 4.1 |
| `idle` / `Client:ClientRead` | 2.6 | | `idle` / (none) | 2.0 |
| `active` / `IO:WALSync` | 2.0 | | `idle in transaction` / `LWLock:WALWrite` | 1.9 |
| | | | `active` / `IO:WalSync` | 1.9 |

This is the first time goopg's write-path wait profile has been observable at
all — the prior study got empty columns on all 28,425 of its goopg samples. The
two distributions are now nearly superimposable: a large majority waiting to
join a WAL flush group, ~2 % actually inside `fdatasync`, the rest on-CPU or in
client I/O. **That is the canonical group-commit-bound profile, and goopg
reproduces it.**

The one goopg-only row is **`Lock:relation` at 3.2 %**, which PG shows none of.

### `-S` select-only — the read-path anomaly

| goopg (`R2`) | % | | PG 18.3 (`P2`) | % |
|---|---:|---|---|---:|
| `idle` / (none) | 36.7 | | `idle` / (none) | 49.6 |
| `active` / (on-CPU) | 29.4 | | `idle` / `Client:ClientRead` | 29.0 |
| **`active` / `Lock:relation`** | **19.9** | | `active` / (on-CPU) | 12.2 |
| `idle` / `Client:ClientWrite` | 7.6 | | `active` / `Client:ClientRead` | 9.1 |
| `idle` / `Client:ClientRead` | 6.2 | | `active` / `LWLock:WALInsert` | 0.1 |
| `active` / `IO:DataFileRead` | 0.1 | | — | |

**19.9 % of every `-S` backend-sample is blocked on `Lock:relation`. PostgreSQL
records exactly zero such samples in the same workload.** A read-only
single-row index lookup should never contend on a relation lock: PG takes
`AccessShareLock` through its per-backend fast-path lock array
(`postgres/src/backend/storage/lmgr/lock.c`, `FastPathGrantRelationLock`),
which touches no shared hash table at all for a weak lock on a
non-conflicting relation. goopg funnels every such acquisition through one
process-global mutex. [03-contention.md](03-contention.md) confirms this from
the mutex profile independently, and [05](05-improvement-plan.md) makes it
candidate A.

## 4. WAL volume and write amplification

goopg: `pg_wal` directory growth over the 90 s `WALPROBE-goopg-N` run
(840,939 txns, 9,344 TPS). PG: `pg_current_wal_lsn()` delta over `P1`
(2,158,865 txns). goopg needs the filesystem method because
`pg_current_wal_lsn()` is not callable (see
[00-methodology.md §6](00-methodology.md)); directory growth includes
preallocated segments, so goopg's figure is a mild **over**-estimate.

| metric | goopg | PG 18.3 | ratio |
|---|---:|---:|---:|
| WAL bytes / txn | ~1,157 | 905 | **1.28×** |
| total process `write_bytes` / txn | 6,652 | — | — |
| write syscalls / txn | 6.24 | — | — |

Against `analysis/perf-optimize3`'s 33.0 KB/txn (18× PG), WAL volume has fallen
**~29×** and is now within 28 % of PostgreSQL. The residual is consistent with
goopg emitting a page image as a *separate* `XLOG_FPI` record rather than
attaching it to the change record the way PG's `XLogRecordAssemble` does — see
[04-wal-persistence.md §4](04-wal-persistence.md).

## 5. Relation growth — the one clearly-open regression

Byte deltas across `R1` (goopg, 1,941,461 txns) and `P1` (PG, 2,158,865 txns).

| relation | goopg | PG 18.3 |
|---|---:|---:|
| `pgbench_accounts` (heap) | +9.7 MB (4.98 B/txn) | +0.8 MB (0.36 B/txn) |
| **`pgbench_accounts_pkey`** | **+202.6 MB (104.3 B/txn)** | **+0 B (0.0 B/txn)** |
| `pgbench_history` | +101.3 MB (52.18 B/txn) | +112.9 MB (52.30 B/txn) |
| `pgbench_tellers`, `pgbench_branches` | +0 | +0 |

`pgbench_history` grows at the same **rate** to within 0.2 % per transaction
(52.18 vs 52.30 B/txn; the totals differ only because the runs processed
different transaction counts). Note this does **not** validate the heap layout
generally — `pgbench_history`'s `filler char(22)` is left `NULL` by the insert
path, so it never exercises the `bpchar` storage difference described in
[00 §5](00-methodology.md). The heap of `pgbench_accounts` grows **13.7× faster**
than PG's per transaction but stays small in absolute terms — HOT updates are
working.

**The primary key is the anomaly and it is unchanged from the prior study.** The
updated column `abalance` is not indexed, so PostgreSQL's index does not grow at
all; goopg's doubles. This is the known-open btree bloat residual. It is
explicitly *not* re-proposed as an easy fix here: the obvious mechanism
(on-probe `LP_DEAD` kills) was implemented, verified and **reverted** on
2026-07-14 after showing an ~18× pkey regression on a re-probe-heavy workload,
because marking duplicate entries `LP_DEAD` defeats btree deduplication's
posting-list consolidation. See [05 §5](05-improvement-plan.md).

## 6. Load time

`pgbench -i -s 100`, wall clock:

| phase | goopg | PG 18.3 | ratio |
|---|---:|---:|---:|
| total | **69 s** | **12 s** | 5.8× |
| client-side generate (COPY) | 34.4 s | 8.7 s | 4.0× |
| primary keys | 29.6 s | 2.7 s | 11.0× |
| vacuum | 2.24 s | 0.54 s | 4.15× |

`analysis/perf-optimize2` measured this at 539 s vs 15.8 s (34×, COPY 56×). The
load path has improved ~8× and the COPY gap ~14×. Index build is now the
dominant remaining component at 11×.
