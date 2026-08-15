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

## S6 Slice 3a — landed 2026-08-15 (create_index.sql prerequisite)

Shipped: `create_index.sql` added to the regress runner's setup phase
(`scripts/pg-regress-runner.sh`, best-effort `timeout 300`, before
`create_aggregate.sql`), so the `tenk1`/`tenk2`/`onek` btree indexes exist and the
already-landed S6 rewrite is exercised end-to-end instead of unit-test-only.

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1543→1527 lines):** blocks 1
(`min(unique1)`) and 2 (`max(unique1)`) now emit `Index Only Scan using tenk1_unique1` /
`Index Only Scan Backward using tenk1_unique1` matching PG — the first end-to-end proof of
the S6 Slice 1+2 rewrite (the `Backward` token included). Full quick set: no PASS→FAIL
regression, no hang (probe: create_index.sql completes ~2:01 wall; btree/gist/gin/hash AMs
all accepted, no wedge).

**New gap surfaced (blocks 3-5, ledgered):** `max(unique1) WHERE unique1 < 42` (and `>42`,
`>42000`) STILL falls back to `Sort → Seq Scan` even with the index — the rewrite does not
push the WHERE qual into the IOS `Index Cond` (`((unique1 IS NOT NULL) AND (unique1 < 42))`).
This is a distinct rewrite-shape gap from the composite-prefix case (block 6, ledger row 1371
— a non-leading column). The design doc's Slice 2 note had projected blocks 2-6 would close
under this prerequisite; the projection was wrong: blocks 3-5 need Index-Cond push, block 6
needs composite-prefix probing.

**Remaining S6 edge cases (unchanged, in dependency order):** (b) composite-prefix probing (block 6,
ledger row 1371); (c) constant `max(100)` (block 14); (d) scalar-subquery nesting (blocks
8/17, class-8); (e) inheritance/MergeAppend (blocks 15/16 + partial-index bug).

## S6 Slice 3b — landed 2026-08-15 (filtered single-column min/max Index Cond push)

Shipped (`commit be5dcf7f`): `rewriteMinMaxAggregates` now resolves the WHERE qual once
up-front and, when a btree index whose leading column is the agg column exists AND the qual
references only that column (`wherePredSafeForIOS`, fail-closed on subplans/`InExpr`/non-agg
refs via `exprChildSlots` — the `walkExprTree`-based first attempt missed subplans and
regressed `subselect.sql` with an out-of-range outer ref), builds the IOS with
`Cond = (col IS NOT NULL) AND <where-qual>` — the IS NOT NULL as the LEFT conjunct, matching
PG's `Index Cond: ((unique1 IS NOT NULL) AND (unique1 < 42))` (`planagg.c:385-396` `lcons`).

**Two Index spaces, one latent bug fixed:** the executor resolves `*ColumnRef` by `Index` only
against the scan row (`slot.Get(cref.Index)`), and the IOS row is 1-wide (`Covered: [agg col]`
at position 0) while the SeqScan row is full-width. The old code shared a single `isNotNull`
(`Index: argCR.Index`) across both — correct for the SeqScan fallback but wrong on the IOS for a
non-leading agg column. The slice splits it: `isNotNullIOS` (Index 0) for the IOS Cond, the
table-position `isNotNull` for the fallback; the pushed qual's agg refs are remapped
`argCR.Index → 0` via `remapColumnRefsToSchema` after the safe check.

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1527→1499 lines, zero
regressions):** blocks 3-5 now emit `-> Index Only Scan Backward using tenk1_unique1` +
`Index Cond: ((unique1 IS NOT NULL) AND (unique1 <op> <const>))`, with the `Sort`/`Seq Scan`/
`Filter` lines gone; the `QUERY PLAN` rule-line width on these blocks is the only remaining
delta (formatter class, out of scope). RESULT values unchanged (41 / `>42`→9999 / `>42000`→
NULL). `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35). 4 unit tests added
(`internal/planner/minmax_rewrite_test.go`): filtered max/min IOS shape, non-covered-column
fallback (`min(x) WHERE y=3`), and the non-leading-column latent-bug regression.

**Residual (ledger row 1374):** multi-clause AND nesting vs PG flat, and no-dedup of a
user-written `col IS NOT NULL` — both cosmetic, recorded not widened.

## S6 Slice 3c — spec (composite-prefix probing, ledger row 1371)

**Target (block 6):** `min(tenthous) WHERE thousand = 33` must use the composite
btree `tenk1_thous_tenthous` (leading `thousand` bound by the WHERE equality, `tenthous`
as the agg pathkey). `findBTreeIndexForColumn` matches only a LEADING column == the agg
column, so it declines (`idx.Columns[0]=thousand != tenthous`) and the block falls back
to `Sort → Seq Scan`.

**Oracle (`aggregates.out:1052-1057`, PG 18.3):**
```
 Result
   InitPlan 1
     ->  Limit
           ->  Index Only Scan using tenk1_thous_tenthous on tenk1
                 Index Cond: ((thousand = 33) AND (tenthous IS NOT NULL))
```
Two divergences from the single-column filtered case (Slice 3b): the prefix equality
qual is the **LEFT** conjunct (by index-column order, `planagg.c:385-396` folding the
bound prefix into the indexqual and lcons-ing the `IS NOT NULL`), and the agg-col
`IS NOT NULL` **IS present** in the Index Cond at the agg column's position.

**PG mechanism (18.3):** `build_minmax_path` (`planagg.c:316`) is generic — it builds
`SELECT tenthous FROM tenk1 WHERE tenthous IS NOT NULL AND thousand=33 ORDER BY tenthous
LIMIT 1` and hands it to `query_planner`. Index choice is `get_cheapest_fractional_path_for_pathkeys`
(`planagg.c:442`); the prefix equality `thousand=33` makes the `thousand` pathkey
`EC_MUST_BE_REDUNDANT` (its EC contains a const, `pathkeys.c:158-178`, `pathnodes.h:1473`),
so the ORDER-BY pathkeys begin at `tenthous` — the composite index's second column. This
is why a non-leading agg column works when a WHERE equality binds the leading prefix.

**goopg design (bounded, no new executor capability):** the executor already decodes an
IOS `Covered` row and filters it by `Cond` (Slice 3b), so the only new work is a planner
index-choice helper plus a wider `Covered` + a slice-down `Project`. Specifically:
- `findCompositePrefixIndexForColumn(cat, tbl, aggCol, wherePred)` — AND-walks `wherePred`
  into `col = const` equality conjuncts (column on EITHER side of `OpEq`, constant other
  side, `isConstantExpr`), then accepts a non-partial btree whose longest equality-bound
  prefix of `idx.Columns` is non-empty (`k ≥ 1`), whose next column is the agg col
  (`idx.Columns[k] == aggCol`), and — first-cut bound — where the agg col is the LAST
  column (`k+1 == len(idx.Columns)`; trailing columns fall back to SeqScan, safe).
- In `rewriteMinMaxAggregates`, a NEW branch between the existing leading-column IOS
  branch and the SeqScan fallback: `Covered = idx.Columns[:k+1]` (prefix + agg col),
  `Cond = andExpr(prefixQuals remapped) AND isNotNullIOS@k` (prefix LEFT, `IS NOT NULL`
  RIGHT — the reverse of Slice 3b's single-col order), remap `{prefixCol_i: i, aggCol: k}`,
  and a `Project` ABOVE the IOS (BELOW the `Limit`) slicing the k+1-wide row to the agg
  column (mirrors the SeqScan fallback's invisible Project, `planner.go:8678-8681`).
  `Backward: isMax` unchanged.
- **Safety gate (fail-closed):** every `wherePred` conjunct must be a `prefixCol = const`
  equality (no range/agg-col/non-prefix conjunct may remain — it would read a column the
  IOS does not decode), else the SeqScan fallback. A NEW `isNotNullIOS` instance at
  `Index: k` (the covered position of the agg col), NOT the Slice 3b hardcoded Index-0 one.

**Acceptance:** block 6 emits the oracle shape above; `scripts/pg-regress-runner.sh aggregates`
closes it with zero regressions; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35).

## S6 Slice 3c — landed 2026-08-15 (composite-prefix index choice, commit `be4ee556`)

Shipped exactly per the spec above: `collectEqualityConjuncts` (AND-walks the RESOLVED
WHERE into per-column `col = const` equalities, fail-closed on non-equality / out-of-prefix
/ duplicate same-column equalities) + `findCompositePrefixIndexForColumn` (non-partial
btree whose equality-bound prefix `k ≥ 1` is followed by the agg column as the LAST index
column) + a composite branch in `rewriteMinMaxAggregates` (Covered = prefix+agg in index
order, Cond = remapped prefix quals LEFT + agg-col `IS NOT NULL`@k RIGHT, slice-down
`Project` above the IOS / below the `Limit`). 5 unit tests in `minmax_rewrite_test.go`.

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1499→1481 lines, zero
regressions):** block 6 (`min/max(tenthous) WHERE thousand = 33`) now emits
`Index Only Scan [Backward] using tenk1_thous_tenthous` + `Index Cond: ((thousand = 33)
AND (tenthous IS NOT NULL))`, result values match PG (`9033`). The exhaustive
audit of every `aggregates.sql` min/max query against `create_index.sql` indexes confirms
the branch fires only for block 6. `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35).

**One residual recorded (ledger row 1375):** a duplicate same-column equality
(`thousand = 33 AND thousand = 44`) — PG pushes both as separate index quals and returns
NULL (contradiction); goopg declines to the SeqScan fallback (which also returns NULL, via
full-qual evaluation) rather than collapse under map last-write-wins. Correct result,
divergent plan shape; degenerate, not in the corpus.

**Remaining S6 edge cases (unchanged, in dependency order):** (c) constant `max(100)`
(block 14); (d) scalar-subquery nesting (blocks 8/17, class-8); (e) inheritance/MergeAppend
(blocks 15/16 + partial-index bug).

## S6 Slice 3d — landed 2026-08-15 (constant-arg min/max rewrite)

Shipped: `rewriteMinMaxAggregates` now accepts a CONSTANT arg (`max(100)`, block 14),
closing the last pure-rewrite-hook edge case. `Result` gained a `Child Node` +
`OneTimeFilter Expr` (PG `nodeResult.c` `resconstantqual`); `resultOp` implements the
`outerPlan(plan)` variant (eval the one-time qual ONCE at Open with a nil slot — NULL/false
→ emit no rows, child never opened — else project `Targets` per child row); EXPLAIN renders
`One-Time Filter:` (explain.c:2234-2240 `show_upper_qual`) and `planChildren` descends into
the child. The const branch builds `Limit(1) → Result{Targets:[arg], OneTimeFilter: <arg>
IS NOT NULL, Child: SeqScan}` (no Sort — ORDER BY a const is dropped — and no per-row
Filter), typed int4 via `ExprResultType` (the `make_const` small-literal answer, NOT the
normal path's `exprType` int8).

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1481→1472 lines, zero
regressions):** block 14 (`select max(100) from tenk1`) now byte-matches the oracle
(`aggregates.out:1191-1201`) — `Result`/`InitPlan 1`/`-> Limit`/`-> Result`/`One-Time
Filter: (100 IS NOT NULL)`/`-> Seq Scan on tenk1` — with only the pre-existing
rule-line-width delta. Result value stays `100`. `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=35); `go test ./internal/planner/ ./internal/executor/` PASS.

**Residual (ledger row 1376):** const-arg min/max WITH a WHERE qual and non-IntegerConst
constants decline to the Aggregate path (fail-closed, correct result); the normal path's
bare-integer-literal int8-vs-PG-int4 typing is a separate latent divergence flagged there.

**Remaining S6 edge cases (unchanged):** (d) scalar-subquery nesting (blocks 8/17,
class-8); (e) inheritance/MergeAppend (blocks 15/16 + partial-index bug).

## S6 Slice 3e — landed 2026-08-15 (partial-index predicate walker fix — e.1)

**Shipped:** one missing-case arm in `walkExprTree` (`internal/planner/unnest.go`):
`case *IsNullExpr: walkExprTree(x.Operand, visit)`. `IsNullExpr` (`plan.go:283-287`)
wraps a single `Operand`; the walker visited the node but never descended into it,
so `ExprContainsColumnRef` (`planner.go:2476-2492`) reported `false` for
`f1 IS NOT NULL`, the CREATE INDEX const-fold guard
(`operators_ddl.go:6483` `!ExprContainsColumnRef(resolvedPred)`) passed, and
`evalExpr(resolvedPred, nil, …)` (`:6484`) hit the nil-slot raise
(`executor/expr.go:356-358`) — the `aggregates` regress
`ERROR: column ref f1/0 on nil slot` at `create index minmaxtest3i … where f1 is not null`.
One regression test (`TestExprContainsColumnRefIsNullOperand`, `unnest_test.go`):
`IS NULL`/`IS NOT NULL` over a ColumnRef → true, bare constant → false.

**Sibling paths (both consume `walkExprTree`, both strictly more correct now):**
`ExprContainsColumnRef` (the DDL guard) and the min/max rewrite's correlated-qual
check (`planner.go:8576-8584`) — a correlated `col IS NULL`/`IS NOT NULL` qual is
now detected and the rewrite correctly declines to the Aggregate path. No existing
min/max unit test flipped.

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1472→1462 lines, zero
regressions):** the `+ERROR`/`LINE`/`^` block is gone and `create index minmaxtest3i`
succeeds; `minmaxtest3i` now appears only on the `-` (PG) side of blocks 15/16
(which remain open — the e.2 MergeAppend shape gap). `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=35); `go test ./internal/planner/ ./internal/executor/` PASS.

**Deferral (ledger row appended):** `walkExprTree` still lacks arms for
`IsBoolExpr`, `IsDistinctFromExpr`, `CollateExpr`, and `InExpr` — each can hold a
`*ColumnRef` operand and under-reports the same way in the CREATE INDEX
const-fold guard. Not exercised by `aggregates.sql`; recorded, not widened.

**Remaining S6 edge cases (unchanged):** (d) scalar-subquery nesting (blocks 8/17,
class-8); (e.2) inheritance/MergeAppend (blocks 15/16 — now unblocked by the e.1
prerequisite).

## S6 Slice 3f — (d)-render attempted, REVERTED (net-negative) — 2026-08-15

The "pure EXPLAIN rendering gap" hypothesis for (d) was **empirically refuted**.
A Slice 3f implementer run registered the skipped `*planner.Project`'s target-list
sublinks in `walkPlanFiltered` (mirroring the childless-`Result` loop at
`operators_explain.go:604-606`), so blocks 8/17 would render their subplan instead
of dropping it. `go test` + `tpch-spotcheck` PASS, but
`scripts/pg-regress-runner.sh aggregates` diff grew **1462 → 1474** — the subplan
now emits MORE non-matching lines, not fewer, and the edit was reverted.

**Why the prediction was wrong:** the divergence is NOT a rendering gap — it is the
**correlated-subquery representation model**:
- goopg executes a correlated subquery as a per-row parameterized scan and renders
  the outer ref as an `ExecParamRef` → `$0` (`operators_explain.go:1128-1132`),
  i.e. `Index Cond: (unique1 >= $0)` / `Filter: (unique1 > $0)`.
- PG keeps the outer ref as a `Var` (`varlevelsup>0`) and deparses it against the
  outer relation — `unique1 > int4_tbl.f1` (`get_rule_expr` T_Var/T_Param).

So even after d-render, blocks 8/17 stay open (inner `Aggregate → Index Scan` vs
`Result → InitPlan 1 → Limit → Index Only Scan`; `SubPlan 1` vs `SubPlan 2`; `$0`
vs `int4_tbl.f1`). Closing them needs a **d-rewrite** slice, not a render tweak:
(1) relax the correlation gate at `planner.go:8576-8584` to build PG's
parameterized min/max InitPlan (`planagg.c:316 build_minmax_path` + PARAM_EXEC);
(2) teach the deparser to map a correlated `ExecParamRef` back to the outer column
(`int4_tbl.f1`, not `$0`); (3) number the outer sublink AFTER the inner InitPlan
(`SubPlan 2`, not 1). All three are coupled; treat (d) as a hard multi-part slice
and do NOT re-attempt the standalone d-render.

## S7 Slice 1 — landed 2026-08-15 (single-relation GROUP BY pruning)

**Design:** the single-relation arm of PG `remove_useless_groupby_columns`
(`postgres/src/backend/optimizer/plan/initsplan.c:412`). `buildAggregateStage`
(`internal/planner/planner.go`) now drops `GROUP BY` columns redundant under a
PK/unique index. New `pruneUselessGroupByColumns` helper (fail-closed `(nil,nil)`
unless every guard holds): ≥2 group items (:422), no grouping sets (:426), exactly
one `RTE_RELATION` base rel (:494), inheritance-parent skip unless
`RELKIND_PARTITIONED_TABLE` (:502), unique + non-deferrable + no-predicate +
non-expression index (:527-532), key cols NOT NULL-or-`NULLS NOT DISTINCT`
(:546-552), proper subset of the group (:567), fewest-columns key wins (:578);
surplus columns dropped (:597,610-625). Pruned columns are accepted as passthrough
via a new `prunedInputCols` set — **not** by extending `isColumnFunctionallyDetermined`
to unique indexes, which would flip `functional_deps`' pinned 42803 (PG's
`check_functional_grouping`, `pg_constraint.c:1740`, is PK-only by design; reviewer
CONFIRMED). `isColumnFunctionallyDetermined` now judges coverage against
`originalGroupInputCols` (the full pre-prune clause).

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1474→1390 lines):**
hunks d447 (`t1 group by a,b,c,d` → `Group Key: a, b`), d554 (unique-index `z`),
d588 (NULLS NOT DISTINCT) all CLOSED; d538 (p_t1 partitioned) SHRUNK; d508 (t1c
inheritance, must-NOT) unchanged. Must-NOT guards verified fail-closed (deferrable
PK t3, nullable-z, partial index, expression index, grouping sets, <2 items).
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); units PASS; plan-gate SKIP (no
server on 65433).

**Reviewer finding (fixed):** a partitioned parent whose unique key omits a partition
column is NOT globally unique (goopg lacks PG's `indexcmds.c:1093` 0A000 DDL
enforcement). `pruneUselessGroupByColumns` now fails closed when `len(PartitionKey)>0`
and any partition column is absent from `bestKey` — no-op for PG-legal DDL, guards the
goopg-legal-but-PG-illegal case (behavioral-probe CASE A: `UNIQUE(b)` on
`PARTITION BY LIST(a)` returns 2 rows, no collapse). The DDL gap is recorded in the
deferral ledger (partitioned-unique-index enforcement).

**Residuals (next-slice candidates, not this slice):** the multi-relation arm of
`initsplan.c:412` (d463/d1234 — iterate every RTE_RELATION); a stricter-than-PG
`GROUPING()` guard (misses a prune for `GROUPING()` under plain GROUP BY — fail-closed,
unexercised); index tie-break iterates name-sorted `IndexesOnTable` vs PG's OID order
(plan-shape-only).

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
