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

**Residuals (next-slice candidates, not this slice):** a stricter-than-PG
`GROUPING()` guard (misses a prune for `GROUPING()` under plain GROUP BY — fail-closed,
unexercised); index tie-break iterates name-sorted `IndexesOnTable` vs PG's OID order
(plan-shape-only). **The "multi-relation arm of initsplan.c:412" is NOT a closing
residual for aggregates.sql — scoping verified 2026-08-15.** Every remaining
multi-relation GROUP BY divergence (`btg t1 JOIN btg t2 … GROUP BY t1.w,t1.z,t1.x`;
`group_agg_pk c1 JOIN c2 … GROUP BY c1.y,c1.x,c2.x` and its sibling) is over tables
with NO qualifying unique index (`btg`'s indexes are non-unique, `group_agg_pk` is a
`CREATE TABLE AS`). PG closes them via equivalence-class redundancy (dropping the
join-equal `c2.x`) plus pathkey reordering (`Group Key:` emitted in access-path order)
plus `GroupAggregate`-over-Sort — i.e. class 6/9 strategy (S8), not the unique-index
prune. Generalising `pruneUselessGroupByColumns` to iterate every RTE would therefore
close ZERO aggregates.diff hunks; it is correct PG-faithful behaviour but lands with no
observable delta in this case, so it is parked until a case that exercises it (a
multi-relation GROUP BY over a PK'd relation) appears.

## S10a — landed 2026-08-15 (refuse to split user aggregates)

**Design (class 9a correctness bug):** `AggregateIsDecomposable`
(`internal/planner/parallel_agg.go:28`) accepted any user aggregate declaring a
COMBINEFUNC (`call.UserAgg.CombineFunc != ""`), but the executor's
`combineAggRuntime` (`internal/executor/parallel_agg_combine.go:53`) has combine rules
for builtin names only — its `default:` arm errors
`no combine rule for aggregate "balk"`. The `balk` aggregate (`COMBINEFUNC=balkifnull`
+ `PARALLEL=SAFE`, aggregates.sql:1392) therefore split in the planner and errored in
the executor where PG returns one row. Fix: `AggregateIsDecomposable` now returns
`false` for `call.UserAgg != nil` (fail-closed), because `combineAggRuntime` deep-merges
the typed `aggRuntime` fields, not transition-state datums, so there is no rule to invoke
a user combinefunc. Re-pinned `TestAggregateIsDecomposableWhitelist` (the
"user aggregate declaring COMBINEFUNC" case moves from decomposable to refused).

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1390→1387 lines):** the
`+ERROR: internal error: no combine rule for aggregate "balk"` block is gone;
`SELECT balk(hundred) FROM tenk1` returns NULL, byte-matching PG
(`aggregates.out:2928-2932`). `EXPLAIN … balk` now emits a serial `Aggregate → Gather →
Seq Scan` where PG emits `Finalize → Gather → Partial Aggregate → Parallel Index Only
Scan` — the expected out-of-scope plan-shape divergence (S10b user-combinefunc + the
parallel-index-scan gap). Baseline-diff proof: the ONLY content change vs pre-change HEAD
is the balk hunk (error→matching result, parallel→serial plan); no PASS→FAIL regression.
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35 — no-op for builtins);
`go test ./internal/planner/ ./internal/executor/` PASS.

**Deferral (ledger row):** PG splits parallel-safe user aggregates and combines by
INVOKING the user combinefunc (`nodeAgg.c` advance_combine_function); goopg refuses
because `combineAggRuntime` has no user-transition-state datum path. Re-enabling
requires storing each user aggregate's transition state as a datum and invoking
`UserAgg.CombineFunc` in `combineAggRuntime` — S10b.

## S4 — landed 2026-08-15 (class 8: preserve the index range comparison op)

**Scope correction (research refuted the "drop the redundant Filter" premise).** The
class map's S4 line ("drop the redundant Filter") is WRONG: goopg's btree
`rangeScanPos` is inclusive at both ends, so for `WHERE c2 < 100` the scan emits the
`c2 = 100` row and only the Filter drops it — the Filter is executor-necessary, not
render-redundant. The faithful fix is four coupled parts: store the original op, make
the btree scan stop at an EXCLUSIVE bound, render the original op, and drop the Filter
only where the exclusive scan now fully covers the WHERE. (The class map's line refs
are stale: the range builder is `tryRangeIndexScan` `planner.go:9379-9510`, not
"8657-8689" (that is the equality builder); `formatIndexCond` is
`operators_explain.go:851-886`, not "777-812".)

**Shipped:**
- `IndexScan.LowOp`/`HighOp` (`plan.go`) — the original comparison op in canonical
  col-op-key form (zero value = inclusive, backward-compatible).
- `tryRangeIndexScan` captures `LowOp`/`HighOp` and drops the Filter only when the
  WHERE is a single conjunct equal to the folded range conjunct AND the index is
  single-column (`len(chosenIdx.Columns) == 1`) AND the bound is a plain
  literal/param (`isPlainConstantBound` — no volatile FuncCall). This conservative
  subset provably preserves row counts; a volatile bound (`c2 < random()`) and a
  composite index (`btg_y_x_w_idx`) both keep the per-row Filter.
- `rangeScanPos` (`btree.go:3873-3992`) stops at `compareHigh(key, hi) >= 0` for an
  exclusive hi and skips `key <= lo` for an exclusive lo; every other caller
  (bitmap/lpdead) defaults inclusive.
- `formatIndexCond` renders the stored op (`>`/`>=`/`<`/`<=`) instead of the hardcoded
  `>=`/`<=`.
- `tryPromoteIndexOnlyScan` refuses IOS promotion for exclusive-bound scans (Option B:
  `indexOnlyScanOp` is inclusive-only, so a promoted IOS would leak the boundary row).

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 1387→1385 lines, zero
regressions):** the single-column hunk (`agg_sort_order_c2_idx`, `c2 < 100`) now emits
`Index Cond: (c2 < 100)` with no Filter, byte-matching PG; the composite hunk
(`btg_y_x_w_idx`, `y < 0`) keeps its Filter (conservative) but the op is corrected
`<=`→`<`. `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); units PASS; 4 new
row-parity tests (exclusive hi/lo, composite-keeps-filter, volatile-keeps-filter).

**Deferrals (ledger rows):** (1) `indexOnlyScanOp` is inclusive-only — an IOS promoted
from a range scan renders neither Index Cond nor Filter and cannot express exclusivity
(Option A scope); (2) multi-conjunct/composite WHERE keeps the redundant range
conjunct in the Filter (PG trims it via `is_redundant_with_indexclauses`).

### S4 follow-up — the Filter-drop broke UPDATE/DELETE row selection (fixed 2026-08-17)

S4's Filter-drop changed a plan-shape *contract* the executor's DML path silently
depended on, and the nightly caught it two runs later (AI-20260817-011734-002 /
-003: `TestE2E_PG{Cold,Crash}StartOnGoopgDataDir`, both failing on a
`WHERE id = <lit>` point lookup that returned 0 rows *after* an earlier
`DELETE ... WHERE id > 15` had wiped the whole table).

**Chain.** `extractScan` (`internal/executor/operators_storage.go:3211`) turns an
UPDATE/DELETE child plan into `SeqScan + predicate`, because `scanMatching` is
inherently sequential. For an `*IndexScan` child it reconstructs the predicate via
`indexScanPredicate`, which handled *only* `Key != nil` (single-column equality) and
returned `nil` otherwise — safe while every range scan was wrapped in a `Filter`
(the `Filter(*IndexScan)` branch then used the Filter predicate alone). After S4 a
single-conjunct single-column range scan arrives as a **bare** `*IndexScan` with
`Key == nil`, so the reconstruction produced `nil` and `scanMatching` ran with **no
predicate at all** — matching, and deleting/updating, every row of the relation.
Verified directly: `UPDATE t SET qty=0 WHERE id > 15` on a 20-row table updated 20
rows (want 5).

**Fix (executor-side, one place, both DML operators).** `indexScanPredicate` now
reconstructs a predicate for *every* bound shape the planner can emit:
`Key` (equality), `Keys` (multi-column equality probe — also previously nil, same
over-match hazard), and `LowKey`/`HighKey` honoring `LowOp`/`HighOp` for exclusive
bounds. A shared `indexScanColumnRef` helper does the output-ordinal lookup. `nil`
is now returned only on catalog inconsistency or a genuinely bound-less scan (where
"match everything" is the correct answer). The planner was deliberately NOT touched:
gating the Filter-drop to SELECT would have kept the executor's fragile contract.

**Invariant to preserve:** any new `IndexScan` bound field that restricts which
rows the scan returns MUST be reconstructed here — the doc comments on both
`extractScan` and `indexScanPredicate` now say so, and `nil` is documented as
"matches every row", not as "safe fallback".

**Gates:** `internal/executor` + `internal/optimizer` PASS; the two E2E tests PASS;
`RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2,
Q13=35). New regression file `internal/executor/range_dml_predicate_test.go` covers
`>`, `>=`, `<`, `<=`, `BETWEEN` and multi-column equality for both DELETE and
UPDATE, asserting affected-row counts *and* that untouched rows keep their values;
it pins the bug-triggering plan shape (bare `*IndexScan`, no `Filter`) and was
verified FAIL-pre (RowsAffected=10 of 10 on every range case) / PASS-post.

## class 10 (VERBOSE `Output:` qualification) — blocked by the varno deferral (2026-08-15)

The class-10 qualification gap is NOT a self-contained formatter fix. A slice
adding SRF cases to `explainRelBaseName` + qualifying the VERBOSE `Output:` line
was implemented, verified correct, and then REVERTED with zero gate impact
(`aggregates.diff` 1370→1370) because the `aggregates.sql` SRF hunks are all
LATERAL/subquery shapes: the inner subquery's `SourceTableIdx` restarts at 1 per
query level (`planner.go` `nextSourceIdx`), so `generate_series s2` collides with
the outer `s1` and `explainNames.collect`'s `seen` guard registers only one —
`qualify()` stays false and every column stays bare. Sort/Join additionally zero
`SourceTableIdx` on columns crossing the subquery boundary. This is the
M0125-0039 deferral (ledger row 615 "`SourceTableIdx` is not a range-table id";
row 616 (b) `schemaColumnNames` never reaches the expression printer). Closing
class 10 needs the planner-level per-statement `nextSourceIdx` promotion (ledger
615 resume point), not formatter work. The S2 "Out of scope" note's "needs SRF
cases in `explainRelBaseName`" was therefore under-scoped — SRF cases alone close
only the same-level multi-SRF shape (two SRFs in one FROM clause), which no
`aggregates.sql` query exercises.

## S3 — landed 2026-08-15 (class 7a: PG-interpolated join labels)

**What changed.** goopg spelled the join type in a parenthetical —
`Hash Join (INNER)`, `Nested Loop (SEMI)`, `Nested Loop (CROSS)`, `Hash Join
(INNER, build=left)`, `Hash Join (ANTI NULL-AWARE)` — while PG interpolates it
into the node name. Rewrote `describePlan`'s `case *planner.Join` and
`case *planner.NestedLoopIndexJoin` to call one `joinLabel(algo, joinType)`
helper (`operators_explain.go`) that mirrors `explain.c` exactly: base words are
`Nested Loop`/`Hash`/`Merge` (pname, 1421-1430); a non-INNER jointype appends
` <Type> Join` and an INNER appends ` Join` unless NestLoop (1754-1758); CROSS
folds to INNER (`parse_clause.c`) so a cross join renders as bare `Nested Loop`.
The `build=left` and `NULL-AWARE` label annotations are dropped — PG never
emits them; the `BuildLeft`/`NullAware` planner fields remain (executor
consumes them). `joinTypeName` is deleted; the old `(SEMI)` "tracked separately"
comment is retired.

**Twin paths covered in the same slice.** `estimateaudit`'s `isJoinLabel` parser
already classified both spellings (`joinLabels` + `upstreamJoinPrefixes`), so no
parser code changed — only its comment (audit.go:64) and the golden fixtures in
`audit_test.go`/`parity_test.go`/`spine_test.go`. The legacy parenthetical
spelling stays a classified input by design (committed leftdeep-joins plan
captures predate the relabel).

**Measured result (`scripts/pg-regress-runner.sh aggregates`, 764→746 lines):**
zero label-spelling hunks remain; the surviving join hunks are join-ALGORITHM
choice (class 7b / S9, cost-model line) plus column-qualification.
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); units PASS; `TestJoinLabelPinsPGLabels`
pins all nine (algo, type) spellings against the explain.c rule.

**Out of scope by design:** join-algorithm choice is S9 (class 7b), a cost-model
slice, untouched here.

## S8 Slice 1 — landed 2026-08-15 (sorted/GroupAggregate executor capability)

**Design.** class 6's executor half: goopg's grouped aggregate is hash-only
(`aggregateOp.Open` builds `groups := map[string]*groupRuntime{}`), so it can
never emit PG's `GroupAggregate` (AGG_SORTED). This slice lands the sorted
capability but is **planner-inert** — nothing sets `Strategy`, so every
existing query keeps the hash path byte-identically. The planner/pathkey
strategy choice is Slice 2.

**Shipped (`operators_join_agg.go`, `plan.go`, `operators_explain.go`):**
- `Aggregate.Strategy` (`internal/planner/plan.go`): `AggStrategy` type with
  `AggStrategyHashed` (iota, zero value) and `AggStrategySorted`. The zero
  value is Hashed, so every existing construction site and test fixture is
  untouched.
- `openSorted` (`operators_join_agg.go`): a materialised run-collapsing walk
  gated on `Strategy == Sorted && GroupingSets == nil && len(GroupExprs) > 0 &&
  Mode == AggModeSimple`. It drains the child (as the hash path does), then
  collapses runs of equal keys — group identity is `sameGroupKey`, an
  element-wise comparison of the per-column `datumKey` vector — finalising and
  emitting one row per key run in input order. It reuses `evalGroupExprs`,
  `applyAgg`, and the shared finalize step.
- `finalizeGroup`: the hash path's emit-loop body (shared-state follower sync,
  `finishAgg`, GROUPING masks, passthrough) extracted verbatim into one method
  used by BOTH paths, so the two strategies cannot diverge on those semantics.
  `groupRuntime` moved package-level for the signature.
- EXPLAIN `describePlan` Aggregate else-branch now returns
  `prefix + "GroupAggregate"` when `Strategy == AggStrategySorted`, else
  `prefix + "HashAggregate"` (explain.c:1531-1553 AGG_SORTED→GroupAggregate).
  Grouping-sets / ungrouped branches unchanged.

**Tests (`operators_join_agg_sorted_test.go`):** `TestAggSortedGrouping`
(multi-column key, adjacent equal keys, output order = key order),
`TestAggSortedNullKey` (NULL keys collapse), `TestAggSortedHashParity` (sorted
== hash on the same input, identical rows in identical order — the M0097-0117
hash pre-sort makes them agree), `TestAggSortedEmptyInput` (0 rows, no panic),
`TestExplainAggregateStrategyLabel` (GroupAggregate/HashAggregate/Aggregate).

**Measured result:** `scripts/pg-regress-runner.sh aggregates` — aggregates.diff
byte-identical to clean-HEAD pre-S8 (no new hunks; the prior loop's "746" was a
stale measurement, the current baseline is larger and unchanged by S8).
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35). `go test ./internal/executor/
./internal/planner/` PASS.

**Discovery (deferral-ledger row):** the hash path's `setGroupKey` string-join
of `datumKey` outputs can collide for text/bytea GROUP BY keys containing the
`|s:`/`|x:` boundary (two distinct groups merge). The sorted path's element-wise
`sameGroupKey` is collision-free, so hash-vs-sorted would diverge on such keys
once the planner chooses strategy — recorded for Slice 2.

**Remaining for S8 (Slice 2):** the planner half — group-key pathkeys
(`pathkeysForSortKeys`/`pathkeysContainedIn` over the existing `PathKey` model),
`Sort` emission when the child isn't presorted, the hash-vs-sort strategy choice
(PG `add_paths_to_grouping_rel` + `cost_agg` AGG_SORTED/AGG_HASHED arms), and
reordering `GroupExprs` into the sort/pathkey order (which moves both the
EXPLAIN `Group Key:` line and the output order together). Load the
`executor-planner-change` + `perf/TPC-H` practice cards; bound it per the
M0072-0002 hang trap.

## S8 Slice 2a landed (presorted aggregates, 0134-0001-s8-s2a)

The presorted-aggregate planner rule (`adjust_group_pathkeys_for_groupagg`,
planner.c:3229) landed in `internal/planner/groupagg_presorted.go`: when an
aggregate has an internal ORDER BY / DISTINCT clause, the planner now picks the
covering set of pathkeys (greedy "most aggregates, tiebreak by position"),
wraps the Aggregate child in a `Sort`, and flips grouped queries to
`AggStrategySorted` (EXPLAIN shows `GroupAggregate`). Gated on the
`enable_presorted_aggregate` GUC (default on). The executor half (Slice 1,
`openSorted`/`applyAgg` per-group sort) predated it and is planner-inert; the
per-group sort means presorted input changes only the plan shape, never the
values.

This closes the aggregates.sql EXPLAIN hunks for the non-grouped
`four`/`two`/`four`/`ten` sorts, the grouped `ten, two, four` and
`ten, four, two` sorts (volatile `random()` ORDER BY aggregates excluded via a
`has_volatile_pathkey` port), the `SET enable_presorted_aggregate to off` no-sort
case, the FILTER `two`/`f1` presorts (with the `f1::varchar(2)` explicit-cast
no-presort kept distinct via a RelabelType-only strip), and the
`group_agg_pk` / `agg_sort_order` Sort Key lines. Residual diffs at last
measurement: the goopg EXPLAIN `QUERY PLAN` underline width (pre-existing,
cosmetic, affects every plan), the `group_agg_pk` join shape (Merge vs Hash —
out of scope), and the deferred Rule-3 GroupExprs reordering + parallel-aggregate
gap (`btg`, `v_pagg_test` — partial closures only).

Pathkey redundancy uses PG's two cases (constant via a plain-literal test, and
duplicate-expression), ported in `internal/planner/pathkeys.go`
(`appendPathKeys`/`makeCandidatePathkeys`/`isPlainConst`). The constant test is
deliberately narrower than `isConstantExpr`: `random()` must survive to the
greedy's volatility check instead of being dropped as "row-constant".

## S8 Slice 2b landed (enable_hashagg bridge, 0134-0001-s8-s2b)

`enable_hashagg` was already registered as a GUC (`internal/config/defaults.go:1035`)
but had no effect. This slice reproduces the cost-model *outcome* of
`SET enable_hashagg = off` directly (goopg has no cost model to disable the
AGG_HASHED arm as `cost_agg` does — `postgres/src/backend/optimizer/path/costsize.c:2755-2756`).

`applyEnableHashAggRule` (`internal/planner/groupagg_hashagg.go`, new) mirrors the
Slice 2a bridge one-to-one: `hashAggEnabled atomic.Bool` (init true) +
`SetHashAggEnabled` test toggle, an `enable_hashagg` OnChange bridge in
`cmd/goopg/main.go` next to `enable_presorted_aggregate`, and a call in
`planner.go:1290` immediately after `applyPresortedAggregateRule`. When the GUC is
off, a plain grouped aggregate (still `AggStrategyHashed`, no grouping sets,
`len(GroupExprs) > 0`, `Mode == AggModeSimple`) is wrapped in an ascending `Sort`
(one key per `GroupExprs`) and switched to `AggStrategySorted` — the
`aggregates.out:3457` shape (`GroupAggregate` → `Sort` → `Seq Scan`).

The five gate conditions mirror the executor's `openSorted` routing gate exactly
(`operators_join_agg.go:1979-1980`), so the `GroupAggregate` EXPLAIN label never
lies about the execution path. Correctness of the sort placement is traceable:
the rule runs before Gather insertion (`applyParallelPostPass`), and `drivingScan`
(`parallel.go:344-369`) does not descend through a `*Sort`, so a wrapped child can
never be split into Partial/Final — it is either a full serial `Sort` or a parallel
`Gather Merge → Sort` whose per-worker sort + identical-key merge deliver a globally
group-key-sorted stream to `openSorted`.

Residual divergences (recorded in the deferral ledger): Rule 3 (index-ordered
input — skip the redundant `Sort` and reorder `GroupExprs`, the `btg`/
`group_agg_pk`/`agg_sort_order` shapes) is still unimplemented, and
`enable_seqscan = off` is not honored; a parallel-eligible table adds a
`Gather Merge` layer PG suppresses by cost (correctness-safe, EXPLAIN-shape only);
and a redundant-outer-parens rendering gap (`(g % 10000)` vs `((g % 10000))`).

## S8 Slice 2c — Rule 3 (index-ordered grouping input): scope + decomposition

Status: `accepted` (design), Slice 2c-i implemented; 2c-ii/2c-iii deferred with
ledger rows.

Slice 2b's residual paragraph above named "Rule 3" as one unit and attributed the
`btg` / `group_agg_pk` / `agg_sort_order` shapes to it. Measurement at HEAD
(`PATH="$PWD/postgres/local_install/bin:$PATH" scripts/pg-regress-runner.sh
--verbose aggregates` ⇒ `tmp/regress-diffs/aggregates.diff`, 1311 lines / 44
hunks / 661 changed lines) shows that attribution was **too broad**. The three
"Rule 3" hunks decompose as follows, and only a part of them is Rule 3:

| hunk | queries | actually caused by |
|---|---|---|
| `btg` block (`aggregates.sql:1266-1314`) | 7 EXPLAINs | Rule 3 proper (all 7), plus goopg having **no Incremental Sort node at all** (5 of the 7), plus a duplicate residual `Filter` alongside an `Index Cond` (1) |
| `group_agg_pk` block 1 (`avg(... ORDER BY ...)`) | 1 EXPLAIN | **not Rule 3** — goopg's `Group Key`/`Sort Key` lines already match PG byte-for-byte; the sole divergence is goopg picking a Hash Join while `enable_hashjoin`/`enable_nestloop` are off |
| `group_agg_pk` block 2 | 2 EXPLAINs | Rule 3 **plus** a join-aware functional-dependency GROUP BY reduction (PG drops `c2.x` given `c1.x = c2.x` and `c1.x` already grouped), plus the same join-GUC gap |
| `agg_sort_order` (`aggregates.diff:1147`) | 1 EXPLAIN | **not Rule 3, not a plan divergence at all** — every plan line is byte-identical; the hunk is only the pre-existing `QUERY PLAN` underline-width cosmetic gap. The Slice 2b residual paragraph naming it was aspirational. |

### What PG actually does (oracle)

`postgres/src/backend/optimizer/path/pathkeys.c:466-550`
`get_useful_group_keys_orderings` — note the Slice-2b-era citation pointing at
`planner.c` is wrong: only the *call sites* are in `planner.c` (`:7144`, inside
`create_ordered_paths` / `add_paths_to_grouping_rel`); the body is in
`pathkeys.c`. It always returns the original GROUP BY ordering, and returns at
most **one** alternate — it does not enumerate permutations. The alternate comes
from `group_keys_reorder_by_pathkeys` (`pathkeys.c:375-450`): walk the *input
path's* own pathkeys in order, move each one that matches a GROUP BY clause to
the front of the reordered list, stop at the first pathkey with no matching group
key, and append the leftover group keys unsorted. It is kept only if it matched
≥1 prefix key, differs from the original, and (`enable_incremental_sort` is on OR
the entire group key list matched). With `GROUPING SETS`, or
`enable_group_by_reordering = off`, the original ordering is the only one. There
is no "no index matched" special case — cost comparison over the returned
orderings simply falls back to the original, which is what forces the full
`Sort`, i.e. exactly today's Slice 2a/2b behaviour.

**The direction is inverted for goopg.** PG reorders group keys *to match a path
that already exists* (the index path was enumerated by the scan-path machinery
and carries pathkeys). goopg's optimizer is rule-based with no path enumeration,
so Rule 3 must run the search the other way: take the group keys, ask
`cat.IndexesOnTable` (`internal/catalog/catalog.go:20351` / `:23597`, the same
accessor `tryPromoteOrderedIndexOnlyScan` uses at
`internal/optimizer/planner.go:13348`) for a usable btree index, and test whether
the group keys can be ordered to match that index's leading columns. Index
usability filters must mirror `tryPromoteOrderedIndexOnlyScan` exactly
(`planner.go:13349-13375`): `Method == "btree"`, not `HasPredicate`, not
`DeclaredHash`, and every consumed column default-ordered
(`!ColDescending[i] && !ColNullsFirst[i]`).

### The pinned correctness risk — `GroupExprs` order is load-bearing

`Aggregate.GroupExprs` order is **not** cosmetic. `buildAggregateStage`
(`internal/optimizer/planner.go:6486-6539`) assigns each GROUP BY item's output
slot as `idx := len(outputSchema)` (`:6514`) in GROUP BY clause order and records
`groupByExpr` / `groupByInputCol` (`:6535-6539`); every downstream target-list,
HAVING and ORDER BY `ColumnRef` is bound to that position, and this happens
*before* the Rule-2a/2b/3 dispatch point at `planner.go:1292`. At runtime
`groupRuntime.groupValues[i]` (`internal/executor/operators_join_agg.go:1828-1836`,
`finalizeGroup`) emits the value of `GroupExprs[i]` at output column `i`. So an
uncompensated in-place permutation of `GroupExprs` **moves data between output
columns**, silently — the worst failure class this project has (Hard-won Rule #2:
the EXPLAIN label and the executor's column mapping are a sibling pair and must
change together).

PG has the same decoupling and solves it the same way: in the `btg_y_x_w_idx`
verbose case PG prints `Group Key: btg.y, btg.x` (reordered to the index) while
`Output: y, x, array_agg(DISTINCT w)` stays in the written projection order.

**Design decision — do not permute `GroupExprs` at all.** The reorder is only
ever needed for two things: deciding that the index's ordering satisfies the
grouping, and printing PG's `Group Key:` line. It is *not* needed by the
executor. A sorted `GroupAggregate` detects a group boundary as "any group key
value differs from the previous row" (`finalizeGroup` /
`operators_join_agg.go:1828-1836`), which is order-independent: an input sorted
by `(x, y)` makes rows with equal `(y, x)` contiguous just as well. So
`GroupExprs` keeps its written order — output column identity, the positional
bindings from `buildAggregateStage`, and `groupValues[i]` are all untouched, and
the data-movement failure mode above cannot occur by construction.

Rule 3 therefore adds a permutation *alongside* `GroupExprs` — indices into it,
in index-column order — consumed only by the EXPLAIN `Group Key:` renderer. This
is a strictly smaller change than a compensating output permutation (the
posMap-style rewrite `remapAggExprsWithBindings` performs at this same dispatch
point, `planner.go:1276-1282`), which stays the documented fallback if the
order-independence premise turns out not to hold — it is an explicit
implementation gate, verified by a test that groups on a permuted key order and
asserts the *result rows*, not just the plan text.

### Decomposition

- **Slice 2c-i — full-prefix match, Sort elimination (THIS SLICE).** Fires when
  the group keys are all plain column references over a bare `*SeqScan` (or
  `*Filter{Child: *SeqScan}`) child and *some* ordering of them is exactly a
  prefix of a usable btree index's `Columns`. Effect: reorder `GroupExprs` to the
  index order (with the output permutation above), replace the child with the
  ascending full-range `*IndexOnlyScan` / `*IndexScan`
  (`plan.go:794-823`, constructed as at `planner.go:13415-13421` — nil
  `Key`/`Keys`/`LowKey`/`HighKey` ⇒ the executor RangeScans the whole index in
  ascending key order), and set `AggStrategySorted` **without** inserting a
  `*Sort`. Runs *before* `applyPresortedAggregateRule` / `applyEnableHashAggRule`
  so those never get the chance to wrap the child in a redundant `Sort`.
  Closes `btg` query (i) (`GROUP BY y, x` ⇒ `Index Only Scan using btg_x_y_idx`,
  no Sort) outright. Measured: aggregates diff 1311/44/661 → **1296/44/651**.

  **Accepted deviation — the rule is additionally gated on `enable_hashagg` being
  off** (`hashAggEnabled.Load()`, the same kill-switch Slice 2b's
  `applyEnableHashAggRule` reads). This was not in the original design and was
  forced by measurement. The gate exists because goopg has no cost model: PG
  reaches this plan by comparing a `HashAggregate` path against an index-ordered
  `GroupAggregate` path (`cost_agg`, `costsize.c:2755-2756`) and picking the
  cheaper, whereas a goopg rule can only fire or not fire. Firing
  unconditionally regressed the case to 1386/47/687, because it also promoted
  `aggregates.sql`'s primary-key functional-dependency block (`t1 GROUP BY
  a,b,c,d`, reduced to `a,b` by an earlier pass, `enable_hashagg` left ON) where
  PG correctly prefers `HashAggregate`. Every `btg` probe this slice targets runs
  inside `SET enable_hashagg = off` (`aggregates.sql:1275-1370`), so the gate
  costs no coverage here — but it does mean the rule is dormant in ordinary
  queries, i.e. this reproduces PG's *output* without PG's *reasoning*. That is
  the same objection this doc raises against gating on `enable_seqscan`, and it
  is accepted here only because the alternative is a measured regression. The
  real fix is a cost comparison between the hashed and index-ordered arms;
  recorded in the deferral ledger, and it is the natural first consumer of the
  cost-model work in `docs/design/cost-model/`.
- **Slice 2c-ii — partial-prefix match ⇒ Incremental Sort. DEFERRED.** Five of
  the seven `btg` queries need PG's `Incremental Sort` node (`Presorted Key:`),
  which goopg does not have in any form — neither planner node nor executor
  operator. That is a new subsystem, not a slice of this rule; it needs its own
  design doc and its own fix_plan item. Until it exists, a partial prefix match
  is deliberately left to fall through to the Slice 2a/2b full `Sort`, which is
  precisely PG's own fallback when `enable_incremental_sort` is off
  (`pathkeys.c:509-511`).
- **Slice 2c-iii — ORDER-BY-aware ordering choice. DEFERRED.** `btg` query (v)
  (`GROUP BY w,x,z,y ORDER BY y,x,z,w`) shows PG choosing the group-key ordering
  that also satisfies the outer `ORDER BY`, so the grouping `Sort` doubles as the
  ORDER BY sort and the plan has one Sort instead of two. This is a second
  ordering candidate on top of 2c-i's index-driven one and is independent of it.

Explicitly **out of Rule 3's scope**, recorded so a later loop does not re-derive
the attribution: `enable_hashjoin` / `enable_nestloop` are not honored (owns all
of `group_agg_pk` block 1 and part of block 2); the join-aware
functional-dependency GROUP BY reduction PG applies via
`remove_useless_groupby_columns`-adjacent logic (block 2); the duplicate residual
`Filter` emitted alongside an `Index Cond` by
`rewriteScanInputsWithSingleTablePredicates` (`btg` query vii); and the
`QUERY PLAN` underline-width cosmetic gap (`agg_sort_order`, and every plan).

### `enable_seqscan = off`

Bridged, but narrowly. The GUC reaches the optimizer as `wrapped.DisableSeqScan`
(`internal/postmaster/dispatch.go:1551-1557`, `:1614-1617`) and is consumed only
by `tryPromoteOrderedIndexOnlyScan` (`internal/optimizer/planner.go:13292-13320`,
gate at `:13318`), which fires exclusively for `Project(Sort(bare *SeqScan))`
shapes (design `0118-0103`) — never for an `Aggregate`. The comment at
`planner.go:13296-13300` states the general position: goopg's rule-based planner
ignores the planner-toggle GUCs outside that one gated promotion. Slice 2c-i does
**not** gate on `DisableSeqScan`: PG picks the index here by cost, and the regress
case merely uses the GUC to make that choice deterministic, so gating on it would
reproduce the output for the wrong reason and leave the rule dead in normal
queries.

### Path-name correction

Earlier sections of this doc cite `internal/planner/pathkeys.go`,
`internal/planner/groupagg_presorted.go` and `internal/planner/groupagg_hashagg.go`.
There is no `internal/planner` package — all three files live in
**`internal/optimizer/`**. The symbols named are correct; only the directory is
wrong.

## S11 — landed 2026-08-17 (PG-faithful cumulative EXPLAIN indentation)

Status: `accepted`. Closes the item this doc has carried since Slice 2b as the
"`QUERY PLAN` underline-width cosmetic gap (`agg_sort_order`, and every plan)".

**That characterisation was wrong, and the correction is the finding.** The
underline was only the visible symptom; the underlying defect was a *plan-text*
divergence that the regress harness was structurally blind to.

- **goopg (before):** `walkPlanFiltered` (`internal/executor/operators_explain.go`)
  and its ANALYZE twin `walkPlanAnalyzeFiltered` computed the prefix as
  `strings.Repeat("  ", depth)` — a pure function of tree depth.
- **PG (`postgres/src/backend/commands/explain.c:1616-1635`, `ExplainNode`):** a
  *stateful, cumulative* `es->indent`, emitted as `es->indent * 2` spaces by
  `ExplainIndentText`. Per node, in TEXT format: (1) if a `plan_name` label is
  present, indent-text, print the label, `es->indent++`; (2) if `es->indent != 0`,
  indent-text, print `"->  "`, `es->indent += 2`; (3) print the node name, then
  **unconditionally** `es->indent++`.
- **Net:** the `->` marker sits at raw columns 0, 2, 8, 14, 20 … (deltas 2, 6, 6),
  not goopg's 0, 2, 4, 6. The two models **coincide at depth 0 and depth 1**,
  which is exactly why the whole S8 Slice-1/2a/2b line (2-level
  `GroupAggregate → Sort → SeqScan` shapes) rendered byte-identical and the bug
  stayed invisible through five landed slices.

**Why the diff hid it.** `scripts/pg-regress-runner.sh`'s `normalise_output()`
collapses any run of 2+ *spaces* to exactly 2, so both sides' content lines
flatten to the same indentation regardless of true depth. The `-----` underline
is dashes, not spaces, so it survives — and real `psql` computes it from the RAW
widest cell (`postgres/src/fe_utils/print.c`, `print_aligned_text`). The dash
count was therefore the *only* channel through which a content-level divergence
could reach the diff. Filed as a ledger row: the regress gate cannot see
whitespace-layout divergence at all.

**Implementation.** Both walkers take an explicit `indent` unit counter threaded
as `es->indent` is (raw spaces = `indent*2`), with PG's save/restore discipline
across recursion — *not* a formula derived from `depth`, which breaks the moment
a `plan_name` label appears. Converted call sites (exhaustive): both wrapper-skip
recursions (Project/Filter, indent passed through unchanged), the CTE-section
callback, the SubPlan-subtree callback, and the single normal-children loop per
walker (which covers Append/MergeAppend/Hash/join children).

**Two corrections found while implementing:**

1. **The CTE/InitPlan `plan_name` branch bumps indent by only +1, not +3.** The
   body's own `"->  "` supplies its own +2. Verified against live PG output for
   `WITH x AS (…) SELECT … JOIN`, and cross-checked against
   `postgres/src/test/regress/expected/rowsecurity.out:3333-3336` and
   `subselect.out:1350-1373`: the CTE body's `->` sits at **4** raw spaces.
   `explain_cte_test.go`'s existing assertion was therefore already correct and
   was kept (only its comment updated to cite `ExplainNode`). A brief that
   predicted 8 was wrong; the oracle capture overruled it.
2. **The VERBOSE `Output:` line carried a second, independent indent bug** — it
   used an ad-hoc `indent + "  "` formula and was wrong at every depth ≥ 1
   regardless of this defect. It now uses `detailIndent` like every other detail
   line.

**Measurement (`scripts/pg-regress-runner.sh aggregates`):** diff lines
**1296 → 1096**, hunks **44 → 30**, dash-only hunks **14 → 0** (eliminated, with
no new dash-only hunks appearing elsewhere). The 30 remaining hunks are
orthogonal — parallel-query plan shapes (`Gather` / `Parallel Seq Scan` /
`Finalize` / `Partial Aggregate`), i.e. S10.

**Guards:** `TestExplainIndentDeepNesting`, `TestExplainAnalyzeIndentDeepNesting`
(the twin — the flat model was present in both walkers and a test on one proves
nothing about the other) and `TestExplainIndentInitPlanBranch` (the `plan_name`
branch, the case a depth-formula gets wrong), all in
`internal/executor/explain_indent_test.go`, with expectations taken from
verbatim PG 18.3 captures rather than transcribed.

**Cross-case leverage:** this is an emitter fix, so every M0134 case that emits
a nested `EXPLAIN` inherits it — `explain.sql` (M0134-0017) most directly.

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
