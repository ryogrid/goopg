# M0134-0001 P2 — `EXPLAIN` text/plan-format divergence (cross-case)

**Status:** accepted
**Date:** 2026-08-15
**Task:** M0134-0001 (`.ralph/fix_plan.md`) — the P2 pattern from
`docs/design/0134-0001-aggregates-sql-divergence.md`. Characterises the
EXPLAIN-format gap and decomposes it into formatter slices (S1–S5) and
planner-behavior slices (S6–S10).
**Gate:** `scripts/pg-regress-runner.sh aggregates` → `tmp/regress-diffs/aggregates.diff`.

## Scope and share

After P1/P3a/P3b/P4 landed, `aggregates.diff` is 1628 lines (941 changed).
**794 changed lines (84% of changed, ~49% of the diff) are EXPLAIN plan output** —
the parent note's "P2 = 20.4%" was measured against the pre-fix 2759-line diff.
61 `EXPLAIN` statements exist in `aggregates.sql`; **50 produce divergent plan
output**. P2 is therefore the dominant remaining class, not a 20% tail.

## Diff-direction correction (binds every slice below)

In this diff **`-` = PG 18.3, `+` = goopg** (the parent note listed the patterns
without direction). Decisive evidence: the `-` side carries PG's canonical
min/max `Result/InitPlan/Limit/Index Only Scan` plans and functional-dep-pruned
`Group Key:` lines; the `+` side carries Go type names (`*planner.GenerateSeries`)
and goopg-only annotations (`(stats)`, `(N keys)`). Consequently the two
"missing" annotations are **goopg-only additions PG never emits** — the fix is to
remove them, not add them.

## Class map (10)

| # | class | goopg side (`file:symbol`) | PG side (`postgres/src/...`) | kind |
|---|-------|---------------------------|------------------------------|------|
| 1 | `(stats)` suffix on Seq Scan | `internal/executor/operators_explain.go` `describePlan` 1665-1682, `describePlanVerbose` 1548-1551 | `explain.c` T_SeqScan 1432-1433 (no suffix) | formatter |
| 2 | `HashAggregate (N keys)` suffix + missing `Group Key:` line | `operators_explain.go` 1661 (`len(p.GroupExprs)`), 1638 | `explain.c` T_Agg 1531-1555; `show_sort_group_keys` 2627/2755 | formatter |
| 3 | `*planner.GenerateSeries` / `*planner.ProjectSet` node leak | `describePlan` `%T` fallback 1777; `planChildren` 1870 | T_FunctionScan 1465-1466 + `Function Call:` 2067/2081 | formatter |
| 4 | missing min/max → InitPlan+IndexOnlyScan | no `preprocess_minmax_aggregates`; `buildAggregateStage` `planner.go:6259` | `planagg.c:73`, called `planner.c:1618` | planner |
| 5 | GROUP-BY functional-dep removal | no prune; `GroupExprs` = full GROUP BY `planner.go:6260-6548`; exec-time `funcDepCols` `5651/6893/7055` | `initsplan.c:412 remove_useless_groupby_columns`, `planmain.c:173` | planner |
| 6 | presorted aggregation (Sort/GroupAggregate) | hash-only executor `operators_join_agg.go:1926/1967` | `planner.c:3230 adjust_group_pathkeys_for_groupagg`, 3780 | planner |
| 7 | join algorithm + `(INNER)`/`(CROSS)` annotation | `describePlan` 1590-1609, 1739-1744; `joinTypeName` 1835 | `explain.c` 1422, suffix-only-for-non-inner 1754-1763 | formatter + planner |
| 8 | index cond `<`→`<=` + redundant Filter | `formatIndexCond` 777-812; inclusive `LowKey/HighKey` `plan.go:697-698`; range builder `planner.go:8657-8689` | original op kept in `Index Cond` | planner+formatter |
| 9 | parallel-agg split (incl. a **runtime correctness bug**) | `parallel.go:597 splitAggregate`; whitelist `parallel_agg.go:28`; `parallel_agg_combine.go:145` | explain.c T_Agg split; parallel agg paths | planner |
| 10 | VERBOSE `Output:` naming | `operators_explain.go:492/1353`, `schemaColumnNames` 1192 | `show_upper_qual` (qualified + full expr) | formatter |

## Root causes (top notes)

- **Classes 1–3, 7-annotation, 8-format, 10 are emit-site/formatter divergences**:
  goopg's `describePlan` prints annotations PG does not (`(stats)`, `(N keys)`,
  `(INNER)`/`(CROSS)`) and falls back to Go `%T` for SRF nodes it has no case for.
- **Classes 4–6, 7-choice, 9 are planner-behavior gaps** that change plan shape
  (node types / scan choices), not just text — they also explain why nearly every
  `(N rows)` count differs (different nodes ⇒ different estimates).
- **Class 9a is a correctness bug, not text**: `balk` (`aggregates.sql:1395`,
  `COMBINEFUNC = balkifnull` + `PARALLEL = SAFE`) makes goopg's planner split the
  aggregate (`AggregateIsDecomposable` accepts any user agg with a combinefunc) but
  the executor combine whitelist (`combineAggRuntime`, builtins only) has no rule
  for it → `ERROR: internal error: no combine rule for aggregate "balk"` where PG
  returns one row.

## Decomposition

Formatter/emit-site slices (self-contained, no planner behavior change):

- **S1 (class 1)** — remove the `(stats)` suffix at `describePlan` 1665-1682 /
  `describePlanVerbose` 1548-1551. It is deliberate M0006 ANALYZE instrumentation;
  the parity rule makes it wrong on the wire, so drop it from default output (or
  gate behind an off-by-default debug GUC). 46 `+` lines, ~15 blocks.
- **S2 (class 3)** — add `describePlan` + `planChildren` cases for `GenerateSeries`,
  `ProjectSet`, `UserSrfScan`, `FromUnnest`, `GenerateSubscripts` → `Function Scan on
  <schema>.<func> <alias>` + `Function Call:` detail. Needs alias plumbing (PG shows
  `s1`/`s2`; goopg's `GenerateSeries` node does not store the RTE alias).
- **S3 (class 7a)** — Join/`NestedLoopIndexJoin` label → PG suffix rule (annotate
  only non-inner; spellings ` Left Join`/` Semi Join`/…). Touches every join plan
  line suite-wide → verify across TPC-H/TPC-DS gates.
- **S4 (class 8)** — preserve the original comparison op on `IndexScan` (new field),
  render in `formatIndexCond`, drop the redundant `Filter`. Contained to
  `planner.go:8657-8689` + `formatIndexCond` 777-812.
- **S5 (classes 2+5 rendering half)** — replace `(N keys)` with PG's
  `Group Key: <exprs>` detail line (`show_sort_group_keys` equivalent).

Planner-behavior slices (own efforts, larger):

- **S6 (class 4)** — port `preprocess_minmax_aggregates` (`planagg.c:73`): rewrite
  min/max into InitPlan+Limit+IndexOnlyScan (incl. Merge Append inheritance,
  parameterized subqueries, constant `max(100)`). Depends on goopg's existing
  SubPlan/InitPlan infra.
- **S7 (class 5 planning half)** — port `remove_useless_groupby_columns`
  (`initsplan.c:412`) to prune `GroupExprs` at plan build; reconcile with the
  exec-time `funcDepCols`/`Passthrough` mechanism.
- **S8 (class 6)** — presorted aggregation: sorted/GroupAggregate strategy in the
  executor (currently hash-only) + pathkey ordering. Largest slice; ties into the
  cost-model line.
- **S9 (class 7b)** — join-shape choice (Hash vs NestedLoopIndexJoin vs Merge) —
  cost-driven; part of the leftdeep-joins/cost-model line, not an EXPLAIN-format task.
- **S10 (class 9)** — parallel agg: (a) fix the `balk` runtime error (invoke the
  user combinefunc in `combineAggRuntime`, or refuse the split when no rule exists);
  (b) extend the decomposable whitelist and make `findPartialSubtree` split over
  UNION-ALL `Append` subtrees.

**Sequencing:** S1+S2 (pure text, unblock the 50-block count) → S5 rendering half
(no planner change) → S6 (biggest single win: 25 InitPlan + 32 IndexOnly lines) →
S7 → S10a (correctness) → S4 (small, independent, anytime). S8/S9 are cost-model
line work.

## S2 design (class 3) — resolved 2026-08-15

**PG rules (oracle `explain.c`):** plain output is `Function Scan on <funcname>
[<refname>]`, where `<refname>` (the FROM-item alias) is printed **only when it
differs from `<funcname>`** (`explain.c:4490-4500`); verbose additionally
qualifies the schema (`Function Scan on pg_catalog.generate_series s1`). The
`Function Call: <deparse>` line is **verbose-only** (`explain.c:2067-2083`),
emitted before Filter. `ProjectSet` renders a bare `ProjectSet` label (no `on`,
no Function Call) and exposes its child (`explain.c:1382-1384`).

**Correction to the working-set note:** PG never auto-generates `s1`/`s2` — the
`s1`/`s2` in the diff are **user-written aliases**; the default alias (no `AS`)
is the function name itself, which is exactly the case where the alias is
omitted from the label (`refname == objectname`).

**Design (minimal planner stamp + executor render):**

- **Planner** (`internal/planner/plan.go` + `planner.go`): add an `Alias string`
  field to the four FROM-SRF nodes (`GenerateSeries`, `GenerateSubscripts`,
  `FromUnnest`, `UserSrfScan`) — NOT `ProjectSet` (bare label). Stamp
  `Alias: alias` at each construction site, where `alias` is already computed as
  `rv.Alias` or the default lowercased funcname (`planner.go:4590-4593`
  generate_series, `4793-4796` generate_subscripts, `4470-4473` user SRF).
  Backward-compatible (execution ignores the field).
- **Executor** (`internal/executor/operators_explain.go`): add `describePlan`
  cases before the `%T` fallback (~1758) — `GenerateSeries`→`generate_series`,
  `GenerateSubscripts`→`generate_subscripts`, `FromUnnest`→`unnest`,
  `UserSrfScan`→`Routine.QualifiedName()`, `ProjectSet`→`ProjectSet` — via a
  helper that appends ` <alias>` iff `alias != "" && alias != funcName`.
  Add `describePlanVerbose` cases (~1533) for the schema-qualified label. Add
  `planChildren` case for `ProjectSet`→`[Child]` (~1851). Add the verbose-only
  `Function Call:` line (build a synthetic `planner.FuncCall{Name, Args}` and
  render via the existing `formatExprQual` `FuncCall` case, ~1022).

**Out of scope (separate slices, noted not lost):** class 10 `Output:`
qualification (`s1` vs `s1.s1`) — needs SRF cases in `explainRelBaseName`
(`explain_names.go:211`) so `qualify()` prefixes the rel name; SRF alias
disambiguation (`_1`/`_2` suffixes) is not wired because `explainNames` is built
from the plan tree with no SRF cases; SQL-function inlining (`Subquery Scan on f`
for inlinable user SRFs, `window.sql`) is a planner-behavior gap. Three further
residuals confirmed live by S2 (deferral-ledger rows): (a) sibling SRF nodes
(`RowsFrom`, `ScalarFuncScan`, `PgPartitionTree`, etc.) still render via `%T` —
PG shows a `Function Scan on <refname>` label with objectname NULL for a
multi-function ROWS FROM (`explain.c:4427`); (b) `Function Call:` arg deparse of
an array constructor renders `array_construct(...)` where PG renders
`ARRAY[...]`; (c) verbose detail ordering — goopg emits `Function Call:` before
`Output:` (emitNodeDetailLines runs before the verbose Output block) where PG
emits `Output:` first (`explain.c:1933` before `2067`), a class-10 sub-item.

## S5 design (classes 2+5 rendering half) — resolved 2026-08-15

**Label (class 2 suffix).** `ExplainNode` labels AGG_PLAIN→`Aggregate`,
AGG_SORTED→`GroupAggregate`, AGG_HASHED→`HashAggregate`, AGG_MIXED→`MixedAggregate`
(`explain.c:1531-1553`). goopg's executor is hash-only (`groups :=
map[string]*groupRuntime{}`, `operators_join_agg.go:1967`) — no AGG_SORTED
streaming variant — so the grouped label must stay `HashAggregate`; the ungrouped
`Aggregate` label is already faithful. Change **only** the `describePlan`
Aggregate case (~line 1739): `HashAggregate (%d keys)` → `HashAggregate`. Leave
the grouping-sets branch (~1707-1717) unchanged — it is the separate M0125-0048
single-node path and out of S5 scope.

**Detail line (class 2 missing `Group Key:`).** The crux resolves decisively: for
a non-grouping-sets grouped aggregate PG's `show_agg_keys` ALWAYS calls
`show_sort_group_keys(..., "Group Key", ...)` regardless of strategy — even
AGG_HASHED (`explain.c:2616-2636`). There is **no** `Hash Key:` line for a plain
hash aggregate; `show_hashagg_info` emits only partition/batch/memory/disk stats
(`explain.c:3716-3830`). The literal `"Hash Key"` exists solely in the
grouping-sets path (`show_grouping_set_keys`, `explain.c:2683-2692`), which S5 does
not touch. So the detail line is always `Group Key: <exprs>`.

**Emit site.** Add a `case *planner.Aggregate:` to `emitNodeDetailLines`
(`operators_explain.go:572-715`), before the `default:` arm: when
`len(p.GroupExprs) > 0 && p.GroupingSets == nil`, deparse each entry with
`formatExprQual(g, reg, qualify)` (same as the Sort key and Memoize Cache Key
cases) and append `"Group Key: " + strings.Join(parts, ", ")`. Then render the
attachedFilter (HAVING) as `Filter:` — PG order is `Group Key:` → `Filter:`
(`explain.c:2196-2197`). `qualify` is already computed at line 583 and is correct
for Aggregate (not a scan node → `reg.names().qualify()` = PG's `rtable_size > 1`).

**Inherited gaps (NOT fixed by S5, format-only):**
- **Cast deparse** — goopg's `formatExprQual` drops top-level casts (`CastExpr`
  returns only the operand, `operators_explain.go:1021-1022`) where PG shows them
  (`showTopLevelCast=true`); `qualify` also omits the `|| es->verbose` term
  (deferral, comment at 580-582). The Group Key line inherits both.
- **VERBOSE ordering** — goopg emits `emitNodeDetailLines` before the verbose
  `Output:` block, so in VERBOSE `Group Key:` renders before `Output:` where PG
  emits `Output:` first (`explain.c:1933` before `2196`). Same pre-existing
  divergence as Sort Key / Function Call (class-10 sub-item, ledgered by S2).
- **Expr list length** — `GroupExprs` is the FULL group-by (no functional-dep
  pruning at plan build; that is class 5 / `remove_useless_groupby_columns`, S7).
  S5 renders the full list; the count/expr divergence vs PG is by design until S7.

**Measured residuals (2026-08-15 `aggregates.diff`, 1583→1534 lines):** 16 bare
`HashAggregate` labels + 23 `Group Key:` lines, every same-shape block byte-matching
(`Group Key: c1.w, c1.z`, `Group Key: ten`, …). The still-diverging grouped-agg
blocks are all attributable to four non-S5 classes — (i) key-list length (S7),
(ii) key ORDER (PG emits the access-path/sort order; goopg the written order — a
consequence of the S8 strategy gap, always coincident with PG `GroupAggregate`-over-
Sort vs goopg `HashAggregate`-over-SeqScan), (iii) Group Key *expression*
qualification/paren spelling (pre-existing `formatExprQual` style, identical to
Sort Key/Output/Filter lines throughout the diff), (iv) plan shape (S6/S9/S10).
No S5-introduced format regression.

## S6 design (class 4) — resolved 2026-08-15 (research) + Slice 1 spec

**Corrected measurements** (fresh `aggregates.diff`, 1534 lines): 17 min/max EXPLAIN
blocks (region lines 86-502) = **19 `InitPlan` + 29 `Index Only Scan`** (18 Backward +
11 forward), not the parent note's stale "25 + 32". Two further IOC lines (btg) and
one Parallel-IOC are not min/max. Blocks 15/16 (MergeAppend of 4 IOCs each) are
additionally blocked by an unrelated partial-index creation bug
(`minmaxtest3i ... where f1 is not null` → "column ref f1/0 on nil slot").

**Go/no-go (research verdict):** GO for the **forward (min) half** on existing infra
plus two additions; **NO for the Backward (max) half** until a backward-scan
capability is built. goopg's `SubqueryExpr{IsNonCorrelated}` already gives InitPlan
once-per-statement runtime semantics (`subplan.go:21-25`, `expr.go:7455`), but there
is **no plan-node InitPlan, no `Result` node, and no backward index scan**.

**Three missing pieces (all new work):**
1. **`Result` node** — PG's top is a childless `Result`; goopg has none. Needs a
   `Result{Targets, schema}` plan node + a `resultOp` (emit exactly one row).
2. **bare `InitPlan N` EXPLAIN** — PG 18.3 renders `sp->plan_name` with NO
   `(returns $N)` suffix (`explain.c` has no `returns` string at all;
   `ExplainNode(..., sp->plan_name, ...)` at `explain.c:4807`). `subPlanName`/
   `emitSubPlanSubtrees` hardcode `SubPlan %d` (`operators_explain.go:541,972`);
   subplans are only assigned from detail lines, so a target-only `SubqueryExpr`
   is never rendered.
3. **Backward index scan** — 18/29 IOC lines need it; no direction field on scan
   nodes and a forward-only btree (`pathindexcarrier.go:60-61` "only direction goopg
   emits").

**Decomposition (3 slices):**
- **Slice 1 — gating + forward shape** (this loop): `Result` node + `resultOp` +
  `Result`/InitPlan EXPLAIN + `rewriteMinMaxAggregates` hook. Closes the 11 forward
  lines (blocks 1, 7, 8, forward children of 15/16).
- **Slice 2 — Backward:** btree backward range scan + scan-node direction field +
  `Index Only Scan Backward` render. Closes the 18 Backward lines.
- **Slice 3 — edge cases:** constant `max(100)` (block 14), inheritance/MergeAppend
  (blocks 15/16 + partial-index bug), scalar-subquery nesting (blocks 8/17, class-8),
  ORDER BY/distinct above InitPlan.

**Slice 1 design (forward half):**

- **Plan node** (`internal/planner/plan.go`): `Result{Targets []Expr, schema Schema,
  pos}` — childless. Executor `resultOp` (`internal/executor/operators.go`): one row,
  eval `Targets` into the projected schema.
- **Rewrite hook** (`internal/planner/planner.go`, `planSelect` ~1241-1244, before
  `needsAggregateStage`): `rewriteMinMaxAggregates` mirroring `planagg.c:73`
  gating — no GROUP BY / no HAVING / no window / single RangeTblRef RTE; per-agg
  `can_minmax_aggs` (1 arg, no ORDER BY, no FILTER, no DISTINCT, non-mutable arg,
  `fetch_agg_sort_op` valid). **Slice 1 fires only for the ASC (min) direction** —
  the DESC (max) direction needs Backward (Slice 2), so max targets stay on the
  existing `Aggregate` path for now.
- **Shape per min agg:** `SubqueryExpr{IsNonCorrelated:true}` wrapping
  `Limit{Limit:IntegerConst(1)}` → cheapest `IndexOnlyScan` (via
  `findBTreeIndexForColumn`, `planner.go:8444`) or `SeqScan` fallback, with the
  original WHERE qual and a `col IS NOT NULL` conjunct; subquery tlist = the raw
  arg column. Top = `Result{Targets:[ExecParamRef]}` fed by the InitPlan Param.
- **EXPLAIN** (`operators_explain.go`): `describePlan` case `Result`; bare
  `InitPlan %d` label; assign subplans from the Result's targets (a target-only
  `SubqueryExpr` must get an ID and a subtree, not be dropped).

**Correctness invariants (Slice 1 must preserve):** empty/all-NULL input → InitPlan
returns NULL → `Result` emits NULL (identical to the current `Aggregate` path);
WHERE quals propagate verbatim into the subquery; the rewrite is a **shape-only**
transform — it must never change a result row or value, only the plan text. Gate the
rewrite to fire only when the chosen path is a forward-ordered IndexOnlyScan/SeqScan,
leaving every other case on the existing `Aggregate` path untouched.

**Slice 1 acceptance:** `scripts/pg-regress-runner.sh aggregates` closes blocks 1/7/8
(forward IOC + SeqScan-fallback min/max); blocks 2-6/9-17 unchanged (Backward/edge —
Slice 2/3). `scripts/tpch-spotcheck.sh` PASS (planner change). Units + pre-commit
pgbench smoke PASS.

## S6 Slice 1 — landed 2026-08-15 (forward/min rewrite)

Shipped: `Result` node (`plan.go`) + `resultOp` (`operators.go`) + `Result`/bare
`InitPlan N` EXPLAIN (`operators_explain.go`) + `IndexOnlyScan.Cond` residual
(`operators_indexonly.go`) + `rewriteMinMaxAggregates` hook (`planner.go:planSelect`,
before `needsAggregateStage`). Gating mirrors `planagg.c:73` (rejects GROUP
BY / HAVING / window / DISTINCT / ORDER BY / LIMIT / multi-agg / expression-arg /
non-plain-relation / WHERE-correlation; `max` stays on Aggregate → Slice 2). 5 unit
tests (`internal/planner/minmax_rewrite_test.go`).

**Two spec corrections (both verified against the oracle):**
1. **Bare `InitPlan 1`, not `InitPlan 1 (returns $0)`** — PG 18.3 `explain.c` has no
   `returns` suffix; `ExplainNode(..., sp->plan_name, ...)` at `explain.c:4807`
   renders the bare name, and the committed `aggregates.out` shows `InitPlan 1`
   (lines 943/960/977/994/…). The researcher's `(returns $0)` was wrong.
2. **`Result.Targets = [SubqueryExpr]`, not `[ExecParamRef]`** — goopg's non-correlated
   `SubqueryExpr` + `evalSubquery` constant-key cache already implements
   InitPlan-once semantics (`expr.go` evalSubquery; `subplan.go` header); an
   `ExecParamRef` indirection would be a needless deeper refactor. `resultOp`
   evaluates the SubqueryExpr target directly.

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1534→1537 lines, zero
regressions):** blocks 1/7 now emit `Result`/`InitPlan 1`/`-> Limit` matching PG, but
the scan line diverges (`Sort → SeqScan` + `Filter` vs `Index Only Scan`) because
(a) the goopg regress runner does **not** run `create_index.sql`, so `tenk1` has no
btree index (SeqScan fallback is the correct behavior), and (b) `findBTreeIndexForColumn`
matches only a LEADING column, so `min(tenthous) WHERE thousand=33` declines the
composite `thous_tenthous`. Backward blocks 2-6/9-17 (max) and correlated block 8 are
unchanged (Slice 2/3). `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35).

**Remaining for S6:** Slice 3 (constant `max(100)`, composite-prefix probing, inheritance
MergeAppend + partial-index bug, class-8 outer-subplan rendering) + a future **true**
backward btree walk (deferred — Slice 2 landed the materialise-reverse shortcut instead,
see the Slice 2 landed note below). Deferral-ledger rows appended for composite-prefix
probing + the `create_index.sql` prerequisite (S1) and the true-backward-walk deferral (S2).

## S6 Slice 2 — landed 2026-08-15 (backward/max rewrite)

Shipped: `IndexOnlyScan.Backward bool` (`plan.go`); max acceptance in
`rewriteMinMaxAggregates` (`planner.go`: `isMax` → `Backward: isMax` on the IOS,
`Sort{Desc: isMax, NullsFirst: isMax}` on the SeqScan fallback, `label` branch); the
direction-aware reverse iteration in `indexOnlyScanOp` (`operators_indexonly.go`: `o.idx`
starts at `len(o.rows)-1` and steps −1 when Backward, with the `Cond` (`col IS NOT NULL`)
residual kept verbatim in BOTH directions — the NULL-trap rule); and the `" Backward"`
token in `describePlan`/`describePlanVerbose` (`operators_explain.go`). Tests:
`internal/planner/minmax_rewrite_test.go` (bare-max-rewrites, bare-max-IOS-Backward,
bare-max-SeqScan-fallback, max-NULL-trap plan-shape) + `subplan_stats_test.go` updated to
expect 2 instrumented sublinks (outer scalar sublink + inner InitPlan — planagg.c's
SubPlan+InitPlan shape).

**Divergence from the Slice 2 spec above (deliberate):** the spec planned a "btree
backward range scan" (a true reverse leaf walk). The implementation instead uses
**materialised-slice-reverse**: goopg's `indexOnlyScanOp` already materialises the entire
index range into `o.rows` in `Open` (`operators_indexonly.go:297`), so Backward is just
iterating that slice from the end — zero extra memory/btree work and byte-identical
results. A true backward btree walk is a separate btree-engine capability (reverse leaf
traversal + high-key descent + split recovery + page-latch handling; `rangeScanPos`
`btree.go:3873` is forward-only) that only matters for a streaming
`ORDER BY col DESC LIMIT n` on huge tables — **deferred** (ledger row), not needed for
min/max correctness.

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1537→1543 lines, zero
regressions):** the 5 max blocks (2-6) now emit `Result`/`InitPlan 1`/`-> Limit` matching
PG; the scan line stays `Sort → SeqScan` fallback (the regress runner still doesn't run
`create_index.sql`, so `tenk1_unique1` is absent — the same env gap as Slice 1's block 1).
The `Index Only Scan Backward` text path is therefore exercised by unit tests only
(plan-shape + `Backward: true`), not end-to-end — the `create_index.sql` prerequisite slice
closes that (already in the S1 deferral row). `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=35).

## Cross-case relevance

Every M0134 regress case whose `.sql` emits `EXPLAIN` inherits the formatter
classes (S1–S5) verbatim; `explain.sql` (M0134-0017) is the direct beneficiary of
the whole set. The planner classes (S6–S10) also move `explain.sql` and any case
with min/max or grouped aggregates. Slices should be planned as cross-case
emitter/planner fixes, then re-measured per case, not hardcoded to `aggregates.sql`.

## PG oracle citations

- `postgres/src/backend/commands/explain.c` — T_SeqScan 1432, T_Agg 1531-1555,
  T_FunctionScan 1465-1466, NestLoop 1422, non-inner suffix 1754-1763,
  `show_sort_group_keys` 2627/2755, `Function Call:` 2067/2081, Incremental Sort 1526.
- `postgres/src/backend/optimizer/plan/planagg.c:73 preprocess_minmax_aggregates`
  (called `planner.c:1618`).
- `postgres/src/backend/optimizer/plan/initsplan.c:412 remove_useless_groupby_columns`
  (called `planmain.c:173`).
- `postgres/src/backend/optimizer/plan/planner.c:3230 adjust_group_pathkeys_for_groupagg`.
