# M0146-0050 — a correlated sublink re-materialises its CTEs per execution

Status: done 2026-10-06 (`0f7cc6d6b`). WRONG RESULTS fix.

## Symptom

```sql
SELECT g, (SELECT k FROM (WITH c AS MATERIALIZED (SELECT g*2 AS k) SELECT k FROM c) z)
FROM generate_series(1,3) g;
```

goopg returned `1|2, 2|2, 3|2`; PG 18.3 returns `1|2, 2|4, 3|6`.

## Causes and fixes

1. **The CTE cache outlived the sublink execution.**
   - `ctx.CTERowCache` is keyed by CTE declaration. A correlated sublink is
     rebuilt and re-run for every outer row, but its body CTE found the
     first execution's rows under the same key and replayed them.
   - PG clears a CTE's tuplestore when the plan's parameters change
     (`ExecReScanCteScan`, nodeCtescan.c).
   - Fix: `enterSublinkCTEWindow` (expr.go) gives each execution of a
     correlated sublink its own window, as the LATERAL join does per outer
     tuple.
     - The window starts as a copy of the enclosing scope's entries, so a
       CTE that scope already materialised stays shared.
     - What the execution adds is dropped when it ends.
     - It applies at all seven sublink evaluation sites (EXISTS, scalar,
       IN, ARRAY, multi-assign, the two row-comparison forms), whether the
       sublink is lowered or not.
     - Uncorrelated sublinks (`IsNonCorrelated`) get no window, and
       outer-free CTEs live in `CTEStableCache`, untouched.
2. **SRF arguments were invisible to correlation detection.**
   - `walkPlanExprs`'s ProjectSet arm visited only `OtherExprs` and
     unnest's array.
   - So `ARRAY(WITH c AS MATERIALIZED (SELECT generate_series(1, g) AS k)
     ...)` was classed uncorrelated and ran as an InitPlan, once for all
     outer rows.
   - Fix: the arm now walks `SrfArgs`, generate_series' start/stop/step,
     regexp_matches' arguments and user SRF arguments. `walkPlanExprsDeep`
     and the outer-reference checks build on it.

## Verification

- `TestCorrelatedSublinkCTERematerialises`: scalar, IN, EXISTS and ARRAY
  sublinks, the SRF body, and an enclosing-scope CTE read from the
  sublink, all with PG 18.3's answers. Five fail at HEAD.
- Gates: units, tpch-spotcheck, acceptance arm 24/24, sf025 (96/96
  values, no plan changed), fire set (no fires), ea-ratchet, regress A/B
  over 14 files (identical).

## Not covered

- **M0146-0071** (filed, S2 escalation): a correlated CTE read twice
  inside a sublink. The join between the two references runs as LATERAL,
  because the CTE body's outer reference looks like a dependence on the
  left sibling. The second reference then re-materialises the body with
  the join's left row standing in for `g` (`2/4` where PG gives
  `2/2`). Pre-existing.
- An enclosing-scope CTE first materialised *inside* a correlated
  sublink execution is re-materialised on the next execution (the window
  drops it). PG would keep it, since its parameters did not change.
  Results differ only for a volatile body (ledger 2026-10-06).
