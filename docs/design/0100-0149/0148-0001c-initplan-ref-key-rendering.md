# M0148-0001c — Sort/Group Key over a min/max-rewritten Result; multi-target tlist + ProjectSet; multi-agg InitPlans

**Loop:** #16 slice (a), #17 slice (b), #18/#19/#20 slice (c) part 1 (2026-11-01) · **Parent:** M0148-0001 `aggregates.sql` regress coverage closure
**Baseline:** `scripts/pg-regress-runner.sh -v aggregates` at `bd65d86bc` → 33 hunks
**After slice (a):** 29 hunks (4 closed: expected-lines 950 / 967 / 984 / 1001; no new diffs)
**After slice (b):** 28 hunks (1016 closed byte-exact; 1066 / 1145 remain, see Remaining)
**After slice (c) part 1:** 28 hunks (hunk 1066 awaits the inheritance sub-slice; the multi-InitPlan machinery itself is unit-witnessed)

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

## Slice (a) design

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

## Slice (a) files

- `internal/executor/operators_explain.go`: Result arm in
  `resolveKeySourceDepth`; new `chaseKeyExprToInitPlanRef`; new composite
  arm in `sortKeyParts`.
- `internal/executor/explain_correlated_minmax_test.go`:
  `TestExplainKeyOverMinMaxResultInitPlanRef` pins the three texts
  byte-exact plus a "key still renders the output alias" leak guard.

## Slice (a) verification

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

## Slice (b) — multi-target tlist: `Sort → ProjectSet → Result` (hunk 1016)

**Loop:** #17. Query (aggregates.sql:411-412, expected block
`postgres/src/test/regress/expected/aggregates.out:1169-1185`):

```sql
explain (costs off)
  select max(unique2), generate_series(1,3) as g from tenk1 order by g desc;
 Sort
   Sort Key: (generate_series(1, 3)) DESC
   InitPlan 1
     ->  Limit
           ->  Index Only Scan Backward using tenk1_unique2 on tenk1
                 Index Cond: (unique2 IS NOT NULL)
   ->  ProjectSet
         ->  Result
```

goopg declined the whole rewrite at `len(s.Targets) != 1` and printed
`Sort Key: g DESC` over `ProjectSet → Aggregate → Index Only Scan` (5 rows).

### Mechanism in the oracle

- **The tlist length is not a gate.** `preprocess_minmax_aggregates` iterates
  the min/max aggs of `root->agginfos` and builds one InitPlan subroot per agg
  (planagg.c:160-183); `can_minmax_aggs` (:236) gates per-agg shape only
  (single arg, no DISTINCT/ORDER BY/FILTER/window/WITHIN GROUP/star).
- **The top plan is a childless Result over the WHOLE surviving tlist**:
  `create_minmaxagg_plan` → `make_result(tlist, quals, NULL)`
  (createplan.c:2546-2612) — the non-agg tlist entries sit beside the
  InitPlan Params.
- **Why only Var-free extras survive:** `build_minmax_path` moves the
  relation RTE into the InitPlan subquery (planagg.c:362-420 builds a fresh
  single-column RTE), so the main query is planned over an emptied jointree —
  a relation Var in the main tlist has no range table left to resolve
  against. That is the exact reason goopg's gate must reject Var-bearing,
  sublink-bearing and aggregate-bearing extras.
- **A tlist SRF is expanded by a ProjectSet ABOVE the Result**: the funcexpr
  stays in the Result's tlist; the ProjectSet pulls the non-SRF entries from
  its child and zips the SRF's steps per child row. The childless Result
  emits one row, so `generate_series(1,3)` yields 3 rows.
- **The Sort Key text is the funcexpr itself**: the Sort's key is an
  OUTER_VAR into the child's targetlist entry, and `get_expr`-deparse of that
  entry is the `generate_series(1, 3)` call — never the `g` alias (S18's
  non-Var force-paren applies).

### Design

1. **Gate (planner.go `rewriteMinMaxAggregates`).** New helper
   `strictMinMaxTarget(parser.Expr)` encodes `can_minmax_aggs`' per-agg shape
   (min/max name, 1 arg, no star/DISTINCT/ORDER BY/FILTER/window/WITHIN
   GROUP, unqualified name). The gate scans the tlist for the ONE strict
   min/max target (`aggIdx`); a second one declines (multi-agg tlist = N
   InitPlans numbered as one list — ledgered). Each non-agg target must pass
   `minmaxTListSurvivor`: resolve in ctx, then accept a constant expression
   or a bare `generate_series` over constants. Trap found by the unit pin:
   `isConstantExpr` recurses on args only, so a **0-arg `FuncCall`**
   (`count(*)`) reads "constant" — the survivor helper dispatches on
   `*FuncCall` FIRST and declines every other call form.
2. **Assembly.** Single-target: unchanged childless Result. Multi-target:
   the Result carries the InitPlan plus the surviving const entries
   (createplan.c's whole tlist); an `generate_series` target is NOT a Result
   target — it becomes a `SrfCol{ColIdx, Start, Stop[, Step]}` and the node
   above is a `ProjectSet` whose `OtherExprs` are `ColumnRef`s into the
   Result row (agg slot index 0) with `nil` at each SRF slot, the executor's
   existing "fill from SrfCols" marker (`openSelectSrfMode`,
   operators_project_set.go). No ProjectSet when the extras are const-only
   (PG adds it only for a set-returning tlist entry). Reuses the proven
   select-list SRF executor path — no executor change.
3. **ORDER BY re-attach (`wrapMinMaxOrderByDistinct`).** Re-scans for the
   strict min/max target (the old single-target extraction is gone), labels
   it from `outSchema[aggIdx]`, and requires `len(outSchema) ==
   len(s.Targets)`. Per ORDER BY item: substitution (ordinal-1 / structural
   min/max match) first; on decline, resolve the RAW expression against the
   rewritten output — that is how `order by g` reaches the SRF slot, mirroring
   `transformSortClause`'s name resolution against the tlist. An **ordinal
   over a multi-target tlist declines** (it names the ordinal-th output, not
   the aggregate; ledgered).
4. **EXPLAIN chase arm (`resolveKeySourceDepth`, new `*optimizer.ProjectSet`
   case).** An SrfCol slot's source is the SRF call itself → the chase
   returns a renderer-synthesised `optimizer.FuncCall{Name:
   "generate_series", Args: Start, Stop[, Step]}` (the established
   renderer-synth idiom: `chaseAggregateResultArgs`, R66's Star/Distinct
   arm); `formatExprQual` prints `generate_series(1, 3)` and S18's
   `forceParen` adds the pair → `Sort Key: (generate_series(1, 3)) DESC`.
   A non-SRF slot steps down through `OtherExprs[j]` into the child exactly
   as the Project passthrough does, so `ORDER BY max(unique2)` over the
   multi-target shape lands on the slice (a) Result arm →
   `(InitPlan 1).col1`.
5. **`InitPlan 1` under the Sort comes for free.** `claimLevelInitPlans`
   (M0146-0104) reads `optimizer.LevelInitPlansOf`, which walks the whole
   level subtree through `planChildNodes` — the ProjectSet's child arm
   already returns the Result — and stops only at query-level walls
   (SubqueryScan/CTEScan). The level top (the Sort) claims and numbers the
   InitPlan, `deferSubPlans` keeps InitPlans queued, so the section prints
   after the Sort Key line and before `->  ProjectSet`, as
   `ExplainNode`'s `initPlan, lefttree, …` order demands.

### Slice (b) files

- `internal/optimizer/planner.go`: `strictMinMaxTarget`,
  `minmaxTListSurvivor`, the gate's `aggIdx` scan + survivor loop, the
  multi-target assembly (Result + ProjectSet), `wrapMinMaxOrderByDistinct`'s
  re-scan / label / raw-expression fallback / ordinal decline.
- `internal/executor/operators_explain.go`: the `*optimizer.ProjectSet`
  chase arm in `resolveKeySourceDepth`.
- Tests: `TestRewriteMinMaxAggMultiTargetProjectSet` +
  `TestRewriteMinMaxAggMultiTargetDeclines` (plan-tree shape + the five
  scope bounds) in `minmax_distinct_orderby_test.go` (the old
  `TestRewriteMinMaxAggMultiTargetOrderByDeclines` pinned the decline this
  slice replaces); `TestExplainMinMaxMultiTargetProjectSet` (rendered block
  byte-texts, InitPlan-above-ProjectSet ordering, executed rows, const-only
  extra with no ProjectSet, Aggregate-path scope bounds) in
  `explain_correlated_minmax_test.go`.

### Slice (b) verification

- `scripts/pg-regress-runner.sh -v aggregates`: 29 → 28 hunks; the hunk at
  expected-line 1016 closes byte-exact (8-row block, `Sort Key:
  (generate_series(1, 3)) DESC`, `InitPlan 1` on the Sort, `-> ProjectSet
  -> Result`).
- `go test ./internal/optimizer/ ./internal/executor/`: green except the
  pre-existing `TestCopyFromZonelessTimetzTakesSessionZone` DST-boundary
  flake (documented under M0148).
- TPC-H spotcheck Q12=2 / Q13=35; TPC-DS sf025 sweep; acceptance arm (off)
  24 MATCH; fireset gate (SF0.25 + SF1) — `internal/optimizer` staged ⇒
  G9 IN-SCOPE; pre-commit pgbench smoke via the hook.

## Slice (c) part 1 — multi-agg tlist: N InitPlans over one childless Result

**Loop:** #18/#19/#20 (finished at `91a958dd2`). Query shape (the minmaxtest
witness, `postgres/src/test/regress/expected/aggregates.out:1229-1292`):
`select min(f1), max(f1) from <plain indexed table>` must plan as

```
Result
  InitPlan 1
    ->  Limit
          ->  Index Only Scan using …
                Index Cond: (f1 IS NOT NULL)
  InitPlan 2
    ->  Limit
          ->  Index Only Scan Backward using …
                Index Cond: (f1 IS NOT NULL)
```

### Mechanism in the oracle

- `preprocess_minmax_aggregates` LOOPS `root->agginfos` (planagg.c:160-183):
  one `build_minmax_path` per strict min/max agg, one MinMaxAggInfo, one
  InitPlan each — InitPlan 1 = the FIRST agg in tlist order (min at
  aggregates.out:1231-1236, max at :1237-1243).
- `create_minmaxagg_plan` (createplan.c:2546-2612) emits ONE `Result` whose
  tlist carries one InitPlan Param per agg (plus the jointree-surviving
  non-agg entries, slice (b)) — `make_result(tlist, quals, NULL)`.

### Design

1. **Gate.** The slice (b) single-`aggIdx` scan became a list: every strict
   min/max target is collected (`aggIdx []int`); a min/max-NAMED call that is
   NOT the strict shape (DISTINCT/FILTER/window/…) rejects the WHOLE rewrite
   (PG: `can_minmax_aggs` false for one agg → no MinMaxAggPath at all,
   planagg.c:100-115).
2. **Per-agg builder `buildMinMaxInitPlan`.** The former inline body is now a
   function over `(s, cat, ctx, tbl, fc, t)` returning the InitPlan
   `*SubqueryExpr` + its output `SchemaColumn` (`min`/`max`, tlist alias
   wins). The WHERE is resolved INSIDE the builder: the IOS cond remap
   (`remapColumnRefsToSchema`) rewrites ColumnRef coordinates into
   covered-index space, so one resolved Expr shared across two InitPlans
   (`min(f1), max(f1)` over one table) would be remapped twice.
3. **Assembly.** tlist order is preserved (the `list_concat` order
   create_minmaxagg_plan walks): agg slots take their InitPlan in
   `aggIdx` order, surviving const slots keep their positions; the
   ProjectSet's `OtherExprs` reference the Result row by the position each
   entry was appended at. Single-target tlists keep the exact slice (a)
   node.
4. **ORDER BY re-attach.** `wrapMinMaxOrderByDistinct` re-scans the
   (FuncCall, label) PAIRS from `outSchema`; each ORDER BY item is tried
   against every agg's structural key (first match wins), then falls back
   to raw resolution against the rewritten output (slice (b) rule).
   Ordinals over a multi-target tlist still decline (ledgered).
5. **Inheritance stays declined** (gate: `AccessibleInheritanceChildren` > 0,
   partitioned parents): the minmaxtest hunk 1066 needs the appendrel
   MergeAppend inner plus four rendering behaviours goopg does not have
   yet — child RTE aliases `minmaxtest_1..9` (global numbering continues
   ACROSS InitPlans), the `Merge Append` `Sort Key:` line, per-child
   `Index Cond: (f1 IS NOT NULL)` from the added NullTest (indxpath.c:2764),
   and the DESC-index/partial-index per-child path selection (planagg.c
   appendrel, create_minmaxagg_plan:2659-2713). Deferral ledger rows.

### Slice (c) part 1 files

- `internal/optimizer/planner.go`: gate `aggIdx []int` + lax-call reject,
  `buildMinMaxInitPlan` extraction (per-agg WHERE resolve), multi-agg
  assembly, `wrapMinMaxOrderByDistinct` pair substitution.
- Tests: `TestRewriteMinMaxAggMultiInitPlans` (two InitPlan SubqueryExpr
  targets, min forward / max Backward IOS, output names) in
  `minmax_distinct_orderby_test.go`; the `min(x), max(x)` case moved out of
  `TestRewriteMinMaxAggMultiTargetDeclines`;
  `TestExplainMinMaxMultiTargetProjectSet` now pins the rendered
  `InitPlan 1`/`InitPlan 2`/`Limit`×2 shape and the executed `(min, max)`
  row in `explain_correlated_minmax_test.go`.

### Slice (c) part 1 verification

- `scripts/pg-regress-runner.sh -v aggregates`: 28 hunks, unchanged (no
  corpus query is a plain-table multi-agg EXPLAIN; hunk 1066 is the
  inheritance shape, still declined).
- `go test -p 2 ./internal/optimizer/ ./internal/executor/`: green except
  the documented `TestCopyFromZonelessTimetzTakesSessionZone` DST flake.
- TPC-H spotcheck Q12=2 / Q13=35; TPC-DS sf025 sweep 99/99 shapes identical;
  acceptance arm (off) 24 MATCH; fireset gate SF0.25 + SF1 99/99 + 99/99,
  `fires=none` (no corpus query carries a multi-agg scalar min/max);
  pre-commit pgbench smoke via the hook.

## Remaining slices of M0148-0001c (open, fix_plan entry)

- **(c2)** hunk 1066: inheritance-tree min/max → Merge Append under the
  Limit (planagg's per-child index paths) + the four rendering behaviours
  above (child RTE aliases, Merge Append Sort Key, per-child Index Cond,
  DESC/partial-index paths).
- **(d)** hunk 1108/1145: correlated `distinct min(f1) … where unique1 > f1`
  sharing one `InitPlan 1` + `SubPlan 2` (PG moves the InitPlan into the
  SubPlan; goopg rejects the correlated rewrite at M0146-0114's gate).
- Slice (b) scope bounds, each with a deferral ledger row: multi-agg tlists
  (N InitPlans), non-`generate_series` SRF extras (unnest / regexp_matches /
  user SRFs) and Var/sublink/aggregate-bearing extras, and ordinals over a
  multi-target tlist.
