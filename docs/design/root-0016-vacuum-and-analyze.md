# 0016 — VACUUM and ANALYZE (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 5 closes with a minimal `VACUUM` and `ANALYZE`. Without them
`pgbench -i` cannot finish — the loader emits `vacuum analyze
pgbench_*` after the COPY — and the steady-state pgbench workload
generates dead tuples on every UPDATE that must eventually be
reclaimed. The MVCC tuple header (0007), buffer manager (0005), heap
tuple format (0006), and B-tree (0009) are all in place; v0 vacuum is
the bridge between "data correctness via xmin/xmax" and "space reclaim
under sustained churn".

References into upstream:

- `postgres/src/backend/access/heap/vacuumlazy.c` — `lazy_scan_heap`,
  `lazy_vacuum_one_index`, retail tuple removal driven by an oldest-xmin
  horizon.
- `postgres/src/backend/access/heap/pruneheap.c` — `heap_page_prune`,
  the per-page LP_DEAD/LP_UNUSED transition that v0 mirrors closely.
- `postgres/src/backend/commands/analyze.c` — `do_analyze_rel`,
  per-column sampling and pg_statistic write-back.
- `postgres/src/backend/storage/freespace/freespace.c` — FSM updates
  on freed space (deferred for v0).

## Decision

### Scope of v0

The minimum that lets pgbench survive a steady-state run:

1. **Heap-page-level VACUUM.** For each block in the heap relation:
   read it, identify dead tuples (xmax committed and below the
   server-wide oldest xmin), mark their line pointers `LP_UNUSED`,
   and compact survivors against `pd_special`. Slot numbers are
   preserved so external pointers (B-tree leaf items, future ctids)
   remain valid.
2. **No index cleanup.** v0 B-tree (0009) does not support page
   deletion or in-leaf entry removal. The bridge is a `REINDEX`-style
   rebuild path (see "Index cleanup" below) that scans the heap and
   re-inserts every surviving tuple. Until then, vacuum'd index
   entries become "dangling" — they point at LP_UNUSED slots, which
   the access-method scan path treats as miss.
3. **Full-scan ANALYZE, no sampling.** v0 walks the heap in full and
   returns `(RowCount, AvgWidth, NumPages)` for each relation. Real
   sampling (`vacuumlazy.c`'s reservoir method) and per-column
   histograms wait on the catalog work in milestone 6.
4. **No FSM/visibility map.** Free-space tracking and all-visible bits
   are not maintained. INSERT continues to walk `pd_lower < pd_upper`
   on the last block; if it doesn't fit, extend.
5. **Single-pass, in-process.** v0 vacuum is invoked by an in-Go
   `vacuum.Vacuum(pool, mvccMgr, rel)` call. Wiring it to the
   `VACUUM` SQL statement happens in milestone 6 alongside the parser.

Out of scope:

- Concurrent vacuum (autovacuum, parallel workers).
- `VACUUM FULL` rewrite-the-whole-relation mode.
- Heap-only-tuple (HOT) chains. v0 doesn't emit HOT updates yet.
- pg_class / pg_statistic persistence — ANALYZE returns stats; the
  catalog will store them once it exists.
- Page-level all-visible bits and index-only scans.

### Oldest xmin horizon

A tuple is reclaimable when its `xmax` is committed and lower than the
horizon below which no current or future snapshot can see anything.
Under v0's MVCC model (`internal/mvcc`):

- A transaction is in-progress iff it lives in `Manager.active`.
- A snapshot's `Xmin` is the lowest active xid (or `nextXID` if none
  are active).
- Anything < `Xmin` is committed and *visible* to every current
  snapshot — so its xmax can never be observed as in-progress.

We therefore expose `Manager.OldestXmin()` returning the same value
that `captureSnapshotLocked` would assign to `Snapshot.Xmin`. A tuple
is dead when `xmax != 0 && xmax < oldestXmin`.

This is the v0 equivalent of upstream's
`GetOldestNonRemovableTransactionId` (`procarray.c`); upstream also
considers replication slots and prepared transactions, neither of which
exist in goopg yet.

### Page-level prune algorithm

```
VacuumHeapPage(p, oldestXmin):
    for slot in 0..PageLinePointerCount(p):
        item = readItemID(p, slot)
        if item.Flags != LP_NORMAL:
            continue                      # already unused/dead/redirect
        body = p[item.Offset : item.Offset + item.Length]
        tuple = ParseHeapTuple(body)
        if tuple.Xmax != 0 && tuple.Xmax < oldestXmin:
            writeItemID(p, slot, ItemID{Flags: LP_UNUSED})
            dead++
            continue
        live = append(live, {slot, item, copyOfBody})
    # Compact: zero the tuple region, re-pack live tuples down from
    # pd_special, rewriting line pointer offsets.
    upper = pd_special
    zero p[lower..pd_special)
    for each live (in any order):
        upper -= len(body)
        copy(p[upper:], body)
        item.Offset = upper
        writeItemID(p, slot, item)
    pd_upper = upper
    return {Dead, Live}
```

The line-pointer array is **not** truncated. A reclaimed slot stays in
the array as `LP_UNUSED` — future inserts can reuse it, and we never
shift slot numbers under live ItemPointer references.

### Index cleanup

Until B-tree v0 grows page deletion + entry removal (deferred —
0009 design doc § "Crash recovery"), `vacuum.Reindex(pool, mvccMgr,
heap, index, encoder)` rebuilds an index by:

1. Truncating the index relation to zero pages via `Manager.Truncate`.
2. Calling `btree.Create` to lay down a fresh metapage + root.
3. Sequentially scanning every heap page; for each LP_NORMAL slot that
   is currently visible to a fresh snapshot, calling
   `bt.Insert(encoder(tuple), ItemPointer{block, slot})`.

`Reindex` is the v0 stand-in for "delete-then-vacuum" on the index
side — pgbench tolerates running `REINDEX TABLE pgbench_*` between
runs as long as the heap remains usable mid-run.

### ANALYZE v0

```
Analyze(pool, rel):
    rows = 0
    bytes = 0
    pages = relationPageCount(rel)
    for blk in 0..pages:
        for each LP_NORMAL slot in block:
            tuple = ParseHeapTuple(slot)
            if visible to fresh snapshot:
                rows++
                bytes += len(tuple.Data) + Hoff
    return AnalyzeStats{
        Rows: rows, AvgWidth: bytes/max(rows,1), Pages: pages,
    }
```

We use a fresh snapshot rather than `oldestXmin` because ANALYZE wants
*currently-live* row count, not the never-removable-by-anyone count.
The two diverge briefly during a long-running transaction; the
`reltuples` field upstream chooses the same definition.

### Concurrency

VACUUM and ANALYZE both pin pages through the buffer pool. The buffer
manager already serialises content access at the slot-mutex level, so
concurrent readers see either the pre-vacuum or post-vacuum page. No
relation lock is taken for v0; pgbench drives one vacuum at a time
between pgbench runs in the typical test setup.

### Crash recovery

VACUUM modifies pages and dirties them; the WAL writer's page-LSN
ordering (0008) ensures durability of pruned pages. v0 does not emit
per-record `xl_heap_clean` records — a redo of a torn vacuum'd page
falls back to "REINDEX rebuilds the index, vacuum reruns on next
invocation" — same shape as the B-tree's recovery story (0009).

### What this doc does NOT cover

- VACUUM FULL / CLUSTER (rewrite to a new heap).
- Per-column statistics, MCV, histograms.
- Visibility map maintenance.
- Autovacuum scheduling / launcher / workers.
- HOT prune chain compaction.

## Alternatives Considered

- **Skip vacuum for v0; lean on REINDEX + period-of-quiet between
  pgbench runs.** Rejected: pgbench's UPDATE-heavy steady state grows
  the heap unbounded, and the requirements (.ralph/specs §) call out
  `VACUUM`/`ANALYZE` as part of the SQL surface for milestone 6.
  Building a minimal vacuum now is cheaper than discovering the gap
  during the parser work.
- **Truncate the line-pointer array on prune.** Rejected: it would
  invalidate every external ItemPointer (B-tree leaf entries, future
  ctids) and force a synchronous index rebuild on every page prune.
  Upstream's LP_UNUSED-with-stable-slot scheme exists for the same
  reason.
- **Sample-based ANALYZE for v0.** Rejected: pgbench tables are
  small enough at default scale (sf=1 → 100k accounts) that the full
  scan completes in <50ms per relation. The reservoir-sampling code
  is non-trivial and pays off once relations are 100k+ pages.

## Consequences

- Heap relations stop growing under steady-state UPDATE/DELETE
  workloads; `pgbench` -T 60 doesn't bloat to multi-GB.
- Index relations *do* grow until REINDEX runs. v0 includes
  `vacuum.Reindex` as the workaround until B-tree page deletion lands.
- ANALYZE results are unused until the catalog ships in milestone 6;
  the function exists so it can be wired up without redesign.
