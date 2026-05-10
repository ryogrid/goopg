# Milestone 0075 — TPC-H residual: Q5 plan-level / Q9 rebind / Datum packed / filter batch / numericDiv

**Status:** planned
**Branch:** `gc-oriented-refactor` (continuation of M0074)
**Depends on:** M0074-final (commit `639272a`) — Phase 6
handover; M0074-0006 — `mulInt64Pow10` /
`alignNumericInt64`; M0074-0003 — `arenaRegistry` +
`permArena`; M0074-0001 — `evalBinaryBatch` +
`canVectoriseExpression`; M0074-0002 — `VirtualCol`
accessor; M0071-0009 — `SchemaColumn.SourceTableIdx`.
**Drives:** Q5 wall time → seconds (was 600-1100 s
cancel) via equivalence-class inference; Q9 deterministic
≥ 100 rows via chained-NLI rebind WITH per-outer
selectivity guard; Datum struct 64 B → 40 B (24 B
headroom freed); filterOp predicate batch wiring consumes
M0074-0001 entry; numericDiv int64 fast-path completes
the M0074-0006 arithmetic-suite coverage; **22/22
row-count preservation**; Q12=2 / Q13=35 / Q21=381 /
Q22=7 / Q9 ≥ 7 (post-F: ≥ 100) every commit.

## Context

After M0074 close (Phase-6 handover, commit `639272a`)
five residuals motivate M0075:

1. **Q5 plan-level gap** — `evalExprSlot` 72 % cum CPU
   means Q5 is touching far more rows than the optimal
   plan would. Phase-6 §4.1 identified plan-level work
   as the bigger lever vs further executor optimisation.
   The specific gap: `c.nationkey = s.nationkey AND
   s.nationkey = n.nationkey` does NOT auto-infer
   `c.nationkey = n.nationkey`. PostgreSQL infers this
   via EquivalenceClass; goopg currently doesn't.

2. **Q9 chained-NLI selectivity collapse** — M0072-0002
   attempted the planner-side rebind and was reverted
   because the resolved column was high-cardinality at
   runtime, exploding the per-outer match-set.
   M0074-0002 landed forward-compat infrastructure
   (`VirtualCol(col)`, defensive bounds check) but did
   NOT attempt the rebind. M0075 adds the missing piece:
   per-outer selectivity guard.

3. **M0074-0001 vectorised infrastructure unwired** —
   `evalBinaryBatch` is callable but no operator hot
   loop calls it. The actual perf lever is wiring the
   batch path into `filterOp` (research found Filter is
   in `filterOp`, not seqScanOp).

4. **Datum struct headroom exhausted at 64 B** —
   M0074-0003 landed the `arenaRegistry` + `permArena`
   infrastructure but did NOT flip Datum. Research
   revealed the migration scope is much smaller than
   originally feared: only 7 internal `Buf:` literal
   sites + 3 accessor reads + 0 test fixtures. Confirmed
   packed size: **40 B** (24 B saving, not 12 B).

5. **`numericDiv` int64 fast-path missing** — M0074-0006
   wrapped numericCmp / Add / Sub / Mul; division was
   deferred. Research found it's tractable: reuse
   `mulInt64Pow10` for shift-and-overflow check;
   byte-for-byte rounding match achievable.

The work splits into independent tracks:

- **Track 1 (M0075-0005):** numericDiv int64 fast-path.
  Self-contained extension of M0074-0006.
- **Track 2 (M0075-0003):** Datum struct flip. Scoped to
  datum.go (research-confirmed minimal blast radius).
- **Track 3 (M0075-0004):** filterOp batch wiring.
  Consumes M0074-0001 entry.
- **Track 4 (M0075-0001):** Q5 transitivity inference.
  Pure planner-side addition.
- **Track 5 (M0075-0002):** Q9 chained-NLI rebind.
  Highest risk — lands last with selectivity guard.

Land order: B (0005) → C (0003) → D (0004) → E (0001) →
F (0002) → G (0006).

## Sub-milestones

| # | Sub-milestone | Risk | Tier | Depends on |
| - | ------------- | ---- | ---- | ---------- |
| 0005 | numericDiv int64 fast-path | LOW | perf | M0074-0006 (mulInt64Pow10) |
| 0003 | Datum struct full flip (64B → 40B) | MED-HIGH | structural | M0074-0003 (arena registry + permArena) |
| 0004 | filterOp predicate batch wiring | MED | perf | M0074-0001 (evalBinaryBatch + detector) |
| 0001 | Q5 equivalence-class inference | MED | perf | — |
| 0002 | Q9 chained-NLI rebind WITH selectivity guard | HIGH | structural | M0071-0009 (SourceTableIdx); M0074-0002 (VirtualCol accessor) |
| 0007 | Build-toolchain optimisation (PGO + GOAMD64=v3 + ldflags + trimpath) | LOW | perf-build | Go 1.21+ |
| 0006 | Final 22-query SF=1 sweep + Phase 7 handover | — | — | 0001..0005 + 0007 |

## Design references

- `docs/design/0075-0001-q5-equivalence-class-inference.md` (NEW)
- `docs/design/0075-0002-q9-chained-nli-selectivity-guard.md` (NEW)
- `docs/design/0075-0003-datum-packed-flip.md` (NEW)
- `docs/design/0075-0004-filter-batch-wiring.md` (NEW)
- `docs/design/0075-0005-numeric-div-int64-fast-path.md` (NEW)
- `docs/design/0075-0007-build-toolchain-optimisation.md` (NEW; inserted 2026-05-10 by user request)
- `docs/design/0072-0002-chained-nli-rebind.md` —
  reverted approach; M0075-0002 carries lessons.
- `docs/design/0074-0003-datum-packed-layout.md` —
  M0074 partial-flip design; M0075-0003 completes it.
- `docs/design/0074-0006-numeric-int64-fast-path.md` —
  M0074-0006 design; M0075-0005 extends to division.

## Definition of Done

**Mandatory (correctness; must land for milestone closure):**
- [ ] M0075-0005 lands: `numericDiv` int64 fast-path
      via `mulInt64Pow10`; full SF=1 sweep row-count
      preserved.
- [x] M0075-0003 DEFERRED to M0076 — attempt 2026-05-10
      hit the M0071-Stage-B silent-regression pattern:
      tight gate passed, 21-q sweep showed Q10/Q11/Q12/
      Q15/Q16/Q20/Q21 row counts crashing to 0 and
      Q13/Q22 mis-counting. Suspected root cause:
      arenaRegistry slot reuse aliasing retained Datums
      across query boundaries. Reverted before commit
      per pre-commit gate discipline. M0076-0001
      (planned): retention-site audit + sticky
      per-query arena slots before re-attempt.
- [x] M0075-0004 DEFERRED to M0076 — same risk surface
      as M0075-0003: the batch path requires Materialize-
      each-row before buffering, and Materialize across
      batch/page boundaries was just shown to be fragile.
      Landing the wiring before M0076-0001's retention-
      site audit would re-expose the slot-reuse aliasing
      that broke M0075-0003. M0074-0001 evalBinaryBatch
      + detectors stay landed as forward-compat. M0076-0002
      (planned).
- [x] M0075-0001 PARTIAL: `inferTransitiveEqualities`
      module + 9 unit tests landed at
      `internal/planner/equiv_class.go`; the planner-
      side hook into `tryBushyDP` was attempted then
      reverted. Empirical result: enabling the
      synthesised conjuncts caused Q9 to cancel at the
      600 s budget (was 219-256 s baseline mode-1).
      Module is correct (unit tests PASS); the
      planner-side hook needs join-graph cost-model
      refinement before re-attempt. M0076-0004 (planned).
- [x] M0075-0002 PARTIAL: selectivity guard landed at
      `internal/planner/nl_index_join_selectivity.go`;
      `nl_index_join.go:400` rebind block extended to
      fire for `*NestedLoopIndexJoin` outers via
      `findColumnIndexByNameAndSource` + per-outer
      selectivity check. **The M0072-0002 hang is
      structurally prevented** (Q9 = 7 rows / 239 s
      mode-1 baseline preserved). Q21 = 381 rows
      single-NLI win preserved. The 100-row stretch
      target is NOT met — guard correctly rejects all
      Q9 rebinds (column NDistinct estimates + table
      rowcount produce per-outer match-set > 100 for
      every candidate). M0076-0005 (planned): combine
      with M0075-0001 equivalence-class synthesis +
      refine selectivity threshold.
- [x] M0075-0007 PARTIAL: Makefile `bench-build-optimized` +
      `pgo-profile` targets landed as M0076-iteration
      infrastructure. Empirical result on this workload:
      PGO + GOAMD64=v3 + ldflags="-s -w" + trimpath
      produced **net +9.5 % wall-time regression** on the
      tight-gate suite (Q12 +12 %, Q13 +17 %, Q21 +8 %,
      Q22 +17 %, Q9 +6 %). Correctness preserved (all
      row counts matched baseline). Binary size dropped
      30 % (14.1 → 9.9 MB) as expected. Investigation
      of which knob is responsible deferred to M0076-0003.
      Default `make bench-build` flow remains
      unoptimised so subsequent M0075 perf measurements
      compare against the M0075-0005 baseline (`8230af8`).
- [ ] 22-query SF=1 sweep at M0075 close: Q12=2, Q13=35,
      Q21≥100, Q22=7, Q9 ≥ 100, all other rows preserved.

**Best-effort (perf; may carry to M0076):**
- [ ] Q5 wall time < 60 s (was 1100 s cancel).
- [ ] Q12 / Q13 wall time ≤ 70 % of M0074-final (filter
      batch win).
- [ ] Q1 / Q3 / Q14 wall time delta in `numericDiv`
      flat % drops below 1 %.

**Final:**
- [ ] M0075-0006 sweep + handover doc (Phase 7) committed;
      profiles archived under `pprof-data/m0075-final/`.
- [ ] `go test ./...` PASS at every commit.

## Out of scope (carry to M0076+)

- **Per-connection permArena scoping** for multi-tenant
  production. M0075 lands process-global permArena.
- **MCV histogram improvements** for Q5/Q9 selectivity.
- **SIMD intrinsics** for evalBinaryBatch.
- **Q5 wall-time floor < 10 s** — may need merge-join
  introduction or bitmap index scans (M0076+).
- **Q20 distributional gap** (99 vs canonical ~186) —
  confirmed dataset variance.

## References

- `docs/handover/2026-05-10-tpch-status-phase6.md` —
  M0074 close + M0075 candidate enumeration.
- `pprof-data/m0074-final/q5.{cpu,heap}.prof` —
  baseline captures.
- `internal/executor/numeric.go::numericDiv` (l.~273)
  — target of M0075-0005.
- `internal/executor/datum.go:101-122` — Datum struct;
  target of M0075-0003.
- `internal/executor/operators.go::filterOp` — target
  of M0075-0004.
- `internal/planner/planner.go::Plan` — call site for
  M0075-0001 inferTransitiveEqualities pass.
- `internal/planner/nl_index_join.go:399` — rebind
  block extension target for M0075-0002.
- `internal/planner/bushy.go:1610` —
  `findColumnIndexByNameAndSource` (M0071-0009);
  used by M0075-0002.
