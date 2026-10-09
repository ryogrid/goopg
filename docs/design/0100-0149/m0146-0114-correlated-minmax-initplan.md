# M0146-0114 — the min/max rewrite in a correlated subquery

Status: done 2026-10-08 (e8341472c). Parent: M0146-0005.

## Problem

Regress aggregates and join run `min()` inside a correlated scalar
subquery:

```
select f1, (select min(unique1) from tenk1 where unique1 > f1) AS gt from int4_tbl;
```

PG 18.3 rewrites it with the min/max optimisation:

```
Seq Scan on int4_tbl
  SubPlan 2
    ->  Result
          InitPlan 1
            ->  Limit
                  ->  Index Only Scan using tenk1_unique1 on tenk1
                        Index Cond: ((unique1 IS NOT NULL) AND (unique1 > int4_tbl.f1))
```

goopg's `rewriteMinMaxAggregates` declined any correlated WHERE, so it
printed `SubPlan 1 -> Aggregate -> Index Only Scan`.

## PG behaviour

`preprocess_minmax_aggregates` / `build_minmax_path` (planagg.c) run in
the subquery's own planning level. Outer references there are Params
supplied by the enclosing level, so the min/max subquery becomes an
InitPlan of the subquery level whose `extParam` holds the outer value.
It re-executes whenever that param changes (`ExecReScan`), which here
means once per outer row.

## Change

- **Accept a correlated WHERE.** The rewritten inner query sits one
  sublink deeper than the min/max level, so `deepenOuterRefs` moves each
  outer reference one level up the scope stack. It declines when the
  WHERE holds a sublink, because the shift does not reach a nested plan.
  The D4.1 lowering pass then turns the refs into PARAM\_EXEC params, as
  for any nested sublink.
- **Run it per row, label it as PG does.** The InitPlan's `SubqueryExpr`
  is correlated (`IsNonCorrelated=false`), so the executor runs it per
  outer row. It also carries a new `ParamInitPlan` flag, which
  `SublinkIsInitPlan` and the `(InitPlan N).col1` reference rendering
  honour, so EXPLAIN labels and places it as PG does. The flag's only
  other readers are the initPlan cost charge and the level-top placement
  (M0146-0104), where PG treats such an initPlan the same way.
- **Keep the Index Only Scan.** `wherePredSafeForIOS` admits outer
  references. An outer value is a param read from the scope stack or a
  PARAM\_EXEC slot, never from the one-column index row.

## Verification

- **Probe** (PG 18.3 vs goopg). The outer table holds NULLs and values
  past both ends of the inner range. Plans and every result row are
  identical to PG for:
  - a correlated min with `u > f1`;
  - a max with `u < f1`, which uses an Index Only Scan Backward;
  - a max with `u = f1`;
  - a correlated expression over the outer value.
- **Test.** `TestCorrelatedMinMaxInitPlan` checks the plan shape and the
  results. Restoring the correlated decline fails five plan checks.
- **Regress A/B** (23 files), diff lines HEAD → new: aggregates 396 → 386,
  join 14874 → 14870.
  - Error counts are unchanged.
  - subselect's join-qual SubPlan now carries the min/max InitPlan.
  - plpgsql's difference is the nondeterministic function lookup
    (M0146-0084): of two reruns of the new binary, one matched HEAD
    exactly.
- **TPC-DS / TPC-H.** No plan changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS) and ea-ratchet all PASS.
