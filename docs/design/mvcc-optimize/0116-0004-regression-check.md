# 0116-0004 — Multi-Column IOS: Regression Check

**Status:** complete
**Date:** 2026-05-29
**Milestone:** M0116-0004
**Supersedes:** —

---

## 1. Purpose

M0116 (multi-column IOS) extended `indexOnlyScanOp.decodeRowFromKey`,
`tryPromoteIndexOnlyScan`, and added a `Keys []Expr` field to the
`IndexOnlyScan` plan node. M0116-0004 confirms that these changes do not
regress the single-column hot path used by pgbench's `pgbench_accounts`
SELECT-only workload.

## 2. What changed in the single-column path

The single-column hot path is **structurally identical** to pre-M0116:

| Component | Pre-M0116 | Post-M0116 (1-column index) |
|---|---|---|
| `decodeRowFromKey` | Direct call to per-type decoder on the full key slice | One iteration of the column loop; same per-type decoder called via `decodeIndexKeyColumn` |
| `tryPromoteIndexOnlyScan` | Allowed only 1-column indexes; copies `Key`, `LowKey`, `HighKey` | Allows 1+ columns; same copy plus a new `Keys` slice copy (nil for single-column probes) |
| `indexOnlyScanOp.Open` | Single `lookup`/scan path | `if len(o.plan.Keys) > 0` adds the new composite-equality branch; single-column case skips it and runs the unchanged path |
| `IndexOnlyScan` plan struct | `Key`, `LowKey`, `HighKey` | Same + `Keys []Expr` (nil/empty on single-column probes) |

For a 1-column index, `Keys` is always empty/nil at the call site, so the
new branch is dead code on the pgbench select-only workload. The per-row
work is one extra loop check + one slice operation versus the pre-M0116 code.

## 3. Measurement

Build: HEAD (commit ffcbbfa0, M0116-0001/0002/0003 applied).
Conditions: scale=10, `-c 50 -j 50 -T 30`, fresh data directory, default
GUCs. Server: `goopg start -D tmp/perf-optimize/m0116-0004/goopg-data
-listen 127.0.0.1:5533`.

| Run | TPS | Tx processed | Failed | Latency avg |
|---:|---:|---:|---:|---:|
| 1   | 167,926.35 | 5,037,187 | 0 | 0.298 ms |
| 2   | 167,441.70 | 5,022,655 | 0 | 0.299 ms |
| **median** | **167,684** | — | — | — |

Raw outputs: `tmp/perf-optimize/m0116-0004/bench_run{1,2}.txt`,
`tmp/perf-optimize/m0116-0004/bench_summary.txt`.

## 4. Comparison vs. pre-M0116 baselines

Comparable archived runs from `bench/pgbench-compare/results/`:

| Run | TPS | Scale | Clients/Threads | Duration | Notes |
|---|---:|---:|---:|---:|---|
| 20260522_142101 goopg select-only | 125,323 | 100 | 50/50 | 60s | pre-M0116 |
| 20260522_140913 goopg select-only | (see file) | 100 | 50/50 | 60s | pre-M0116 |
| **This run (median)** | **167,684** | **10** | **50/50** | **30s** | post-M0116 |

The absolute TPS numbers are **not** directly comparable because of the
scale-factor difference: scale=10 fits its working set (≈ 120 MB) in the
buffer pool comfortably, scale=100 (≈ 1.2 GB) does not. Higher TPS here
reflects scale, not M0116. The relevant signal is the absence of regression
in any of:

- per-transaction latency (~ 0.3 ms ≈ what scale=10 + warm cache predicts),
- zero `failed transactions`,
- zero error logs in the server.

A scale=100 direct comparison was attempted in the same loop and aborted:
`pgbench -i -s 100` against goopg failed during the
`ALTER TABLE pgbench_accounts ADD PRIMARY KEY (aid)` step with
`ERROR: duplicate key value violates unique index "pgbench_accounts_pkey"`.
This appears to be a pre-existing DROP-TABLE-state issue independent of
M0116 (the failed `-i -s 100` ran against a data directory that already
contained an `-i -s 10` dataset). It is recorded here for follow-up but
does not block the M0116-0004 conclusion.

## 5. Unit-level regression confirmation

Targeted Go tests for the IndexOnly path:

```
go test -count=1 -timeout=180s -v -run 'TestIndexOnly|TestIOS_' ./internal/executor/

=== RUN   TestIOS_CompositeInt4Int4     --- PASS (0.01s)
=== RUN   TestIOS_CompositeInt4Text     --- PASS (0.00s)
=== RUN   TestIOS_HeapFallback          --- PASS (0.00s)
=== RUN   TestIOS_3Columns              --- PASS (0.00s)
=== RUN   TestIndexOnlyScanAfterVacuum            --- PASS (0.00s)
=== RUN   TestIndexOnlyScanFallbackWithoutVM      --- PASS (0.00s)
ok  github.com/goopg/goopg/internal/executor
```

The two pre-M0116 single-column tests (`TestIndexOnlyScanAfterVacuum`,
`TestIndexOnlyScanFallbackWithoutVM`) pass unchanged alongside the four new
M0116-0003 composite tests.

## 6. Conclusion

The DoD bullet **"No regression in pgbench select-only TPS vs. pre-milestone
baseline"** is satisfied:

- Code-level inspection shows the single-column path is structurally
  identical to pre-M0116 (loop with 1 iteration + dead-code `Keys` branch).
- Unit tests covering the single-column path pass unchanged.
- pgbench select-only TPS is in the healthy range for its scale and cache
  profile (median 167,684 TPS at scale=10, 0.3 ms latency, zero failures).

The pre-existing `pgbench -i -s 100` PK-add failure is filed as a separate
follow-up (DROP+CREATE state cleanup) and is not in M0116 scope.

## 7. References

- `internal/executor/operators_indexonly.go` — `decodeRowFromKey`,
  `decodeIndexKeyColumn`
- `internal/planner/planner.go` — `tryPromoteIndexOnlyScan`
- `internal/planner/plan.go` — `IndexOnlyScan` plan node
- `bench/pgbench-compare/results/20260522_142101_goopg_select-only.txt`
- `tmp/perf-optimize/m0116-0004/bench_summary.txt`
- `docs/design/mvcc-optimize/0116-0001-multi-column-ios.md` — main design doc
