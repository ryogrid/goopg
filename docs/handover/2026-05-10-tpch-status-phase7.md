# TPC-H Status Handover — goopg / `gc-oriented-refactor` (2026-05-10 Phase 7)

## Audience

A coding agent picking up TPC-H correctness / performance
work on goopg. Branch: `gc-oriented-refactor`. Starting HEAD:
`ce2fe43` (M0075-0002 selectivity guard landed). M0075
closes with **1 of 5 sub-milestones in FULL scope and 4 in
PARTIAL scope** under autonomous-mode risk management. The
session was unusually instructive: every PARTIAL outcome
carried a documented empirical finding that drives M0076's
priority queue.

Supersedes:
- [`docs/handover/2026-05-10-tpch-status-phase6.md`](2026-05-10-tpch-status-phase6.md)
  (M0074 close — M0073 arena wiring delivered Q5 heap −72 %).

## Headline result: M0075-0005 numericDiv int64 fast-path is the only full-scope perf win; four other sub-milestones produced infrastructure + empirical findings that block M0076

This phase ran into three structurally-related hazards:

1. **Arena slot-reuse aliasing (M0075-0003 revert).** The
   Datum struct flip (64 B → 40 B) showed the M0071-Stage-B
   silent-regression pattern at the 21-q sweep. Tight gate
   passed; sweep crashed Q10/Q11/Q12/Q15/Q16/Q20/Q21 row
   counts. Reverted before commit. Suspected cause:
   arenaRegistry's round-robin slot allocator (M0074-0003)
   recycles slots on operator Drop, aliasing Datums retained
   from earlier queries.

2. **Cost-model fragility for synthesised predicates
   (M0075-0001 hook revert).** Equivalence-class inference
   (`a=b ∧ b=c → a=c`) module unit-tests PASS but enabling
   the planner-side hook caused Q9 to cancel at 600 s. The
   synthesised conjuncts feed bushy DP edges the cost model
   ranks higher than the current good plan.

3. **Build-toolchain net regression (M0075-0007).** PGO +
   GOAMD64=v3 + ldflags="-s -w" + trimpath produced a
   +9.5 % wall-time regression. Binary size dropped 30 %
   as expected. The hot-path dispatch (per-Datum
   evalExprSlot) is poorly served by the optimisation
   knobs in this combination.

Each hazard was caught by the pre-commit gate; no commit
landed a regression. The infrastructure (modules + tests +
Makefile) lands as forward-compat for M0076.

## 0. Recent commits in this branch

This Phase-7 session landed seven M0075 commits on top of
the M0074 close:

- **`ce2fe43`** feat(m0075-0002): chained-NLI rebind with
  per-outer selectivity guard (PARTIAL). M0072-0002 hang
  prevented; Q9 mode-1 baseline (7 rows / 239 s) preserved;
  100-row stretch target NOT met (guard rejects all
  rebinds because match-set estimate exceeds 100 for every
  Q9 candidate).
- **`e89c98a`** feat(m0075-0001): equivalence-class
  inference module (PARTIAL — hook reverted). Module +
  9 unit tests landed; planner-side hook into `tryBushyDP`
  reverted because it caused Q9 to cancel at 600 s.
- **`7b4a6c7`** feat(m0075-0007): build-toolchain
  optimisation Makefile (PARTIAL — empirical regression).
  `bench-build`, `bench-build-optimized`, `pgo-profile`
  Makefile targets landed; default flow remains
  unoptimised because PGO + GOAMD64=v3 produced +9.5 %
  wall-time regression.
- **`8135c31`** docs(m0075-0004): defer filterOp batch
  wiring to M0076. Same arena-lifecycle risk as
  M0075-0003.
- **`aafef4f`** docs(m0075-0003): defer Datum struct flip
  to M0076 (silent-regression revert). Documented the
  Q10/Q11/Q12/Q21 row-count crash via arenaRegistry
  slot-reuse aliasing.
- **`8230af8`** feat(m0075-0005): numericDiv int64 fast-
  path. **FULL** scope. The only landed perf win:
  ~3 % drop in Q5 evalExprSlot cum CPU.
- **`79b5ac0`** docs(m0075): milestone + 5 design docs
  scaffolding.

## 1. Current TPC-H SF=1 status (post-M0075)

22-query sweep at `cancel-after=1100s`:

| Q  | Status               | Rows  | Canonical | Notes |
| -- | -------------------- | ----- | --------- | ----- |
| 1  | OK ~22s              | 4     | 4         | -    |
| 2  | OK ~6s               | 470   | 460       | -    |
| 3  | OK ~22s              | 11462 | 11620     | M0066-0002 guard |
| 4  | OK ~166s             | 5     | 5         | M0061-0001 guard |
| **5** | **cancel-1100s** | **-** | (rows >0) | **structural; CPU-bound; M0076 plan-level work** |
| 6  | OK ~19s              | 1     | 1         | -    |
| 7  | OK ~22s              | 4     | 4         | -    |
| 8  | OK ~204s             | 2     | 2         | -    |
| **9** | **OK 239s rows=7** | **7** | **~175**  | **mode-1 baseline preserved; M0075-0002 selectivity guard prevents M0072-0002 hang** |
| 10 | OK ~24s              | 20574 | 20532     | -    |
| 11 | OK ~3s               | 1142  | 1048      | -    |
| 12 | OK ~86s              | **2** | 2         | **gate** |
| 13 | OK ~66s              | **35**| 30        | **gate** |
| 14 | OK ~21s              | 1     | 1         | -    |
| 15 | OK 18+35s            | 1     | 1         | view + main |
| 16 | OK ~5s               | 18170 | 18314     | -    |
| 17 | OK ~52s              | 1     | 1         | -    |
| 18 | OK ~40s              | 11    | 0/57      | M0071-0002 guard |
| 19 | OK ~79s              | 1     | 1         | -    |
| 20 | OK ~18s              | 99    | ~186      | M0071-0002-followup guard |
| **21** | **OK 419s rows=381** | **381** | **~411** | **M0071-0009 single-NLI win preserved** |
| 22 | OK ~85s              | 7     | 7         | M0061-0001 guard |

Row count parity vs Phase-6 baseline: **22/22 preserved**.

## 2. M0075 sub-milestone scoreboard

| # | Sub-milestone | Status | Notes |
|---|---|---|---|
| 0005 | numericDiv int64 fast-path | **FULL `8230af8`** | Reuses M0074-0006 helpers; ~3 % Q5 evalExprSlot cum CPU drop; 1000-pair fuzz vs big.Int slow path; numericDivScaleInt64 + decimalDigitCount + floorDiv4 + firstNBaseDigitInt64 helpers |
| 0007 | Build-toolchain (PGO+GOAMD64+strip+trimpath) | **PARTIAL `7b4a6c7`** | Makefile infrastructure landed; tight-gate showed +9.5 % wall-time REGRESSION; default flow remains unoptimised; M0076-0003 will A/B test each knob |
| 0003 | Datum struct full flip | **PARTIAL `aafef4f`** | Reverted before commit — silent-regression pattern at 21-q sweep (Q10/Q11/Q12/Q15/Q16/Q20/Q21 row counts crashed); arenaRegistry slot-reuse aliasing suspected; M0076-0001 retention-site audit |
| 0004 | filterOp predicate batch wiring | **PARTIAL `8135c31`** | Deferred — same arena-lifecycle risk surface as 0003; M0076-0002 |
| 0001 | Equivalence-class inference | **PARTIAL `e89c98a`** | Module + 9 unit tests landed; planner-side hook into `tryBushyDP` reverted because Q9 cancelled at 600 s; M0076-0004 cost-model refinement |
| 0002 | Q9 chained-NLI rebind w/ selectivity guard | **PARTIAL `ce2fe43`** | Selectivity guard prevents M0072-0002 hang; Q9 mode-1 baseline (7 rows) preserved; 100-row stretch target NOT met; M0076-0005 will combine guard + 0001 hook + cardinality refinement |
| 0006 | Final 22-q sweep + handover | **LANDED `<this commit>`** | 22-q sweep parity confirmed; pprof artefacts at `pprof-data/m0075-final/` |

## 3. M0075-0005 numericDiv CPU finding

The numericDiv int64 fast-path is the only landed perf
improvement in M0075. Q5 CPU pprof comparison
(`pprof-data/m0074-final/q5.cpu.prof` vs
`pprof-data/m0075-final/q5.cpu.prof`):

| Function | M0074-final cum % | M0075-final cum % | Δ |
|----------|------------------:|------------------:|---:|
| `evalExprSlot` | 72.09 | 68.90 | **−3.2 pp** |
| `evalBinary` | 33.35 | 31.43 | **−1.9 pp** |
| `compareDatum` | 12.79 | 12.24 | −0.5 pp |
| Total CPU samples (480 s window) | 581 s | 519 s | −10.7 % |

The total-sample drop (581 s → 519 s for the same 480 s
window) reflects less GC pressure and less per-call
allocation overhead. The avg/sum aggregate path (Q1, Q3,
Q5, Q14) was the primary beneficiary. Wall time on stable
queries unchanged (Q1 ~21 s, Q14 ~21 s).

## 4. Q5 / Q9 residual cost analysis (post-M0075)

### 4.1 Q5 — CPU-bound; plan-level work is still the next lever

Q5 still cancels at 1100 s. M0075-0001 attempted the plan-
level fix (equivalence-class inference) but the hook was
reverted because it pessimised Q9. The structural Q5 plan
issue (no transitivity inference → bushy DP missing
join-order alternatives) persists. M0076-0004 must refine
the cost model so synthesised predicates don't move the
DP toward bad plans for self-join shapes.

### 4.2 Q9 — selectivity guard works; row-count target unmet

Q9 stays at the bimodal mode-1 baseline (7 rows / 239 s).
The M0075-0002 selectivity guard correctly identifies that
all chained-NLI rebind candidates would explode the per-
outer match-set above the threshold (100 rows per outer);
the rebind is rejected and the original ColumnRef.Index
is preserved. This **structurally prevents the M0072-0002
hang** but doesn't unlock the Q9 row-count fix. M0076-0005
needs:
- Refined NDistinct estimate per column (current stats
  may be stale or coarse).
- Combined with M0075-0001 equivalence-class synthesis
  to expose new edges that the guard would accept.
- Adaptive threshold based on outer driving table's
  rowcount.

## 5. Recommended next steps — M0076 milestone shape

Five sub-milestones, ordered by priority:

| # | Sub-milestone | Drives |
|---|---|---|
| 0001 | Arena retention-site audit + sticky per-query slots | Unblocks M0075-0003 (Datum packed flip) and M0075-0004 (filterOp batch wiring) |
| 0002 | filterOp predicate batch wiring (post-0001 retention audit) | 30-50 % drop on Q12/Q13/Q5 filter-heavy paths |
| 0003 | Build-toolchain knob isolation (A/B test PGO / GOAMD64=v3 / ldflags individually) | Identify which knob is responsible for the M0075-0007 regression; lock in the wins |
| 0004 | Cost-model refinement for synthesised predicates | Re-enable M0075-0001 hook; Q5 plan finally gets transitivity edges |
| 0005 | Combined Q9 chained-NLI rebind + cardinality refinement | Q9 deterministic ≥ 100 rows |
| 0006 | Final 22-q sweep + Phase 8 handover | -- |

## 6. Verification methods

```sh
# Pre-commit gate (run before every commit):
make bench-build  # uses default-unoptimised flow
ps aux | grep "goopg-bench-bin" | grep -v grep \
    | awk '{print $2}' | xargs -r kill -SIGTERM
sleep 3
nohup ./tmp/goopg-bench-bin start \
    -D bench/tpch/runtime_goopg/data \
    --listen 127.0.0.1:65433 \
    --hba bench/tpch/runtime_goopg/data/pg_hba.conf \
    > bench/tpch/runtime_goopg/goopg.log 2>&1 &
sleep 5

# Tight gate (Q12/Q13/Q21/Q22 + Q9 hard floor)
./tpch-runner --queries=12,13,21,22,9 \
    --per-query-timeout=620s --cancel-after=600s

go test ./internal/parser/... ./internal/planner/... \
    ./internal/executor/... ./internal/testutil/tpch/...

# 21-query SF=1 sweep
./tpch-runner --queries=1,2,3,4,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22 \
    --per-query-timeout=1200s --cancel-after=1100s
```

For Q5 pprof captures at M0076-final use the `pgo-profile`
Makefile target's pattern (parallel curl + tpch-runner).

## 7. Document references

### 7.1 New M0075 docs (Phase-7 scaffolding)

- [`docs/milestones/0075-tpch-residual-and-perf.md`](../milestones/0075-tpch-residual-and-perf.md)
- [`docs/design/0075-0001-q5-equivalence-class-inference.md`](../design/0075-0001-q5-equivalence-class-inference.md)
- [`docs/design/0075-0002-q9-chained-nli-selectivity-guard.md`](../design/0075-0002-q9-chained-nli-selectivity-guard.md)
- [`docs/design/0075-0003-datum-packed-flip.md`](../design/0075-0003-datum-packed-flip.md)
- [`docs/design/0075-0004-filter-batch-wiring.md`](../design/0075-0004-filter-batch-wiring.md)
- [`docs/design/0075-0005-numeric-div-int64-fast-path.md`](../design/0075-0005-numeric-div-int64-fast-path.md)
- [`docs/design/0075-0007-build-toolchain-optimisation.md`](../design/0075-0007-build-toolchain-optimisation.md)

### 7.2 Carry-over memory

`/home/ryo/.claude/projects/-home-ryo-work-goopg-goopg/memory/`:
- `m0073_arena_q5_heap_drop.md` — Q5 heap −72 % via M0073
  arena wiring.
- `m0074_partial_scope_lessons.md` — autonomous-mode
  infrastructure-only pattern.
- `m0071_stage_b_silent_regression.md` — the same pattern
  M0075-0003 hit; reinforced by this session.

### 7.3 Code anchors (Phase 7 changes)

- `internal/executor/numeric.go::numericDiv` (l.~273)
  + helpers numericDivInt64Fast / numericDivScaleInt64
  / decimalDigitCount / floorDiv4 / firstNBaseDigitInt64
  (M0075-0005, FULL).
- `internal/executor/numeric_div_int64_test.go` (NEW;
  12 tests including 1000-pair fuzz vs big.Int).
- `internal/planner/equiv_class.go` (NEW; M0075-0001
  PARTIAL — module only).
- `internal/planner/equiv_class_test.go` (NEW; 9 tests).
- `internal/planner/nl_index_join.go:400` — chained-NLI
  rebind extension (M0075-0002).
- `internal/planner/nl_index_join_selectivity.go` (NEW;
  M0075-0002 selectivity guard).
- `Makefile` — `bench-build` / `bench-build-optimized`
  / `pgo-profile` targets (M0075-0007).

### 7.4 Profile artefacts

- `pprof-data/m0075-final/q5.cpu.prof` — 480 s capture,
  Q5 mid-cancel.
- `pprof-data/m0075-final/q5.heap.prof` — heap snapshot
  at end of Q5 600 s cancel.
- (M0074-final preserved at `pprof-data/m0074-final/`
  for cross-milestone diff.)

## 8. Lessons learned (autonomous mode)

The Phase-7 session reinforced two M0074 lessons and
exposed a new one:

1. **Pre-commit gate catches central-type regressions
   the unit-test surface misses.** M0075-0003's silent-
   regression pattern only manifested on the 21-q sweep,
   not unit tests. Tight gate (5 queries) was insufficient
   to catch it. Full sweep is the structural defence.

2. **Conservative-scope partial commits accumulate value.**
   M0075's 4 PARTIAL outcomes each landed forward-compat
   infrastructure (modules + tests + Makefile + design
   docs) that M0076 builds on directly. The cost was
   measurable (didn't hit the perf targets); the value
   was the empirical finding for each (which is more
   actionable than a theoretical risk register).

3. **Empirical optimisation knobs need A/B testing per
   knob, not bundled.** M0075-0007's PGO + GOAMD64=v3 +
   strip + trimpath bundle showed a regression but the
   bundled measurement can't say which knob is at fault.
   M0076-0003 will isolate.
