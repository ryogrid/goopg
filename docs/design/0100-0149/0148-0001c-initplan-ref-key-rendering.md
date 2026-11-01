# M0148-0001c (slice a) — Sort/Group Key over a min/max-rewritten Result renders `(InitPlan N).col1`

**Loop:** #16 (2026-11-01) · **Parent:** M0148-0001 `aggregates.sql` regress coverage closure
**Baseline:** `scripts/pg-regress-runner.sh -v aggregates` at `bd65d86bc` → 33 hunks
**After this change:** 29 hunks (4 closed: expected-lines 950 / 967 / 984 / 1001; no new diffs)

## Why

goopg already ports PG's MIN/MAX index optimization forward half —
`rewriteMinMaxAggregates` (planner.go, for `preprocess_minmax_aggregates`,
postgres/src/backend/optimizer/plan/planagg.c:73 + `build_minmax_path`
:362-420) — so `select max(unique2) from tenk1` already plans as

```
Result
  InitPlan 1            (non-correlated SubqueryExpr, once per statement)
    ->  Limit
        ->  Index Only Scan Backward using tenk1_unique2 on tenk1
              Index Cond: (unique2 IS NOT NULL)
```

(verified by the unchanged hunk context lines). The four remaining label
hunks were pure EXPLAIN-rendering gaps: an upper `ORDER BY`/`DISTINCT`
re-attached by `wrapMinMaxOrderByDistinct` keys on the Result's **output
label** (`max`), while PG deparses the same OUTER_VAR through the Result's
targetlist, hits the initplan Param entry, and `get_variable` →
`get_special_variable` (ruleutils.c) prints it as `(InitPlan N).colM`.
goopg printed the bare alias:

| query | PG 18.3 expected | goopg before |
|---|---|---|
| `select distinct max(unique2) from tenk1` | `Group Key: (InitPlan 1).col1` | `Group Key: max` |
| `… order by 1` / `order by max(unique2)` | `Sort Key: ((InitPlan 1).col1)` | `Sort Key: max` |
| `… order by max(unique2)+1` | `Sort Key: (((InitPlan 1).col1 + 1))` | `Sort Key: ((max + 1))` |

## Design

The chase machine for upper keys (`resolveKeySourceDepth`, the R66 family)
is the single deparse path shared by the Sort arm and the Group Key arm
(the M0134-0001 S18 "sibling pair"), so the fix lands **inside the chase**,
not at a site:

1. **Result arm (shared chase).** The existing M0146-0129 childless-Result
   arm declined every subplan target. It now recognises the min/max-rewrite
   shape first: target is a `*SubqueryExpr` with
   `optimizer.SublinkIsInitPlan` (IsNonCorrelated — the rewrite's InitPlan;
   the correlated form rejects the rewrite upstream) and a non-nil plan,
   at a name-checked output index. The chase returns the sublink, so every
   caller — `aggGroupKeyText`, `sortKeyParts` entry (iii), the Distinct /
   DistinctOn / grouping-sets arms — renders PG's label for free, and the
   pair stays position-consistent by construction.
2. **Rendering needs no new site code.** `formatExprQual`'s SubqueryExpr
   arm (R65 Arm B) already prints `(InitPlan N).col1` for the InitPlan
   branch, and each key site applies S18's `forceParen` to a non-Var
   referent: bare label → `((InitPlan 1).col1)` on a Sort line, single
   pair on a Group Key line (the child is a Result, not a pass-through,
   so the site adds no wrap — exactly PG, whose parens belong to the Param
   rendering).
3. **Computed keys.** `sortKeyParts`' composite-key chase only accepted
   joins and kept-Subquery-Scan Projects. New fail-closed helper
   `chaseKeyExprToInitPlanRef`: clone-replace each key ColumnRef through
   the shared chase, keep only hits that land on an InitPlan sublink, and
   decline (today's rendering) when nothing lands. `max+1` becomes
   `((InitPlan 1).col1 + 1)` and S18 adds the outer pair.
4. **SubPlan numbering is untouched by design.** The key render calls
   `subPlanName` → `assign` during the node's detail lines; the label is
   the first number in render order and `claimLevelInitPlans` (M0146-0104,
   SS_attach_initplans) dedupes on the same expression, so `InitPlan 1`
   prints once under the level top (before `->  Result`), unchanged.
5. **The alias-pin survives.** `TestExplainSortKeySubqueryGroupByUnchanged`
   pins `Sort Key: max` for a scalar sublink used as *target/GROUP BY*
   (no rewrite Result on the chase path — the child is the Aggregate and
   `aggOutputKeyExpr` declines sublink-bearing sources). The new arm
   fires only on the rewrite's childless Result, so that guard is
   untouched.

## Files

- `internal/executor/operators_explain.go`: Result arm in
  `resolveKeySourceDepth`; new `chaseKeyExprToInitPlanRef`; new composite
  arm in `sortKeyParts`.
- `internal/executor/explain_correlated_minmax_test.go`:
  `TestExplainKeyOverMinMaxResultInitPlanRef` pins the three texts
  byte-exact plus a "key still renders the output alias" leak guard.

## Verification

- `scripts/pg-regress-runner.sh -v aggregates`: 33 → 29 hunks; hunks
  950/967/984/1001 close byte-exact (no deferral ledger row — the label
  matches PG's mechanism, not a shortcut).
- `go test ./internal/executor/`: green except
  `TestCopyFromZonelessTimetzTakesSessionZone`, a pre-existing date-boundary
  flake (US DST ended 2026-11-01; fails identically on the clean stashed
  HEAD) — documented in fix_plan.
- TPC-H spotcheck Q12=2 / Q13=35 PASS; TPC-DS sf025 sweep PASS (plan-shape
  99/99 identical); acceptance arm (off) 24 MATCH PASS; fireset-scope OUT
  (`internal/executor` cannot move plan election, G9); pre-commit pgbench
  smoke PASS.

## Remaining slices of M0148-0001c (open, fix_plan entry)

- **(b)** hunk 1016: `select max(unique2), generate_series(1,3) from tenk1`
  — needs multi-target min/max rewrite (PG's `preprocess_minmax_aggregates`
  handles a tlist of min/max + non-agg targets via a Result over
  ProjectSet), plus the `(InitPlan 1).col1, (generate_series(1, 3))`
  target rendering entangled with 0001f.
- **(c)** hunk 1066: inheritance-tree min/max → Merge Append under the
  Limit (planagg's per-child index paths).
- **(d)** hunk 1108: correlated `distinct min(f1) … where unique1 > f1`
  sharing one `InitPlan 1` + `SubPlan 2` (PG moves the InitPlan into the
  SubPlan; goopg rejects the correlated rewrite at M0146-0114's gate).
