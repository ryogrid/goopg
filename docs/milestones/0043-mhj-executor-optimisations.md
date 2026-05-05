# Milestone 0043 — MHJ Executor Optimisations

**Status:** accepted
**Depends on:** Milestone 0038 (multi-way hash join — landed),
Milestone 0041 (TPC-H result-parity gate — landed).
**Drives:** unblocking TPC-H Q9 / Q5 / Q7 / Q8 SF=1 wall-time, and
laying groundwork for M0044 (B-tree key support across the HammerDB
schema types) by establishing a clear executor performance baseline.

## Context

The multi-way hash join (M0038) replaced N-1 binary hash joins with a
single chain of hash-table lookups. Run-005 demonstrated that the
M0041-0002 `expandChain()` implementation materialised the entire
Cartesian product into `[]Row` before yielding any output row,
filling >19 GB heap on Q9 (1.8 M lineitem rows / 30 % SF=1) and
hitting 91 % CPU in GC.

This milestone closes that regression in two landings.

## Required Design Docs

- `0043-0001-mhj-lazy-iterator.md` — *(referenced from
  M0043-0001 commit `b9dc46f`; the design rationale lives inline
  in the executor source — see the M0043-0001 paragraph in
  `internal/executor/multi_hash_join.go` and the run-005
  pre-mortem in `analysis/tpch-hammerdb-run-005.md`).*
- `0043-0002-mhj-predicate-pushdown.md` — predicate pushdown into
  chain steps, lands in commit `b7cb6aa`.

## Definition of Done

- [x] **M0043-0001**: lazy per-call iterator replaces
      `expandChain()`. RSS bounded; Q9 no longer crashes the heap.
      Verified in run-006 (`analysis/tpch-hammerdb-run-006.md`).
- [x] **M0043-0002**: residual filters partitioned by deepest
      chain step; evaluated at the earliest binding step so failed
      prefixes abort without expanding deeper steps. Verified by
      `TestMultiHashJoinPredicatePushdown` and the run-007 SF=1
      power test (`analysis/tpch-hammerdb-run-007.md`).
- [x] `TestTPCHResultParity` identical=22, divergent=0,
      errored=0 throughout — no parity regression.
- [x] **End-to-end**: HammerDB TPC-H Q9 finishes in 891 s
      (~14.9 min) on full SF=1 — first-ever completion. Q14 and
      Q2 unaffected. The test harness advances to Q20.

## Open follow-ups (deliberately deferred)

- **M0043-0003 (optional / non-blocking)**: `datumKey()` string
  serialisation per probe lookup (~22 M calls for Q9) and the
  `evalExpr` per-call dispatch overhead for trivial BinaryOp
  shapes are the remaining hot paths. A byte-coded fast path for
  the common `ColumnRef ⊕ ColumnRef` shape, or a fixed-width
  numeric/byte-slice keyed hash table, would close the gap to
  the design's "single-digit minutes" target on Q9. **Not** a
  blocker for M0043 acceptance; tracked in
  `.ralph/fix_plan.md` under M0043-0002's sub-bullet.
- **Q20 wall time**: independent of MHJ. Tracked under
  Milestone 0040 (M0040-0004 — recursive scalar subquery
  unnest); design doc already at
  `docs/design/0040-0002-recursive-subquery-unnest.md`.
