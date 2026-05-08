# Milestone 0070 — Executor Slot Pipeline Completion + Long-Tail Query Closure

**Status:** planned
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0069 (commits `a8a272a` / `77499e5` /
`d0de10d` / `ebb267d` / `5f120c1` / `e4ee8a2`)
**Drives:** Full TupleSlot pipeline migration, byte-arena
allocation for varlen payload, Q5 / Q9 / Q21 long-tail
closure, buffer-pool poolMu sharding, IndexScan lazy
iteration. **Hard requirement:** no further deferral of any
sub-milestone (per user directive).

## Context

M0069 (PARTIAL) landed three sub-milestones:

- **M0069-0001 Stage A** — TupleSlot interface scaffold
  (`internal/executor/slot.go`). Stages B-E carried.
- **M0069-0005** — non-correlated IN→SemiJoin: Q20
  cancel-1200s → 30.24 s; Q18 −39 %.
- **M0069-0007** — HasInProgress sort.Search above N=16.

Five sub-milestones remained as "carry to M0070" with
named successors. The user has now directed all six be
finished in M0070 (no further deferral). M0070 also folds
in the final sweep + report.

## Sub-milestones

| # | Sub-milestone | Risk | Depends on |
| - | ------------- | ---- | ---------- |
| 0001 | Q21 inner-only conjunct verification + Q9 composite-NLI | LOW | — |
| 0002 | Buffer-pool poolMu partitioning | MED | profile gate ignored |
| 0003 | TupleSlot pipeline Stages B-E (signature flip, VirtualSlot wiring, Materialize, Borrowable removal) | HIGH | — |
| 0004 | Per-batch String/Bytes arena | MED | 0003 |
| 0005 | Q5 build-time predicate pushdown | MED | 0003 helpful |
| 0006 | IndexScan lazy iteration (btree cursor) | HIGH | — |
| 0007 | Final 22-query SF=1 sweep + report | — | 0001..0006 |

## Design references

- `docs/design/0068-0001-datum-compact-layout.md` — landed
  in M0068; arena field added in M0070-0004.
- `docs/design/0068-0002-tuple-slot-pipeline.md` —
  authoritative for **M0070-0003**.
- `docs/design/0068-0003-batch-string-arena.md` —
  authoritative for **M0070-0004**.
- `docs/design/0068-0004-row-slot-pool.md` — already
  partially landed (M0068).

The other M0070 sub-milestones (0001 / 0002 / 0005 / 0006)
are localised enough to track via fix_plan task specs alone.

## Definition of Done (no defer policy)

- [ ] M0070-0001 lands: Q9 row count > 7 (target ≥ 90);
      Q21 row count > 0 (target ≥ 411).
- [ ] M0070-0002 lands: poolMu sharded; mutex profile
      delta documented.
- [ ] M0070-0003 lands: `Borrowable` /
      `OwnedRow` / `BorrowedRow` /  `setChildBorrow`
      removed; `TupleSlot` interface in use across
      every operator's `Next()`.
- [ ] M0070-0004 lands: Arena allocator in place; Q5
      `inuse_space` shows arena pages dominate string
      memory.
- [ ] M0070-0005 lands: Q3 row count preserved at 11462;
      Q5 probe-time ≥ 30 % drop OR Q5 row count > 0.
- [ ] M0070-0006 lands: Q9 SF=1 peak heap drops ≥ 5 GB.
- [ ] M0070-0007 sweep + report committed.
- [ ] `go test ./...` PASS at every phase commit.

## Out of scope

- Columnar batches (true vector pipeline).
- WAL format convergence.
- Checkpoint request decoupling.

## References

- `analysis/tpch-m0069-baseline-2026-05-08.md` — M0069
  results.
- `practice/go_gc_optimized_programming.md`.
- `review/postgres_vs_goopg_performance_divergence.md`.
