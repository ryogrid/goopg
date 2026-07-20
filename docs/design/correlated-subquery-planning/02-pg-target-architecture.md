# 02 — PostgreSQL's Sublink Architecture (Oracle Reference)

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| part of | [correlated-subquery-planning](README.md) design bundle |
| oracle | PostgreSQL 18.3 source under `postgres/` (read-only) |

This chapter is the **fidelity contract** for the rest of the bundle: a
file/line-grounded description of how PostgreSQL 18.3 plans and executes
subqueries, against which chapters [03](03-planner-decorrelation-extensions.md)–
[06](06-cost-model-touchpoints.md) are reviewed. It extends the repo-root
primer `PostgreSQL_SubPlan_Correlated_Subqueries_Design_Guide.md` with the
specifics that primer omits. Every claim below was verified against the
`postgres/` tree at the cited location.

User-visible semantics (NULL propagation of `IN`/`NOT IN`, `EXISTS` row
semantics, scalar-subquery multi-row error) are specified in the official
documentation: `create_pg_super_document/official_doc_in_md/functions-subquery.md`.

---

## 1. Two-stage strategy

PostgreSQL applies exactly two strategies to a sublink, in a fixed order:

1. **Decorrelate (pull up) when legal.** `pull_up_sublinks`
   (`postgres/src/backend/optimizer/prep/prepjointree.c:468`) runs inside
   `subquery_planner` **before any join-order search**. A pulled-up sublink
   becomes a first-class range-table entry joined with a **semijoin** or
   **anti-semijoin** join type, and from that point on it participates in
   ordinary join planning — join-order enumeration, join-method choice,
   parameterized paths, Memoize.
2. **Otherwise, build a SubPlan.** Sublinks that survive pull-up are converted
   during expression preprocessing by `make_subplan`
   (`postgres/src/backend/optimizer/plan/subselect.c:162`) into a `SubPlan`
   node: a separately-planned child plan referenced from an expression, fed
   per-outer-row parameters through PARAM_EXEC slots.

There is **no cost-based arbitration between the two stages**: pull-up fires
whenever it is legal (structural conditions only). Costing enters only later,
when the resulting semijoin is planned like any other join, and when
`cost_subplan` (`postgres/src/backend/optimizer/path/costsize.c:4534`) charges
the per-call cost of surviving SubPlans into the expressions that contain them.
This bundle adopts the same posture (decision [D2.1](#d21) below;
cost touchpoints in [06](06-cost-model-touchpoints.md)).

## 2. What PG pulls up — exactly

`pull_up_sublinks_qual_recurse` (`prepjointree.c:652`) dispatches on the
sublink type at each qual node:

| sublink shape | converted to | converter |
| --- | --- | --- |
| `x op ANY (SELECT ...)` (incl. `IN`) | `JOIN_SEMI` JoinExpr | `convert_ANY_sublink_to_join` (`subselect.c:1333`) |
| `EXISTS (SELECT ...)` | `JOIN_SEMI` JoinExpr | `convert_EXISTS_sublink_to_join` (`subselect.c:1450`) |
| `NOT EXISTS (SELECT ...)` | `JOIN_ANTI` JoinExpr | same, via the `is_notclause` branch (`prepjointree.c:788`) — "If the immediate argument of NOT is EXISTS, try to convert" |
| `= ANY (VALUES ...)` | `ScalarArrayOpExpr` | `convert_VALUES_to_ANY` (PG 18, called at `prepjointree.c:669`) |

The **positional restriction** is absolute: pull-up applies only at the top
level of `WHERE` or `JOIN/ON` quals, recursing through explicit `AND` nodes and
stopping at the first non-AND node (`prepjointree.c:883`, "Stop if not an
AND"). The header comment (`prepjointree.c:440-466`) gives the reason: below an
`OR` (or any other non-AND context) the planner cannot distinguish whether the
sublink ought to return `FALSE` or `NULL` for NULL inputs, so the rewrite would
change results. The same comment states the outer-join restriction: in an outer
join's ON clause the sublink is pulled up only when it is *degenerate* —
references only the nullable side — because otherwise the semijoin cannot be
pushed into either input.

The correlation restrictions are looser than often assumed (PG 18 behavior):

- **ANY:** a body-correlated sub-select does *not* block pull-up —
  `convert_ANY_sublink_to_join` treats a sub-select containing parent-level
  Vars as **LATERAL** and still builds the semijoin (`subselect.c:1352-1364`,
  "If the sub-select contains any Vars of the parent query, we treat it as
  LATERAL"). The only rejection is parent-level Vars of relations outside
  `available_rels`. Vars of levels *above* the parent are explicitly fine
  (`subselect.c:1355`, "(Vars from higher levels don't matter here.)").
- **EXISTS:** the restriction is *positional*, not level-based — parent-level
  Vars are permitted only in the sub-select's WHERE clause; the rest of the
  sub-select must not reference the parent (`subselect.c:1498-1510`), while
  higher-level Vars are again allowed (`subselect.c:1500`, "(Vars of higher
  levels should be okay, though.)").

### What PG does NOT pull up

This list is the boundary this bundle must not silently over-promise past
(decision [D2.3](#d23)):

- **Sublinks under `OR`, `CASE`, function arguments, or any non-AND position**
  (`prepjointree.c:883`). They always become SubPlans.
- **`NOT IN` / negated ANY.** The `is_notclause` branch converts only
  `NOT (EXISTS ...)`; `NOT (x = ANY (...))` is never pulled up, because
  anti-join semantics do not match `NOT IN`'s three-valued NULL behavior.
- **`ALL` sublinks** — no converter exists; always SubPlans.
- **Sublinks in targetlists** (SELECT-list scalar subqueries) — pull-up walks
  quals only.
- **Correlated scalar (`EXPR_SUBLINK`) subqueries** — PostgreSQL has **no
  scalar-subquery decorrelation at all**. A correlated
  `x < (SELECT avg(...) FROM ... WHERE inner.k = outer.k)` is always executed
  as a per-row SubPlan; its speed comes entirely from the executor machinery
  in §4 (typically a parameterized index-scan subplan). TPC-H Q2/Q17/Q20 run
  fast on PG *without* decorrelation.

goopg already implements two transforms **beyond** this list (correlated
scalar-aggregate decorrelation; NullAware `NOT IN` anti-join). §7 records the
policy for those divergences.

## 3. The SubPlan node contract

`make_subplan` (`subselect.c:162`) plans the sub-select, then `build_subplan`
(`subselect.c:319`) assembles the `SubPlan` node. The parts of the contract
that matter for goopg:

- **Correlation as parameters, not free variables.** Before planning, upper-
  level `Var`s inside the subquery were replaced by PARAM_EXEC `Param` nodes
  by `SS_replace_correlation_vars` (`subselect.c:1971`), whose
  `replace_outer_var` machinery records one `PlannerParamItem` per replaced
  Var in `root->plan_params`. `build_subplan` drains that list into paired
  entries of `splan->parParam` (the PARAM_EXEC slot id) and `splan->args`
  (the outer-level expression producing its value) — `subselect.c:351-374`.
  At runtime, `args[i]` is evaluated against the current outer tuple and its
  result stored into the shared `ParamExecData` slot `parParam[i]` (by the
  enclosing expression's compiled steps — see §4).
- **Test expression conversion.** For `ANY`/`ALL`/`ROWCOMPARE`, the parser's
  combining expression references the sub-select's output via PARAM_SUBLINK
  params; `convert_testexpr` (`subselect.c:644`) rewrites those into PARAM_EXEC
  params bound to the subplan's output columns, so the executor can evaluate
  `testexpr` against each row the subplan returns.
- **InitPlan vs SubPlan.** If `parParam == NIL` (no direct correlation) and the
  sublink type is `EXISTS`, `EXPR`, `ARRAY`, `ROWCOMPARE`, or `MULTIEXPR`, the
  subplan becomes an **initPlan** (`subselect.c:388-455`): it is evaluated at
  most once per rescan of the enclosing node, its result is stored in a
  PARAM_EXEC slot via `setParam`, and the original expression site collapses to
  a bare `Param`. Uncorrelated `ANY`/`ALL` cannot be initPlans (their output
  must be scanned per outer tuple, `subselect.c:511-516`) — they stay regular
  SubPlans and get the hash-table or Material treatment below.
- **plan_id / global registration** — each subplan is appended to
  `root->glob->subplans` so EXPLAIN can print `SubPlan N` and the executor can
  instantiate all of them once per query.

**goopg mapping (current vs target).** goopg's `IsNonCorrelated` flag +
constant-key `SubqueryCache` (`internal/executor/context.go:99`,
`internal/executor/expr.go:12089`) is a functional InitPlan analog — non-correlated subqueries execute once
`[measured-at-HEAD e4a43ba6]`. What goopg
lacks is the *correlated* half of the contract: there are no per-query param
slots and no per-node subplan instances — correlation is resolved by walking a
dynamic `ctx.OuterRows` scope stack (`internal/executor/expr.go:372`), and
cache keys serialize the **entire outer row** rather than the referenced
correlation values. Chapter [04](04-subplan-execution-engine.md) (D4.1)
introduces the PARAM_EXEC analog.

## 4. Executor: rescan, never rebuild

`ExecSubPlan` (`postgres/src/backend/executor/nodeSubplan.c:62`) dispatches to
one of exactly two paths (`:87-89`):

**`ExecScanSubPlan` (`nodeSubplan.c:204`) — the default.** Per outer tuple:

1. The `args[i]` expressions have already been evaluated into the PARAM_EXEC
   slots by the **enclosing expression's** compiled steps —
   `ExecInitSubPlanExpr` (`postgres/src/backend/executor/execExpr.c:2821`)
   emits one `EEOP_PARAM_SET` step per `parParam` (`execExpr.c:2841-2855`)
   ahead of the SubPlan step. `ExecScanSubPlan` itself only marks each
   `parParam[i]` slot dirty in the child's `chgParam` bitmap
   (`nodeSubplan.c:236-241` — it "relies on the caller" for the values).
2. Call `ExecReScan(planstate)` (`nodeSubplan.c:244`) on the **same PlanState
   tree** — no re-planning, no re-initialization, no memory-context rebuild.
   `ExecReScan` propagates the `chgParam` set down the tree
   (`postgres/src/backend/executor/execAmi.c`), and each node resets only what
   the changed parameters invalidate; a parameterized index scan simply
   re-descends the btree with the new key.
3. Pull rows, evaluate `testexpr` (ANY/ALL) or use row presence (EXISTS) or
   copy the first row's value into the result (EXPR, with the one-row check
   raising `more than one row returned by a subquery used as an expression`).

The tree is built **once** per query (`ExecInitSubPlan`), and per-tuple state
lives in a short-lived expression context. This is the *rescan-not-rebuild
contract*, and it is the single biggest structural difference from goopg's
default path, which on every cache miss re-runs `Build(x.Plan)` → `op.Open` →
drain → `op.Close` — reconstructing the operator tree from the plan
(`internal/executor/expr.go:6674` `collectInValues`, `:6827` `existsImpl`,
`:6912` `subqueryImpl` fallback branch).

Honest scale note `[measured-at-HEAD e4a43ba6]`: goopg's cache layers
(`SubqueryCache`, `CorrSubqOps`, `CorrSubqHashMaps`) already blunt the worst of
this — Q4 runs in 7.41 s (≈39× PG) and Q22 in 1.80 s (≈31× PG) at SF1, not the
≈1156×/1452× recorded in the (stale) §7 of the plan-compare study
(`analysis/tpch/goopg-pg-tpch-plan-compare-260718/`, on `origin/master`, commit
`be4f0291`; not on branch `wal-pg-nodetree`). The value of adopting the rescan
contract is therefore stated precisely: it removes the remaining
per-invocation constant factor and — more importantly — removes the
**algorithmic cliff** for every shape the fast-path caches do not recognize
(`planIsIndexScanBased` and the `Project(Filter(SeqScan))` hash-map shape are
allowlists; anything else is O(outer-rows × subplan-build)). PG has no such
cliff: *every* subplan shape rescans.

## 5. Hashed SubPlan

For uncorrelated `ANY` subplans, PG upgrades the scan-per-tuple strategy to a
build-once hash table. The condition (`subselect.c:518-522`) is exact:

```
subLinkType == ANY_SUBLINK
&& splan->parParam == NIL          -- no direct correlation
&& subplan_is_hashable(plan)       -- projected size fits hash_mem
&& testexpr_is_hashable(...)       -- all combining ops hashable
```

`ExecHashSubPlan` (`nodeSubplan.c:101`) then answers `x IN (...)` by hash
probe; `buildSubPlanHash` (`nodeSubplan.c:477`) loads the table by scanning the
subplan output once, and the table is rebuilt only when `chgParam != NULL`
(`nodeSubplan.c:118`) — i.e. never, for a truly uncorrelated subplan.

The NULL semantics are carried by a **second table**: `buildSubPlanHash`
maintains `hashtable` for fully-non-null rows and `hashnulls` for rows with
some null column, and the probe logic distinguishes "no match" (return FALSE)
from "no match but NULLs could match" (return NULL) — this is what makes
uncorrelated `NOT IN` NULL-correct while still hashed. Any goopg hashed-SubPlan
design must reproduce both tables (chapter [04](04-subplan-execution-engine.md),
D4.3).

If a regular (non-initPlan) subplan is uncorrelated but does not take the
hashed path — any sublink type, including `ALL` — `build_subplan` instead
tacks a `Material` node on top, under the exact condition
`parParam == NIL && enable_material && !ExecMaterializesOutput(nodeTag(plan))`
(`subselect.c:534-536`), so repeated scans read a tuplestore rather than
re-executing the child. goopg's `SubqueryCache` (a
memoized `[]Datum` result list) already plays this role; whether to keep it or
adopt Material-style spilling is decision D4.3/D4.5 territory.

## 6. Memoize — a join cache, not a SubPlan cache

`Memoize` (PG 14+) is frequently confused with subquery caching; the oracle
facts:

- It is a **plan node inserted between a nested loop and its parameterized
  inner side**, chosen by `get_memoize_path`
  (`postgres/src/backend/optimizer/path/joinpath.c:675`, considered for every
  nestloop inner path at `:1977`). It never attaches to a SubPlan.
- Preconditions (`get_memoize_path`): the inner path must be parameterized by
  the outer rel, rescannable without side effects, with a **complete** cache
  key (every parameter the inner side consumes appears in the key —
  `paraminfo_get_equal_hashops`, `joinpath.c:439`) and hashable equality
  operators for each key column.
- Execution (`postgres/src/backend/executor/nodeMemoize.c`): an LRU-evicting
  hash of parameter-values → cached inner result
  (`MemoizeHash_hash` `:158`, `prepare_probe_slot` `:302`); only **complete**
  cached entries are served; a `singlerow` mode exists for provably-unique
  inners (`:37`); planner sizes the cache via `est_entries` from
  `cost_memoize_rescan` (`costsize.c:2541`).

Relationship to this bundle: after pull-up (chapters 03/05 in PG terms:
semijoin + parameterized inner index path), PG can stack Memoize under the
join and get O(distinct outer keys) subquery evaluations. goopg has no Memoize
operator; chapter [05](05-memoize-operator.md) designs one (D5.1 owns the
stats gate; [06](06-cost-model-touchpoints.md) D6.4 supplies its memory
budget).

## 7. Fidelity matrix — goopg divergences that are features

<a id="d22"></a>
goopg deliberately implements two transforms PG refuses. Policy decision
**D2.2**: **both are retained**, because they are semantics-preserving under
their current gates and TPC-H-critical — but each carries a standing
correctness obligation that chapters [03](03-planner-decorrelation-extensions.md)
and [07](07-verification-and-measurement.md) must enforce whenever the gates
are loosened.

| goopg transform | where | PG behavior | why PG refuses | goopg's correctness obligation |
| --- | --- | --- | --- | --- |
| Correlated scalar-aggregate decorrelation → GROUP BY + inner hash join (`internal/planner/unnest.go:952`, gate `:177`) | M0054-0008 | never decorrelates scalar sublinks | the *count bug*: an aggregate like `COUNT(*)` returns a non-NULL value (0) for outer rows with **no** matching inner rows; an INNER-join rewrite silently drops those rows | gate must exclude aggregates that are non-NULL on empty input (COUNT/COALESCE'd forms), or switch to LEFT JOIN + COALESCE; regression rows in [07 §1](07-verification-and-measurement.md) |
| Non-correlated `NOT IN` → NullAware anti join (`Join.NullAware`, `internal/planner/plan.go:751`; executor `internal/executor/operators_join_agg.go:81-89`; M0122-0011) | commit `be47cc93` | keeps `NOT IN` as a (possibly hashed) SubPlan | plain anti-join returns "row" where `NOT IN` must return NULL when the inner side contains a NULL | NullAware build-side NULL tracking must mirror PG's two-table hashed-SubPlan semantics (§5); NULL matrix rows in [07 §1](07-verification-and-measurement.md) |
| Value-keyed rescan-skip + correlated SubPlan result caching (D4.2 `lastParams` short-circuit; D4.4 projected-key cache — [04](04-subplan-execution-engine.md)) | S2 design (extends today's `SubqueryCache`) | **no value-based change detection anywhere**: `ExecScanSubPlan` marks every `parParam` changed unconditionally per outer tuple (`nodeSubplan.c:236-244`) and always rescans; PG's only param-keyed result caches are gated — hashed SubPlan is uncorrelated-ANY-only (§5) and Memoize rejects any volatile function in the inner target/restrict lists (`joinpath.c:768-800`) | a volatile inner qual (e.g. `... AND random() < 0.5`) or side-effecting subplan re-executes per row in PG; freezing its result for repeated params is observably divergent | **cacheability gate delivered with S2**: value-keyed skip/cache applies only when the inner plan contains no volatile function and no LockRows (`FOR UPDATE`) node; volatile-subquery + locking rows in [07 §1](07-verification-and-measurement.md). Note today's `SubqueryCache` already carries this hazard latently |

Divergences that are **gaps**, not features (owned by later chapters): no
param-slot correlation, no rescan contract, no hashed SubPlan, no Memoize, and
— per [evidence/unnest-probes-e4a43ba6.txt](evidence/unnest-probes-e4a43ba6.txt)
`[measured-at-HEAD e4a43ba6]` — the EXISTS/scalar pull-up loops
currently never fire even on minimal shapes (chapter
[01](01-current-state-and-gap-analysis.md) §5, root-caused and fixed in
[03](03-planner-decorrelation-extensions.md) D3.0).

## 8. Decisions

<a id="d21"></a>
### D2.1 — North star: decorrelate first, make SubPlan cheap second

Adopt PG's two-stage strategy wholesale. Pull-up is structural and uncosted;
it runs before join-order search so semi/anti joins are planned as ordinary
joins. Every sublink that survives pull-up executes through a SubPlan path
whose cost model is "rescan of a persistent operator tree", not "rebuild".
Consequences: the planner work in chapters 03/05/06 and the executor work in
chapter 04 are **complementary, not alternatives** — PG needs both to reach
its measured speed, and so does goopg. Rationale: matches the oracle;
minimizes plan-shape divergence PG-vs-goopg (a stated project goal); the
`[measured-at-HEAD e4a43ba6]` evidence shows neither half alone suffices (caches without
decorrelation still leave 30–40×; decorrelation without cheap SubPlans leaves
the OR/targetlist/ALL residue on the cliff).

### D2.2 — Retained beyond-PG divergences

See §7. Scalar-aggregate decorrelation and NullAware `NOT IN` are retained
with named correctness obligations; every future gate-loosening on either must
add the corresponding rows to the chapter-07 semantics matrix *in the same
change*.

<a id="d23"></a>
### D2.3 — PG-matching non-goals

goopg will **not** attempt: OR-position/CASE/function-arg sublink
decorrelation; `NOT IN` pull-up beyond the existing NullAware transform; `ALL`
sublink pull-up; targetlist-sublink pull-up; pull-up of sublinks referencing
the non-nullable side of an outer join's ON clause. Each matches an explicit
PG refusal documented in §2, and each refusal is grounded in a NULL-semantics
or outer-join-legality argument, not implementation convenience. The
verification suite ([07 §1](07-verification-and-measurement.md)) includes
"must NOT decorrelate, must stay correct" rows for every entry in this list.

---

*Next: [03 — Planner Decorrelation Extensions](03-planner-decorrelation-extensions.md)
applies this contract to goopg's `unnest.go`, starting from the
`[measured-at-HEAD e4a43ba6]` fact that the existing EXISTS/scalar pull-up loops never
fire.*
