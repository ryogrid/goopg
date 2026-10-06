# M0146-0052 — a Sort orders by ctid

Status: done 2026-10-06 (`de7f5b92b`).

## Symptom

- `SELECT ctid FROM nation ORDER BY ctid DESC LIMIT 2` on the TPC-H
  reference cluster returned `(0,1), (0,2)`. PG 18.3 returns the highest
  tids first.
- Reproduced on a throwaway 200000-row table:
  - in memory, `ORDER BY ctid DESC` returned input order;
  - with `work_mem = 64kB`, the spilled sort returned the wrong order and
    a NULL `ctid` for every row (`... ORDER BY ctid DESC`, and a ctid
    projected over `ORDER BY a % 7, a`).

## Cause

goopg carries a row's tid on the slot (`hasCTID`, see M0146-0051), not as
a column.

1. `sortOp` evaluated its keys on the bare Row (`sortKeyVals(row)` →
   `evalSortKeyValue`). `CTIDExpr` reads NULL from a Row, so every key was
   NULL and the stable sort kept input order. The incremental sort and the
   Merge Append merge keys had the same defect.
2. A spilled chunk wrote rows only. On the first spill the sort set
   `ctidsDisabled` and dropped the side channel, and keys recomputed for
   read-back rows had no tid.
3. A Sort with a `ctid` key never marked its child spine, so a Gather
   below it (the parallel shape here is Sort over Gather) shipped no tids.

PG never meets this: ctid is a scan output column (`Output: ctid`), the
sort key is a column reference, and a spilled tuple carries it like any
other attribute.

## Change

- `sortOp.sortKeyValsView` evaluates the keys on the materialised slot.
  `sortKeyVals(row)` wraps it.
- `incrementalSortOp.sortKeyVals` takes the slot.
- `setOp.mergeAdvance` evaluates on the owned, transferred row (the keys
  are retained across the input's next `Next`). It presents the row
  through a scratch `mergeKeySlot` stamped with the input slot's tid.
- `trackCTIDs = wantCTIDs || sortKeysUseCTID(keys)`, set in Open.
  - While it is set, `ctids` is maintained and permuted with every chunk
    (`sortChunk` now passes it to `applySortPerm`).
  - A spilled record gets a trailing int column: `block<<16 | offset`, or
    −1 for none (`sortCTIDDatum`).
  - `sortSource` strips the trailing column (`tidCol`), or takes an
    in-memory tail's tid from `ctids`. It evaluates keys through
    `sortKeyValsTID`, and `popMerge` returns the tid, which `Next` stamps
    when a consumer wants it.
  - `ctidsDisabled` is gone.
- Sort and incremental sort call `markSortWantCTIDs(child)` when a key
  reads ctid. `markSortWantCTIDs` passes an `opNodeOperator` bridge to
  `markSlabSorts`, so the marker crosses into a slab subtree (and from
  there, through an `OpAdapter`, to a Gather).
- A sort with no ctid consumer and no ctid key is unchanged: no tid
  bookkeeping and the same spill format.

## Verification

- `TestSortByCTID` (`internal/executor/sort_ctid_test.go`):
  - three queries: `ORDER BY ctid DESC`, the same with `LIMIT 3`, and a
    ctid projected over `ORDER BY a % 7, a`;
  - each runs in memory and spilled (work_mem 64kB), with plain and packed
    retention, through both builders, over three plan shapes: serial,
    forced-parallel (Sort over Gather) and a Gather Merge spliced over the
    Sort;
  - expected tids come from PG 18.3 on the same table (7 rows per page);
  - 60 of the 72 combinations fail at HEAD.
- Throwaway server: in-memory and spilled `ORDER BY ctid DESC` match PG;
  no NULL ctids remain.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24 MATCH;
  - sf025 96/96 with no verdict change;
  - ea-ratchet PASS.
- Regress A/B over 15 files (`--port`, fresh clusters), including window,
  limit and incremental_sort:
  - 12 byte-identical;
  - join, subselect and plpgsql moved by the same amounts on a second
    HEAD run (38, 40 and 411 lines), so those deltas are HEAD's own
    nondeterminism. join and plpgsql are byte-identical to that run.

## Not covered

- A ctid key orders through `compareDatum`'s `(e1,e2,...)` composite-row
  string comparison: `CTIDExpr` yields text. PG compares tids with
  `bttidcmp`. The order is right, but the M0146-0053 text-comparison fix
  must keep tid keys ordered, either with a typed tid datum or a
  comparator keyed on the expression.
- A spilled sort on a ctid key costs about 3x a spilled int sort (1.24 s
  against 0.38 s for 200000 rows), because of per-row text formatting and
  string parsing in the comparator. A typed tid would remove both.
- A Merge Append output row still carries no tid (only its merge keys
  do).
