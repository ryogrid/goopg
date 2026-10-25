# M0147-0011 — retire `GOOPG_SORT_PACKED` (packed sort retention)

Status: landed 2026-10-24.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 1. Lever count 30 → 29
(`scripts/lever-count.sh`).

## What was retired

The D-06 / MD-05 measurement arm of `sortOp` (`docs/design/minimize-datum-d06-sort/DESIGN.md`). With
`GOOPG_SORT_PACKED=on` at server start, a sort retained its rows as `PackedTuple`s (PG MinimalTuple encoding)
instead of `[]Row`:

- a packed chunk sort and permutation (`sortPackedChunk`, `applySortPermPacked`);
- a flush that deformed each tuple back to a Row (`flushPackedChunk`);
- a lazy-deform output slot;
- a double-buffered packed merge source (`sortSource.packed` / `pslots`).

The arm stayed default OFF: DESIGN §8b measured wall time +103 % with allocations flat. AGENT.md R6 / B1 (amended
2026-10-21) admits no packed retention, so deleting this leftover arm is in scope.

## Removed

`operators.go`:

- `sortPackedOn`, its `init`, `SetSortPackedEnabled`, `sortPackedEnabled` and `errSortKeyvalsMismatch`;
- the sortOp fields `packUse`, `packed`, `desc`, `outSlot`, `packScratch` and `mergeSlots`, with their Open setup and
  Close teardown;
- every `o.packUse` branch: ingestion, chunk sort, tail sort, flush, Next, and the merge's packed tail source;
- `sortSource`'s packed fields and advance arm.

The `[]Row` arm, which was the default, is unchanged.

`PackedTuple`, `PackedSlot` and `TupleDesc` stay: other code still uses them.

**Tests.**

- `TestSortPackedOrderingMatrix` and `TestSortPackedCTIDFollowsOwnRow` tested only the packed arm and are deleted.
- `TestSortByCTID` loses its packed dimension.
- `TestPackedSlotHasNoProducer` no longer admits `operators.go`: with the sort gone, no non-test file constructs a
  packed slot, and the tripwire is strict again.

## Inert proof (AGENT.md C6)

- `o.packUse` was `sortPackedEnabled()`, false unless `GOOPG_SORT_PACKED=on`, and every removed branch was gated on
  it.
- No script, CI stage, bench config or Makefile sets the variable (repository grep: design docs only).
- The default path is the code left in place.

## Gates

- units PASS;
- spotcheck PASS;
- acceptance arm 24 MATCH;
- sf025 PASS=99 (61 ck);
- standalone plan capture PLAN-SHAPE same=99 changed=0;
- census on the tree: TPC-H 22/22, SF0.25 99/99, SF1 99/99, unchanged;
- TestPort\_RegressSuite PASS;
- TestPort\_Isolation PASS.

**The sweep's appended plan channel.** It stalled twice on this host. It runs on the same server right after the 99
queries, which under `GOGC=off` sits at its memory limit (CLAUDE.md's sweep-tail collapse), and unrelated processes
were loading the host. The stamped sweep therefore ran with `SF025_NO_PLANS=1`, and plan identity was taken from the
standalone `plans` capture (27 s) and the census instead.

## Supersedes

The packed-retention part of M0141-S7-exec-d (sortOp feature parity for an Incremental Sort) and of ledger row
2026-09-17 (M0141-S7-exec-a..d): packed retention is no longer a parity goal (R6 / B1).
