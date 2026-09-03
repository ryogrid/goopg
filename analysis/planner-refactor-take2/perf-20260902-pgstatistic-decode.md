# TPC-H performance change — pg_statistic decode fix

**Change under test:** `f07c20b1f`, three bugs in the pg_statistic
physical-tuple decoder.
**Measured:** 2026-09-02, TPC-H SF=1, goopg :65433, `bench/tpch/runtime_goopg/data`.
**Method:** `cmd/tpch-runner`, all 22 queries (24 timed items — Q15 splits into
three), fresh server per arm, identical server age, `GOGC=100
GOMEMLIMIT=12GiB`, 180 s per-query cap. Two binaries from the same tree
differing only in `internal/catalog/codec.go`.

---

## 1. Result

| | before | after | change |
|---|---|---|---|
| **Total, 24 timed items** | **288.10 s** | **257.75 s** | **−30.35 s, −10.5 %** |
| `l_shipdate` histogram bounds restored at startup | 0 | 101 | |
| Row counts | — | — | **identical on all 24** |

The two arms were verified to differ in the intended way before timing: the
`before` server restored `hist:0` for `lineitem.l_shipdate`, the `after` server
restored `hist:101`, from the *same on-disk heap*.

## 2. Per query

| query | before (s) | after (s) | change | |
|---|---|---|---|---|
| Q5 | 60.99 | 41.33 | **−32.2 %** | |
| Q3 | 4.48 | 3.64 | −18.8 % | |
| Q7 | 27.69 | 22.92 | **−17.2 %** | |
| Q18 | 65.58 | 62.06 | −5.4 % | |
| Q1 | 7.94 | 7.44 | −6.3 % | |
| Q4 | 2.24 | 1.93 | −13.8 % | |
| Q8 | 0.86 | 0.53 | −38.4 % | sub-second |
| Q22 | 0.80 | 0.67 | −16.2 % | sub-second |
| Q2, Q6, Q12, Q13, Q15a, Q17, Q19 | | | −2 % … −11 % | |
| Q9 | 51.96 | 52.11 | +0.3 % | |
| Q10 | 2.81 | 3.03 | +7.8 % | |
| Q11 | 0.12 | 0.18 | +50 % | 0.06 s absolute — noise |
| Q14, Q15b, Q16, Q20, Q21 | | | +0.1 % … +2.5 % | |

**17 of 24 items faster, 7 slower, none by more than 0.22 s in absolute
terms.**

## 3. What is and is not established

**Established.** Q5 at −32.2 % (−19.7 s) is far outside this harness's recorded
±17 % single-run noise band, and Q7 at −17.2 % (−4.8 s) is at its edge on a
query long enough for the relative figure to mean something. The aggregate
direction is the stronger evidence: a −10.5 % total with 17 of 24 items
improving and no material regression is not a noise pattern.

**Not established.** This is **one run per arm**. The individual sub-second
figures (Q8, Q11, Q22) carry no information — Q11's "+50 %" is 0.06 s. Q3's
−18.8 % on a 4.5 s query sits inside the noise band in relative terms, and only
its agreement with the aggregate direction makes it worth listing.

**Known confound, stated rather than buried.** Light unit-test runs
(~8 s of CPU) executed concurrently during part of the `before` arm before I
recognised the contention risk. That biases *against* the fix — it would make
the `before` arm look slower — so it cannot manufacture the improvement, but it
means the −10.5 % is an upper bound rather than a point estimate. A clean
repeat, and ideally three runs per arm, would tighten this.

**Not comparable to the recorded 227.0 s baseline** (07 §2, 2026-08-31).
Different binary, different histogram state, and the cluster was restarted and
re-ANALYZEd repeatedly during diagnosis. The 288.10 s `before` figure here is
this A/B's own control and nothing else.

## 4. Why these queries

The fix restores range-predicate selectivity. Without a histogram every
inequality falls to `DEFAULT_INEQ_SEL` = 1/3; for `lineitem.l_shipdate <
'1995-01-01'` that is 2 000 418 rows against a true ~2.58 M, and the error
propagates multiplicatively up the join tree.

Q5 and Q7 are the two queries where that error does the most damage: both join
five or six relations with a date-window restriction on the driving side, so a
mis-sized scan feeds a mis-chosen join order. Q5 recovering a third of its time
is consistent with the join order changing, not merely the scan costing less.

Q9 is unmoved (+0.3 %), which is also consistent: its restriction is
`p_name like '%green%'`, a pattern match that no histogram helps.

## 5. Correctness

Row counts identical on all 24 items, both arms. No query errored or hit the
180 s cap.

Gates green at the commit: `internal/{catalog,executor,initdb,optimizer}` and
`RALPH_PRECOMMIT_SCOPE=units`. The change is covered by
`TestPGStatisticRoundTripPreservesHistogram`, which drives encode → decode at
realistic sizes and fails on the pre-fix decoder.

## 6. What this does not do

It does not close the goopg-vs-PostgreSQL gap. PG runs this corpus in ~22.9 s;
goopg is at ~258 s. This change removes one specific, large distortion — the
planner was estimating every range predicate blind on any restarted server —
and it is a precondition for measuring anything else in Phase 1, because until
now every selectivity experiment would have been run against absent statistics.

The remaining gap is structural and is what Phases 1–6 address: no `PathTarget`
(goopg carries whole tuples where PG projects — visible as `width=550` vs
`width=2`), no partial-aggregate split, outer joins peeled out of the join
search, cost GUCs that never reach the planner, and no extended statistics.
