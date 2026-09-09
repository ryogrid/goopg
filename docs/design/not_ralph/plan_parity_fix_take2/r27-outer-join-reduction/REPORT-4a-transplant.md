# R27 §4a — the demotion now reaches the plan

*2026-09-09. Completes K29's sequence: fix the analysis (R27), then the
ordering (this), then the declines.*

## 1. Result

| | before | after | PG |
|---|---|---|---|
| **Q49 seam declines** | 3 | **0** | — |
| **Q49 join nodes** | 3 `Hash Join` + 3 **`Hash Left Join`** | 5 `Nested Loop` + 1 `Merge Join` | 6 `Nested Loop` |
| TPC-DS seam declines, total | 13 | **9** | — |
| TPC-DS `join-method` | 74 | **73** | — |
| TPC-DS `scan-type` | 71 | **70** | — |

Q49's `Hash Left Join`s are gone and five of its six join nodes now
carry PG's node type. It is admitted to the PG-shaped search again, so
it is at last *eligible* to converge — which, per K27, it was not
before at any cost setting.

## 2. What landed

`demotedForPlan` runs the demotion analysis on a throwaway copy and
transplants only the join-TYPE verdicts onto a fresh copy in the
ORIGINAL orientation; `planFromClause` plans from that. `s.FromExprs`
is untouched, so the late `reduceOuterJoins` call, the S9.4 flip and
the jointree deconstruction behave exactly as before.

The plan and `root->join_info_list` now agree on join type — which is
all `outerLinksHaveSJInfos` compares — while each consumer keeps the
representation it needs.

**Only the INNER verdict is transplanted.** LEFT→ANTI is declined,
because that conversion is not plan-complete in goopg: PG's version also
DROPS the `IS NULL` qual that forced it, since an anti-join's output has
no nullable-side column for it to test. goopg changes the type and
leaves the qual, so `LEFT JOIN … WHERE p.y IS NULL` filters every
surviving row — measured, 0 rows where 1 is correct. INNER is safe on
both counts: orientation-free, and it drops no qual.

That is the design's own rule applied to itself — an un-demoted join is
today's shipped behaviour, a mis-converted one is wrong rows.

## 3. Gates

| gate | result |
|---|---|
| optimizer + executor suites | green |
| TPC-H values, 22 queries | byte-identical |
| TPC-H parity | unchanged (match 2, every category identical) |
| **TPC-DS sweep** | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0** |
| TPC-DS parity | `join-method` −1, `scan-type` −1, match 0→0 |

The sweep is the binding gate here and was treated as such: this round
had already found one row-dropping bug in the same code, so the change
was held uncommitted until the values came back clean.

## 4. Match count did not move — as predicted

`DESIGN.md` §6 predicted exactly this: Q49 becomes eligible and loses
its Left Joins, declines drop, and **no match appears**, because Q49
still faces join-order (95/99). Recording the prediction in advance is
what makes the flat match count readable as success rather than
disappointment (`METHODOLOGY.md` §2).

## 5. Remaining, in K29's order

Step three: the 9 surviving declines — `outer-link-no-sjinfo` 3,
`outer-over-derived` 3, `outer-spine` 2, `lateral` 1. The first group is
now a *different* cause from Q49's, since Q49's is fixed; re-attribute
before assuming.

Also filed: **LEFT→ANTI is not plan-complete** (§2). Completing it
means dropping the forcing `IS NULL` qual as PG does, and it is the
prerequisite for transplanting that verdict.
