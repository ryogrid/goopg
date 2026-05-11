# pgbench measurement against goopg — 2026-05-11 (post-M0090)

## Parameters

Per `bench/pgbench-compare/README.md` + `run_comparison.sh`:

- scale factor: 100 (~1.5 GB / 10M accounts rows)
- clients: 100 (`-c 100`)
- threads: 100 (`-j 100`)
- duration: 180 s per workload (`-T 180`)
- progress: every 10 s (`-P 10`)
- `shared_buffers = 2560MB`, `wal_buffers = 100MB`,
  `checkpoint_timeout = 24h`, `max_wal_size = 1024GB`

Each workload was run on a **freshly-restarted goopg** (fresh
init → start → checkpoint+stop → start → pgbench → checkpoint+stop)
per the user's protocol.

## Results

| Workload | Result file | TPS | Txns OK | Failed | Avg lat (ms) |
|---|---|---:|---:|---:|---:|
| standard (TPC-B-like) | `20260511_112153_goopg_standard.txt` | 71.04 | 12 815 | **54 (0.42 %)** | ~1 400 |
| simple-update (`-N`) | `20260511_112511_goopg_simple-update.txt` | 83.22 | 15 046 | 0 | ~1 200 |
| select-only (`-S`) | `20260511_112833_goopg_select-only.txt` | 386.50 | 69 647 | 0 | ~258 |

Post-run row counts (verified with the patched binary
re-opened against the data dir):

| Table | Expected | Observed |
|---|---:|---:|
| `pgbench_accounts` | 10 000 000 | 10 000 000 |
| `pgbench_branches` | 100 | **100** ✅ |
| `pgbench_tellers` | 1 000 | **1 000** ✅ |
| `pgbench_history` | 0 (truncated by simple-update's pre-run step) | 0 |

The branches / tellers row counts EXACTLY match the init scale
— no MVCC duplicate-row drift. This is the M0090-0002 fix
landing correctly.

## What changed in this run vs the pre-M0090 baseline

### Standard workload — correctness over throughput

- **Pre-M0090** (M0088+M0089 only): 12 841 transactions
  reported committed (0 failed), but `pgbench_branches`
  visible row count drifted to **1 610** (16 × the real
  count). The drift was caused by concurrent HOT updates
  silently overwriting each other's xmax stamps, leaving
  orphan visible tuples in MVCC.
- **Post-M0090**: 12 815 transactions committed, **54
  intentionally aborted with SQLSTATE 40001
  (`serialization_failure`)**. The 0.42 % abort rate is the
  cost of correctness — under contention, transactions
  abort instead of silently corrupting MVCC state. Branches /
  tellers counts are exact.

### Simple-update post-restart — no more `short read at block`

- **Pre-M0090**: every client aborted within ~1 second of
  start because pgbench's `scaling factor: 161` auto-detect
  (driven by the inflated branches count) made it sample
  `aid` values past the real accounts data — the pkey
  returned TIDs past the heap's EOF.
- **Post-M0090**: pgbench auto-detects scale=100 correctly,
  all queries hit valid rows, **83.22 TPS, 15 046 txns, 0
  failed**.

### Select-only — unchanged

386.50 TPS, no regressions.

## Throughput trade-off — context for the 0.42 % standard
workload abort rate

goopg currently has no EvalPlanQual (the PostgreSQL machinery
that under READ_COMMITTED re-fetches the latest tuple version
when a concurrent UPDATE is detected and re-evaluates the
predicate against it). Without EvalPlanQual, the safe response
to detected concurrent xmax-stamps is to abort the second
transaction with `serialization_failure`. The application is
expected to retry.

For pgbench, that surfaces as a low-single-digit-percent
serialization failure rate at -c 100 contention. The TPS
number (71.04) is comparable to the pre-fix pre-M0089 value
(69.81) — the correctness fix does NOT meaningfully hurt
overall throughput; it just converts silent corruption into
visible aborts.

For workloads that need PG-equivalent zero-abort concurrency
under contended UPDATEs, EvalPlanQual is tracked as a separate
follow-up (filed under fix_plan.md's open items if surfaced).

## Issues fixed during the M0090 work

1. **TRUNCATE / DROP left stale FSM + VM entries**
   (`fix(m0090-0001)` commit `e6778f0`). After TRUNCATE,
   the FSM still answered `GetPageWithFreeSpace` with block
   numbers that no longer existed on disk; the next INSERT
   errored with `short read at block`. Fixed by wiring
   `FSM.DropRelation` + `VM.DropRelation` into the TRUNCATE
   and DROP TABLE paths. Both helpers existed but were
   unused.

2. **Concurrent HOT UPDATE overwrote xmax / created orphan
   visible tuples** (`fix(m0090-0002)` commit `be320c9`).
   Under the page exclusive Lock, `PageStampHotOldTuple` did
   not check whether another transaction had already stamped
   xmax; the second writer's stamp clobbered the first's,
   leaving the first's new tuple orphaned but visible under
   MVCC. Fixed by adding an `isConcurrentlyUpdated` check
   under the Lock at all 4 sites that stamp xmax (HOT +
   updateViaIndex non-HOT + updateOp.Next seq-scan non-HOT
   + deleteOp.Next).

## Outstanding work

None within M0090 scope. Both bugs identified in the
milestone (TRUNCATE / FSM + UPDATE / xmax) are closed and
verified end-to-end.

A future EvalPlanQual implementation would reduce the
serialization-failure abort rate at high contention but is
outside M0090's "correctness" theme.
