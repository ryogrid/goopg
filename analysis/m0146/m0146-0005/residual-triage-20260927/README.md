# M0146-0005 residual triage, 2026-09-27 (HEAD `32d779e41`)

Re-run of the first-divergence census on the post-slice-25 fire-set
captures, plus one flag experiment and one live DPPATH trace. Purpose:
decide whether any census record still belongs to M0146-0005's
join-order / candidate-pool scope, or whether everything remaining is
owned by a later named task.

## Inputs

- `census-sf025-head-32d779e41.txt` — `pg-plan-first-divergence.py` over
  the fire-set capture taken at `32d779e41` (`tmp/fireset-m0146-analyze-ownxid/`).
  Headline: `queries=99 divergent=89 match=10`.
- `census-sf025-partial-sort-paths-on.txt` — same census over a fresh
  capture of the identical binary with `GOOPG_PARTIAL_SORT_PATHS=on`
  (`tmp/pson-capture/`, private clone `tmp/m0146-0005-pson-data-tpcds-sf025`).
- `plans-sf025-partial-sort-paths-on.txt` — the flag-on goopg plan file.
- `q91-dppath-rel0-6.txt` — DPPATH lines for Q91's divergent rel
  `{call_center, cr, d, c, ca, hd}` on a `GOOPG_PGSHAPED_DP_TRACE=1`
  private lane (port 5590, clone above, binary `tmp/goopg-pson`).

## Result 1 — `GOOPG_PARTIAL_SORT_PATHS=on` moves zero plans at SF0.25

Header-only diff vs the flag-off capture; all 99 first-divergence records
byte-identical. The tournament in `partialsortpaths.go` never fires
because the Sorts in question sit above join spines `findPartialSubtree`
cannot reach — the flag-off type switch was not the binding constraint
on this corpus. Consequence: **do not flip the default as a parity move
on TPC-DS** (the C-19e design doc's TPC-H q16 measurement stands on its
own, and its §6.5 parking reason — the shared `plan_snapshots/` re-pin —
is unchanged).

## Result 2 — Q91 routed to M0146-0010 with mechanism detail

Census record: `depth=5 [join-order] PG <Nested Loop, Seq Scan> | goopg
<Seq Scan, Nested Loop>` — same join tree, swapped NL children at the
`call_center ⋈ chain` node (rel `{0,1,2,3,4,6}`).

DPPATH shows BOTH orientations in the pathlist at a 0.012 cost margin:
`outer={0} inner={1,2,3,4,6}` (elected, total 3246.9710) and
`outer={1,2,3,4,6} inner={0}` (total 3246.9835). PG elects the second.

The mechanism is PG's `match_unsorted_outer` (joinpath.c:1893): for the
cheapest inner it builds `create_material_path` and tries the NL against
raw inner and materialized inner as **separate candidates**; the raw
rescan is charged `(outer_rows - 1) * inner_rescan_run_cost` at full
inner cost, the materialized variant pays `cost_material` build plus
`cost_rescan` replays, and `Materialize` appears in the plan exactly when
the materialized candidate wins (Q8's `Materialize` node is the same
mechanism surfacing at a different node).

goopg's `addNestLoopPathFor` (pathgen.go:205-218, the take2 P2-06
shortcut) instead always prices the inner rescan as a cache replay —
`matRescan` from `nestLoopInnerRescanCost` plus one `matBuild` — with no
raw-rescan candidate and no Materialize node. That is M0146-0010's filed
scope (`cost_material`, `cost_rescan`, nested-loop admission rules,
removing `join_nl_stream.go`'s unconditional wrapping). The Q91 trace
sharpens it: the fix is not only emitting the node — the two-candidate
adjudication is what flips the 0.012 margin.

## Result 3 — per-family routing at SF0.25 (89 records)

| family | size | owner |
|---|---|---|
| `PG Incremental Sort | goopg Sort` under `Limit`/other parents | ~13 | M0146-0006 (sequenced after 0005; M0141-S2b-8/S2b-9 filed inside it) |
| `PG Nested Loop Inner | goopg Sort` under `GroupAggregate`; `PG Gather Merge | goopg Sort`; `PG Gather | goopg Nested Loop` | ~10 | **unowned → filed as M0146-0027** (parallel partial-subtree reach; Q6/Q17/Q25/Q29/Q50/Q77 + related cells) |
| `qual-placement` on same-node records (Q13/Q24/Q46/Q48/Q68/Q78/Q94/Q95) | 8 | M0146-0012/0012a — PG pushes composite OR join clauses into the parameterized inner index scan's `Filter`; goopg leaves them as NL `Join Filter` (verified on Q13/Q48) |
| `PG Nested Loop Inner | goopg Hash Join Inner` under `Sort`/`Nested Loop` | 4 | partially stale-`char(n)` (Q79/Q30) + sublink residual (Q1) + Q92 — already routed in the 2026-09-26 triage |
| `parameterisation` (Q32/Q44/Q81) | 3 | M0146-0011 |
| `error` (Q36/Q70/Q86) | 3 | parse-level; both sides error — not a plan divergence to chase in the planner |
| `scan-type` IOS (Q23/Q82) | 2 | M0146-0019 |
| `scan-type` `Subquery Scan on ssr/foo` under `Append`/`CTE` (Q5/Q39/Q80) | 3 | M0146-0026 / M0146-0007 |
| `join-order` Materialize (Q8) + swapped children (Q91) | 2 | M0146-0010 (Result 2) |
| `PG CTE avg_sales | goopg Sort` (Q14) | 1 | M0146-0007 |
| `aggregation-strategy` `Finalize*`/`MixedAggregate`/`GroupAggregate` cells | ~14 | M0146-0009 statistics burn-down + M0141-S4/S5/S6 (partial-agg shape work, parked under closed M0146-0003) |
| `WindowAgg`/`Unique`/`Group` sort cells | ~6 | M0146-0017 / M0146-0018 / M0146-0020a |
| `PG Sort | goopg Gather Merge` over-election | ~3 | unpriced-side residual of the same partial-sort arm; under Result 1's measurement this is the arm's judgement, not a flag position |

SF1 shows the same shape (divergent 86, sort-strategy 36, parallelism
13); no separate routing needed.

## Loop verdict

Every remaining SF0.25 record now routes to a named task except the
parallel partial-subtree-reach family, which has no owner — filed as
M0146-0027. M0146-0005 keeps no in-scope direct slice this loop; the
umbrella stays open (burn-down convention) while the parallel-reach
family and stats-tied residuals are worked elsewhere.
