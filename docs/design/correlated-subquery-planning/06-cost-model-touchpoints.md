# 06 — Cost-Model Touchpoints: Subplans, Semi/Anti Methods, and Caches

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| part of | [correlated-subquery-planning bundle](README.md) |
| owns phase | S6 NLI semi/anti + cost touchpoints (see [08-roadmap-and-milestones.md](08-roadmap-and-milestones.md)) |
| positions against | the 0077 cost-model line ([milestone 0077](../../milestones/0077-q5-planner-fix-binary-tree-and-cost-model.md), bundle [fix-for-q5/](../fix-for-q5/README.md)) — this chapter adds the *minimum* costing the bundle needs, not a cost-model rewrite |

## 1. Scope

Chapters [03](03-planner-decorrelation-extensions.md) and
[04](04-subplan-execution-engine.md) mostly need *no* costing: decorrelation
is structural (D6.1) and the SubPlan execution floor is unconditionally
better. Three places genuinely need cost input:

1. choosing the **physical method** for a Semi/Anti join (hash vs NLI — D6.2),
2. keeping join-order search honest once SubPlan-bearing filters participate
   in DP after the S5 pipeline reorder (D6.3),
3. sizing/enabling the **caches** (hashed SubPlan D4.3, Memoize ch. 05 — D6.4).

Everything else — real cost surfacing in EXPLAIN, selectivity refinement,
per-operator cost constants — stays with the 0077 cost-model line. Note the
current reality: goopg computes `EstimateRows`
(`internal/planner/cardinality.go:38`) and an internal join cost
(`estimateJoinCost`, `internal/planner/bushy.go:785`; `rowSortCost`,
`internal/planner/joincost.go:68`) used only inside DPccp and the NLI gate,
and EXPLAIN prints a hardcoded `cost=0.00..0.00`
(`internal/executor/operators_explain.go:376-378`) — the printed zeros are
cosmetic, not evidence that no model exists.

## 2. D6.1 — Decorrelation stays structural (uncosted)

**Decision: goopg does not add an unnest-vs-SubPlan cost gate. Whenever a
sublink transform is legal, it fires.**

This is PG-faithful: `pull_up_sublinks`
(`postgres/src/backend/optimizer/prep/prepjointree.c:440`) applies whenever
the shape allows, with no costing — the planner's premise is that a
set-oriented semi/anti join dominates per-row SubPlan re-execution at any
realistic cardinality, and that a stable, predictable plan beats a
cost-sensitive one for this class of rewrite. goopg adopts the same premise
for D3.x (and already behaves this way for the transforms that fire today,
e.g. correlated IN → semi join, [measured-at-HEAD e4a43ba6] probe P6 in
[evidence/unnest-probes-e4a43ba6.txt](evidence/unnest-probes-e4a43ba6.txt)).

Two consequences the bundle accepts explicitly:

- A decorrelated plan can in principle lose to a cached SubPlan when the
  outer side is tiny and the inner side is huge — but with D4.x's cheap
  SubPlan floor the loss is bounded (one hash build vs a handful of probes),
  and PG accepts the identical trade.
- The *method* choice within the decorrelated form (hash vs NLI semi/anti)
  IS costed — that is D6.2, mirroring how PG costs semijoin paths even though
  the pull-up itself is uncosted.

## 3. D6.2 — NLI semi/anti joins and their cost gate

### 3.1 Current state and why the skip exists

Semi/Anti joins currently execute only as hash joins
(`internal/executor/operators_join_agg.go:122-129` asserts
`Algo == JoinAlgoHash`) with a nested-loop fallback
(`internal/executor/operators_nljoin.go:148-195`, M0063-0004's Q21 shape).
The unnester **deliberately prevents** the NLI rewriter from converting
unnested SemiJoins: it marks the inner subquery's root `Project` as
`IsolatedScope` (`internal/planner/unnest.go:1364-1368` and :1425-1445,
M0071-0002) so `tryBuildNLI` (`internal/planner/nl_index_join.go:284`)
declines. The recorded reason is a correctness bug, not cost: the NLI
conversion flips `pickInnerSide`'s outer/inner roles and shifts inner-side
`Filter` ColumnRef indexes (the comment cites Q20's `p_name LIKE 'forest%'`
resolving at part idx 1 → idx 4 mismatch).

So "add NLI semi/anti" = (a) an executor variant + (b) a planner
column-remap fix + (c) a cost gate. The prize: Q4 spends its measured 7.41 s
[measured-at-HEAD e4a43ba6] driving per-row EXISTS probes — ≈1.5 M or ≈57 K
invocations depending on the executor's AND short-circuit order (the date
conjunct filters `orders` to ≈3.7 %; both counts are estimates derived from
the plan-compare PG actuals, and S0's V6 counters decide which is real);
PG's plan for the same query is a parallel index nested-loop **semi** join
at 0.188 s (analysis/tpch/goopg-pg-tpch-plan-compare-260718/ §4 — on
origin/master, commit be4f0291). After S1 gives goopg the hash semi join,
the NLI semi join is what removes the full `lineitem` hash build for
selective outer sides.

### 3.2 Design

**Executor.** Extend `NestedLoopIndexJoin` execution with Semi/Anti modes:

- *Semi*: probe the inner index with the outer key; emit the outer row on the
  **first** matching inner row and abandon the rest of the probe (early-out —
  this is the whole point; a semi probe touches ≤ 1 matching index entry plus
  residual-filter rejects). Consequence for ch. 05: because the probe does
  not scan the inner to completion, a Memoize node can never complete its
  cache entry under these joins — PG refuses Memoize for SEMI/ANTI unless
  `inner_unique` (joinpath.c:721-728), and goopg inherits that rule
  ([05 §2](05-memoize-operator.md)).
- *Anti*: emit the outer row iff the probe yields **no** row passing the
  residual filters. Design doc `0063-0004-q21-anti-join-index-driven.md`'s
  index-driven anti join becomes a special case of this operator and is
  absorbed here (its standalone rewrite retires when S6 lands).
- Output schema = outer side only, matching the hash semi/anti contract.

**Planner.** Replace the blanket `IsolatedScope` veto with a remap-correct
conversion: `tryBuildNLI` learns to translate inner-scope ColumnRefs through
the same `SourceTableIdx`-keyed mapping the unnester builds
(`unnestParam`, `internal/planner/plan.go:452`), instead of assuming
inner-side refs are positioned past `outerWidth`. The M0071-0002 veto stays
as the fallback whenever the remap cannot prove every inner ref resolves.

**Cost gate.** Extend `nliCostGateAccepts`
(`internal/planner/nl_index_join.go:918`) — today a bare
`outerRows ≤ 100000` heuristic — with semi/anti semantics, using
`postgres/src/backend/optimizer/path/costsize.c:5114`
`compute_semi_anti_join_factors` and `final_cost_nestloop` (:3349) as the
oracle:

| factor | inner (today) | semi | anti |
| --- | --- | --- | --- |
| probe cost per outer row | full probe, all matches | first match only: probe cost × `match_frac` amortization; PG models this as `outer_matched_rows` stopping early | full probe of the key's entries (must prove absence) |
| output rows | `outerRows × sel` | `outerRows × match_frac` | `outerRows × (1 − match_frac)` |
| `match_frac` source | — | `min(1, innerRows / max(1, ndistinct(inner key)))`-style estimate from ANALYZE stats; PG derives it in `compute_semi_anti_join_factors` from join selectivity | same |

Compare against the hash alternative (`estimateJoinCost`, bushy.go:785:
build innerRows + probe outerRows): NLI-semi wins when
`outerRows × probeCost < innerBuildCost`, i.e. selective outer sides — Q4's
≈3.7 % date-filtered `orders` at SF1 (≈57 K rows vs a ≈6 M-row `lineitem`
build; figures derived from the PG `EXPLAIN ANALYZE` actuals,
[plan-compare-260718 @701a5f57]) is exactly this case. When ANALYZE stats are absent, mirror the DPccp
rule: no stats → keep hash (the conservative default), unlike plain-NLI's
optimistic default — a wrong NLI-anti probes the full inner per outer row.

## 4. D6.3 — Charging SubPlan cost to the enclosing node

Once S5 moves unnesting before join search, filters that *retain* SubPlans
(the irreducible cases of ch. [03](03-planner-decorrelation-extensions.md) §6)
participate in DPccp placement. Today `estimateJoinCost`/`EstimateRows` treat
a SubPlan-bearing conjunct as a free predicate, so DP may happily place it
where the filter input is maximal.

**Decision:** introduce `estimateSubplanCostPerCall(plan Node) int64` (rough:
`EstimateRows` of the subplan's driving scan, discounted ×0.01 when the D4.2
rescan floor or a D4.3 hash applies) and charge
`perCall × inputRows(filterPosition)` into the cost DP uses when comparing
join orders — the analog of PG's `cost_subplan`
(`postgres/src/backend/optimizer/path/costsize.c:4534`), which charges
SubPlan cost through `cost_qual_eval` into every path that evaluates the
qual. The goal is one property, stated as the S6 acceptance criterion in
[07](07-verification-and-measurement.md): **DP never moves a SubPlan-bearing
filter to a higher-cardinality position than the pre-S5 pipeline produced.**
Precision beyond that property is explicitly not a goal (0077's territory).

## 5. D6.4 — Cache thresholds and memory budgets

The budget primitive already exists: `Context.WorkMem`, consumed by the
hash-join build with disk spill (`internal/executor/operators_join_agg.go:418-421`,
which substitutes 512 MiB when the value is 0; spill machinery
`internal/executor/spill.go:314` `drainRowsBounded`). The GUC `work_mem` is
registered at `internal/config/defaults.go:641-647`. Two decisions up front:

- **hash_mem mapping.** PG gates hashed structures against
  `hash_mem = work_mem × hash_mem_multiplier`, not bare `work_mem`
  (`subplan_is_hashable`,
  `postgres/src/backend/optimizer/plan/subselect.c:712-727`). goopg maps
  hash_mem to plain `Context.WorkMem` and does **not** add a
  `hash_mem_multiplier` analog initially — a single budget knob, recorded as
  a documented divergence; revisit if operator-level parity of memory GUCs
  becomes a goal.
- **`WorkMem == 0` semantics.** Zero means "unlimited"
  (`internal/executor/context.go:195-198`). The bundle's result caches honor
  that (no cap); only the plan-time hashability *gate* needs a finite bound
  to decide, and uses the documented 512 MiB fallback — never a silent
  substitution at run time ([04 §7](04-subplan-execution-engine.md)).

Allocation policy for the bundle's caches:

| consumer | budget | overflow behavior |
| --- | --- | --- |
| hashed SubPlan (D4.3) | ≤ `WorkMem` per SubPlan hash | fall back to the uncached scan path for the remainder of the query (PG falls back by not choosing `useHashTable`; goopg decides at run time because it builds lazily) — never OOM |
| projected-key SubPlan cache (D4.4) | ≤ `WorkMem / 4` | LRU eviction |
| Memoize (ch. 05) | `min(EstEntries × est_entry_bytes, WorkMem / 4)` | LRU eviction; abandon current entry (`passThrough`) |

Plan-time hashability gate for D4.3, mirroring `subplan_is_hashable`
(subselect.c:712-727; chosen at subselect.c:518-522): estimated
inner rows × row width must fit the budget, else plan the unhashed path.
All three consumers report byte usage through the S0 instrumentation
counters so D6.4's constants can be revisited with data.

## 6. EXPLAIN visibility (display only)

Today un-unnested subqueries print as opaque Go type strings —
`<*planner.ExistsExpr>`, `<*planner.SubqueryExpr>`, `<*planner.InExpr>`
([evidence/explain-head-e4a43ba6.txt](evidence/explain-head-e4a43ba6.txt),
every query) — which makes plan-gate assertions and human diffing against PG
needlessly hard.

**Decision (lands with S0, not S6, because verification depends on it):**
render retained subqueries PG-style:

- `Filter: (EXISTS(SubPlan 1))`, `Filter: (l_quantity < (SubPlan 2))` in the
  expression text, and
- an indented `SubPlan N` subtree under the owning node showing the inner
  plan, as PG prints it, with the correlation params listed
  (`Params: o_orderkey`).

Scope strictly display: **cost numbers keep printing `0.00`**
(`operators_explain.go:376-378` unchanged); real cost surfacing is deferred
to the 0077 line. `EXPLAIN ANALYZE` additionally prints the D4.x counters
(calls / rescans / cache hits / rebuilds — see
[07](07-verification-and-measurement.md) §6) and ch. 05's Memoize counters.

## 7. Open questions

1. **Stats sufficiency.** Are goopg's ANALYZE ndistinct estimates on
   `l_orderkey` / `o_custkey` at SF1 accurate enough for the D6.2
   `match_frac` gate to pick NLI-semi for Q4 and hash-anti for Q21 (whose
   6 M-row outer side must stay hash)? S0's predicted-vs-actual counters
   answer this before S6 commits to constants.
2. **No-stats fallback asymmetry.** Plain NLI is optimistic without stats
   (nl_index_join.go:918 accepts), while D6.2 proposes conservative-hash for
   semi/anti. Is the asymmetry justified? (Current answer: yes — the
   downside of a wrong NLI-anti is O(outer × probe) with zero early-out; the
   downside of a wrong hash-semi is one avoidable build.)
3. **MCV awareness.** `compute_semi_anti_join_factors` uses full selectivity
   machinery including MCVs; the proposed ndistinct-only `match_frac` is
   cruder. Is ndistinct enough for the S6 acceptance queries, or does skew
   (e.g. Q21's `l_suppkey`) demand MCV input earlier than 0077 planned?
4. **Where does the NLI-semi/anti rewrite run after S5?** Today
   `rewriteJoinsToNLI` runs after unnesting by pipeline position; after the
   S5 reorder both DP and unnest move earlier, and the NLI pass must still
   see final join shapes. Provisional answer: NLI stays a post-DP rewrite;
   confirm in S5's design review.
5. **Double-charging risk in D6.3.** A SubPlan that D4.3 hashes is charged
   `perCall × 0.01 × inputRows` *and* its build cost is sunk once; should
   the build cost appear in the DP number too? (Provisional: yes, as a
   one-time adder, matching `cost_subplan`'s startup/per-call split.)
