# Milestone 0071 — TPC-H Correctness Closure (planner-first) + Slot Pipeline Carry-Forward

**Status:** planned
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0070 (commit `30cd511` PARTIAL) and M0069
(commit `a32d0fb` PARTIAL).
**Drives:** TPC-H SF=1 row-count correctness on Q9 / Q20 /
Q21; Q5 cancel-resolution either via planner-side pushdown
or via the structural slot-pipeline track.

## Context

After M0070 close, the gc-oriented-refactor branch's
TPC-H SF=1 state is captured in
`analysis/tpch-m0070-baseline-2026-05-08.md`:

- **18 / 22 queries return correct row counts and complete.**
- **4 queries are incorrect or do not complete:**

| Q | Status | Current | Canonical | Cause |
| - | ------ | ------- | --------- | ----- |
| Q5 | cancel 1200 s | — | — | structural; ~60 % CPU is `runtime.duffcopy` + `memmove` + `memclr` (row-shaped copies). |
| Q9 | OK 215 s, **rows = 7** | 7 | ≈ 175 | "schema-annotation-vs-runtime-layout mismatch" surfaced during the M0067-0003 composite-NLI attempt (returned 1 row, reverted). M0064 fixed a sibling rebind bug by gating Name re-bind on `outerNode == *MultiHashJoin` (`internal/planner/nl_index_join.go:399`); Q9's chained-NLI shape needs a similar narrow audit. |
| Q20 | OK 30 s, **rows = 0** | 0 | ≈ 186 | Likely correctness bug in the M0069-0005 non-correlated IN-unnest path (`unnestNonCorrelatedInExpr` at `internal/planner/unnest.go:~1095`, commit `ebb267d` + `5f120c1`). Cause undocumented. |
| Q21 | OK 384 s, **rows = 0** | 0 | ≈ 411 | Anti-side residual conjunct issue. M0070-0001 pinned the inner-only conjunct invariant (`TestM0070Q21InnerOnlyConjunctsStay`), but the EXISTS-unnest's lift of inner-only conjuncts (`innerOnlyLifted []Expr` at `internal/planner/unnest.go:1747`) is declared empty and never populated. |

The M0069-0001 attempt to land the **TupleSlot pipeline
Stages B-E** in 2026-05-08 was reverted — the per-call
slot wrap regressed Q11 ~+90 % (sync.Pool variant) and the
per-op `outSlot` retry introduced silent correctness
regressions on Q12 (rows 2 → 0) and Q13 (rows 35 → 2).
Reverted via commits `336550c` and `41dd715`. The slot
pipeline is the structural fix for Q5's GC residual but is
multi-day work.

**Per the user's directive (2026-05-08): "基本的にはplanner
単独fixで可能そうな範囲はその方向で解決する計画"** (prefer
planner-only fixes where feasible) and "**Q20 の調査タスク
も加えて**" (add a Q20 investigation task), M0071 is
structured as:

1. **Front-loaded planner-only correctness work**
   (M0071-0001..0004) — Q9 rebind, Q20 investigation, Q21
   lift, Q5 pushdown. Each is localised; landing all four
   gets the row-count picture to ≥ 21 / 22 correct
   without the slot pipeline.
2. **Carry-forward structural runtime track**
   (M0071-0005..0008) — slot pipeline, arena, IndexScan
   lazy, poolMu partition. Multi-day; runs as its own
   focused session.
3. **Final sweep + report** (M0071-0009).

## Sub-milestones

| # | Sub-milestone | Risk | Tier | Depends on |
| - | ------------- | ---- | ---- | ---------- |
| 0001 | Q9 NLI column-rebind fix (planner-only) | LOW-MED | planner-first | — |
| 0002 | Q20 zero-rows investigation (NEW; planner-only) | MED | planner-first | — |
| 0003 | Q21 anti-side inner-Filter conjunct lift (planner-only) | LOW | planner-first | — |
| 0004 | Q5 build-time predicate pushdown (planner-only, guarded) | MED | planner-first | — |
| 0005 | TupleSlot pipeline Stages B-E (multi-day) | HIGH | structural | — |
| 0006 | Per-batch String/Bytes arena | MED | structural | 0005 |
| 0007 | IndexScan lazy iteration (btree cursor API) | HIGH | structural | — |
| 0008 | Buffer-pool poolMu byTag partitioning | MED | structural | profile gate |
| 0009 | Final 22-query SF=1 sweep + report | — | — | 0001..0008 (whichever land) |

## Design references

- `docs/design/0068-0002-tuple-slot-pipeline.md` —
  authoritative for **M0071-0005**.
- `docs/design/0068-0003-batch-string-arena.md` —
  authoritative for **M0071-0006**.
- `docs/design/0071-0002-q20-zero-rows-diagnostic.md` (NEW)
  — Q20 investigation methodology + likely candidates.

The other planner-only items (M0071-0001 / 0003 / 0004) are
localised enough to track via the `.ralph/fix_plan.md` task
specs alone; no separate design docs.

## Definition of Done

Per the user directive (planner-only fixes where feasible;
slot pipeline as later track):

**Mandatory (planner-first, this milestone):**
- [ ] M0071-0001 lands: Q9 row count ≥ 90 (target 175);
      Q3 preserved.
- [ ] M0071-0002 lands: Q20 row count > 0 (target ≥ 100);
      Q18 row count preserved at 11.
- [ ] M0071-0003 lands: Q21 row count > 0 (target ≥ 100);
      `TestM0070Q21InnerOnlyConjunctsStay` preserved.
- [ ] M0071-0004 lands: Q5 elapsed drops ≥ 30 % vs M0070
      OR Q5 completes; Q3 row count preserved at 11462.

**Best-effort (structural, may carry to M0072):**
- [ ] M0071-0005 lands: Borrowable types removed;
      Q5 pprof duffcopy/memmove/memclr ≤ 25 %; row-count
      parity preserved.
- [ ] M0071-0006 lands: arena allocator; Q5 inuse_space
      shows arena pages dominate string memory.
- [ ] M0071-0007 lands: btree cursor API; Q9 SF=1 peak
      heap drops ≥ 5 GB.
- [ ] M0071-0008 lands OR documented null result.

**Final:**
- [ ] M0071-0009 sweep + report committed.
- [ ] `go test ./...` PASS at every phase commit.

The session's success metric is **22-query row-count
correctness ≥ 21 / 22** after the four planner-only items
land (Q5 may still cancel structurally even if its row
count is unknown).

## Out of scope (carry to M0072+)

- Columnar batches (true vector pipeline) — successor to
  the slot pipeline.
- WAL format convergence
  (`review/postgres_vs_goopg_performance_divergence.md` §3).
- Checkpoint request decoupling (review §2).

## References

- `analysis/tpch-m0070-baseline-2026-05-08.md` — M0070
  baseline (Q1 −10 %, bgwriter contention −89 %).
- `analysis/tpch-m0069-baseline-2026-05-08.md` — M0069
  baseline (Q20 cancel-1200 s → 30 s; Q18 −39 %).
- `analysis/tpch-m0067-baseline-2026-05-08.md` — Q9 schema-
  runtime mismatch root-cause notes; Q21 newly OK (0 rows).
- `internal/planner/nl_index_join.go:399` (M0064 outer-MHJ
  rebind gate) — precedent pattern for M0071-0001's
  chained-NLI gate.
- `internal/planner/q21_live_test.go::
  TestM0070Q21InnerOnlyConjunctsStay` — invariant test
  M0071-0003 must preserve.
