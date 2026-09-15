# M0142-0004 — re-measure TPC-DS's row-estimate error at HEAD

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md`'s M0142-0004 line. The ledger row
`take3-rowest-collapse-diagnosed` (`.ralph/deferral_ledger.md:2120`,
2026-09-06) named four cuts — B1, A1, A2, A3 — and pinned the headline
"3-5 orders out" (Q22 9,460,201 vs PG 11,987 = 789x; 22 of 100 Sort inputs
estimate 1) *before any of them* landed. All four are confirmed landed as of
this task (B1 `joinkeyproof.go:248`, A1 `rangequery.go:211-215`, A2
`selectivity.go:322`, A3 `reduce_outer_joins.go:141`), so the old headline
number is stale and there is nothing left to "cut" — only a fresh
measurement to take, per the task's own framing. Deliverable: re-score with
`make ea-ratchet` at a declared stats epoch, state the current error
distribution, confirm or refute whether the one still-open mechanism (B2 —
the missing `*Append` arm in `resolveBaseColumn`) still reproduces, and file
whatever the fresh data surfaces as new tasks.

## Method

`make ea-ratchet` was already re-scored at HEAD (`4c5c13905`) by the
immediately preceding M0137-0018 task (measurement-only; no code changed
between that commit and this task's HEAD `108f88c3f`, so the capture is
still valid — confirmed via `git log 4c5c13905..HEAD -- internal/` returning
no planner/executor/catalog diffs). Rather than re-running the ~10-minute
capture a second time for the same code state, this task analyzed the
existing artifacts it produced:
`analysis/planner-refactor-take3/c20a-estimator-census-20260915/ea-findings-20260915.json`
(140 findings, `scored=605`, `tol=2.0`, `floor=10.0` — i.e. every plan node
scored corpus-wide, findings are the subset with `qerr >= 10`).

B2's structural check: `grep -n "case \*" internal/optimizer/joinkeyproof.go`
— the resolver family's arm list still ends at `*Aggregate`; no `*Append`
(or `*SetOp`, the actual Go type EXPLAIN renders as "Append") arm exists.

## Findings

**1. Error distribution at HEAD (605 nodes scored, 140 flagged at qerr>=10):**

| qerr band | count |
|---|---|
| >= 1000x | 20 |
| 100x - 1000x | 41 |
| 10x - 100x | 79 |
| max observed | 14,500x (Q47, `CTE Scan on v2`, est=2 vs actual=29,000) |

This is best read as "roughly 1-4 orders of magnitude out, for the ~23% of
scored nodes that miss by 10x or more" — not a restatement of the old "3-5
orders" headline, which was a single worst-case anecdote (Q22) rather than a
distribution, and predates every one of the four landed cuts. A like-for-like
before/after on the *same* worst-case query is not possible because Q22 (the
old headline's subject) has zero findings >= qerr 10 at HEAD — B1 alone
already collapsed it from 9,460,201 to 72,001 against a PG estimate of
71,857 per the original ledger row, and it no longer surfaces here.

**2. B2 (missing `*Append` arm, `resolveBaseColumn`) no longer reproduces at
its original witness.** The ledger's cited symptom was Q76's UNION ALL/Append
join keys (goopg 67,352 vs PG 6,810 vs actual 470). Q76 is in the scored
corpus (`"queries"` includes 76) and has **zero** findings at qerr>=10 now —
one of the four landed cuts (most likely A3, the anti-join transplant, since
Q76 is exactly the correlated-NOT-EXISTS shape A3 targets) apparently already
fixed the join-key pricing that ran through the union. The structural gap in
`resolveBaseColumn` — no `*Append`/`*SetOp` case — is still real (confirmed
by the grep above) but is not currently the cause of any >=10x miss in the
SF0.25 corpus. Reclassifying B2 from "STILL OPEN, concrete Q76 witness" to
"structurally present, currently latent — no live witness" rather than
closing it outright, since a future plan-shape change could reawaken it with
no code change of its own.

**3. Two new mechanisms surfaced by this fresh pass, neither previously
named in the ledger:**

- **(C1) Unique-pkey equality-bound `IndexScan` hard-codes 1 row, 19 of the
  140 findings** (`Index Scan using X_pkey`, `est=1`), qerr from 27x to
  12,445x. `indexScanRows` (`cardinality.go:329`) returns 1 whenever
  `idx.Unique && nEq >= len(idx.Columns)` — a direct, deliberate port of PG's
  `btcostestimate` unique-equality special case (see the function's own
  comment) and correct for a single, non-repeated probe. But several of these
  findings carry a **PG-side estimate that is also far from 1** (q34
  `household_demographics_pkey`: goopg=1, PG=489, actual=10082; q68 same
  index: goopg=1, PG=1800, actual=5899) — if the probe were genuinely a
  single unique-key equality lookup, PG's planner would price it at 1 too
  (upstream shares the identical `btcostestimate` special case). PG pricing
  a materially higher number for the *same named index* on the *same table*
  is evidence the two engines are not looking at the same physical operation
  (most likely a scan repeated once per outer row of a correlated
  subplan/lateral context, where EXPLAIN ANALYZE's `actual` field is a
  per-loop average that this task's capture tooling may be reading as a
  bare total — an instrumentation question, not necessarily a planner
  question) rather than the same equality-bound pkey probe mispriced by only
  one side. Filed as **M0142-0004a**, recon-scoped: first determine whether
  this is a capture/accounting artifact (loops vs total) or a genuine
  per-engine plan-shape divergence, before touching `indexScanRows`.
- **(C2) `SetOp`/"Append" underestimates a 3-way `UNION ALL` of CTE
  branches, 4 findings.** Q33/Q56/Q60 all union three CTEs (`cs`, `ss`,
  `ws`) and estimate `est=3` against actuals of 405-1557 (135x-519x); Q49's
  `HashSetOp Union` estimates 1 against 12 actual (12x). `est=3` for a
  3-branch union is consistent with each branch (a `CTEScan` over a
  filtered fact-table query) independently estimating ~1 row and
  `estimateSetOp` (`cardinality.go:208`) summing `1+1+1`, which points at
  the CTE branches' own row estimates rather than at `estimateSetOp` itself
  (which already implements PG's `prepunion.c` UNION/INTERSECT/EXCEPT
  arithmetic correctly, per its header comment). This is a different
  mechanism from B2 (`resolveBaseColumn`'s missing arm is about join-key
  *selectivity* through an Append, not the CTE branch's own row count) and
  was not previously named. Filed as **M0142-0004b**, recon-scoped: trace
  why `cs`/`ss`/`ws`'s own `EstimateRows` collapses to ~1 before deciding
  whether the fix belongs in CTE row estimation (a known-active area — see
  `docs/design/*/cte_leaves*` in the memory index) or in `estimateSetOp`.

## Gate-gap note (unchanged from the ledger)

`scripts/pg-plan-parity-diff.py`'s nine categories carry no `rows=`
dimension, so none of this is visible to the plan-parity gate directly — it
only reaches the metric indirectly, via changed plan shape. `make ea-ratchet`
remains the only instrument that scores it, and per M0137-0018 it is now
correctly baselined at the current SF0.25 corpus scale.

## Verification

Read-only analysis of an already-captured, already-verified artifact
(M0137-0018's own commit measured 99/99 clean capture at the same HEAD this
task treats as current). No production code changed by this task; no gate
re-run needed beyond `make ralph-state-guard`.
