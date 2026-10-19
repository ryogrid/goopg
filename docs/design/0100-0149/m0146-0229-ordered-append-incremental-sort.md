# M0146-0229 — TPC-DS SF1 Q23 statement 2: Incremental Sort over a Merge Append

Status: open, escalated `[!]` on the tpcds-fireset gate (§4). Slice 1 is parked as
`analysis/m0146/m0146-0229/slice1-ordered-mergeappend.patch`. Filed by the S9 pass at 4906691d7
(`analysis/m0146/m0146-0014/routing-20261019e/`).

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

## 3. Slice 1 (parked): the ordered-stage Merge Append arm

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

## 5. Next slices

- The branch row estimate: the semi join of catalog\_sales ⋈ frequent\_ss\_items against the best\_ss\_customer CTE
  (150 vs PG 173; the web branch 76 vs 126).
- The member projection charge (§3, tried-and-dropped), once the partial and mixed Append costs are aligned with
  PG's numbers for TestParameterisedAppendOverUnionAll's fixture.
