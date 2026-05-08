# Milestone 0069 — Executor Slot Pipeline + GC Follow-Through + Long-Tail Query Fixes

**Status:** accepted (PARTIAL — 3 of 9 sub-milestones landed:
M0069-0001 Stage A scaffold, M0069-0005 Q20 / Q18 IN-unnest,
M0069-0007 HasInProgress; remaining 5 deferred to M0070 with
named successor sub-tasks)
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0068 (commits `aef72b7` / `e9080ac` /
`d79ebda` / `965c2a0`)
**Drives:** Q5 / Q20 cancel-resolution; Q9 silent FN; Q21
silent zero; cross-query GC ceiling; buffer-pool concurrency.

## Context

M0068 (PARTIAL) reduced `Datum` to 56 B / 2 pointers, wired
`sync.Pool` into the row pipeline, and added external sort.
The 22-query SF=1 sweep at `cancel-after=1200s` showed Q21
dropping from 1129.85 s → 387.76 s (−66 %) and a handful of
single-digit improvements (Q2 −24 %, Q11 −19 %, Q16 −19 %,
Q19 −10 %, Q13 −7 %), but **Q5 / Q20 still cancel**.

M0068 explicitly deferred three sub-milestones:

- The **TupleSlot pipeline** that replaces `Row = []Datum`
  and removes `BorrowSemantics`. M0068's analysis showed the
  residual `runtime.duffcopy` + `memmove` + `memclr` ≈ 60 %
  share on Q5 is row-shaped copying that only the slot model
  can eliminate structurally.
- The **per-batch String/Bytes arena** that depends on the
  slot lifetime contract.
- The **IndexScan lazy iteration** that needs a btree cursor
  API change.

Five "M0069 candidate" items also accrued in
`.ralph/fix_plan.md` from earlier milestones (Q5 / Q20 / Q21
planner improvements, SI HasInProgress, buffer-pool
partitioning).

This milestone consolidates all eight items plus a final
sweep into one phased landing. Per the user's
"順次着地、可能な限り進める" directive, we land sub-milestones
in **risk tier order** (LOW first) so each commit is
independently durable, and document any sub-milestones that
don't fit in one session as carried-forward at the milestone
close.

## Sub-milestones

| # | Sub-milestone | Risk | Depends on |
| - | ------------- | ---- | ---------- |
| 0001 | TupleSlot pipeline (replaces BorrowSemantics) | LOW | — |
| 0002 | Per-batch String/Bytes arena | MED | 0001 |
| 0003 | IndexScan lazy iteration (btree cursor) | HIGH | — |
| 0004 | Q5 build-time predicate pushdown (guarded re-attempt) | MED | 0001 helpful |
| 0005 | Q20 non-correlated IN-list unnest extension | MED | — |
| 0006 | Q21 anti-side hash-join inner-Filter conjunct lift + composite-NLI for Q9 | LOW | — |
| 0007 | SI HasInProgress non-linear lookup | LOW | — |
| 0008 | Buffer-pool `poolMu` partitioning | MED-HIGH | profile gate |
| 0009 | M0069 final 22-query SF=1 sweep + report | — | 0001..0008 |

## Design references

The four design docs from M0068 stay authoritative for the
overlapping sub-milestones:

- `docs/design/0068-0001-datum-compact-layout.md` — Datum
  layout (already landed in M0068).
- `docs/design/0068-0002-tuple-slot-pipeline.md` — TupleSlot
  interface, MaterializedSlot / VirtualSlot / BatchRefSlot,
  migration plan. Authoritative for **M0069-0001**.
- `docs/design/0068-0003-batch-string-arena.md` — Arena
  layout, Datum.arena field, batch lifecycle. Authoritative
  for **M0069-0002**.
- `docs/design/0068-0004-row-slot-pool.md` — sync.Pool
  cross-query slot reuse (already partially landed in
  M0068).

The new sub-milestones (M0069-0003..0008) are localized
enough that they're tracked as `.ralph/fix_plan.md` task
specs rather than separate design docs.

## Definition of Done

- [ ] M0069-0001 lands: `Borrowable` / `OwnedRow` /
      `BorrowedRow` / `setChildBorrow` removed;
      `TupleSlot` interface in use across operators.
- [ ] M0069-0007 lands: `HasInProgress` non-linear at N > 16.
- [ ] M0069-0006 lands: Q21 row count > 0
      (canonical ~411); Q9 row count > 7.
- [ ] M0069-0005 lands: Q20 row count > 0 within budget.
- [ ] M0069-0004 lands: Q5 probe time drops ≥ 30 %; Q3 row
      count preserved.
- [ ] M0069-0002 lands: arena allocator in place; Q5 `inuse_space`
      shows arena pages dominate string memory.
- [ ] M0069-0003 lands: Q9 SF=1 peak heap drops ≥ 5 GB.
- [ ] M0069-0008 lands OR is documented as "no observed
      contention" after profile.
- [ ] M0069-0009 sweep + report committed.
- [ ] `go test ./...` PASS at every phase commit.

Sub-milestones that don't land in this session remain
`[ ]` in `.ralph/fix_plan.md` with a named successor — no
forwarding without a written reason.

## Out of scope (carry to M0070+)

- Columnar batches (true vector pipeline).
- WAL format convergence (`review/postgres_vs_goopg_performance_divergence.md` §3).
- Checkpoint request decoupling (review §2).

## References

- `analysis/tpch-m0068-baseline-2026-05-08.md` — M0068
  results.
- `review/postgres_vs_goopg_performance_divergence.md` §1, §7.
- `practice/go_gc_optimized_programming.md`.
