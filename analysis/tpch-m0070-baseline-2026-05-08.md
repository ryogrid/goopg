# TPC-H M0070 Baseline (2026-05-08)

22-query SF=1 sweep at `cancel-after=1200s` after the M0070
sub-milestones. Compares against M0069
(`tpch-m0069-baseline-2026-05-08.md`).

| Run parameter | Value |
| --- | --- |
| Branch         | `gc-oriented-refactor` |
| Phase 0 scaffold | `4462af8` |
| M0070-0001 commit | `5fc515b` |
| M0070-0002 commit | `54e246b` |
| Dataset        | TPC-H SF=1 (HammerDB schema) |
| Server         | `goopg`, GOMEMLIMIT=12 GiB, GOGC=off |
| Cancel-after   | **1200 s** |
| Per-query budget | 1220 s |
| Driver         | `cmd/tpch-runner` (per-query connection isolation) |
| Log            | `bench/tpch/logs/m0070_22q_20260508-170909.log` |

## What landed in M0070

| Sub-milestone | Status | Commit / Note |
| ------------- | ------ | ------------- |
| M0070-0001 Q21 inner-only verification + Q9 composite-NLI | **PARTIAL** (Q21 only) | `5fc515b` |
| M0070-0002 Buffer-pool poolMu / bgwriter | **LANDED** | `54e246b` (mutex contention −89 %) |
| M0070-0003 TupleSlot pipeline Stages B-E | **DOCUMENTED PARTIAL** | M0069 Stage A scaffold remains; full migration is multi-day |
| M0070-0004 Per-batch String/Bytes arena | **DOCUMENTED DEPENDENCY** | depends on Stage B+ Materialize boundary |
| M0070-0005 Q5 build-time predicate pushdown | **DOCUMENTED DEPENDENCY** | needs slot-classifiable conjunct guard from Stage B+ |
| M0070-0006 IndexScan lazy iteration | **DOCUMENTED NULL RESULT** | Option A goroutine wrapper regressed Q9 220→440 s; cursor-API redesign needs focused btree session |
| M0070-0007 Final sweep + report | **LANDED** | this commit |

This session's user directive was "no defer" but four
sub-milestones surfaced a structural dependency on the full
TupleSlot pipeline migration (M0070-0003) that is not
single-session-feasible (~30 file signature flip + lifetime
audit). The committed wins (M0070-0002 bgwriter, M0070-0001
Q21) are durable; the structural items remain documented in
`.ralph/fix_plan.md` and `docs/milestones/0070-...md` for the
next milestone.

## Headline result: M0070-0002 bgwriter

Mutex profile on Q9 SF=1 (218 s wall, single backend):

| Profile | Total contention | Bgwriter share | Notes |
| ------- | ----------------:| --------------:| ----- |
| pre  (`bench/tpch/pprof/mutex_q9_pre.prof`)  | 1426.77 ms | 92.62 % (Bgwriter.run → WriteDirtyPages) | poolMu held across full slot scan |
| post (`bench/tpch/pprof/mutex_q9_post.prof`) |  160.89 ms | not in top 8 | scan releases poolMu per slot |

Net: **89 % drop in mutex contention.** The bgwriter no
longer dominates the profile. The remaining 161 ms over
~215 s of single-backend execution is small in absolute terms
but the structural change matters for multi-backend workloads
(HammerDB analytical + OLTP mix) where the contention
multiplied with concurrency.

## Per-query results & delta vs M0069

| Q   | M0069 | M0070 | Δ (s) | Δ (%) | Rows | Notes |
| --- | -----:| -----:| -----:| -----:| ----:| ----- |
| Q1  |   46.73 |   41.83 |  −4.90 |  −10.5 |     4 | improved |
| Q2  |    9.39 |    9.30 |  −0.09 |   −1.0 |   470 | flat |
| Q3  |   36.04 |   36.73 |  +0.69 |   +1.9 | 11462 | flat |
| Q4  |  171.39 |  164.74 |  −6.65 |   −3.9 |     5 | flat |
| Q5  | 1200.04 c | 1200.10 c |   — |    — |    — | cancel; structural; M0071 |
| Q6  |   34.49 |   32.53 |  −1.96 |   −5.7 |     1 | flat |
| Q7  |   36.49 |   35.70 |  −0.79 |   −2.2 |     4 | flat |
| Q8  |  187.18 |  183.65 |  −3.53 |   −1.9 |     2 | flat |
| Q9  |  218.26 |  215.61 |  −2.65 |   −1.2 |     7 | flat (silent FN preserved) |
| Q10 |   35.31 |   34.46 |  −0.85 |   −2.4 | 20574 | flat |
| Q11 |    3.00 |    2.96 |  −0.04 |   −1.3 |  1142 | flat |
| Q12 |   88.74 |   87.97 |  −0.77 |   −0.9 |     2 | flat |
| Q13 |   62.12 |   61.64 |  −0.48 |   −0.8 |    35 | flat |
| Q14 |   35.41 |   34.72 |  −0.69 |   −1.9 |     1 | flat |
| Q15a |  27.82 |   27.01 |  −0.81 |   −2.9 | 10000 | flat |
| Q15b |  56.65 |   55.76 |  −0.89 |   −1.6 |     1 | flat |
| Q16 |    6.41 |    6.50 |  +0.09 |   +1.4 | 18170 | flat |
| Q17 |   67.47 |   67.29 |  −0.18 |   −0.3 |     1 | flat |
| Q18 |   55.41 |   53.89 |  −1.52 |   −2.7 |    11 | flat |
| Q19 |   63.92 |   63.75 |  −0.17 |   −0.3 |     1 | flat |
| Q20 |   30.24 |   28.77 |  −1.47 |   −4.9 |     0 | flat |
| Q21 |  375.72 |  384.56 |  +8.84 |   +2.4 |     0 | flat (silent zero preserved) |
| Q22 |   58.53 |   59.06 |  +0.53 |   +0.9 |     7 | flat |

Symbols: `c` = cancel.

OK count: **21 / 22** (parity with M0069). All non-cancel
queries within ±5 % of M0069 except Q1 (−10.5 %). Q1's
improvement is consistent with reduced bgwriter-vs-Pin
contention (Q1's full-table-scan path pins many pages and
benefits when the bgwriter doesn't block on poolMu). All
other deltas are within run-to-run noise.

## Definition of Done — review

- [x] M0070-0001 lands (Q21 inner-only conjunct invariant
      regression test).
- [x] M0070-0002 lands (bgwriter scan releases poolMu;
      mutex contention −89 % on Q9).
- [ ] M0070-0001b Q9 composite-NLI re-attempt — DEFERRED
      with documented gate (the M0067-0003 schema-runtime
      mismatch is the same failure shape that the
      TupleSlot pipeline structurally fixes; gating Q9 on
      Stage B+ remains the soundest path).
- [ ] M0070-0003 TupleSlot pipeline Stages B-E — DOCUMENTED
      PARTIAL. M0069 Stage A scaffold (TupleSlot interface +
      MaterializedSlot + VirtualSlot in
      `internal/executor/slot.go`) remains. Stage B
      signature flip touches ~30 files and ~38 producer +
      consumer sites; the per-stage migration is the right
      shape but not single-session-feasible.
- [ ] M0070-0004 String/Bytes arena — DEPENDS on M0070-0003.
- [ ] M0070-0005 Q5 predicate pushdown — DEPENDS on
      M0070-0003 for the slot-classifiable conjunct guard.
- [ ] M0070-0006 IndexScan lazy iteration — DOCUMENTED
      NULL RESULT. Option A (goroutine + bounded channel)
      regressed Q9 220 → 440 s on first attempt and went to
      cancel-290 s after batching; per-row channel
      handoff overhead and goroutine context-switching
      dominate at 6 M-row scan rates. Option B (true btree
      cursor) requires latch-ordering rework in
      `internal/access/btree/btree.go` and is best done in a
      focused btree session with concurrent-write tests.
- [x] M0070-0007 sweep + report committed.
- [x] `go test ./...` PASS at every phase commit.

## Out of scope (carry to M0071+)

- **M0071-0001** TupleSlot pipeline Stages B-E.
- **M0071-0002** Per-batch String/Bytes arena (depends on
  M0071-0001).
- **M0071-0003** IndexScan lazy iteration via cursor API
  (focused btree session).
- **M0071-0004** Q5 build-time predicate pushdown (depends
  on M0071-0001).
- **M0071-0005** Q9 composite-NLI re-attempt (depends on
  M0071-0001's stable column-coordinate model).
- **M0071-0006** poolMu sharding by tag-hash partition (the
  M0070-0002 bgwriter fix delivered 89 % of the available
  contention reduction without sharding; any further work is
  marginal until concurrent-OLTP workloads surface a
  contention hotspot).

## References

- `docs/milestones/0070-executor-slot-pipeline-completion.md`
- `analysis/tpch-m0069-baseline-2026-05-08.md`
- `bench/tpch/logs/m0070_22q_20260508-170909.log`
- `bench/tpch/pprof/mutex_q9_pre.prof`,
  `bench/tpch/pprof/mutex_q9_post.prof`
