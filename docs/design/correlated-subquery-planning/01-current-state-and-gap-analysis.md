# 01 — Current State and Per-Query Gap Analysis

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| part of | [correlated-subquery-planning bundle](README.md) |
| evidence | [evidence/explain-head-e4a43ba6.txt](evidence/explain-head-e4a43ba6.txt), [evidence/unnest-probes-e4a43ba6.txt](evidence/unnest-probes-e4a43ba6.txt) |

This chapter is the evidence base for the bundle: what subquery machinery goopg
has today, what actually fires at HEAD, and how far each TPC-H
correlated-subquery query is from PostgreSQL 18.3. Every factual claim carries
an evidence tag:

- `[measured-at-HEAD e4a43ba6]` — captured 2026-07-19 on branch
  `wal-pg-nodetree` against the SF1 bench data dir (evidence files above).
- `[plan-compare-260718 @701a5f57]` — from
  `analysis/tpch/goopg-pg-tpch-plan-compare-260718/` (on `origin/master`,
  commit `be4f0291`; not present on branch `wal-pg-nodetree` at time of
  writing). The two hashes differ deliberately: `@701a5f57` is the goopg
  build the artifact *measured*; `be4f0291` is the `origin/master` commit
  *containing* the artifact.
- `[measured-at-HEAD e4a43ba6]` claims dated 2026-07-20 come from the
  adversarial-review probe session
  ([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md)):
  throwaway capped server, `t1`/`t2` micro-fixtures, PG 18 oracle side-by-side.
- `[report-2026-05-06]` — `analysis/tpch-runner-measurement-report-2026-05-06.md`.
- `[verification-2026-05-07]` — `analysis/tpch-m0058-verification-2026-05-07.md`.
- `[timings-20260616]` — `analysis/tpch-sf0.5-query-timings-20260616.md`.

Claims made only by older artifacts and not re-verified at HEAD are stale
candidates; §4 shows several were in fact stale in both directions.

---

## 1. Existing planner machinery

### 1.1 Subquery expression types

The planner represents sublinks as expression nodes carried inside ordinary
predicates (`internal/planner/plan.go`):

| type | location | meaning | key fields |
| --- | --- | --- | --- |
| `InExpr` | plan.go:229 | `x [NOT] IN (subquery or list)`, `ANY`/`ALL` | `Operand`, `Negated`, `Plan Node`, `List []Expr`, `IsNonCorrelated` |
| `ExistsExpr` | plan.go:259 | `[NOT] EXISTS (subquery)` | `Negated`, `Plan Node`, `IsNonCorrelated` |
| `SubqueryExpr` | plan.go:313 | scalar sub-SELECT | `Plan Node`, `IsNonCorrelated` |
| `ArraySubqueryExpr` | plan.go:321 | `ARRAY(SELECT ...)` | — |

Correlation is not stored on these nodes. It is represented structurally: a
`ColumnRef` inside the sub-select that resolves up the parent scope chain
becomes an `OuterColumnRef{Level, Index, Name, Type, SourceTableIdx}`
(plan.go:437) embedded in the inner `Plan`. `Level` is the 1-based analog of
PostgreSQL's `varlevelsup`; `SourceTableIdx` disambiguates self-join outer
references (Q21). `IsNonCorrelated` is computed at creation as "no
`OuterColumnRef` anywhere in `Plan`". The pair type `unnestParam{OuterRef,
SubCol}` (plan.go:452) carries one extracted correlation equality during
decorrelation.

This is the equivalent of PostgreSQL's SubLink-with-Vars representation
*before* `SS_replace_correlation_vars` turns outer Vars into `PARAM_EXEC`
Params (see [02-pg-target-architecture.md](02-pg-target-architecture.md) §3);
goopg has no Param stage — `OuterColumnRef` survives to execution and is
evaluated against a runtime stack of outer rows (§2.1).

### 1.2 The decorrelation pass and its pipeline position

All decorrelation lives in `unnestSubqueriesInPlan`
(`internal/planner/unnest.go:9-86`), called once from the `planSelect`
pipeline at `internal/planner/planner.go:945` — **after** predicate pushdown
and the bushy join-order DP (`tryBushyDP`, `internal/planner/bushy.go:66`),
and **before** the `MultiHashJoin` collapse (`rewriteMultiWayChain`,
planner.go:951) and the index nested-loop rewrite (`rewriteJoinsToNLI`,
`internal/planner/nl_index_join.go:72`, planner.go:966). PostgreSQL performs
sublink pull-up *before* join planning (`pull_up_sublinks`,
`postgres/src/backend/optimizer/prep/prepjointree.c`); goopg performs it
after. The consequences are analyzed in
[03-planner-decorrelation-extensions.md](03-planner-decorrelation-extensions.md)
(D3.0/D3.1) and the reorder is phase S5.

The pass recurses through `Filter`/`Join`/`Project`/`Aggregate`/`Sort`/`Limit`
nodes, but the three pull-up loops run **only when the current node is a
`*Filter`**, against the top-level conjuncts of `Filter.Predicate`
(unnest.go:14-67). Predicates that pushdown has already merged into
scan-embedded filter fields or join predicates are never visited by the
pull-up loops (they are only visited by `walkSubqueryPlansInExpr`,
unnest.go:94, which recursively optimizes inner plans but does not pull
anything up). This positional property is central to the §4 findings.

The three loops, in order:

1. **Correlated scalar aggregate** (M0054-0008): `findSubqueryInExpr` →
   `unnestSubquery` (unnest.go:952). Gate `canUnnestSubquery` (unnest.go:177):
   inner plan must be an `Aggregate` (optionally under one `Project`) with
   exactly one aggregate, no `Star`/`Distinct`, and `collectUnnestParams`
   (unnest.go:202) must account for **every** `OuterColumnRef` as an `OpEq`
   equijoin pair (`extractEquijoinPair`, unnest.go:233) — any outer reference
   outside an equality bails (unnest.go:≈227). Rewrite: GROUP BY over the
   correlation columns + INNER hash join (`buildUnnestedSubquery`,
   unnest.go:452). Note this exceeds PostgreSQL, which never decorrelates
   correlated scalar sublinks (see ch.02 §7).
2. **IN / NOT IN** (M0040-0002, M0069-0005, M0097-0094, M0122-0011):
   `findInExprInExpr` → `unnestInExpr` (unnest.go:1296). Correlated IN →
   `JoinTypeSemi`, correlated NOT IN → `JoinTypeAnti`; non-correlated IN →
   semi join (`unnestNonCorrelatedInExpr`, unnest.go:1418); non-correlated
   NOT IN → anti join with `NullAware=true` for three-valued NULL semantics.
3. **EXISTS / NOT EXISTS** (M0061-0001): `findExistsExprInExpr` →
   `unnestExistsExpr` (unnest.go:1976). Gate `canUnnestExistsExpr`
   (unnest.go:1924); `collectExistsUnnestParamsAndResiduals` (unnest.go:1833)
   splits inner conjuncts into equijoin params and outer-referencing non-equi
   residuals lifted onto the join predicate. Non-correlated EXISTS is
   deliberately kept as a cached SubPlan (comment at unnest.go:≈1916).
   Nested un-unnestable sublinks inside the EXISTS body are rejected outright
   (`hasNestedSub`, unnest.go:1937).

### 1.3 Semi/anti join infrastructure

`JoinType` includes `JoinTypeSemi` (plan.go:689) and `JoinTypeAnti`
(plan.go:693) on the ordinary `Join` node (plan.go:725), produced **only** by
the unnesting loops, always with `Algo == JoinAlgoHash`. `Join.NullAware`
(plan.go:751) marks the NOT-IN anti join. The bushy DP excludes semi/anti
joins from several rewrites (bushy.go:1352-1360, 1941-1966, 2063-2074), and
`remapOuterRefsInSubplan` (bushy.go:1689) handles outer-reference index
remapping. The NLI rewrite deliberately skips semi joins (comments at
unnest.go:≈1364/≈1434) — there is no index-driven or merge semi/anti join.

---

## 2. Existing executor machinery

### 2.1 SubPlan evaluation sites

Un-unnested sublinks are evaluated per outer row by three sites in
`internal/executor/expr.go` (dispatch at expr.go:387-399); correlation is
resolved by pushing the outer row on the `Context.OuterRows` lexical stack
(`internal/executor/context.go:91-97`) and evaluating `OuterColumnRef`
against `OuterRows[len-Level]` (expr.go:≈372):

| site | location | shape served | per-miss cost |
| --- | --- | --- | --- |
| `collectInValues` | expr.go:6674 | `IN (subquery)` | full `Build → Open → drain → Close` of the inner operator tree |
| `evalExistsExpr` / `existsImpl` | expr.go:6784 / 6827 | `[NOT] EXISTS` | `Build → Open → Next(1) → Close` per call (early-out `maxDrain=1` for `lockRowsOp`, M0100-0005) |
| `evalSubquery` / `subqueryImpl` | expr.go:6860 / 6912 | scalar | three-path, see below |

Rebuilding the operator tree per evaluation is the goopg-specific overhead
PostgreSQL does not have: PG builds the subplan PlanState once and *rescans*
it with new parameter values (`ExecScanSubPlan`,
`postgres/src/backend/executor/nodeSubplan.c`; ch.02 §4). Attacking this is
[04-subplan-execution-engine.md](04-subplan-execution-engine.md) (D4.1/D4.2),
phase S2.

Scope note: these three are the *primary* but not the only Build-per-row
surfaces. Correlated row-comparison subqueries (expr.go:6521, :6609),
`evalArraySubquery` (expr.go:7135-7141) and `evalMultiAssignSubqRow`
(expr.go:7191) also rebuild per evaluation, and the nested-loop join fallback
pushes its left row as an outer scope per iteration
(operators_join_agg.go:270) without being a SubPlan site at all. Ch.04 states
explicitly which of these its design covers and which remain stack-resolved.

### 2.2 Caches and fast paths

On `Context` (context.go:99-123):

- `SubqueryCache map[string][]Datum` with two keying schemes
  (expr.go:12075/12089): non-correlated sublinks use a constant per-node key
  (`nonCorrelatedCacheKey` — M0058-0001, the fix that collapsed the
  Q11/Q16/Q18/Q22 timeouts `[verification-2026-05-07]`); correlated sublinks
  key on the **full serialized outer row** (`subqueryCacheKey`), not a
  projection of the referenced correlation columns — distinct outer rows that
  agree on the correlation column still miss (D4.4).
- `CorrSubqOps` (context.go:109-115): pre-built, rescan-style-reused operator
  for correlated **scalar** subplans whose plan is index-scan-based
  (`planIsIndexScanBased`, expr.go:6992).
- `CorrSubqHashMaps` (context.go:≈117): one-shot inner-table hash map for the
  `Project(Filter(SeqScan, col = OuterColumnRef))` scalar shape
  (`extractCorrSubqHashInfo`, expr.go:≈7008).

The asymmetry matters: `collectInValues` and `existsImpl` have **none** of
the correlated fast paths — a correlated IN/EXISTS that the planner fails to
unnest always pays per-row rebuild (mitigated only by the full-outer-row
cache).

### 2.3 Semi/anti executors and MultiHashJoin residuals

- Hash semi/anti: `internal/executor/operators_join_agg.go` (join-type
  handling :122-129, emit-once probe logic :1002-1058, NullAware NOT-IN
  build-side null tracking :81-89/:526-530/:898-902).
- Nested-loop semi/anti fallback: `internal/executor/operators_nljoin.go`
  :148-195 (used by the Q21 index-driven anti-join design, 0063-0004).
- `MultiHashJoin` (`internal/executor/multi_hash_join.go`) partitions residual
  filters into probe/step/leaf buckets (:44-46, `partitionFilters` :298), but
  `walkColumnRefs` treats `SubqueryExpr`/`ExistsExpr`/`InExpr`/`OuterColumnRef`
  as opaque (:386), so any subquery-bearing residual lands in the innermost
  `leafFilters` catch-all (:362) and re-invokes the §2.1 per-row eval sites
  from the deepest, highest-cardinality position of the join (Q21's shape).

---

## 3. Enumerated gates and bail-outs

The complete list of reasons a sublink stays a SubPlan today. Rows marked ⊙
match PostgreSQL's own behavior and are (mostly) non-goals; rows marked ✗ are
goopg-specific gaps this bundle addresses.

| # | gate / bail-out | where | PG? | addressed by |
| --- | --- | --- | --- | --- |
| G1 | pull-up loops fire only at `*Filter` nodes; scan-embedded filters and join predicates are never visited | unnest.go:14-67 | ✗ (PG pulls up from parse-tree quals before planning) | D3.0, S1; structurally removed by D3.1/S5 |
| G2 | any `OuterColumnRef` outside an `OpEq` equijoin pair bails the scalar and IN paths | unnest.go:≈227, ≈1904 | ✗ (for EXISTS PG lifts all quals into the join) | D3.2, S4 |
| G3 | nested un-unnestable sublink inside an EXISTS body rejects the whole pull-up | `hasNestedSub`, unnest.go:1937 | ✗ (PG recurses and leaves inner sublinks as SubPlans) | D3.3, S4 |
| G4 | scalar path requires single-aggregate `Aggregate` root, no `Star`/`Distinct` | unnest.go:177 | ⊙ (PG never decorrelates scalars; goopg exceeds PG here) | D3.4, S4 (gate refinement only) |
| G5 | non-conjunct-position sublinks: the `topConjunct` guard exists in the **EXISTS loop only** (unnest.go:≈2012). The IN loop has no such guard → **live planner infinite loop** for IN under `OR`/`NOT (…)`; the scalar loop has none → **live wrong results** for OR-position scalars (rows dropped by the injected INNER join). Both measured 2026-07-20 [measured-at-HEAD e4a43ba6] | EXISTS: unnest.go:≈2012; IN: missing (find at :≈1539 vs removal at :≈1374-1385/:≈1486-1497); scalar: missing | ⊙ (prepjointree.c stops at any non-AND node — PG never rewrites these) | guards are S1-blocking (ch.03 §6, D3.0); position stays a non-goal |
| G6 | non-correlated EXISTS deliberately kept as cached SubPlan | unnest.go:≈1916 | ✗-ish (PG converts uncorrelated EXISTS to InitPlan-like one-shot) | D4.3/S3 covers cost; revisit in D3.4 |
| G7 | no cost-based SubPlan-vs-unnest decision; structural gates only | — | ⊙ (PG pull-up is also uncosted) | D6.1 confirms as policy |
| G8 | semi/anti exist only as hash join (+NL fallback); no NLI/merge variant | unnest.go:≈1364/≈1434 | ✗ (PG costs semi/anti over all join methods) | D6.2, S6 |
| G9 | subquery residuals in `MultiHashJoin` fall to innermost leafFilters | multi_hash_join.go:386 | n/a (PG has no MHJ) | D3.0 invariant, ch.03 §9 |
| G10 | correlated cache key = full outer row, not correlation projection | expr.go:12075 | n/a (PG params are exactly the correlation set) | D4.4, S2 |

---

## 4. Measured evidence at HEAD (2026-07-19)

### 4.1 What fires and what does not

Minimal-shape probes against the SF1 bench data
([evidence/unnest-probes-e4a43ba6.txt](evidence/unnest-probes-e4a43ba6.txt))
`[measured-at-HEAD e4a43ba6]`:

| probe | shape | result |
| --- | --- | --- |
| P1 | `EXISTS (SELECT 1 FROM lineitem WHERE l_orderkey = o_orderkey)` | **not unnested** — `Filter: (<*planner.ExistsExpr>)` on Seq Scan |
| P2 | P1 + outer local predicate | **not unnested** |
| P3 | P1 + inner-only residual (`l_commitdate < l_receiptdate`) | **not unnested** |
| P4 | minimal `NOT EXISTS` (Q22 core) | **not unnested** |
| P5 | correlated scalar agg (Q17 core) | **not unnested** — `l_quantity < <*planner.SubqueryExpr>` |
| P6 | correlated `IN` | **unnested** — `Hash Join (?)` + Index Only Scan on `order_customer_fkidx` |

The full-query captures
([evidence/explain-head-e4a43ba6.txt](evidence/explain-head-e4a43ba6.txt))
corroborate: the IN-family loop fires (Q18's `IN (GROUP BY ... HAVING)`
becomes a `Hash Join (?)` with a `GroupAggregate` build side; Q20's two
IN-subqueries become nested `Hash Join (?)` nodes; Q16's non-correlated
`NOT IN (SELECT s_suppkey FROM supplier ...)` becomes a `Hash Join (?)`
against supplier — the `<*planner.InExpr>` remaining in Q16's filter is the
*literal* `p_size IN (49, 14, ...)` list, not the subquery), while the EXISTS
family and the correlated scalar path never fire **on the TPC-H bench
schema** — not on the TPC-H shapes and not on the minimal P1/P4/P5 shapes
either, because the bench schema indexes the inner correlation columns.

The 2026-07-20 review probes closed the mechanism question
[measured-at-HEAD e4a43ba6]
([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md) §5):
on **index-less** inner tables both loops DO fire (minimal EXISTS → semi
join; minimal correlated scalar → `Hash Join (INNER)` + `GroupAggregate`).
The controlling variable is an index on the inner correlation column: the
inner planner absorbs the correlation equijoin into `IndexScan.Key`;
`collectUnnestParams` / `collectExistsUnnestParamsAndResiduals` harvest
equijoins only from the inner plan's Filter conjuncts, while the
all-accounted walk `walkPlanExprs` *does* visit `IndexScan.Key`
(unnest.go:≈310-319), sees an unaccounted `OuterColumnRef`, and bails.
`CREATE INDEX` / `DROP INDEX` toggles the behavior deterministically.

> Note: the directive brief for this chapter presumed Q16's NOT-IN had also
> failed to unnest; the captured plan shows the opposite. The dossier below
> follows the captured plans.

So the measured state is *not* "EXISTS unnesting bails on complex TPC-H
shapes" and *not* "the loops are dead" — it is "an index on the inner
correlation column silently disables the EXISTS and scalar pull-up loops,
and every TPC-H correlation column is indexed." This also reconciles the
M0061-0001 verification narrative `[timings-20260616]` and the M0054-0008
history (Q17 scalar decorrelation `[report-2026-05-06]` §6) without a code
regression: those transforms fire whenever the inner side plans as a
filtered scan rather than an index probe. The remaining W1 work (§5) is
per-query confirmation, not mechanism discovery.

**Two live planner bugs were also measured in the same session** — because
the loops DO fire outside the indexed schema, their missing guards are
reachable today [measured-at-HEAD e4a43ba6]
([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md)):

- **Infinite planning loop:** `EXPLAIN SELECT * FROM t1 WHERE a=1 OR b IN
  (SELECT b FROM t2)` never returns — the IN pull-up finds the sublink by
  pointer anywhere in the predicate (including under OR/NOT), fails to
  remove it (removal matches only top-level conjuncts), installs the join
  anyway, and re-finds it forever (unbounded allocation; the 30 GB RSS
  incident). `NOT (b IN (…))` also triggers it. See ch.03 §6/D3.0.
- **Live count bug:** `t1.a > (SELECT count(b) FROM t2 WHERE t2.a=t1.a)`
  returns {3} vs PG's {2,3,4} — `canUnnestSubquery`'s `Star` check excludes
  `count(*)` only; `count(col)` decorrelates through an INNER join and drops
  empty groups. There is no NULL-on-empty aggregate whitelist (D3.4); the
  whitelist is S1-blocking.

A secondary observation: EXPLAIN renders semi/anti joins as `Hash Join (?)` —
the join-type label is missing, and un-unnested sublinks print as opaque
`<*planner.ExistsExpr>` strings. EXPLAIN visibility is ch.06 §6 and lands in
phase S0 (the plan gates depend on it).

### 4.2 Runtime scoreboard correction — the 1000× narrative is stale

Timed runs at HEAD, SF1, warm cache `[measured-at-HEAD e4a43ba6]`:

| query | HEAD (s) | May 2026 SF1 record (s)¹ | PG 18.3 SF1 (s) | HEAD gap vs PG | plan-compare §7 claimed |
| --- | ---: | ---: | ---: | ---: | ---: |
| Q22 | **1.80** (7 rows) | 84.9 | 0.058 | ≈31× | 1452× |
| Q4 | **7.41** (5 rows) | 217.2 | 0.188 | ≈39× | 1156× |

¹ Source: `analysis/tests-overview-260706/04-performance-baselines.md` §B —
run `run_goopg_20260526-135117.log`, goopg `26cf58d`, 2026-05-26 (the newest
all-pass SF1 record; the same numbers plan-compare §7 used). Note this
post-dates the `[report-2026-05-06]` measurements, whose Q4/Q21 were still
&gt;1 h timeouts — the M0058 fixes landed in between, which is why the two May
sources disagree.

The plan-compare's §7/§8 executor-layer decomposition
`[plan-compare-260718 @701a5f57]` used the 2026-05-26 goopg build's timings;
its headline "correlated-subquery queries ≈120–1450×, geomean ≈256×" is
**stale by 1.5–2 orders of magnitude** for at least Q4/Q22. At HEAD, even
*without* any EXISTS/scalar decorrelation firing, the executor-side work since
May (M0058-0001 constant-key cache, `CorrSubqOps`, `CorrSubqHashMaps`,
per-row eval cost reductions) has pulled these queries into the same
≈19–40× band as the bulk-operator queries (Q1 ≈11×, Q6 ≈38× per §7's own
data). Row counts match the canonical results (Q4=5, Q22=7).

HEAD SF1 runtimes for Q2, Q7, Q8, Q17, Q20, Q21 are **unmeasured**; treat any
per-query ratio for them as `[plan-compare-260718 @701a5f57]`-vintage until S0
re-measures (Q21 was 305 s and Q4 111 s at SF≈0.5 on 2026-06-16
`[timings-20260616]`, so Q21 in particular may still be in a bad band).

What remains worth winning, stated honestly:

1. **The residual ≈30–40×** on the measured queries — decorrelation
   (set-oriented semi/anti instead of 1.5 M subplan invocations) plus the
   rescan-not-rebuild SubPlan floor (ch.04) target most of it; the final
   ≈19× bulk-executor constant factor is out of this bundle's scope (owned by
   the perf-optimize line).
2. **Removing the algorithmic cliff.** Per-row SubPlan evaluation is
   O(outer-rows × subplan-cost) whenever a shape misses the caches — the May
   timeouts (>1 h on Q4/Q21 `[report-2026-05-06]`) show what that cliff looks
   like. Today's 7.41 s Q4 is a cache-mitigated cliff, not a removed one.
3. **Plan-shape parity with PG** — semi/anti joins that participate in join
   ordering, index-driven anti joins, and EXPLAIN output a PG operator would
   recognize.

---

## 5. Work item W1 — per-query confirmation of the non-firing mechanism

**Status update (2026-07-20): the mechanism question is ANSWERED**
[measured-at-HEAD e4a43ba6]
([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md) §5).
The original hypothesis space (H-a positional / H-b inner-plan shape / H-c
regression) resolved to a sharpened form of H-b: with an index on the inner
correlation column, the inner planner absorbs the correlation equijoin into
`IndexScan.Key`; the param collectors read only the inner plan's Filter
conjuncts, while the all-accounted walk (`walkPlanExprs`, which does visit
`IndexScan.Key`/`LowKey`/`HighKey`, unnest.go:≈310-319) sees the unaccounted
`OuterColumnRef` and bails. No code regression is required to explain the
history (H-c dismissed): the loops fire whenever the inner side plans as a
filtered scan. The IN loop is unaffected because its correlation rides the
*operand*, not an inner Filter conjunct.

**Magnitude answered too (Stage 2, V6 counters).** The open question of whether
Q4 evaluates its `EXISTS` once per `orders` row (≈1.5 M) or only for the
date-filtered subset (≈57 K) is settled by measurement: **57 640** — the
date-range conjunct short-circuits first. Measured with `EXPLAIN (ANALYZE,
TIMING OFF)` against the SF1 bench data at commit `379dd402`
[measured-at-HEAD 379dd402]:

| Query | SubPlan | kind | calls | rebuilds | rescans | cache hits | query exec |
|---|---|---|---:|---:|---:|---:|---:|
| Q2 | 1 | scalar `min` (correlated) | 621 | 621 | 0 | 0 | 16.4 s |
| Q4 | 1 | `EXISTS` (correlated) | 57 640 | 57 640 | 0 | 0 | 6.45 s |
| Q17 | 1 | scalar `avg` (correlated) | 6 668 | **1** | **6 667** | 0 | 54.5 s |
| Q20 | 1 | scalar `sum` (correlated) | 8 552 | 8 552 | 0 | 0 | 2.55 s |
| Q22 | 1 | scalar `avg` (non-correlated) | 11 828 | 1 | 0 | **11 827** | 0.91 s |
| Q22 | 2 | `NOT EXISTS` (correlated) | 5 415 | 5 415 | 0 | 0 | (same query) |

Three findings that re-rank the roadmap's expected wins:

- **Q2 is the worst per-call site, not Q4.** 621 calls consume a large share of
  16.4 s — ≈26 ms per invocation, because the sublink's inner plan is an
  `Aggregate` over a **4-table Multi-Way Hash Join** that is rebuilt from
  scratch every call. Q4's `EXISTS` costs ≈112 µs per call by comparison. The
  D4.2 rescan contract therefore pays off in proportion to inner-plan
  complexity, and Q2 is its headline case.
- **Q17 is already on the rescan path and is *not* a SubPlan problem.**
  `rebuilds=1, rescans=6667` shows `CorrSubqOps` working as designed; its 54.5 s
  is dominated by the 6 M-row outer `lineitem` scan, which belongs to the
  bulk-executor gap (out of this bundle's scope). Q17 should be removed from the
  "SubPlan-bound" bucket.
- **The non-correlated cache works** (Q22 SubPlan 1: 11 827 hits / 1 miss),
  confirming M0058-0001 is healthy and that S3's hashed SubPlan targets a
  different cost (per-probe linear scan), not a cache miss.

Remaining W1 work in S0 (confirmation, not discovery):

1. ~~Per TPC-H query, confirm the gate with V6 counters~~ — **done**, table
   above. Q21 was not measured (its runtime exceeds the probe budget at SF1);
   its dual-EXISTS gate attribution stays as analysed in §6.
2. Re-run the P1–P6 probes plus the 2026-07-20 review probes and archive as
   `evidence/unnest-probes-<newhead>.txt`.
3. Record each surviving sublink's gate (G1–G10) in the dossier below,
   replacing "pending W1" labels.

Exit criterion: every `<*planner.*Expr>` string in
[evidence/explain-head-e4a43ba6.txt](evidence/explain-head-e4a43ba6.txt) has a
named gate or a named regression commit attached.

---

## 6. Per-query dossier

PG plans and timings from `[plan-compare-260718 @701a5f57]` §4 (PG 18.3, SF1,
warm single samples); goopg plans `[measured-at-HEAD e4a43ba6]`.

### Q2 — correlated scalar MIN (min-cost supplier)

- **Shape:** `ps_supplycost = (SELECT min(ps_supplycost) FROM partsupp, supplier, nation, region WHERE p_partkey = ps_partkey AND ...)` — correlated on `p_partkey`.
- **PG:** hash join with a correlated SubPlan evaluated via parameterized index probes; 0.256 s.
- **goopg HEAD:** NL/index join chain region→nation→supplier→partsupp + Seq Scan part; the scalar stays `ps_supplycost = <*planner.SubqueryExpr>` on the top hash join. Not decorrelated (inner-index absorption, W1-confirmed mechanism §5); executes via `subqueryImpl`'s correlated paths (`CorrSubqOps`/hash-map if shape matches, else rebuild).
- **Gap class:** scalar loop non-firing (G1/G2 pending W1) + SubPlan rebuild floor. **Owner:** D3.0/S1, D4.2/S2. HEAD runtime unmeasured.

### Q4 — correlated EXISTS over lineitem

- **Shape:** `EXISTS (SELECT * FROM lineitem WHERE l_orderkey = o_orderkey AND l_commitdate < l_receiptdate)` under a date-range filter on orders (1.5 M rows).
- **PG:** parallel Nested Loop **Semi Join** (orders × index lineitem); 0.188 s.
- **goopg HEAD:** `Seq Scan orders, Filter: (dates AND <*planner.ExistsExpr>)` — per-row EXISTS eval, 1.5 M invocations. **7.41 s measured** (≈39× vs PG; was 217 s in May).
- **Gap class:** EXISTS loop non-firing (W1); after S1 should become hash/NLI semi join. **Owner:** D3.0/S1, D6.2/S6.

### Q17 — correlated scalar AVG

- **Shape:** `l_quantity < (SELECT 0.2*avg(l_quantity) FROM lineitem WHERE l_partkey = p_partkey)`.
- **PG:** hash join + correlated SubPlan (bitmap lineitem probes); 1.503 s.
- **goopg HEAD:** NL(lineitem × part_pk) with `l_quantity < <*planner.SubqueryExpr>` in the join filter — the M0054-0008 GROUP-BY+join rewrite demonstrably does not fire (P5). **Owner:** D3.0/S1 (restore), D3.4/S4 (gate refinement). HEAD runtime unmeasured (70.4 s in `[report-2026-05-06]`, decode-bound then).

### Q20 — IN chain + correlated scalar SUM

- **Shape:** `s_suppkey IN (SELECT ps_suppkey ... WHERE ps_partkey IN (SELECT p_partkey WHERE p_name LIKE 'forest%') AND ps_availqty > (SELECT 0.5*sum(l_quantity) ... WHERE l_partkey = ps_partkey AND l_suppkey = ps_suppkey ...))`.
- **PG:** Hash Right Semi Join with an inner correlated SubPlan; 0.209 s.
- **goopg HEAD:** both IN levels **are** unnested (`Hash Join (?)` ×2 — the IN loop works); the correlated `0.5*sum` scalar remains `ps_availqty > <*planner.SubqueryExpr>` evaluated per joined partsupp row (inner-index absorption; two-column correlation `l_partkey, l_suppkey`).
- **Gap class:** scalar non-firing (W1) + multi-param scalar decorrelation (G2/G4). **Owner:** D3.0/S1, D3.2/S4, D4.2/S2.

### Q21 — EXISTS + NOT EXISTS with non-equi residual

- **Shape:** `EXISTS (l2: l_orderkey = l1.l_orderkey AND l_suppkey <> l1.l_suppkey)` AND `NOT EXISTS (l3: same keys AND l_receiptdate > l_commitdate)` over a 4-table join.
- **PG:** NL Semi Join + parallel NL **Anti Join** with index probes on lineitem; 0.859 s.
- **goopg HEAD:** `Multi-Way Hash Join (4 tables)` with both `<*planner.ExistsExpr>` conjuncts in its **leafFilters** — evaluated at the innermost, highest-cardinality position (G9), against 6 M-row lineitem, per row. Worst standing case (305 s at SF≈0.5 `[timings-20260616]`).
- **Gap class:** EXISTS non-firing (W1) + residual placement (G9) + `<>` residual lifting (needs `collectExistsUnnestParamsAndResiduals`, exists) + index anti-join method (G8). **Owner:** D3.0/S1, D3.3/S4, D6.2/S6 (absorbs 0063-0004).

### Q22 — non-correlated scalar AVG + correlated NOT EXISTS

- **Shape:** `c_acctbal > (SELECT avg(c_acctbal) ...)` (non-correlated) AND `NOT EXISTS (SELECT * FROM orders WHERE o_custkey = c_custkey)`.
- **PG:** InitPlan for the avg + parallel NL **Anti Join** (index-only orders); 0.058 s.
- **goopg HEAD:** `Seq Scan customer, Filter: (IN-list AND c_acctbal > <*planner.SubqueryExpr> AND NOT <*planner.ExistsExpr>)`. The avg is served by the constant-key cache (InitPlan-equivalent, G6 ⊙); the NOT EXISTS runs per row. **1.80 s measured** (≈31×).
- **Gap class:** EXISTS non-firing (W1); anti-join target. **Owner:** D3.0/S1, D6.2/S6.

### Q7 / Q8 — no sublinks at HEAD (reclassified)

Both HEAD plans are pure join trees with **no subquery expressions**
`[measured-at-HEAD e4a43ba6]` (the queries' "subquery" is a FROM-clause
derived table, flattened at plan time). Their large §7 ratios (286× / 837×,
May timings) therefore cannot be SubPlan overhead at HEAD; they are join-order
/ bulk-executor questions **out of scope for this bundle** — flagged here
because the plan-compare §8 grouped them into the "correlated-subquery
geomean ≈256×" bucket, which W1's re-measurement (S0) should correct. Q11,
Q16, Q18 similarly retain only non-correlated (cached) or already-unnested
sublinks at HEAD.

---

## 7. Summary — gap → owner map

| gap | evidence | owning design | phase |
| --- | --- | --- | --- |
| EXISTS + scalar pull-up loops disabled by inner-index absorption (`IndexScan.Key`) on the bench schema | P1–P5 + review probes §5 | D3.0 (collector fix + guards, mechanism confirmed) | S0/S1 |
| IN pull-up under `OR`/`NOT (…)`: planner infinite loop (live bug) | review probes §1 | D3.0 guard (top-conjunct bail for the IN loop) | S1 (blocking) |
| OR-position scalar decorrelation drops rows (live bug) | review probes §3 | D3.0 guard (scalar AND-reachability gate) | S1 (blocking) |
| `count(col)` scalar decorrelation count bug (live bug) | review probes §2 | D3.4 whitelist (NULL-on-empty aggregates only) | S1 (blocking) |
| Correlated `NOT IN` NULL-operand over empty inner wrong on SubPlan path (live bug) | review probes §4 | ch.07 M2 pins; executor fix alongside D4.3 | S2/S3 |
| SubPlan rebuild per row (no rescan contract) | §2.1 | D4.1/D4.2 | S2 |
| correlated cache key = full outer row | G10 | D4.4 | S2 |
| correlated IN/EXISTS lack hashed-SubPlan fallback | §2.2 asymmetry | D4.3 | S3 |
| non-equi / multi-param correlation bails scalar+IN | G2 | D3.2 | S4 |
| nested sublinks reject EXISTS pull-up | G3 | D3.3 | S4 |
| unnest runs after join-order search | §1.2 | D3.1 | S5 |
| no NLI/merge semi-anti; Q21 residual placement | G8/G9 | D6.2, ch.03 §9 | S6 |
| EXPLAIN prints opaque sublink strings, `(?)` join type | §4.1 | ch.06 §6 | S0 |
| no Memoize-style parameterized cache | ch.05 | D5.x | S7 |

The next chapter ([02-pg-target-architecture.md](02-pg-target-architecture.md))
fixes the PostgreSQL-fidelity contract these designs converge toward.
