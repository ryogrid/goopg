# 08 — Phased Implementation Roadmap and Milestones

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-20 |
| part of | [correlated-subquery-planning bundle](README.md) |
| depends on | all chapters; acceptance gates defined in [07](07-verification-and-measurement.md) |

This chapter turns the bundle's decisions (D2.x–D6.x) into landable,
milestone-sized phases **S0–S7**, each independently valuable, each with
acceptance criteria drawn from chapter 07 and an explicit rollback story.
Phase ordering was **revised against the original blueprint** after the HEAD
measurements: the probes ([evidence](evidence/unnest-probes-e4a43ba6.txt),
[review probes](evidence/review-probes-20260720.md)) proved that the
already-shipped EXISTS/scalar unnesting machinery never fires **on the
indexed TPC-H bench schema** (mechanism confirmed: `IndexScan.Key`
absorption; it does fire on index-less inner tables — where its missing
guards are live bugs), so re-enabling shipped machinery with its guards (S1)
is the cheapest large win and comes **before** the new SubPlan execution
engine (S2), not after.

## 1. Phase table

| Phase | Title | Decisions | Depends on |
|---|---|---|---|
| S0 | Ground truth & instrumentation | W1, V6 | — |
| S1 | Re-enable structural decorrelation | D3.0 | S0 |
| S2 | SubPlan execution floor | D4.1, D4.2, D4.4, D4.5 | S0 |
| S3 | Hashed SubPlan | D4.3 | S2 |
| S4 | Decorrelation coverage extensions | D3.2, D3.3, D3.4 | S1 |
| S5 | Pipeline reorder before join search | D3.1 + bushy-DP semi/anti awareness | S1, S4 |
| S6 | NLI semi/anti + cost touchpoints | D6.2, D6.3 | S5 |
| S7 | Memoize operator | D5.1, D5.2 | S2 (rescan contract), S6 (NLI insertion point) |

### S0 — Ground truth & instrumentation

- **Scope:** execute chapter 01's W1 (classify, with code-level certainty,
  which gate stops each of Q2/Q4/Q17/Q20/Q21/Q22 from decorrelating —
  mechanism already confirmed as `IndexScan.Key` absorption
  ([review probes](evidence/review-probes-20260720.md) §5), so this is
  per-query confirmation plus spotting any query where a different gate
  dominates); land the V6 per-SubPlan counters; implement the V1 semantics
  matrix against current behavior — **pinning the known-bad rows as
  expected-fail entries, not green and not skipped** (M2's correlated
  NULL-operand case, M5's `count(col)` case, and M6's hang-prone IN-under-OR
  probes, which must run under `timeout` until the S1 guard lands);
  record `scripts/pg-regress-runner.sh -v subselect` parity; fill
  the SF1 scoreboard for Q2/Q7/Q8/Q17/Q20/Q21 (only Q4/Q22 are measured at
  HEAD); land the PG-style SubPlan EXPLAIN rendering (chapter 06 §6 —
  scheduled here, not in S6, because V3's plan-gate assertions depend on
  readable sublink rendering); run the outer-join probe audit of
  chapter 03 §8.5 (M7 shapes) so any needed guard can land in S1.
- **Key files:** `internal/executor/context.go`, `internal/executor/expr.go`
  (counter hooks), `internal/testport/` (matrix), chapter 01 dossier update.
- **Acceptance:** V6 counters reproduce the Q4 pathology (`calls == rebuilds`,
  `rescans = 0`, at a magnitude consistent with the executor's conjunct
  ordering — ≈57 K or ≈1.5 M; see V6, which pins the measured value as the
  baseline); dossier cells all tagged `[measured-at-HEAD <head>]`; no behavior
  change (plan-gate diff empty).
- **Rollback:** counters are additive; revert is a no-op to planning.
- **Expected TPC-H effect:** none (measurement only).

### S1 — Re-enable structural decorrelation (D3.0)

- **Scope — in this order:** (1) **the three live-bug guards first**
  (chapter 03 §2.5, [review probes](evidence/review-probes-20260720.md)):
  IN-loop top-conjunct bail (fixes the planner infinite loop for IN under
  `OR`/`NOT (…)`), scalar AND-reachability gate (fixes OR-position scalar
  wrong results), NULL-on-empty aggregate whitelist (fixes the live
  `count(col)` bug) — plus the driver-loop shrink assertion as a permanent
  belt; then (2) the collector fix for the **confirmed** non-firing
  mechanism — teach the param collectors to harvest correlation equijoins
  absorbed into `IndexScan.Key`/`LowKey`/`HighKey` (chapter 03 §2.1/§2.4
  option (iii)) — re-enabling M0061-0001 / M0054-0008 behaviors on the
  indexed TPC-H shapes. Add a planner regression test per probe so this
  class of silent de-activation can never recur unnoticed. The chapter 03
  §8.5 outer-join guard lands here preemptively (the S0 audit found today's
  safety rests on a 0A000 plan-time rejection, not on a guard).
- **Key files:** `internal/planner/unnest.go`, `internal/planner/planner.go`
  (pass ordering vs predicate pushdown), planner unit tests.
- **Acceptance:** V3 shape gates for Q4/Q21/Q22; P-S1a (Q4 ≤ 3 s),
  P-S1b (Q22 ≤ 1 s); V1 rows M3/M12, **plus S0's expected-fail rows flip to
  green: M5 `count(col)` (whitelist), M6 no-hang and correct (guards)**; V5
  protocol; probes P1–P6 and the 2026-07-20 review probes re-run and
  archived as `evidence/unnest-probes-<newhead>.txt`.
- **Rollback:** the fix must be a conditional planner change; reverting the
  commit restores SubPlan behavior, which S2 keeps fast — no correctness
  exposure either way.
- **Expected effect:** Q4 7.41 s → ≤ 3 s; Q21/Q22 partial (their EXISTS
  sublinks become semi/anti joins; residual shapes refined in S4/S6).

### S2 — SubPlan execution floor (D4.1, D4.2, D4.4, D4.5)

- **Scope:** param slots (PARAM_EXEC analog) with change detection; the
  rescan-not-rebuild contract for all three eval sites (`collectInValues`,
  `existsImpl`, `subqueryImpl`); correlation-projected cache keys replacing
  full-outer-row keys; cache lifecycle and memory budgets (D4.5). The
  **cacheability predicate** — inner plan free of volatile functions and
  LockRows nodes (V1 M13) — gates every cache and rescan-skip added in this
  phase. The shared hash+LRU cache library (`kvcache`) is built here
  alongside D4.4 and reused by S3 (hashed SubPlan) and S7 (Memoize).
  Independent of S1 — attacks the sublinks that remain SubPlans **by
  design** (OR-position, non-equijoin correlation, M6/M7 cases).
- **Key files:** `internal/executor/expr.go`, `internal/executor/context.go`,
  operator `Open`/rescan seams in `internal/executor/operators_*.go`.
- **Acceptance:** P-S2 (Q4 ≤ 1 s if any SubPlan remains on it; no query
  &gt; 2× its S1 time); V1 M8/M11/M13; `make race-gate`; V6 shows
  `rebuilds → 0, rescans > 0` on surviving SubPlan sites.
- **Rollback:** keep the legacy Build/Open/Close path behind the same
  interface; a kill switch (env/GUC-style, following `SetNLIEnabled`
  precedent in `internal/planner/nl_index_join.go`) selects the old path.
- **Expected effect:** every remaining SubPlan site drops its per-row
  constant factor by the operator-lifecycle cost; Q2/Q17/Q20-class wins.

### S3 — Hashed SubPlan (D4.3)

- **Scope:** hash-table execution for non-correlated IN/NOT-IN (and
  optionally EXISTS-as-cached-SubPlan) sites, with PG's two-table NULL
  handling (`buildSubPlanHash`,
  `postgres/src/backend/executor/nodeSubplan.c`); retires the linear
  `collectInValues` probe.
- **Acceptance:** V1 M1/M2/M10 green through the hashed path; P-S3;
  race-gate.
- **Rollback:** hash path gated on the same kill switch pattern; falls back
  to S2 rescan path.
- **Expected effect:** Q16/Q18-class stability; removes the O(N·M)
  worst case for any un-unnested IN.

### S4 — Decorrelation coverage extensions (D3.2, D3.3, D3.4)

- **Scope:** residual-lifting for IN/scalar correlation (non-equi conjuncts
  become join residuals instead of bail-outs); tolerate nested sublinks
  inside EXISTS bodies (lift `hasNestedSub` rejection, leaving inner
  sublinks as SubPlans inside the pulled-up subtree); scalar-gate count-bug
  policy (documented exclusion per D3.4). The outer-join ON-clause
  interaction (M7) was audited in S0 with any guard landed in S1
  (chapter 03 §8.5); this phase re-checks it only because the coverage
  extensions widen the set of shapes the pass touches — M7 stays a blocking
  gate here.
- **Acceptance:** V1 M5/M6/M7/M9; V3 shapes Q20/Q21; P-S4; V5.
- **Rollback:** each lifted gate is a separate commit with its own planner
  test; revert per-gate.
- **Expected effect:** Q20 and Q21 fully decorrelated; Q21 within 5× PG.

### S5 — Pipeline reorder before join search (D3.1)

- **Scope:** move sublink pull-up **before** `tryBushyDP` so semi/anti joins
  participate in join-order search (PG ordering: `pull_up_sublinks` runs in
  `subquery_planner` before join planning,
  `postgres/src/backend/optimizer/prep/prepjointree.c`). Riskiest phase:
  every TPC-H plan can move. **Split into two sub-slices:** S5a — reorder
  with semi/anti *pinned* at their current positions (DP treats them as
  opaque unary inputs; plans must be identical except where sublinks now
  unnest earlier); S5b — DP participation with legality rules
  (`join_is_legal` analog, conservative first cut).
- **Acceptance:** Q12/Q13 plan **byte-stability** (they contain no sublinks —
  any drift blocks the phase); full V3 sweep; P-S5 (correlated-query geomean
  within the bulk band, no query &gt; 60×); plan-compare regeneration (V5.7).
- **Rollback:** pass-ordering flag keeping the post-DP unnest path alive for
  one release cycle; S5a and S5b independently revertible.
- **Expected effect:** join-order-optimal semi/anti placement; unlocks S6.

### S6 — NLI semi/anti + cost touchpoints (D6.2, D6.3)

- **Scope:** index-driven nested-loop semi/anti join (lifting the deliberate
  skip in `internal/planner/unnest.go` NLI conversion), extending
  `nliCostGateAccepts` with early-out (first-match) semantics; SubPlan cost
  charging into enclosing filter/join estimates. Absorbs the
  0063-0004 Q21 index-driven anti-join design as a special case.
- **Acceptance:** V3 NLI shapes where PG chooses index anti-join; P-S6/S7;
  Q21 target met with the anti/semi hash build staying within the D6.4
  WorkMem budget (no unbudgeted spill).
- **Rollback:** NLI-semi/anti behind the existing `SetNLIEnabled`-style gate.
- **Expected effect:** Q21 final form; large-anti-join memory reduction.

### S7 — Memoize operator (D5.1, D5.2)

- **Scope:** parameterized result-cache operator under NLI joins; opt-in
  behind a flag initially; EXPLAIN counters (hits/misses/evictions).
- **Acceptance:** P-S6/S7 (synthetic microbench ≥10×, no TPC-H regression);
  race-gate; EXPLAIN output stable.
- **Rollback:** flag off = operator never inserted.
- **Expected effect:** non-TPC-H workloads with duplicate-heavy outer keys;
  architecture completeness vs PG.

### New and changed executor operators (consolidated)

Achieving this bundle's goal requires executor-side additions, not only
planner rewrites. Each item below is specified in its owning chapter; this
table consolidates them so the roadmap makes the executor work visible as a
first-class deliverable.

| Item | Kind | Phase | Owning chapter |
|---|---|---|---|
| PG-style SubPlan EXPLAIN rendering + V6 per-site counters | display / instrumentation change | S0 | [06 §6](06-cost-model-touchpoints.md), [07 V6](07-verification-and-measurement.md) |
| `subPlanHandle` — lifecycle wrapper giving every SubPlan eval site build-once / rescan-per-row semantics | new executor structure (not a plan node) | S2 | [04 §4](04-subplan-execution-engine.md) |
| Per-operator rescan/reset work: `limitOp` state reset at `Open`; `sortOp` rescan via `Close()+Open()` until a dedicated reset path exists; `seqScanOp` rewind resource-safety; `MultiHashJoin`/NLI rebuild gated on `ParamDirty` | operator changes | S2 | [04 §4.2](04-subplan-execution-engine.md) |
| Shared hash+LRU cache library (`kvcache`) with the volatile/LockRows cacheability gate | new executor-support package | S2 | [04 D4.4/D4.5](04-subplan-execution-engine.md) |
| Hashed-SubPlan state — main + null-partial-match hash tables per sublink site | new executor structure | S3 | [04 D4.3](04-subplan-execution-engine.md) |
| NLI semi/anti join variants + first-match early-out cost gate | operator change (nested-loop-index join) | S6 | [06 D6.2](06-cost-model-touchpoints.md) |
| `memoizeOp` — parameterized result cache under the NLI inner side; requires widening the `NestedLoopIndexJoin.Inner` plan-node contract | new plan node + executor operator | S7 | [05](05-memoize-operator.md) |

## 2. Dependency graph

```
S0 ──► S1 ──► S4 ──► S5 (S5a → S5b) ──► S6 ──► S7
 │                                        ▲
 └───► S2 ──► S3 ─────────────────────────┘ (S7 also needs S2's rescan contract)
```

- S1 and S2 are **parallel tracks** after S0 (planner vs executor; disjoint
  files); either may land first.
- S4 needs S1 (no point extending gates that don't fire).
- S5 needs S1+S4 (reorder pays off only when pull-up coverage is real);
  S5 is the highest-risk phase and is deliberately late.
- S6 needs S5 (costed placement of NLI-semi/anti assumes DP sees the joins).
- S7 last: it consumes both the rescan contract (S2) and the NLI insertion
  point (S6).

## 3. Milestone policy

- Milestone numbers are **reserved, not claimed**: "next free number ≥ 0124
  at implementation start" (0123 is the highest existing milestone at time of
  writing). This bundle is design-only and must not squat numbers that the
  nightly/Ralph pipeline may allocate first.
- Granularity (repo precedent: `wal-backend-flush` = one design, per-slice
  gates; `perf-optimize` = one milestone, chaptered design): **one milestone
  per 1–2 phases** — suggested packing: M(a) = S0+S1 (measurement +
  re-enable), M(b) = S2+S3 (SubPlan engine), M(c) = S4+S5 (coverage +
  reorder), M(d) = S6+S7 (cost + Memoize). Each milestone document lists its
  phases' acceptance gates verbatim from chapter 07.
- Every phase follows the loop discipline: design-doc updates land in the
  same commit as behavior changes; `.ralph/fix_plan.md` items reference the
  phase IDs (S0…S7) and this bundle's path.

## 4. Hygiene tasks (first implementation loop, cheap, non-optional)

1. **Stale milestone annotation** — `docs/milestones/0058-tpch-subplan-join-perf.md`
   and `0061-tpch-m0058-followups.md` still show unchecked boxes although
   M0058-0001/-0003/-0004/-0005 and M0061-0001 landed (commits `d5091071`,
   `faf2e71f`; verification `analysis/tpch-m0058-verification-2026-05-07.md`).
   Do **not** rewrite their history: add a dated annotation block at the top
   of each pointing at this bundle and at chapter 01's landed/regressed
   status table, and tick only boxes whose landing evidence is cited.
2. **Design index** — add the bundle bullet under `## Design Bundles` in
   `docs/design/README.md` (done in the same commit that adds this bundle).
3. **Plan-compare regeneration** — S0 re-captures goopg EXPLAIN plans on the
   current branch (already begun:
   [`evidence/explain-head-e4a43ba6.txt`](evidence/explain-head-e4a43ba6.txt));
   the full two-engine artifact regeneration happens at S5 close (V5.7).
4. **Deferral ledger** — any phase that lands a shortcut against PG semantics
   (e.g. S4's documented count-bug exclusion) appends a row to
   `.ralph/deferral_ledger.md` citing the upstream behavior
   (`postgres/src/backend/optimizer/plan/subselect.c` /
   `prepjointree.c`) and the resume point in this bundle.

## 5. Relationship to prior designs

| Prior doc | Relationship | Detail |
|---|---|---|
| `0058-0001-subplan-and-join-optimisation.md` | **superseded on overlap** | Gaps 1/3/4/5 landed (see chapter 01); its Gap-2 sketch and SubPlan-cache sections are subsumed by D3.0/D4.x; NUMERIC/cancel content remains authoritative there |
| `0061-0001-exists-anti-semi-join-unnesting.md` | **absorbed as current state; follow-ups superseded** | its implemented core is chapter 01 baseline; its non-firing at HEAD is exactly D3.0/S1 |
| `0063-0004-q21-anti-join-index-driven.md` | **absorbed** | becomes a special case of D6.2 (S6) |
| `0040-0001-subquery-caching-and-unnest.md`, `0033-0001-subquery-unnesting.md`, `0003-0008-subqueries.md` | built on (historical background) | terminology and the original unnest lineage |
| `fix-for-q5/` bundle, 0077 cost-model docs | built on; deliberately not extended | chapter 06 stays minimal; real cost surfacing stays with the 0077 line |
| M0122-0011 NullAware NOT-IN | retained divergence | correctness contract restated in chapter 02 §7 and guarded by V1 M2 |

## 6. Exit criteria for the bundle

The bundle is **implemented** when: all S0–S7 acceptance gates are green; the
end-state plan assertion holds (no opaque `<*planner.*Expr>` strings in any
TPC-H EXPLAIN); the correlated-query geomean sits inside the bulk-operator
band with the residual gap explicitly re-attributed to the executor
constant-factor line (out of scope here); and the regenerated plan-compare
artifact on the implementation branch documents the new plans side-by-side
with PG 18.3. Chapter 01's scoreboard table is then frozen with a final
`[measured]` column and the bundle status flips to `accepted`.
