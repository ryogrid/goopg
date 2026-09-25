# R32 — EXPLAIN must render targetlist subqueries as InitPlan/SubPlan subtrees (K37 campaign, slice 1)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Rendering axis, in service of the parallelism axis (K37). No planner
change; display-only by construction, so values-safe.*

## 0. How this round was chosen

K37 ("66 of 99 TPC-DS queries: PG parallel, goopg serial") is not one
mechanism. A fresh-clone census on PG-faithful heap (R31b rebuilt
`/tmp/pp2/ds05fresh` with the current binary; `store_sales` 25866
pages vs PG 25928) plus per-query probes partitions the Gather-MISS
set into at least four classes. The census, all captured with pinned
`work_mem='64MB'` / `max_parallel_workers_per_gather=4` on
`GOOPG_ANALYZE_SEED=20260905` data:

```
PG-live:      180 Gather lines, 162 Parallel-Hash-Join lines
goopg default: 42 Gather lines,   0 Parallel-Hash-Join lines
goopg all:    104 Gather lines, 162 Parallel-Hash-Join lines
goopg gathers where PG has none: ZERO queries (no over-admission)
```

Per-query alignment (`goopg-all` vs PG-live Gather counts) matches on
~40 queries, misses on ~40, exceeds on ~20 — and the misses split:

- **Display gap (this round, K42).** Q9: PG 111 lines / 15 InitPlan-or-
  SubPlan lines / 15   Gathers; goopg 6 lines / 0 / 0 — yet Q9 executes
  correctly (`tpcds-results-sf05/oracle.txt`: `9|OK`). The 15 PG
  Gathers live INSIDE scalar-subquery InitPlans (every PG Gather sits
  under an InitPlan); goopg plans and runs
  those subqueries but prints none of them. (Line counts exclude the
  `===== Qn =====` section marker: 112/7 with it.) Smaller deltas of
  the same shape: Q1, Q32, Q81, Q92 (PG 2 Init/Sub lines, goopg 0). No planner
  fix can ever close these; until display lands, the REAL Q9 gap
  (partial aggregation inside subqueries) cannot even be measured.
- **Append-driven (later slice, K43).** Q5 trace
  (`GOOPG_GATHER_PATHS=all` + `GOOPG_PGSHAPED_DP_TRACE=1`): base-level
  partials accepted, base Gathers produced-but-dominated, and ZERO
  partial paths on any join rel — the joins sit above serial `Append`
  legs with no partial-Append producer. Corpus-wide PG uses Parallel
  Append in only 6 queries (Q2/Q5/Q14/Q71/Q75/Q76) and only Q5+Q76
  miss, so this class is real but narrow — not slice 1.
- **Join-order-driven (NOT K37; K26 owns it).** Q6: goopg hash-joins
  1.4M rows where PG nested-loops 182 rows via index probes.
  Parallelism here is downstream of join order/method; costing or
  admission work cannot reach it. Referred to K26, excluded from this
  campaign.
- **Base-dominated, cause unattributed (later slice).** Single-Gather
  misses (Q20/21/22/34/37/40/43/50/62/63/67/68/73/82/91/92/99 class)
  where partials exist but the Gather loses. Estimate vs cost still
  unseparated — one known confound inside it: Q5's date-range rows
  (goopg 8116–12121 vs PG 8–14 on the same `d_date` predicate, new
  item K45, estimate side). Per K39a's lesson (a 15% `relpages` error
  moved nothing), attribution comes BEFORE fixing.

Slice 1 is the display gap because it is (a) a prerequisite for
measuring the Q9 real gap, (b) zero planner risk, (c) the single
largest per-query line delta in the corpus (105 lines on Q9 alone).

## 1. Root cause

`internal/executor/operators_explain.go:548`: `emitSubPlanSubtrees`
"drains the sublinks assigned while rendering" — InitPlan/SubPlan
subtrees appear only for sublink expressions the outer-plan text
renderer visits. There is a specific skip site, not a vague gap:
`walkPlanFiltered` lines 424–427 (ANALYZE twin 1549–1552) skips
`*optimizer.Project` by recursing into `Child` without visiting
`p.Targets` (`plan.go:1576`). Q9 goopg's visible plan is a bare
IndexScan — the Project carrying the 15 CASE expressions is skipped
silently. (Scans carry no `Targets` field, and the walker skips only
Project and Filter wrappers at 421–447, so Project (+Result, done
below) is the complete gap for Q9.) EXPLAIN CAN print InitPlans (Q6
shows 3, Q14 shows 14, Q23 shows 4) — only the Project/Targets site
is unvisited. The fix precedent is already in-tree: the
`*optimizer.Result` arm (`operators_explain.go:622-635`) does exactly
the pattern — `formatExprQual` over `Targets`, discard the text, keep
the assign side effect — for the childless-Result S6 case. R32 is
"extend the Result precedent to Project." Numbering/label invariants
(review 2026-09-09): `subPlanName` lives at
`operators_explain.go:1266-1275` with an inline duplicate of the
InitPlan-vs-SubPlan discriminator at 567–574; the two copies must
stay in agreement (already commented in-tree at 569–570). `assign`
(1230–1243) dedupes, so double-visiting cannot renumber or duplicate
— but it numbers in first-visit order, so visiting Project Targets
can renumber queries where other sites also hold sublinks; "PG's
numbering" is expected (left-to-right CASE order) but unverified
until the post-fix capture.

## 2. Change (display-only)

Make the EXPLAIN expression renderer visit Project Targets
(`formatExprQual` over `p.Targets`, discarding text, keeping the
assign side effect — the Result-arm precedent at
`operators_explain.go:622-635`) so their sublinks are assigned and
`emitSubPlanSubtrees` prints them, with the existing
InitPlan-vs-SubPlan discriminator copies kept in agreement. The fix
must land in BOTH text walkers (`walkPlanFiltered` ~514 and
`walkPlanAnalyzeFiltered` ~1656), or ANALYZE text silently diverges.
The `CaseExpr` arm (1423–1442) already recurses into WHEN/THEN/ELSE
and the `SubqueryExpr` arm (1388–1391) already assigns, so no
renderer-expression change is needed. JSON (`planToJSONNamed` family)
is OUT of scope for this round — TEXT only; structured EXPLAIN stays
stale and is named as a follow-up. No planner,
executor, or costing input may change: the plan struct is read-only
here. PG reference shapes: Q9 (fifteen scalar subqueries over
`store_sales` in CASEs, outer query over `reason`),
Q1/Q32/Q81/Q92 (smaller deltas, same mechanism).

## 3. Success tests (all must hold)

1. Q9 section: goopg shows 15 InitPlan-or-SubPlan lines (was 0); the
   fifteen scalar subqueries appear with PG's InitPlan labels/numbering
   (numbering expected from left-to-right CASE visit order, verified
   only by the post-fix capture). DONE 2026-09-09: InitPlan 1..15 in
   CASE order under the top IndexScan, matching PG's placement; the
   newly visible inner plans are serial `Aggregate`s — the K44 real
   gap, now measurable.
2. Q1/Q32/Q81/Q92 Init/Sub deltas close (PG 2 -> goopg 2 each).
   REFUTED 2026-09-09, reclassified: their sublinks live in
   Filter/Join-Filter quals (Q32/Q92 `Join Filter: (... > (SubPlan
   1))`, Q1/Q81 `Filter: (ctr_total_return > (SubPlan 2))`), where
   goopg's planner chose decorrelation (Q32: HashAggregate-as-input,
   `Filter: (... > (1.3 * avg))`) while PG kept SubPlan. That is
   planner subquery strategy, NOT the Project-Targets display
   mechanism — out of scope for this round, referred to a future
   subquery-planning round.
3. Previously byte-identical sections stay byte-identical — the
   load-bearing guard, not decoration: first-visit renumbering can
   touch any query where a Project sits above other sublink sites, so
   run it on the FULL corpus, not just the sensitive cases
   (Q14/Q23/Q6 with 14/4/3 lines).
   Instrument for tests 1–3: `scripts/tpcds-plan-diff.py` (native
   `===== Qn =====` format, byte-compare per query, `--verbose` for
   the Q9/Q1/Q32/Q81/Q92 diffs) under the pinned env archived in §5.
   `scripts/pg-plan-parity-diff.py` is shape-verdicts-only here (needs
   reformatting to `=== QN`, and its N6 normalisation erases subplan
   numbering by design).
   GUARD RESULT 2026-09-09: TPC-DS full-corpus A/B (base-HEAD binary
   vs r32, same fresh-clone data, pinned env) — exactly one section
   changed (Q9: 6 -> 66 lines, 0 -> 15 Init/Sub); Q36/70/86 differ
   only in the capture temp-file PID inside the psql error prefix
   (shared PG-unparsable syntax errors, out of scope). TPC-H A/B on
   `/tmp/pp2/tpch` — 22/22 byte-identical modulo the header line.
4. Values gates unchanged by construction (no plan struct touched);
   confirm via the standard sweep, not by reasoning.
5. `match` count is NOT a success criterion (ROADMAP conjunction:
   Q9 still differs in partial-agg-inside-subquery, K44, which this
   round only reveals).

## 4. Non-goals / sequencing

- K43 (partial Append producer + executor): needs a producer, a path
  kind, and executor support — a full round of its own.
- K44 (partial aggregation inside InitPlans): unmeasurable until
  this round lands; becomes slice 2 or 3.
- K45 (date-range row inflation): estimate side; the attribution
  sweep separates it from Gather-costing before any fix.
- K26 (join-order): Q6-class misses belong there, not here.
- K41 (dimension heap density): costing confound, noted, not
  blocking (K39a).

## 5. Evidence archive

- `/tmp/pp2/k37-ds-default.plans.txt`, `/tmp/pp2/k37-ds-all.plans.txt`,
  `/tmp/pp2/k37-ds-pg.plans.txt` (fresh-clone captures, pinned env)
- `/tmp/pp2/k37-trace.log` (Q5 DPPATH/DPTRACE under `all`+trace)
- PG-faithful heap proof: R31b `REPORT-fresh-clone.md`
