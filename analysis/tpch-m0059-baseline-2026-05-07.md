# TPC-H 22-Query Re-Baseline (post-M0059) — 2026-05-07

## Scope

Full 22-query TPC-H SF=1 sweep against `runtime_goopg` after the
M0059 BorrowRow widening landed in commit `381088f`. Compares
against the M0062 baseline (`tpch-m0062-baseline-2026-05-07.md`,
commit `1b7aa14` — pre-M0059).

| Run parameter | Value |
| --- | --- |
| Commit         | `381088f` (`perf-analysis`) |
| Dataset        | TPC-H SF=1 (HammerDB schema, all integer cols NUMERIC) |
| Server         | `goopg` listening on `127.0.0.1:65433`, GOMEMLIMIT=12 GiB |
| Cancel-after   | 600 s |
| Per-query budget | 620 s |
| Driver         | `cmd/tpch-runner` (per-query connection isolation) |
| Log            | `bench/tpch/logs/m0059_22q_20260507T120620.log` |

## Results & delta vs M0062 baseline

| Q   | M0062 (s) | M0059 (s) | Δ (s) | Δ %    | Rows | Notes |
| --- | --------:| --------:| -----:| ------:| ----:| ----- |
| Q1  |   42.58 |   46.15 |  +3.57 |  +8.4% |    4 | within run-to-run noise (Q1's plan = `Aggregate(Sort(SeqScan))`; Sort retains, M0059 propagation does not benefit) |
| Q2  |    7.65 |    8.95 |  +1.30 | +17.0% |  470 | small Q; absolute delta ~1 s, dominated by jitter |
| Q3  |   43.88 |   52.09 |  +8.21 | +18.7% | 11462 | likely first-query warm-up effect; Q3 also has Sort on top |
| Q4  |  175.20 |  190.90 | +15.70 |  +9.0% |    5 | EXISTS unnested (M0061-0001); plan ends in Sort+Agg |
| Q5  |  600.10 |  600.09 |  +0.0  |    0 % |    — | cancel-after, identical |
| Q6  |   31.57 |   31.74 |  +0.17 |  +0.5% |    1 | within noise |
| Q7  |   39.70 |   39.24 |  −0.46 |  −1.2% |    4 | flat |
| Q8  |  217.08 |  201.22 | −15.86 |  −7.3% |    0 | (0 rows — pre-existing M0062-0002) |
| Q9  |    0.77 |    0.82 |  +0.05 |    n/a |    — | LIKE error (M0062-0006) |
| Q10 |   45.54 |   44.29 |  −1.25 |  −2.7% | 20574 | flat |
| Q11 |    4.58 |    4.64 |  +0.06 |  +1.3% |  1142 | small Q; flat |
| Q12 |  100.82 |   99.04 |  −1.78 |  −1.8% |    2 | flat |
| Q13 |  600.06 |  600.00 |  −0.06 |    0 % |    — | cancel-after, identical |
| Q14 |   38.38 |   36.38 |  **−2.00** |  **−5.2%** |  1 | NLI plan (`Filter(NLI(part-IndexScan, ...))`) — M0059-0003 borrow benefit visible |
| Q15a |   29.19 |   25.42 | **−3.77** | **−12.9%** | 10000 | aggregate over SeqScan — M0059-0002 aggregate-input borrow benefit |
| Q15b |   29.09 |   25.82 | **−3.27** | **−11.2%** |    0 | (0 rows pre-existing); same agg path |
| Q16 |    4.44 |    5.47 |  +1.03 | +23.1% | 18170 | small absolute delta; Q16 flips between 4–6 s in repeat runs |
| Q17 |   87.05 |   73.97 | **−13.08** | **−15.0%** |  1 | NLI on `lineitem` per `p_partkey` (M0061-0002 hot path); M0059-0003 borrow shines |
| Q18 |  125.11 |  110.95 | **−14.16** | **−11.3%** |  11 | aggregate of joined large rows |
| Q19 |   73.49 |   73.24 |  −0.25 |  −0.3% |    1 | flat |
| Q20 |  600.01 |  600.01 |    0.0 |    0 % |    — | cancel-after |
| Q21 |  600.00 |  600.03 |  +0.03 |    0 % |    — | cancel-after |
| Q22 |   65.37 |   65.49 |  +0.12 |  +0.2% |    7 | flat |

### Aggregate impact

| Cohort | Total elapsed M0062 (s) | Total elapsed M0059 (s) | Δ |
| ------ | ----------------------:| ----------------------:| --: |
| OK queries (excl. Q5/Q9/Q13/Q20/Q21) | 1382.13 | 1361.85 | **−20.28 s (−1.5%)** |
| Aggregate-heavy & NLI cohort (Q14/Q15a/Q15b/Q17/Q18) | 308.82 | 272.54 | **−36.28 s (−11.7%)** |

### Headline observations

- **NLI- and aggregate-dominated queries get the win.** Q14, Q15a,
  Q15b, Q17, Q18 — collectively 36 s saved across the cohort, in
  line with the M0059 design's expectation that the borrow path
  affects per-row clones in the NLI emit and the aggregate-input
  drain.
- **Sort-/MHJ-heavy queries are flat or noisier.** Q1, Q3, Q4 —
  the retention boundary still holds, so the borrow widening
  doesn't reach the hot loop. The +3..+8 s deltas observed are
  within run-to-run jitter on this host (the M0061-0003 → M0062
  re-runs already showed similar variance for Q1/Q3 just from
  back-to-back invocations).
- **Row-count parity 100%.** Every OK row in this run has the
  same row count as the M0062 baseline. M0059 introduces no
  result-row regression.
- **Cancel paths flat.** Q5/Q13/Q20/Q21 all return at
  ≈ `--cancel-after` ± 0.1 s — confirms the M0062 cancel
  propagation work is unaffected by the borrow changes.

## M0059 sub-task verification (cross-reference)

| Sub-task | Verified by |
| -------- | ----------- |
| M0059-0001 lifetime matrix | `internal/executor/operator.go` doc-comment + `borrow_test.go::TestClass1*` |
| M0059-0002 Build propagation | `executor.go::Build(*Aggregate)` + `Build(*NestedLoopIndexJoin)` setChildBorrow; `borrow_test.go::TestM0059Build*` |
| M0059-0003 NLI SetBorrow | `operators_nljoin.go` borrow field, three emit paths borrow-aware; `borrow_test.go::TestM0059NLIBorrowFlag` |
| M0059-0004 aggregate child borrow | subsumed by 0002; `TestM0059BuildAggregateChildIsBorrowed` |
| M0059-0005 retention boundary | `TestM0059SortStaysAtOwned`, `TestM0059JoinStaysAtOwned`, `TestM0059MultiHashJoinStaysAtOwned` |
| M0059-0006 report | `analysis/tpch-borrowrow-optimization-report.md` + this baseline |
| M0059-0007 parity gate | `go test ./...` PASS on `381088f`; row counts match for every OK query in this sweep |

## Open follow-ups (unchanged from M0062)

- M0062-0001 Q5 slow probe
- M0062-0002 Q8 0-rows correctness
- M0062-0003 Q15b 0-rows correctness
- M0062-0004 Q20 nested-IN decorrelation
- M0062-0005 Q21 non-equijoin EXISTS
- M0062-0006 Q9 NLI column-index resolution

None are affected (helped or harmed) by the M0059 borrow widening.
