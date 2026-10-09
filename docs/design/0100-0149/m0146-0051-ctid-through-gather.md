# M0146-0051 — ctid survives a Gather and a Gather Merge

Status: done 2026-10-06 (`19db7a266`).

## Symptom

- On the TPC-H reference cluster, `SELECT count(DISTINCT ctid) FROM
  customer` returned 0. PG 18.3 returns 150000. The plan was Aggregate →
  Gather Merge → Sort → Parallel Seq Scan.
- Reproduced on a throwaway 200000-row table:
  - the same count returned 0;
  - `SELECT ctid, a FROM c WHERE a % 50000 = 0` (Gather → Parallel Seq
    Scan, ctid projected above the Gather) returned NULL for every row a
    worker produced; only the leader's own rows kept their ctid.

## Cause

- goopg carries a row's tid on the slot (`MaterializedSlot.hasCTID`,
  stamped by the scan), not as a column. `CTIDExpr` reads it from the slot
  it is evaluated against.
- A worker ships its rows through `transferRowForQueue`, which copies only
  the Datums. Every worker row therefore reached the leader without its
  tid.
- The consumer marker that switches on the Sort's tid side channel
  (`markSortWantCTIDs`, EX3-05 Cut A) stopped at a gather. It could not
  reach the Sorts inside the participant trees anyway, because those are
  built only at the gather's Open.
- The slab twin (`markSlabSorts`) stopped at the `OpAdapter` that wraps
  the gather.
- Gather Merge evaluated its merge keys on a bare Row, so a `ctid` key
  read NULL.

PG projects ctid into the scan's target list (`Output: ctid`), so the
value travels as an ordinary column. goopg keeps its side channel and
extends it across the queue.

## Change

- `markSortWantCTIDs` sets `wantCTIDs` on `gatherOp` and
  `gatherMergeOp`. Each gather re-applies the marker to every participant
  tree it builds (the leader's and each worker's).
- Under the marker a worker sends `rowBatch.tids`, index-aligned with
  `rows`. `queueTID` holds block, offset and validity.
  - Gather stamps the tid of each worker row onto its output slot. The
    leader's own rows are returned as the child produced them, so they
    already carry their tid.
  - Gather Merge keeps a tid per source (`curTID`, `pendingTIDs`). It
    stamps the tid on its output slot and on `keySlot`, the slot its
    merge keys are evaluated on (`evalSortKeyValueSlot`).
- `evalSortKeyValue` is now a Row wrapper over `evalSortKeyValueSlot`.
- `markSlabSorts` hands an `OpAdapter` subtree to `markSortWantCTIDs`.
- Without the marker nothing changes: no per-row tid work and no extra
  allocation.

## Verification

- `TestCTIDSurvivesGather` (`internal/executor/parallel_ctid_test.go`):
  - forces parallel plans (`parallel_setup_cost = 0`,
    `parallel_tuple_cost = 0`, `min_parallel_table_scan_size = 0`) and
    asserts the plan contains the expected gather;
  - runs each query through `Build` and `BuildFastIterator`;
  - expected values are PG 18.3's answers on the same table:
    - `count(DISTINCT ctid)` over Gather Merge returns 3000;
    - `ctid … ORDER BY a` over Gather Merge returns `(142,6)`, `(285,5)`,
      `(428,4)`;
    - `ctid` over Gather returns the same three tids;
  - five of the six runs fail at HEAD. The sixth (Gather, legacy builder)
    passes there only when every matching row falls to the leader.
- Throwaway server, 200000 rows: the count is 200000 and every projected
  ctid matches PG.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24 MATCH;
  - sf025 96/96 with no verdict change;
  - ea-ratchet PASS.
- Regress A/B over 14 files against a HEAD binary, run symmetrically
  with `--port`:
  - 13 byte-identical, including select_parallel, write_parallel, tidscan
    and tidrangescan;
  - plpgsql moved only within its known `f1` overload flap.

## Not covered

- `ORDER BY ctid` is still unsorted. `sortOp.sortKeyVals` evaluates its
  keys on a bare Row, so a `ctid` key reads NULL and the sort keeps input
  order. This is M0146-0052, and the same `evalSortKeyValueSlot` over the
  materialised slot fixes it.
- A Sort that spills drops its tid side channel (`ctidsDisabled`). That
  is pre-existing and unchanged.
- The marker reaches a gather only along a single-child pass-through
  spine. A consumer that does not call it, such as a join qual on `ctid`
  above a Gather, still reads NULL for worker rows.
- goopg does not project ctid into the scan's output as PG does, so
  `EXPLAIN VERBOSE` shows no `Output: ctid` on the scan.
