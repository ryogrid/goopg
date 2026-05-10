# Milestone 0076 — M0075 carry-forward + plan-snapshot regression harness

**Status:** planned
**Branch:** `gc-oriented-refactor` (continuation of M0075)
**Depends on:**
- M0075-final (commit `9120dc8`) — Phase 7 handover
  documents the 4 PARTIAL outcomes that motivate
  M0076-0001..0005.
- M0074-0001 — `evalBinaryBatch` +
  `canVectoriseExpression` infrastructure at
  `internal/executor/expr_batch.go` (consumed by 0002).
- M0074-0003 — `arenaRegistry` + `permArena` +
  `permanent` / `registryIdx` fields on Arena
  (consumed by 0001 audit).
- M0075-0001 — `internal/planner/equiv_class.go` +
  9 unit tests (consumed by 0004).
- M0075-0002 — `internal/planner/nl_index_join_selectivity.go`
  + nl_index_join.go:400 rebind extension
  (consumed by 0005).
- M0075-0007 — `Makefile` `bench-build-optimized` +
  `pgo-profile` targets (consumed by 0003).

**Drives:** unblock Datum packed full-flip via
arena retention-site audit; consume the M0074-0001
batch infrastructure into filterOp; isolate which
build-toolchain knob caused M0075-0007's +9.5 % wall-
time regression; refine cost model so M0075-0001's
synthesised predicates don't pessimise Q9; combine
selectivity guard + transitivity inference + adaptive
threshold to finally unlock Q9 ≥ 100 rows
DETERMINISTICALLY; **add a plan-snapshot regression
harness** so planner-only changes don't pay the
~25 min full-sweep cost per commit.

## Context

After M0075 close (Phase 7 handover, commit `9120dc8`),
4 of 5 sub-milestones landed in PARTIAL scope with
documented empirical findings:

1. **M0075-0003 Datum packed flip** hit a silent-
   regression pattern at the 21-q sweep (Q10/Q11/Q12/
   Q15/Q16/Q20/Q21 row counts crashed to 0 or wrong
   values; Q22 returned 210 instead of 7). Tight gate
   passed; sweep failed. Suspected root cause:
   `arenaRegistry`'s round-robin slot allocator
   (M0074-0003) recycles slots on operator `Drop()`,
   aliasing Datums retained from earlier queries.
   Reverted before commit per pre-commit gate.

2. **M0075-0004 filterOp batch wiring** shares the
   same arena-lifecycle risk surface as M0075-0003.
   Deferred preemptively; the batch path's
   per-row-Materialize-then-buffer pattern would
   re-expose the slot-reuse aliasing in any per-batch
   arena scenario.

3. **M0075-0001 equivalence-class inference** module +
   9 unit tests landed cleanly; the planner-side hook
   into `tryBushyDP` was reverted because Q9 cancelled
   at 600 s. The synthesised conjuncts feed bushy DP
   edges the cost model ranks higher than the current
   good plan for Q9's chained-NLI shape.

4. **M0075-0002 chained-NLI selectivity guard** prevents
   the M0072-0002 hang (M0075's structural defence
   delivered) but does NOT meet the 100-row stretch
   target. The guard correctly rejects all Q9 rebind
   candidates because per-outer match-set estimates
   exceed 100 for every column NDistinct stats are
   populated for.

5. **M0075-0007 build-toolchain bundle** showed +9.5 %
   wall-time regression on PGO + GOAMD64=v3 +
   `-ldflags="-s -w"` + `-trimpath` together. Binary
   size dropped 30 % as expected. The bundled
   measurement can't say which knob is responsible.

A new productivity issue surfaced from M0075's pace:
**the per-commit 21-q sweep cost (~25 min) is
prohibitive for planner-only iterations** (selectivity,
transitivity, rebind). Phase 7 §8 documented the
lesson; the user's 2026-05-10 request added the
plan-snapshot harness as M0076-0006.

The work splits into 7 sub-milestones with a clear
dependency graph (0001 → 0002, 0001 + 0004 → 0005;
0006 stands alone; 0003 stands alone; 0007 final).

## Sub-milestones

| # | Sub-milestone | Risk | Tier | Depends on |
| - | ------------- | ---- | ---- | ---------- |
| 0001 | Arena retention-site audit + sticky per-query slots | HIGH | structural | M0074-0003, M0075-0003 findings |
| 0002 | filterOp predicate batch wiring (post-0001) | MED | perf | 0001, M0074-0001 entry |
| 0003 | Build-toolchain knob isolation (A/B test) | LOW | perf | M0075-0007 Makefile |
| 0004 | Cost-model refinement for synthesised predicates | MED-HIGH | structural | M0075-0001 module |
| 0005 | Combined Q9 chained-NLI rebind + cardinality refinement | HIGH | structural | 0001 + 0004; M0075-0002 guard |
| 0006 | **NEW: Plan-snapshot regression harness** | LOW | tooling | none (independent) |
| 0007 | Final 22-q SF=1 sweep + Phase 8 handover | — | — | 0001..0006 |

## Design references

References to existing M0075 design docs (carrying
forward — these documents have status sections noting
the deferral path to M0076):

- `docs/design/0075-0003-datum-packed-flip.md` —
  Datum struct flip design + revert findings.
  Re-attempted in M0076-0001 (post-audit).
- `docs/design/0075-0004-filter-batch-wiring.md` —
  filterOp batch wiring design. Re-attempted in
  M0076-0002 (post-audit).
- `docs/design/0075-0007-build-toolchain-optimisation.md`
  — Makefile + flag matrix + empirical regression
  data. Refined in M0076-0003 (knob isolation).
- `docs/design/0075-0001-q5-equivalence-class-inference.md`
  — module landed; hook reverted. Re-enabled in
  M0076-0004 (cost-model refinement).
- `docs/design/0075-0002-q9-chained-nli-selectivity-guard.md`
  — guard landed; threshold + cardinality combined in
  M0076-0005.

New M0076 design docs (NEW; created at M0076 start):

- `docs/design/0076-0001-arena-retention-audit.md`
  (NEW) — retention-site audit checklist + sticky-
  slot vs always-Materialize trade-off.
- `docs/design/0076-0006-plan-snapshot-harness.md`
  (NEW) — capture/diff design + 3 equality levels
  (structural / strict-text / semantic-cost) + the
  decision tree for when plan-diff is sufficient.

(0002, 0003, 0004, 0005 may extend the M0075 design
docs in place rather than creating new ones — decide
at sub-milestone start.)

## Definition of Done

**Mandatory (correctness; must land for milestone closure):**
- [ ] M0076-0001: arena retention-site audit complete
      across all 8 retention sites (executor.Run,
      sortOp.Open, windowOp.Open,
      lockRowsOp.drainAndStamp, aggregateOp
      evalGroupKey/applyAgg, drainRowsCtx,
      drainRowsBounded, filterOp batch buffer);
      Datum packed flip RE-ATTEMPTED with full 21-q
      sweep + `go test ./...` PASS; struct = 40 B
      exact; arenaRegistry slot-reuse aliasing
      structurally impossible (sticky-per-query slots
      OR audit-verified Materialize-before-Drop
      invariant).
- [ ] M0076-0002: filterOp batch wiring landed
      (post-0001); 21-q row-count parity; new
      `filter_batch_test.go` passes batch-vs-per-row
      equivalence + NULL three-valued logic + fallback.
- [ ] M0076-0003: build-toolchain knobs A/B-tested
      individually; design doc updated with the
      recommended default configuration; +5 % wall-
      time win on at least one of Q1/Q3/Q12/Q13/Q21.
- [ ] M0076-0004: cost-model refinement re-enables
      M0075-0001 hook; Q5 EXPLAIN visibly different
      (synthesised `c.nationkey = n.nationkey`
      appears); Q9 row count ≥ 7 (mode-1 baseline
      preserved OR improved); Q9 does NOT cancel.
- [ ] M0076-0005: Q9 ≥ 100 rows DETERMINISTICALLY
      (5 consecutive runs at SF=1); Q21 = 381
      preserved; Q12 = 2 / Q13 = 35 / Q22 = 7
      preserved; 21-q row-count parity for all other
      queries.
- [ ] M0076-0006: plan-snapshot harness lands;
      capture + diff for all 22 TPC-H queries in
      ≤ 30 s wall time; first baseline captured at
      M0076-0001 binary; used as primary regression
      mechanism in 0004 / 0005 (executor commits
      still gate on full sweep).
- [ ] 22-q SF=1 sweep at M0076 close: Q12=2, Q13=35,
      Q21=381, Q22=7, Q9 ≥ 100, all other rows
      preserved.

**Best-effort (perf; may carry to M0077):**
- [ ] Q5 wall time < 60 s (was 1100 s cancel; depends
      on 0004 unlocking transitivity edges + 0002 batch
      eval).
- [ ] Q12 / Q13 wall time ≤ 70 % of M0075-final after
      0002 lands (filter batch win).
- [ ] Datum struct + arena lifecycle invariant
      documented as a permanent design contract.

**Final:**
- [ ] M0076-0007 sweep + handover doc (Phase 8)
      committed; profiles archived under
      `pprof-data/m0076-final/`.
- [ ] `go test ./...` PASS at every commit.
- [ ] `MEMORY.md` updated with M0076 outcomes +
      retention-site invariant if it differs from
      the M0073-0004 documented contract.

## Out of scope (carry to M0077+)

- **Per-connection permArena scoping** for multi-
  tenant production deployments. M0076 keeps the
  process-global permArena from M0074-0003.
- **MCV histogram improvements** for selectivity
  estimates — M0076-0005 uses what's currently
  populated. Stats refresh / re-ANALYZE flow is
  M0077 candidate.
- **SIMD intrinsics** for `evalBinaryBatch` — pure
  Go loops only.
- **Q5 wall-time floor < 10 s** — may need merge-
  join introduction or bitmap index scans (M0077+).
- **Q20 distributional gap** (99 vs canonical ~186)
  — confirmed dataset variance.
- **Continuous PGO** (auto-regenerating profiles in
  CI) — depends on M0076-0003 landing a beneficial
  configuration first.

## References

- `docs/handover/2026-05-10-tpch-status-phase7.md` —
  M0075 close + M0076 priority queue + plan-snapshot
  harness rationale (§5, §8).
- `docs/handover/2026-05-10-tpch-status-phase6.md` —
  M0074 close (cross-reference for M0076-0003 build
  baseline).
- `pprof-data/m0075-final/q5.{cpu,heap}.prof` —
  M0075-final captures; M0076 perf measurements
  compare against these.
- `internal/executor/arena_registry.go` (M0074-0003)
  — target of M0076-0001 audit.
- `internal/executor/datum.go` — Datum struct;
  target of M0076-0001 re-flip.
- `internal/executor/operators.go::filterOp` —
  target of M0076-0002 batch wiring.
- `internal/planner/equiv_class.go` (M0075-0001) —
  module re-enabled by M0076-0004.
- `internal/planner/nl_index_join_selectivity.go`
  (M0075-0002) — guard refined by M0076-0005.
- `Makefile` (M0075-0007) — `bench-build-optimized`
  / `pgo-profile` targets used by M0076-0003.
- `.ralph/fix_plan.md` § "Milestone 0076" — task
  list (mirrors this milestone's sub-milestones).

## Memory carry-forward

Existing memory entries that inform M0076 planning:
- `m0073_arena_q5_heap_drop.md` — Q5 heap −72 %
  baseline (M0073).
- `m0074_partial_scope_lessons.md` — autonomous-mode
  partial-commit pattern.
- `m0075_partial_outcomes_and_findings.md` — per-sub-
  milestone failure modes from M0075 (the load-
  bearing reference for M0076 planning).
- `m0071_stage_b_silent_regression.md` — the same
  pattern M0075-0003 hit; cited inline at M0076-0001.
- `feedback_tpch_pre_commit_gates.md` — pre-commit
  gate operation; M0076-0006 plan-snapshot harness
  reduces the cost of these gates for planner-only
  changes.
