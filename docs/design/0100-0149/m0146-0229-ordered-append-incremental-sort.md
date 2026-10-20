# M0146-0229 — TPC-DS SF1 Q23 statement 2: Incremental Sort over a Merge Append

Status: closed 2026-10-20. Slice 1 landed 2026-10-19, after the owner un-parked it (OWNER DECISIONS 2026-10-19 (b)).
Slice 2 (§7) fixed the branch row estimate, and SF1 Q23 now MATCHes PG. M0146-0229a, the Q74 executor recon, closed with no mechanism to port (§6). Filed by the S9 pass
at 4906691d7 (`analysis/m0146/m0146-0014/routing-20261019e/`).

## 1. Record

TPC-DS SF1 Q23, sort-strategy, SHAPE-DIFF at depth 1 (under Limit: PG Incremental Sort | goopg Sort), in query23's
second statement. It persists on the reloaded corpus, which carries the reference's statistics.

- PG: Limit → Incremental Sort (Sort Key c\_last\_name, c\_first\_name, sum; Presorted Key c\_last\_name,
  c\_first\_name) → Merge Append → two GroupAggregates, each over a Sort on its group key.
- goopg: Limit → Sort → Append over the same two GroupAggregates.

## 2. Diagnosis

The UNION ALL reaches the ORDER BY stage (createOrderedPaths) as the SetOp itself. Its members are Project →
GroupAggregate, and memberOrdering already reports their (c\_last\_name, c\_first\_name) ordering.
orderedAppendInput already builds a Merge Append over them. Only the grouping stage called it (M0146-0005au); the
ordered stage never offered PG's generate\_orderedappend\_paths input to create\_ordered\_paths.

The candidate is close on cost. On the SF1 PG-statistics clone it costs 72528.97..72546.65 against the full Sort's
72544.89..72545.45. PG elects it by 0.69. Two inputs differ from PG:

- Rows: goopg's branches estimate 150 + 76 = 226 rows, PG's 173 + 126 = 299. The bounded full Sort is cheaper over
  fewer rows. The branch estimate is the semi join against the best\_ss\_customer CTE (goopg a Hash Right Semi
  Join of 150 rows, PG a Hash Semi Join of 173).
- Member cost: goopg charges each member's Project a full cpu\_tuple\_cost per row (DeriveLegacyDisplayCost's
  default arm). In PG a branch's SELECT list is its Agg's own target, and a trivial SubqueryScan member costs its
  subquery's path (cost\_subqueryscan, costsize.c:1513). In this shape the charge cancels between the Append and the
  Merge Append.

The streaming UNION ALL's cost (costSetOp) also left out cost\_append's per-row overhead, cpu\_tuple\_cost *
APPEND\_CPU\_COST\_MULTIPLIER (costsize.c:2400-2403).

## 3. Slice 1 (landed 2026-10-19): the ordered-stage Merge Append arm

`analysis/m0146/m0146-0229/slice1-ordered-mergeappend.patch`:

- addOrderedAppendPaths (orderedappend.go), called from createOrderedPaths. For every distinct ordering a UNION ALL
  member delivers (all\_child\_pathkeys), it builds the Merge Append through orderedAppendInput. It offers that
  Merge Append as-is when its pathkeys contain the ORDER BY, or under an Incremental Sort when they cover a leading
  prefix and enable\_incremental\_sort is on. Producer `upper.ordered.mergeappend`.
- costSetOp's streaming arm adds cost\_append's per-row overhead.
- Pins (ordered\_append\_upper\_test.go): TestOrderedStageMergesUnionAllMembers,
  TestOrderedStageOffersIncrementalSortOverMergeAppend, TestUnionAllAppendCostsMembersPlusAppendOverhead. Each is
  mutation-checked; TestCostSetOpIsCostAppendAndCreateSetopPath is updated.
- Tried and dropped: pricing set-op members through their Projects (upperInputPathCost in seedPathForNode). It flips
  TestParameterisedAppendOverUnionAll, a PG-oracle pin (Nested Loop over a parameterised Append), to a Gather →
  Hash Join → Parallel Append. goopg's partial and mixed Append arms currently rely on the member charge. Ledger row
  2026-10-19.

Q23 does not flip with slice 1 alone (rows, §2). No PLAN-PARITY count moved: SF0.25 97/99, SF1 94/99, TPC-H 22/22.

## 4. Gate escalation

Gates on slice 1:
- units, TestPort\_RegressSuite, tpch-spotcheck and the acceptance arm (24 MATCH on values) PASS.
- The sf025 sweep PASSes (PASS=99, no verdict changes).
- tpcds-fireset FAILs twice (runs m29a and m29b): SF1 Q74 is an introduced TIMEOUT.

Why the Q74 timeout is not a regression:
- Q74's plan text, costs stripped, is identical between baseline and candidate, and it MATCHes PG.
- All other fired results are byte-identical at both scales.
- Timed directly on fresh clones, Q74 takes 543 s on the baseline and 522 s on the candidate, with identical
  output (`analysis/m0146/m0146-0229/q74-timing/`). The gate's limit is 600 s, and the candidate lane always runs
  second.

Per the banner's S9 (5)(a), the task does not revert or game the gate: it escalates `[!]` naming tpcds-fireset. The
owner's options are a Q74 timeout pin, a raised FIRESET\_TIMEOUT, or a gate-exceptions row. Resume: apply the patch,
re-run the gates, commit.

### 4.1 Resolution (2026-10-19)

The owner un-parked the slice. It ports PG's generate\_orderedappend\_paths / create\_ordered\_paths logic, and
withholding it would leave goopg reaching PG's plans by other means. Q74's runtime is not a rejection reason (S9
(5)(a)). tpcds-fireset reads an owner allow-list, `scripts/tpcds-fireset-timeout-allow.tsv`; its SF1 Q74 row expires
2026-11-30.

The patch was applied unchanged on 80fc64342 and re-gated:

- units, TestPort\_RegressSuite, tpch-spotcheck and the acceptance arm pass.
- The sf025 sweep is PASS=99.
- tpcds-fireset passes. Q74 finished inside the timeout this run, so the allow-list was not needed. All 19 fired
  results are byte-identical. Parity is held at SF0.25 97/99 and SF1 94/99.

Evidence: `analysis/m0146/m0146-0229/fireset3/` and `accept2`.

## 5. Next slices

- The branch row estimate: the semi join of catalog\_sales ⋈ frequent\_ss\_items against the best\_ss\_customer CTE
  (150 vs PG 173; the web branch 76 vs 126).
- The member projection charge (§3, tried-and-dropped), once the partial and mixed Append costs are aligned with
  PG's numbers for TestParameterisedAppendOverUnionAll's fixture.

## 6. M0146-0229a (2026-10-19): SF1 Q74's runtime is the plan's, in PG as in goopg

The owner filed this recon to establish whether Q74's 520–560 s, which straddles the fire-set's 600 s
FIRESET\_TIMEOUT, is a goopg slowdown or a PG executor mechanism goopg lacks. Evidence:
`analysis/m0146/m0146-0229/q74-recon/` (goopg) and `analysis/m0146/m0146-0229/q74-timing/` (PG, owner-captured).

- **Same plan.** Q74's plan is PG's (text-identical with costs stripped). The `year_total` CTE (UNION ALL of two
  grouped legs, about 3 s to build in both engines) is read by four CTE Scans. Each scan's quals (sale\_type, year,
  year\_total > 0) leave PG and goopg alike estimating 1 row; actually 37708 / 38252 / 11256 / 11153 rows come out.
  So the planner nests them: about 1.44 × 10⁹ pairs on `t_s_secyear`, then 14505 and 1585 passes over the web legs.
- **PG is slower.** PG 18.3 takes 896 s plain (owner, 78c59cd5d) and 897 s under EXPLAIN (ANALYZE, BUFFERS).
  542 s of that is the `t_s_secyear` CTE Scan (14.4 ms × 37708 loops) and 250 s is `t_w_firstyear`. goopg takes
  452 s under EXPLAIN (ANALYZE) on a fresh clone of the reloaded corpus, and 520–560 s in the earlier plain runs.
  goopg's inner CTE Scans report `loops=1` because its nested loop replays a materialized inner, where PG rescans the
  tuplestore. That is an instrumentation-counting difference, not a plan difference.
- **Profile (goopg, 90 s during the nested-loop phase).** 98% of CPU is under `nlJoinStream.stepInner` →
  `joinPredicateMatch`, the `customer_id` (bpchar) equality.
  - 37 s of 101 s lands on `textShapeAmbiguous`'s first byte read (`as[0]`). That is the first touch of each
    compared string, a cache miss on heap-scattered string data. The comparison itself would take the same miss, so
    the shape check is not the cost.
  - The rest is the evaluator (`evalExprSlot`, `compareDatum`, bpchar padding helpers).
- **Verdict.** There is no PG executor mechanism that makes this shape fast — PG is about 2× slower on it — and no
  goopg-side slowdown relative to PG. Nothing is filed. Q74's runtime is inherent to the PG-identical plan. Leaving it
  under 600 s would need a plan PG does not choose. So the fire-set's SF1 Q74 timeout is structural, not transient:
  owner's call whether to keep the allow-list row past 2026-11-30 or to raise FIRESET\_TIMEOUT for SF1.

## 7. Slice 2 (2026-10-20): the semi join against a grouped CTE is examined as the CTE

**Record.** After slice 1, PG's Incremental Sort over a Merge Append was generated but lost to a full Sort. goopg's
UNION ALL branches estimated 150 + 76 = 226 rows against PG's 173 + 126 = 299. In both branches the first
difference is the semi join against the `best_ss_customer` CTE, whose body is `GROUP BY c_customer_sk HAVING ...`.
The catalog branch's input is 302 rows in both engines. PG keeps 173 of them (selectivity 0.5732) and goopg kept
150 (0.4971).

**What PG does.**

- `cs_bill_customer_sk IN (SELECT c_customer_sk FROM best_ss_customer)` becomes a semi join (pull\_up\_sublinks).
  pull\_up\_subqueries then flattens the simple sub-select, so the semi join's inner Var is the CTE's own
  (RTE\_CTE).
- `examine_simple_variable`'s RTE\_SUBQUERY / RTE\_CTE arm finds the CTE's planned subroot. Its lone GROUP BY
  key marks the column `isunique` (selfuncs.c).
- `get_variable_numdistinct` therefore returns nd2 = the CTE rel's 33333 tuples, not DEFAULT\_NUM\_DISTINCT.
- `eqjoinsel_semi` takes the nd arm: nd2 / nd1 × (1 − nullfrac1) = 33333 / 57821 × 0.9943 = 0.5732.

**What goopg did.** A FROM-clause derived leaf already gets this through `derivedLeafUniqueCols` (M0146-0009e).
A leaf pulled up from a sublink body got only `pulledLeafUniqueOutput`, which marks the output unique when the
sub-select itself is grouped. Here the sub-select is a plain `SELECT c_customer_sk FROM cte`, and the grouping is in
the CTE beneath it. A trace of `eqJoinSelectivitySemi` showed the CTE operand with `isUnique=false`, so
nd2 = 200 (default) and the 0.5 × (1 − nullfrac1) arm applied.

**Change.** `tryPGShapedJoinSearch` (joinsearchseam.go) gives a pulled leaf that is itself a derived rel (`isSubplanLeaf`) the same
`derivedLeafUniqueCols` classification as a FROM-clause derived leaf. `examineJoinVar` then sets `isUnique` and
`getVariableNumDistinct` returns the CTE's rows.

**Result.**

| instrument | before | slice 2 | PG |
|---|---|---|---|
| SF1 Q23 catalog / web branch rows | 150 / 76 | 173 / 126 | 173 / 126 |
| SF1 Q23 plan | Sort over Append (depth-1 sort-strategy) | **MATCH** | Incremental Sort over Merge Append |
| TPC-DS fire-set PLAN-PARITY match, SF0.25 / SF1 | 98 / 95 | 98 / 96 | |

- Every row estimate in SF1 Q23 statement 2 now equals PG's. The one remaining text difference is the Group Key
  parenthesisation of `substr(...)`, which is M0146-0042's rendering class.
- SF0.25 Q23 stays MATCH. Its plan changed (the sf025 sweep reports one changed shape, Q23), and both captures
  match PG.
- Fired Q23 results are byte-identical at both scales.

**Pin.** `TestSemiJoinAgainstGroupedCTEUsesIsUnique` plans `fk IN (SELECT gk FROM b)` over a materialized
`GROUP BY gk` CTE of 30000 rows. It checks that the semi joinrel has 1000000 × 30000 / 50000 = 600000 rows, against
the default arm's 500000. Removing the classification fails it with 500000.

**Residual (ledger).** The member projection charge (§5, tried and dropped in slice 1) stays deferred.

