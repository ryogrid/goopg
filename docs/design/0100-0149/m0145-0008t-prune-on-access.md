# M0145-0008t: a heap page is pruned when it is read, as PG does

Status: **LANDED 2026-09-25**. Task: `.ralph/fix_plan.md` M0145-0008t (Kind:
impl, Parent: M0145-0008q). Evidence: `analysis/m0145/m0145-0008t/`.

## What PG does

`heap_page_prune_opt` (`postgres/src/backend/access/heap/pruneheap.c`) runs
whenever a scan brings a heap page into use:
- `heap_prepare_pagescan` (seq scan);
- `heapam_index_fetch_tuple`, when the index fetch switches to a new heap
  buffer;
- the bitmap heap scan's page fetch.

It is skipped during recovery. It prunes only when both of these hold:
- `pd_prune_xid` is set and removable against the relation's horizon;
- the page is full, or its free space is below
  `Max(RelationGetTargetPageFreeSpace(fillfactor), BLCKSZ/10)`.

It then takes `ConditionalLockBufferForCleanup`, re-checks, and prunes. A
page someone else has pinned is left alone.

Before this slice goopg pruned only in two places: a HOT update that found
its page full, and VACUUM.

## What changed

- `storage.PagePruneXIDSet` and `storage.PagePruneOnAccessWanted` port the
  gate. The first is a cheap hint test made before the horizon is computed.
  The second checks the horizon (modular `XIDPrecedes`) and the free-space
  threshold, including the fillfactor target.
- `executor.pruneHeapPageOnAccess` is the helper:
  - it reads the hint under the share lock; only then computes the horizon
    and checks the gate;
  - it takes `Pool.ConditionalLockForCleanup` (M0145-0008q), re-checks under
    the lock, runs `storage.PagePruneOpt`, and WAL-logs through the HOT
    path's own `markHeapPruneOptDirty`;
  - it is skipped:
    - on a standby (`ctx.IsStandby`, PG's `RecoveryInProgress`);
    - when `enable_opportunistic_prune` is off;
    - for catalog relations (OID < 16384).
- Call sites, one per PG caller:
  - `seqScanOp`: after the pool pin of a new block. Ring-buffer pages are
    private copies and are not pruned.
  - `indexScanOp`: after the heap pin, once per heap-block switch
    (`prunedBlock`), since goopg pins per TID.
  - `bitmapHeapScanOp`: both page-pin sites (prefetched and direct).

## The premise that did not hold

The task was filed after pgbench's 2-row `pgbench_branches` grew to ~80
heap blocks in 30 s, on the theory that goopg never prunes on read. That is
true, but it does not cause the growth. **goopg's `PageAddHeapTuple` never
reuses an unused line pointer.** It always appends, and
`PageGetHeapFreeSpace` answers 0 once a page holds `MaxHeapTuplesPerPage`
(291) line pointers. However much a page is pruned, a row updated 291 times
fills its page's line-pointer array, and every later version goes to a new
page.

The pgbench A/B confirms it. Table growth is unchanged with the prune on
read:

| arm | branches | tellers | accounts |
|---|---|---|---|
| HEAD | 80, 83 | 85, 85 | 3335, 3335 |
| candidate | 80, 81 | 86, 87 | 3334, 3335 |

Throughput is within noise: 603 / 628 tps on HEAD against 607 / 614 on the
candidate.

Recycling isn't safe yet either. goopg's prune marks a dead **non-HOT**
tuple `LP_UNUSED` while index entries still point at it. PG marks it
`LP_DEAD`; only VACUUM sets `LP_UNUSED`, after index cleanup, and only then
may `PageAddItemExtended` reuse the slot (`PD_HAS_FREE_LINES`). Recycling
before that ordering exists would hand an old index entry a new, unrelated
tuple. The real fix is filed as **M0145-0008v**: the line-pointer lifecycle
(`LP_DEAD` on prune, `LP_UNUSED` after index vacuum, recycling,
`PageTruncateLinePointerArray`).

## Tests

- `TestPagePruneOnAccessWantedGate` (storage) pins the gate:
  - no hint means no prune, and a hint must precede the horizon;
  - the `BLCKSZ/10` floor applies;
  - the fillfactor target raises the threshold (1000 bytes free: kept at
    fillfactor 100, pruned at 50).
- `TestPruneOnAccessReclaimsDeadSpace` (executor) builds a page filled and
  then deleted down to one row, and runs `SELECT count(*)`:
  - with the GUC on, the read reclaims free space;
  - with the GUC off, or while another pin is held, the page is untouched.
- The existing HOT-prune tests still pass. Their UPDATE's index scan now
  prunes on the way in, before the HOT path looks for room.

## Measured

- **TPC-DS SF0.25 sweep, first candidate pass:** Q39 went 3.1 s → 5.4 s. The
  persistent gate cluster held pages with a stale `pd_prune_xid`, and the
  first read pruned and WAL-logged them. On the second pass Q39 took
  3.06 s. This is a one-time cost per stale page, which is PG's behaviour
  too.
- **Sweep totals:** 148 s (-0.7%) on the first pass and 154 s (+4.1%) on the
  second. Recent quiet sweeps took 141–148 s and noisy ones up to 200 s, so
  the second reading is within noise; no query moved on it.

## Gates (staged tree)

- units: PASS.
- `tpch-spotcheck`: PASS.
- acceptance arm: 24 MATCH.
- `tpcds-sf025`: 96/96, 99/99 shapes same, twice.
- Isolation family: only `ReadWriteUnique4` / `TemporalRangeIntegrity`,
  which also fail on HEAD.
- Full regress suite: only `portals_p2` / `union`, which also fail on HEAD
  (M0145-0008r / M0145-0008s).

Movement: none. No plan or value moved, and table growth is unchanged
because of the line-pointer ceiling above.

## Not ported (ledgered)

- Catalog relations are excluded. goopg writes catalog heaps through paths
  outside the executor.
- Ring-buffer (bulk-read) pages are not pruned. They are private copies in
  goopg; PG's ring holds real shared buffers.
- `pgstat_update_heap_dead_tuples` after an on-access prune.
- The line-pointer lifecycle, M0145-0008v.
