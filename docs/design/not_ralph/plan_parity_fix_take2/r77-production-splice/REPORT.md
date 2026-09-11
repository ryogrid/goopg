# R77 REPORT: production NLI price splice landed

## What shipped

`internal/optimizer/nlipricesplice.go` (new) + two call sites in
`planner.go` (post-`rewriteJoinsToNLI` :1646, end of
`planSelectWithSettings` :2417) + keeper
(`semiadmission_test.go: TestSemiProbeSpliceStampsPricedCost`).

- P0 verdict: post-pass branch. R76's in-situ stamp did not land on
  the EXPLAIN-read instance for Q22 (displayed 16666 vs stamped
  18200 — a later pass rebuilds the node); `maybeAttachMemoize`
  returns the same instance, so the rebuild is downstream.
- Dual placement rationale (found during P1): end-of-pipeline only
  leaves STALE upper stamps (ordered winner 1426.78 over a repriced
  491169 HashAgg — parent cheaper than child). The :1639 call feeds
  the aggregate/ORDER-BY elections the real base; the end call
  catches rebuilt nodes (idempotent via carrier-set skip). Q4 with
  both: `Sort 491312.45 → HashAgg 491169.67 → Semi 490456.34
  rows=57066` — monotone, rows unmoved.
- Pricing = R76 probe verbatim (production helpers, joinrel rows =
  node's own legacy estimate, `numQualOps` = unbound-movable count,
  cp from `ps`). Fail-closed: unresolvable table/stats/index/keys,
  no filed path, any error → untouched (today's display).
  Bitmap-probe inners decline (no table handle) — today's behavior.

## Gates (all green)

- values 24/24 MATCH (`/tmp/pp2/r77/values-arm.txt` vs
  `bench/tpch/baseline-digests.txt`, VERDICT PASS).
- TPC-DS SF0.25 sweep PASS=96 MISMATCH=0 SKIP=3
  (`bench/tpcds/runtime_goopg/tpcds-results-sf025/sweep-20260912-013556.txt`).
- pp: Q4 `[aggregation-strategy,sort-strategy]` on PG-scale
  estimates (491312 vs 192121); Q22 same family read-only; Q13
  untouched.
- 19/22 TPC-H byte-identical (`/tmp/pp2/r77/sweep22/` vs
  `/tmp/pp2/r76/sweep22clean/` — only Q4/Q21/Q22 differ, costs-only
  in the EXISTS family; rows unmoved everywhere).
- optimizer suite 2863 passed.

## DS plan-channel footprint (adjudicated, non-blocking)

90/99 DS plans differ vs the Sep-11 capture — which predates R66
Slice-2 + R69, so it conflates three slices. Attribution:

- Join-method flips (NL↔Hash/Merge) CANNOT be this slice: the
  search never reads display costs (all `legacyDisplayCostOf`
  readers are upper-rel agg/sort/distinct seeds — verified by
  grep). They are R66/R69's (R69 adjudicated 24 NL→hash vs live
  PG: 14 toward, 4 neutral, 6 residuals).
- Agg/sort downstream flips (Q51/Q58/Q59/Q8/Q77/Q96 Gather +
  HashAgg↔GroupAgg) ARE this slice's expected footprint (upper
  seeds now read stamped prices) — values green; PG-oracle
  direction adjudication belongs to the rows/width program.
- Memoize loss / Index→Seq / Gather appearance: search-cost
  effects (R69's repricing), display-blind by the same argument.

## Out (per SCOPE)

Join order, cost terms, hash-SEMI, selectivity/rows, widths,
election rules, fuzz, Q13-inner, parallelism; Q22-seam
root-causing beyond the stamp-site check (gates did not complain).
Next: rows/width program (R72 items 1–2) to move the Q4 election.
