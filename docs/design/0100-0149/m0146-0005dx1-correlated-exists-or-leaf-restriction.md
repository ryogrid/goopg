# M0146-0005dx1 — an OR of correlated EXISTS is the first relation's restriction

Status: done (2026-10-04). Parent: M0146-0005. Filed by the M0146-0005dx
recon (`m0146-0005dx-sublink-restriction-recon.md`), together with dx2.

## The divergence

TPC-DS Q10 and Q35 filter `customer c` with
`(EXISTS (… web_sales … c.c_customer_sk …) OR EXISTS (… catalog_sales …))`.

- **PG.** `distribute_qual_to_rels` (`./postgres/src/backend/optimizer/plan/initsplan.c`)
  makes the OR a restriction of `c`, because the SubPlans' args read only
  `c`.
  - `cost_qual_eval` prices it per row by the plain correlated SubPlan
    (costsize.c:5027-5038), about 21123 per `customer_pkey` probe in Q10.
  - That price is why PG puts `customer_demographics` outermost and
    materializes the rest.
- **goopg.** It held the OR as a Filter above the whole join tree, and the
  search never paid for it.

## Change

- **Placement.** `correlatedScalarSublinkLeaf` (`local_filters.go`, from
  M0146-0005bu) now also admits correlated EXISTS sublinks under the same
  rule as scalar ones: every inner-plan outer reference names binding 0,
  where leaf and FROM-cumulative coordinates coincide, so nothing in the
  sublink's plan needs rebasing. Two refinements:
  - An EXISTS that is the conjunct itself, or its NOT, stays above. It
    belongs to the post-planning unnest pass as a semi or anti join. Only
    an EXISTS nested in a larger qual (Q10's OR) can never become a join.
  - A conjunct with no same-scope column, like Q10's OR whose only Vars
    are the SubPlans' outer references, is attributed by those
    references. `tableForCol` answered -1 for it.
- **Hashed ANY at the probe.** `rewriteExistsToAny` (`exists_to_any.go`)
  now also rewrites the `Cond` of an `IndexScan`, `IndexOnlyScan` and
  `BitmapHeapScan`. That is where a restriction rides when its relation is
  a nested loop's index-probed inner. The host row is the scan's own
  output, the coordinates `Cond` uses.
  - Without this, Q10's filter stayed `EXISTS(SubPlan 1) OR
    EXISTS(SubPlan 2)` and ran both subplans per customer.
  - With it, the line reads PG's
    `(ANY (c_customer_sk = (hashed SubPlan 2).col1)) OR (ANY …)`.
- **dx2 — pricing needed no code.** M0146-0005di already made
  `qualEvalOps` price a planned sublink by `cost_subplan`, per evaluation
  when correlated. The restriction is now charged on the scan: Q10's probe
  is 0.25..22876.41 against PG's 0.29..21123.25.

## Effect

- **Q10 and Q35** run in 1.4 s and 0.7 s on an SF0.25 clone and return
  the oracle's row counts (0 and 100).
- **Fire set.** Only Q10 and Q35 fired; matches are flat (41 / 32).
  CATEGORIES-EXCL-MATCH:

  | category | SF0.25 | SF1 |
  |---|---|---|
  | join-order | 50 | 56 → 55 |
  | parameterisation | 27 → 26 | 34 → 32 |
  | aggregation-strategy | 13 → 12 | 22 → 20 |
  | sort-strategy | 26 | 29 → 28 |
  | parallelism | 29 | 46 → 45 |
  | scan-type | 29 → 30 | 34 → 35 |
  | qual-placement | 10 → 12 | 8 → 9 |

- **Q10's tree** now has PG's order: `customer_demographics` Seq Scan
  outermost, Materialize over unique `ss` → customer probe → `ca`. The one
  remaining difference is the `ca` join, and it is the source of the
  qual-placement increase:

  | | `ca` join | join qual |
  |---|---|---|
  | goopg | Nested Loop over Materialize(Seq Scan `ca`) | Join Filter |
  | PG | `customer_address_pkey` probe | Index Cond |

- **Q35 at SF1** is down to sort-strategy and parallelism. PG reads `ca`
  through Gather Merge + Incremental Sort.
- **Other gates.**
  - The sweep passed 96/96.
  - The TPC-H arm matched 24/24, and tpch-spotcheck passed.
  - ea-ratchet passed. Its "Q92 fixed" line predates this change.
  - Regress A/B over 18 planner cases moved nothing beyond nondeterminism
    (row order, parallel NOTICE order, diff alignment).

Test: `TestCorrelatedExistsOrIsLeafRestriction` fails on HEAD, where the
filter's host is the Hash Join.

- **Inner join.** The hashed ANY OR must sit on `Seq Scan on xo_c c`, which
  is PG 18.3's plan on the same data. The values must equal those of the
  same query with the qual held above the join.
- **RIGHT JOIN with `c` nullable.** The qual must not sink below the join.
  It must stay above, and the values must match.

## The `ca` tie: why the arm reorder is not in this slice

Q10's `ca` probe ties the Materialize(Seq Scan) inner within
STD_FUZZ_FACTOR. Totals are near 2.2e7, about 21980879 against 21984115.

- **Why PG keeps the probe.** Under LIMIT, `consider_startup` is on, and the
  tight 1.0000000001 comparison returns COSTS_DIFFERENT (the probe's startup
  is 0.25 higher), so `add_path` keeps the path filed first.
  `match_unsorted_outer` offers the `cheapest_parameterized_paths` loop
  (bare inner, index probes, Memoize) before the `matpath`
  (joinpath.c:1883-1971), so the probe is PG's incumbent.
- **Why goopg kept the matpath.** `addNestLoopPath` filed the matpath before
  `addNLIPaths` ran, so the matpath was goopg's incumbent.
- **Trying the reorder.** Offering the matpath after the NLI arm made Q10
  match PG at both scales, but:
  - Q8 lost its match at both scales. PG has the same near-tie there: its
    hash alternative is 28305.93 against the chosen 28552.
  - ea-ratchet failed on a new `Q8:date_dim+store+store_sales` relset
    (estimate 818, actual 19725, PG plan has no such node).
- **Saved for the follow-up.** The reorder is kept as
  `analysis/m0146/m0146-0005dx1/matpath-after-nli.wip.patch`, with its fire
  set and ea-ratchet evidence beside it. It is filed as M0146-0005ea.

## Not done (ledgered)

- **Only binding 0.** A correlated sublink whose references name another
  relation needs its plan's outer references rebased into the leaf's
  coordinates. The recon's `remapOuterRefsInSubplan` no longer exists.
- **Plain SubPlan cost.** On the regression fixture PG's `c` scan costs
  392058 and goopg's 200218. The plain correlated plan's per-call cost
  read by `subPlanCostOps` is about half of PG's. On Q10 it is close:
  22876 against 21123.
- **Q35.** PG feeds `ca` through Gather Merge + Incremental Sort below the
  Materialize.
