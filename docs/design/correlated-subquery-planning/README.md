# Correlated Subquery Planning — Design Bundle

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| branch | `wal-pg-nodetree` (HEAD `e4a43ba6` at time of writing) |
| scope | planner + executor design bundle (design only — no code changes in this bundle) |
| goal | PG-faithful, efficient planning and execution of queries containing correlated (and non-correlated) subqueries |
| supersedes | on overlap: the unimplemented remainder of [0058-0001](../0050-0099/0058-0001-subplan-and-join-optimisation.md) and follow-ups implied by [0061-0001](../0050-0099/0061-0001-exists-anti-semi-join-unnesting.md); absorbs [0063-0004](../0050-0099/0063-0004-q21-anti-join-index-driven.md) into the NLI semi/anti design |

## Why this bundle exists

The goopg-vs-PostgreSQL 18.3 TPC-H plan comparison of 2026-07-18
(`analysis/tpch/goopg-pg-tpch-plan-compare-260718/`, on `origin/master`,
commit `be4f0291`; not on this branch at time of writing) showed that the
queries containing correlated subqueries are the single worst-performing
class in goopg: measured wall-clock ratios of 120–1450× vs PG were
attributed to per-row SubPlan re-instantiation and missing decorrelation.

Ground-truth measurement at HEAD `e4a43ba6` (2026-07-19, this bundle's
[evidence/](evidence/) captures) materially revised that picture:

- **The catastrophe is largely gone at runtime.** Q22 runs in 1.80 s and
  Q4 in 7.41 s at SF1 (≈31× / ≈39× vs PG) — executor-side cache work since
  May (M0058-0001 constant-key cache, `CorrSubqOps`, `CorrSubqHashMaps`)
  collapsed the 1000×-class blowups into the same ≈19–40× band as goopg's
  bulk operators.
- **But structural decorrelation is not firing at all.** At HEAD, even a
  minimal correlated `EXISTS` or correlated scalar-aggregate subquery is
  left as an opaque per-row `SubPlan` filter — the M0061-0001
  EXISTS→semi/anti pass and the M0054-0008 scalar unnest exist in
  `internal/planner/unnest.go` but never trigger (while the correlated-`IN`
  →semi-join path does fire). Every TPC-H correlated-subquery plan shape
  diverges from PG's.

So the prize is threefold: re-enable and extend planner decorrelation
(plan-shape parity with PG), put a PG-style rescan-not-rebuild floor under
the irreducible SubPlan cases (remove the O(outer-rows × operator-lifecycle)
cliff that reappears whenever a shape misses today's ad-hoc caches), and
give the remaining constructs first-class costing and observability.

## Guiding principle

**Decorrelate first, make SubPlan cheap second** — PostgreSQL's own
two-stage strategy (`pull_up_sublinks` before join planning; parameterized
rescan for what remains). Where goopg already exceeds PG (correlated
scalar-aggregate unnest, NullAware `NOT IN` anti-join), the divergence is
retained but must carry the same correctness obligations PG uses to justify
refusing those rewrites. Where PG refuses a rewrite (OR-position sublinks,
`ALL`, targetlist sublinks), goopg refuses too.

## Documents in this bundle

| Doc | Title | Scope |
| --- | --- | --- |
| [01](01-current-state-and-gap-analysis.md) | Current State and Per-Query Gap Analysis | Existing machinery, measured HEAD evidence, enumerated bail-outs, per-query dossier, W1 classification work item |
| [02](02-pg-target-architecture.md) | PostgreSQL's Sublink Architecture (Oracle Reference) | pull_up_sublinks coverage and limits, SubPlan/PARAM_EXEC contract, rescan-not-rebuild, hashed SubPlan, Memoize; fidelity matrix |
| [03](03-planner-decorrelation-extensions.md) | Decorrelation Coverage Extensions | D3.0 root-cause/re-enable, pipeline reorder before join search, residual lifting, nested sublinks, count-bug policy, non-goals |
| [04](04-subplan-execution-engine.md) | SubPlan Execution for Irreducible Cases | Param slots, rescan contract, hashed SubPlan, projected cache keys, cache lifecycle |
| [05](05-memoize-operator.md) | Memoize-Style Parameterized Result Cache | Join-path cache operator under NLI; insertion rule, executor design, honest applicability |
| [06](06-cost-model-touchpoints.md) | Costing Subplans, Semi/Anti Methods, and Caches | NLI semi/anti, SubPlan cost charging, thresholds, EXPLAIN visibility |
| [07](07-verification-and-measurement.md) | Correctness Gates and Measurement Plan | Semantics test matrix, oracle parity, plan gates, perf gates, instrumentation |
| [08](08-roadmap-and-milestones.md) | Phased Implementation Roadmap | Phases S0–S7 with acceptance criteria, dependency graph, milestone policy, hygiene tasks |
| [09](09-round3-open-items.md) | Round 3: Closing the Open Ledger Rows | Investigation-backed designs for the five rows left open by rounds 1–2: NLI LEFT residual (live bug), zero-rows falsification, hash-probe widening + string-shape hazard, composite EXISTS, DML-sublink lowering |

Raw evidence captured for this bundle lives in [evidence/](evidence/):
`explain-head-e4a43ba6.txt` (EXPLAIN of the 11 subquery-bearing TPC-H
queries at HEAD) and `unnest-probes-e4a43ba6.txt` (minimal-shape unnest
probes P1–P6 plus timed Q22/Q4 runs).

## Review outcome — mechanism found, two live bugs surfaced

The adversarial review (2026-07-20, log below) resolved the bundle's original
"known unknown" and surfaced two live code bugs
([evidence/review-probes-20260720.md](evidence/review-probes-20260720.md)):

- **Non-firing mechanism confirmed.** The EXISTS/scalar unnest loops DO fire
  on index-less inner tables; on the TPC-H schema an index on the inner
  correlation column makes the inner planner absorb the correlation equijoin
  into `IndexScan.Key`, where the unnest collectors never look — so the
  all-accounted check bails. See [01 §W1](01-current-state-and-gap-analysis.md)
  and D3.0 in [03](03-planner-decorrelation-extensions.md).
- **Live bug 1 — planner infinite loop:** `EXPLAIN SELECT … WHERE a=1 OR b IN
  (SELECT …)` never terminates (the IN pull-up loop wraps joins unboundedly;
  unbounded allocation). Guard is S1-blocking.
- **Live bug 2 — count bug:** `x > (SELECT count(col) … WHERE corr)`
  decorrelates through an INNER join and drops unmatched outer rows (goopg
  returns wrong results at HEAD). NULL-on-empty aggregate whitelist is
  S1-blocking.

Claims in this bundle are tagged `[measured-at-HEAD e4a43ba6]` versus
`[plan-compare-260718 @701a5f57]` (historical); design decisions cite only
measured facts as drivers.

## Relationship to prior designs

| Prior doc | Relationship |
| --- | --- |
| [0058-0001](../0050-0099/0058-0001-subplan-and-join-optimisation.md) | Gaps 1/3/4/5 landed (M0058, `d5091071`); the unimplemented remainder and its SubPlan-cache sections are superseded by docs 03/04 |
| [0061-0001](../0050-0099/0061-0001-exists-anti-semi-join-unnesting.md) | Implemented core absorbed as current state in doc 01; its non-firing at HEAD is this bundle's D3.0 |
| [0063-0004](../0050-0099/0063-0004-q21-anti-join-index-driven.md) | Absorbed as a special case of D6.2 (NLI anti-join) |
| [0040-0001](../0000-0049/0040-0001-subquery-caching-and-unnest.md), [0033-0001](../0000-0049/0033-0001-subquery-unnesting.md), [0003-0008](../0000-0049/0003-0008-subqueries.md) | Historical background; built on, not superseded |
| [fix-for-q5/](../fix-for-q5/README.md), 0077 cost-model line | Cost-model ownership stays there; doc 06 adds only the minimal touchpoints this bundle needs |
| M0122-0011 NullAware NOT-IN | Retained beyond-PG divergence (doc 02 fidelity matrix) |

## Review log

| date | reviewer lens | outcome |
| --- | --- | --- |
| 2026-07-20 | docs-convention & evidence | PASS-WITH-FIXES (0 blocker, 4 major cross-chapter contradictions) — all fixes applied same day |
| 2026-07-20 | PG-fidelity (vs subselect.c / prepjointree.c / nodeSubplan.c / nodeMemoize.c / joinpath.c / costsize.c) | PASS-WITH-FIXES (1 blocker: volatility/LockRows cacheability gate for D4.2/D4.4 + divergence flag; 3 major: PG18 LATERAL-ANY pull-up, fabricated `hasParamsOnly`, Memoize inner_unique precondition) — all fixes applied same day |
| 2026-07-20 | executor-feasibility (vs internal/executor at HEAD) | PASS-WITH-FIXES (0 blocker; 4 major: limitOp/sortOp rescan-state corrections, NLI inner contract change D5.3, LockRows cache exclusion) — all fixes applied same day |
| 2026-07-20 | planner-correctness (first run) | aborted (session limit) after triggering the planner infinite loop uncapped; re-run below |
| 2026-07-20 | planner-correctness (re-run, capped live probes vs PG 18 oracle) | PASS-WITH-FIXES (2 live code-bug discoveries F1/F2, factual correction F5 = W1 mechanism answered, matrix gaps M14–M16) — all doc fixes applied same day; code fixes tracked as S1-blocking work items |
