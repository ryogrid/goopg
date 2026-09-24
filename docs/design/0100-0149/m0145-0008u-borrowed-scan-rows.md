# M0145-0008u: a seq scan hands a qualifying aggregate its own row, uncloned

Status: **LANDED 2026-09-25**. Task: `.ralph/fix_plan.md` M0145-0008u (Kind:
impl, Parent: M0145-0008q). Evidence: `analysis/m0145/m0145-0008u/`.

## Why

PG hands an aggregate a slot over the pinned buffer
(`ExecStoreBufferHeapTuple`) and copies only where a consumer keeps the row
(`ExecMaterializeSlot`). goopg's `seqScanOp` cloned every surviving tuple
(`cloneRowOwnedPrefix`): a fresh row plus a `MaterializeArena` per datum.
The 0008k profile of `sum(l_quantity)` over lineitem attributes **37%** of
the query to that clone (4.13 s of 11.19 s cumulative).

## What the clone protects, and what this changes

The clone detaches two things:
- **the row slice.** `scanRow` is reused for the next tuple;
- **arena-backed datums.** Varlena values and big numerics decode into the
  scan's per-page arena, which resets at the next page.

Decoded datums never alias page bytes: the arena is a copy, and
`MaterializeArena` leaves non-arena datums as they are. So once a consumer
has consumed the row, neither the page lock nor the pin matters.

A parent may therefore take `scanRow` itself when two things hold:
- it finishes with each row before its next `Next()` and never keeps the
  slice;
- it detaches or recomputes every value it keeps.

The aggregate's drain loop does exactly that for a whitelisted shape,
`aggregateBorrowsScanRows` (`internal/executor/scan_borrow.go`):
- **Built-in `count`, `sum`, `avg`, `min`, `max`** only.
  - Group keys are `MaterializeArena`'d at group creation.
  - count, sum and avg keep counters or freshly computed sums;
    `numericAdd` always builds a new value.
  - min and max `MaterializeArena` what they store.
- **Refused**, because each can keep an input value as is:
  - passthrough columns, stored from the group's first row undetached;
  - user aggregates, whose sfunc may return its argument as the state;
  - DISTINCT, which keeps seen-sets;
  - `ORDER BY` inside the call and `WITHIN GROUP`, which collect rows;
  - the sorted strategy, where the current group's key spans rows;
  - Finalize, whose child is a Gather.

The Aggregate arm of `buildNode` (the slab builder reaches it through its
adapter) sets `seqScanOp.borrowRows` on a direct seq-scan child. That
includes the child whose Filter was absorbed into the scan, and each
parallel worker's scan under a Partial aggregate. `seqScanOp.Next` then
skips the clone. The tail-poison harness still sees the undeformed tail.

## Tests

- `TestAggregateBorrowsScanRowsWhitelist` pins the whitelist and each
  refusal.
- `TestScanBorrowMatchesClonedResults` runs four aggregate queries, borrowing
  on and then forced off (`scanBorrowDisabled`), with the tail poison armed,
  over a 6,000-row multi-page table. The four:
  - ungrouped int aggregates;
  - `GROUP BY` a text key with text `min`/`max` and big-numeric `sum`;
  - ungrouped text and big-numeric `min`/`max`;
  - a filtered scan.

  Each asserts that the scan actually borrows, and that the results equal
  the clone path's text for text.
- Found on the way: the shared test helper `sortedRowStrings` renders only
  `Datum.Int`, so it compares text and numeric cells meaninglessly. The new
  test uses a value-rendering `sortedRowValues`, and the M0145-0008v
  lifecycle tests were switched to it; their text column is now really
  compared.

## Measured

TPC-H SF1 lineitem, private clone, alternating arms; values are identical:

| query | HEAD | candidate | PG 18.3 |
|---|---|---|---|
| `count(*)`, serial | ~1.50 s | **~0.95 s** | 0.96 s |
| `count(*)`, 2 workers | ~0.57 s | **~0.24 s** | — |
| `sum(l_quantity)`, serial | ~2.91 s | **~2.13 s** | 1.40 s |
| `sum(l_quantity)`, 2 workers | ~1.10 s | **~0.65 s** | — |

On the TPC-H acceptance arm, Q1 went 4.17 → 3.12 s and Q6 stayed at
0.61 → 0.60 s: Q6's filter rejects most rows before the clone.

## Gates

- units: PASS.
- `go test -race` over the borrow, deform and parallel-aggregate tests:
  PASS.
- `tpch-spotcheck`: PASS.
- acceptance arm: 24 MATCH.
- `tpcds-sf025`: 96/96, 99/99 shapes; a Q5 swing reversed on the rerun.
- Isolation and full regress: only the failures HEAD also has.

Movement: none. This is an executor-only change and no plan moved.

## Not ported (ledgered)

PG avoids the copy for every consumer that does not retain, not just
aggregates: Filter, Project, the probe side of a join, Limit and others.
goopg borrows only under the whitelisted aggregate. Extending it means
proving, for each consumer, that it neither keeps the slice nor keeps an
arena-backed value past the page. The passthrough and user-aggregate
refusals could lift once those paths detach what they keep.
