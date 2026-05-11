# pgbench select-only @ -c 10 post-M0093 (2026-05-11)

## TL;DR

**TPS jumped from ~317 → 2,740** (8.6× improvement) on
pgbench select-only `-c 10 -j 10 -T 180` against goopg at
scale 100. Walwriter flush rate dropped from ~19,600 / 60 s
to **0**. M0091's ≥ 1,000 TPS acceptance bar is met with
margin.

## Parameters

scale=100, `-c 10 -j 10 -T 180 -P 10 -S` (select-only).
Server config identical to M0091 / M0092 summaries
(`shared_buffers=2560MB`, `wal_buffers=100MB`,
`checkpoint_timeout=24h`, `max_wal_size=1024GB`).
`track_io_timing` off (M0092-0005 default).

## Results

### Select-only — primary acceptance

| run | TPS | failed |
|---|---:|---:|
| post-M0092 baseline (re-measured today) | 317.47 | 0 |
| post-M0092 followup (re-measured today) | 283-342 (3 runs) | 0 |
| **post-M0093 #1** | **2705.23** | 0 |
| **post-M0093 #2** | **2739.98** | 0 |
| **post-M0093 #3** | **2741.95** | 0 |

Median post-M0093: **2,740 TPS**. Run-to-run variance is tiny
(~1.4 %) compared to the order-of-magnitude lift over the
M0092 baseline.

### Walwriter flush count over a 90 s window

| binary | walwriter flushes |
|---|---:|
| pre-M0093 | ~19,600 / 60 s (1 per txn — every commit synced) |
| **post-M0093** | **0 / 90 s** |

Read-only transactions emit zero WAL bytes on commit, exactly
matching PostgreSQL's `RecordTransactionCommit` lazy-XID
fast-path.

### CPU pprof during pgbench-S

```
Duration: 30s, Total samples = 60ms (0.2 %)
```

The server is still at < 1 % CPU. The 2,740 TPS ceiling is
the network round-trip / pgbench client limit, not goopg.
With pgbench using `-M prepared` and/or running with more
clients, headroom likely extends well past this. Documenting
as the next bottleneck to chase if higher throughput is ever
required.

### Read-write regression check (M0093-0004)

| workload | TPS | failed |
|---|---:|---:|
| pgbench standard `-c 10 -T 60` | 58.43 | 2 (0.057 %) |
| pgbench simple-update `-N -c 10 -T 60` | 109.55 | 0 |

The 2 standard-workload failures are M0090's documented
concurrent-UPDATE serialisation conflicts (SQLSTATE 40001 —
goopg's documented behaviour where PG would EvalPlanQual).
Both write workloads remain commit-fsync-bound, as expected.
No regression: read-write commits still emit WAL XactCommit
records and `walWriter.FlushUpTo` — only read-only commits
skip them.

### Crash-recovery sanity

Procedure: start goopg, kill -9 mid-pgbench-S after 3 s,
restart, count pgbench_accounts.

| | rows |
|---|---:|
| before crash | 10,000,000 |
| after recovery | 10,000,000 |

Recovery clean; no missing-WAL / torn-WAL errors. Confirms
that the absence of read-only commit records doesn't break
crash recovery semantics (read-only XIDs leave clog at
`Unknown`, treated as aborted on replay — correct because
they wrote nothing).

## Implementation summary

5 implementation commits:

- `c00caa5` — mvcc: `TxnHandle` + lazy `AssignXID` +
  `OldestXmin` snapshotXmin tracking (R-B6 VACUUM
  correctness fix).
- `0a17eed` — executor: `Context.MaterializeWriterXID`
  helper + `BasicSession.OnTopLevelXIDAssigned` sync hook.
- `54f53d4` — wire `MaterializeWriterXID` at INSERT /
  TOAST (writeHeapRowReturning).
- `e383a61` — wire at UPDATE / HOT / DELETE / LockRows
  (R-B1 invariant — before `isConcurrentlyUpdated`).
- `40a1da0` — test fixups for sites that previously
  assumed eager XID allocation.

Plus 2 docs commits:

- `2bc8809` — design doc 0093-0001 status accepted,
  Design B chosen.
- (this commit) — measurement results.

Full unit + testport + protocol + storage + server +
btree + initdb + vacuum + activity test suite passes (only
the pre-existing `TestReplicationEndToEnd` failure on this
branch remains — unrelated to M0093).

## Comparison to the M0092 followup audit's bottleneck claim

The M0092 followup summary identified that "the dominant
per-query cost is per-commit WAL fsync for read-only
transactions" — exactly the bottleneck M0093 removed. The
empirical result (8.6× TPS lift) confirms the audit's
diagnosis was correct.

The earlier M0092 follow-up commits (SlotFromRow stack-
aliasing, protocol DataRow allocation reduction,
ParseHeapTupleNoCopy, track_io_timing GUC gating) still
matter for other workloads — they remove broadly-distributed
allocation overhead that's not on pgbench-S's critical path
but does affect long-running TPC-H queries and mixed
read/write workloads with higher CPU pressure.

## Cross-references

- `bench/pgbench-compare/results/20260511_goopg_select-only_m0092_summary.md`
- `bench/pgbench-compare/results/20260511_goopg_select-only_m0092_followup_summary.md`
- `docs/milestones/0093-read-only-commit-skip-wal-emission.md`
- `docs/design/0093-0001-readonly-commit-skip-wal.md`
- `pprof-data/m0093/cpu.prof`
- Raw run logs (select-only post-M0093, three runs):
  `20260511_162817_goopg_select-only_c10_m0092_followup.txt`
  (2705.23 TPS),
  `20260511_163148_goopg_select-only_c10_m0092_followup.txt`
  (2739.98 TPS),
  `20260511_163518_goopg_select-only_c10_m0092_followup.txt`
  (2741.95 TPS).
  Filenames retain the `_m0092_followup` suffix because the
  same harness was reused; content is post-M0093.
- Raw run logs (read-write regression check):
  `20260511_164226_goopg_standard_c10_m0093.txt`,
  `20260511_164226_goopg_simple-update_c10_m0093.txt`.
