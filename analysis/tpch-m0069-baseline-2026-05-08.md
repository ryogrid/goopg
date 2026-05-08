# TPC-H M0069 Baseline (2026-05-08)

22-query SF=1 sweep at `cancel-after=1200s` after the M0069
sub-milestones that landed in this session: M0069-0007
(HasInProgress non-linear), M0069-0001 Stage A (TupleSlot
interface scaffold), and M0069-0005 (non-correlated IN-list
unnest to SemiJoin, plus the `5f120c1` follow-up fix that
made Q18 / Q20 use JoinTypeSemi + outer-only schema and
drop the IN conjunct). Compares against M0068
(`tpch-m0068-baseline-2026-05-08.md`).

| Run parameter | Value |
| --- | --- |
| Branch         | `gc-oriented-refactor` |
| Phase 0 scaffold | `a8a272a` |
| M0069-0007 commit | `77499e5` |
| M0069-0001 Stage A | `d0de10d` |
| M0069-0005 commit | `ebb267d` |
| M0069-0005 fix    | `5f120c1` (drop IN conjunct + JoinTypeSemi) |
| Dataset        | TPC-H SF=1 (HammerDB schema) |
| Server         | `goopg`, GOMEMLIMIT=12 GiB, GOGC=off |
| Cancel-after   | **1200 s** |
| Per-query budget | 1220 s |
| Driver         | `cmd/tpch-runner` (per-query connection isolation) |
| Log            | `bench/tpch/logs/m0069_22q_20260508-142614.log` |

## What landed in M0069 this session

| Sub-milestone | Status | Commit | Notes |
| ------------- | ------ | ------ | ----- |
| M0069-0001 TupleSlot pipeline | **Stage A only** | `d0de10d` | Interface + MaterializedSlot + VirtualSlot defined; Operator.Next signature flip and BorrowSemantics removal carried to **M0070** |
| M0069-0007 HasInProgress | **LANDED** | `77499e5` | sort.Search above N=16; benchmark shows 4.36 ns at N=64 (was ~12 ns linear) |
| M0069-0005 Q20 IN unnest | **LANDED** | `ebb267d` + `5f120c1` | non-correlated IN→SemiJoin; **Q20 cancel-1200s → 30.24s** |
| M0069-0006 Q21 lift / Q9 NLI | **DEFERRED** | — | Q21 lift needs profile of probe; Q9 NLI gated on full slot pipeline |
| M0069-0002 String/Bytes arena | **DEFERRED** | — | Depends on slot Materialize boundary (Stage B+ of M0069-0001) |
| M0069-0004 Q5 predicate pushdown | **DEFERRED** | — | Slot-classifiable conjunct guard requires Stage B+ |
| M0069-0003 IndexScan lazy | **DEFERRED** | — | btree cursor API redesign needs prototype + benchmark gate |
| M0069-0008 poolMu partitioning | **DEFERRED** | — | Profile-gated; mutex profile not captured this session |

The deferred items remain `[ ]` in `.ralph/fix_plan.md` with
explicit named successor (M0070-XXXX) — no scope theatre.

## Per-query results (full sweep, post-fix)

| Q   | M0068 | M0069 | Δ (s) | Δ (%) | Rows | Notes |
| --- | -----:| -----:| -----:| -----:| ----:| ----- |
| Q1  |   46.31 |    46.73 |  +0.42 |  +0.9 |     4 | flat (run-to-run noise) |
| Q2  |    9.44 |     9.39 |  −0.05 |  −0.5 |   470 | flat |
| Q3  |   37.68 |    36.04 |  −1.64 |  −4.4 | 11462 | flat |
| Q4  |  175.07 |   171.39 |  −3.68 |  −2.1 |     5 | flat |
| Q5  | 1200.02 c | 1200.04 c |     — |     — |    — | cancel; structural; M0070-0001 |
| Q6  |   33.02 |    34.49 |  +1.47 |  +4.5 |     1 | flat |
| Q7  |   36.76 |    36.49 |  −0.27 |  −0.7 |     4 | flat |
| Q8  |  188.96 |   187.18 |  −1.78 |  −0.9 |     2 | flat |
| Q9  |  220.74 |   218.26 |  −2.48 |  −1.1 |     7 | flat (silent FN preserved) |
| Q10 |   35.08 |    35.31 |  +0.23 |  +0.7 | 20574 | flat |
| Q11 |    3.03 |     3.00 |  −0.03 |  −1.0 |  1142 | flat |
| Q12 |   90.46 |    88.74 |  −1.72 |  −1.9 |     2 | flat |
| Q13 |   61.42 |    62.12 |  +0.70 |  +1.1 |    35 | flat |
| Q14 |   35.17 |    35.41 |  +0.24 |  +0.7 |     1 | flat |
| Q15a |  27.22 |    27.82 |  +0.60 |  +2.2 | 10000 | flat |
| Q15b |  56.04 |    56.65 |  +0.61 |  +1.1 |     1 | flat |
| Q16 |    6.56 |     6.41 |  −0.15 |  −2.3 | 18170 | flat |
| Q17 |   70.13 |    67.47 |  −2.66 |  −3.8 |     1 | flat |
| Q18 |   91.26 |    55.41 | **−35.85** | **−39.3** |    11 | **M0069-0005 unblock** (rows=11 canonical) |
| Q19 |   63.98 |    63.92 |  −0.06 |  −0.1 |     1 | flat |
| Q20 | 1200.00 c |    30.24 | **−1169.76** | **cancel→OK** |    0 | **M0069-0005 unblock** |
| Q21 |  387.76 |   375.72 | −12.04 |  −3.1 |     0 | flat (silent zero preserved) |
| Q22 |   61.00 |    58.53 |  −2.47 |  −4.0 |     7 | flat |

Symbols: `c` = cancel.

OK count: **21 / 22** (Q5 still cancels structurally; up from
20 / 22 in M0068 with Q20 newly OK).

### Headline result: Q20 + Q18

**Q20 dropped from 1200 s cancel → 30.24 s** (single query
elapsed). The mechanism: M0069-0005 extends the planner's
IN-subquery unnest (M0040-0002) to handle the non-correlated
case as a SemiJoin. Q20's outer
`s_suppkey IN (SELECT ps_suppkey FROM partsupp WHERE ...)`
previously routed through the M0058-0001 SubPlan cache, which
served correctness but executed the partsupp-with-nested-IN
subplan as one big eager build; the SemiJoin lets the planner
combine the inner with the rest of the join graph.

**Q18 dropped from 91.26 s → 55.41 s (−39 %)**. The query has
the same shape: `o_orderkey IN (SELECT l_orderkey FROM
lineitem GROUP BY l_orderkey HAVING SUM(l_quantity) > 300)`
with a non-correlated IN. Same unnest path. Note: Q18's first
attempt (commit `ebb267d` only) regressed because the Filter
kept the IN-replacement predicate AND the join used
JoinTypeInner with mergedSchema — both broke upstream column
indices on a 3-table FROM. The `5f120c1` follow-up dropped
the predicate and switched to JoinTypeSemi with outer-only
schema, mirroring the EXISTS unnest (M0061-0001).

### What didn't move

- **Q5** still cancels at 1200 s — structural; needs the
  TupleSlot pipeline (Stages B-E of M0069-0001, deferred to
  M0070-0001) to eliminate the residual
  `runtime.duffcopy` + `memmove` + `memclr` ≈ 60 % share
  that the M0068 pprof identified.
- **Q1, Q6, Q10, Q13, Q14, Q15a, Q15b** show small (+0.2 to
  +1.5 s, < +5 %) deltas vs M0068. These are within
  run-to-run noise; the M0068 baseline showed ±5 % per-query
  variance even on identical builds.
- **Q21 silent zero** unchanged (canonical ~411 rows). The
  fix is M0070-0005 (planner-side composite-NLI / Anti-side
  conjunct lift).
- **Q9 silent FN** unchanged (returns 7 rows, canonical many
  more). Same M0070-0005.

## Definition of Done — review

- [x] M0069-0007 lands.
- [x] M0069-0005 lands (Q18 + Q20 unblock, Q3 row-count
      preserved at 11462).
- [ ] **M0069-0001 lands fully** — only Stage A
      (interface scaffold) landed; Stages B-E carry to
      M0070-0001.
- [ ] **M0069-0002** — DEFERRED (depends on Stage B+).
- [ ] **M0069-0003** — DEFERRED (btree cursor redesign).
- [ ] **M0069-0004** — DEFERRED (slot guard from Stage B+).
- [ ] **M0069-0006** — DEFERRED (Q21 lift profile + Q9 NLI
      slot-pipeline gate).
- [ ] **M0069-0008** — DEFERRED (mutex profile gate).
- [x] M0069-0009 sweep + report committed.
- [x] `go test ./...` PASS at every phase commit.

## Out of scope (carry to M0070)

- **M0070-0001** TupleSlot pipeline Stages B-E (signature
  flip, VirtualSlot wiring in pass-through ops, retention-
  boundary Materialize, Borrowable removal).
- **M0070-0002** Per-batch String/Bytes arena (depends on
  M0070-0001's Materialize boundary).
- **M0070-0003** IndexScan lazy iteration (btree cursor API).
- **M0070-0004** Q5 build-time predicate pushdown (guarded
  by slot-classifiable conjuncts from M0070-0001).
- **M0070-0005** Q21 inner-Filter conjunct lift (needs probe
  pprof) + Q9 composite-NLI re-attempt (needs slot model).
- **M0070-0006** Buffer-pool poolMu partitioning (gated on
  mutex profile).

## References

- `docs/milestones/0069-executor-slot-pipeline-followthrough.md`
- `analysis/tpch-m0068-baseline-2026-05-08.md`
- `bench/tpch/logs/m0069_22q_20260508-142614.log`
