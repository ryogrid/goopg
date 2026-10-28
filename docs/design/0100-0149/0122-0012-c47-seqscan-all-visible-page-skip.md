# M0122-0012 C47 — the seq scan skips per-tuple visibility on an all-visible page

Status: slice 1 landed (2026-10-28, loop #233); **slice 2 blocked on P0-E15** (loop #234). Design written loop #232. Areas: `internal/commands/vacuum`
(sets the page bit), `internal/executor` (DML clears it, the seq scan reads it). Perf-only: plans, values and EXPLAIN
output unchanged. Parent: M0122-0012 (banner item 10). The Target is written in the M0122-0012 entry.

## Symptom

TPC-DS Q39 (census-20261028 #9, 502 ms over release PG). An instructions:u profile at `102c74a5e`
(`tmp/g3-232/q39.data`) puts `seqScanOp.Next` at 10.97 %. Two of its lines carry half of that:

- `operators_storage.go:2093`, the `PageGetHeapTupleNoCopy` call (26 %);
- `:2104`, the per-tuple `transam.TupleVisibleSubxact` call (22.6 %, about 2.5 % of Q39).

The visibility functions themselves add another ~1.9 %: `Manager.IsSubxact` 0.68 %, `isCurrentTxXID` 0.52 %,
`TupleVisibleSubxact` 0.45 %, `SubxactMap.IsSubxact` 0.23 %. In total, about 4.4 % of Q39 goes to deciding whether each
scanned tuple is visible.

PG does not do this work on these pages. `heap_prepare_pagescan` (heapam.c) computes
`all_visible = PageIsAllVisible(page) && !snapshot->takenDuringRecovery`, and `page_collect_tuples` then sets
`valid = true` for every normal line pointer without calling HeapTupleSatisfiesVisibility. HeapCheckForSerializableConflictOut
still runs when the scan needs serializable checks. The reference corpus is almost entirely all-visible
(`:65438` tpcds025: inventory 12730 / 12768 pages, store\_sales 12933 / 12936, date\_dim 1405 / 1424, item 1236 / 1284).

## The gap is in the page header, not only in the scan

goopg keeps the visibility map, but **never sets the heap page's `PD_ALL_VISIBLE` flag on the primary**:

- `vacuum.go` calls `VM.SetAllVisible` / `SetAllFrozen` when `storage.PageAllVisible(page, horizon)` holds, and stops
  there. PG's lazy\_scan\_prune / lazy\_scan\_new\_or\_empty (vacuumlazy.c) call `PageSetAllVisible(page)` and
  `MarkBufferDirty` before `visibilitymap_set`. heap\_multi\_insert with `HEAP_INSERT_FROZEN` sets it too (heapam.c
  `PageSetAllVisible` at the ALL\_FROZEN\_SET branch).
- The DML paths clear the VM bit (`ctx.VM.ClearBlockNoWAL` at about 12 executor sites: insert, update old/new
  block, delete, the TOAST writes, operators\_ddl's rewrite). PG's heap\_insert, heap\_multi\_insert, heap\_delete
  and heap\_update clear the page bit at the same point, while holding the buffer's exclusive lock:
  `if (PageIsAllVisible(page)) { all_visible_cleared = true; PageClearAllVisible(page); visibilitymap_clear(...) }`.
  heap\_lock\_tuple clears only ALL\_FROZEN in the VM, which goopg's `operators_lockrows.go` already mirrors.
- **Recovery already does it.** `recovery.go` sets `PDAllVisible` when it replays XLOG\_HEAP2\_VISIBLE and an
  ALL\_FROZEN\_SET multi-insert, and clears it for ALL\_VISIBLE\_CLEARED records. A goopg standby's pages therefore
  carry the bit while the primary's pages never do — a physical divergence on top of the missing scan skip.

Measured on the private TPC-DS clone `tmp/q64-clone`: every block of the four largest relations is all-visible in
the VM fork (21856 / 12936 / 12768 / 9416), and **0** of their heap pages has `PD_ALL_VISIBLE`.

## Design (three slices)

1. **Producer and invalidation (correctness first).**
   - VACUUM sets `PDAllVisible` on the page, and marks it dirty, wherever it now sets the VM's ALL\_VISIBLE or
     ALL\_FROZEN bit. The page bit is set before the VM bit, as PG orders it.
   - Every DML site that calls `VM.ClearBlock*` for a block first clears `PDAllVisible` on that page while it holds
     the page's write lock, and only when the bit was set (PageIsAllVisible then PageClearAllVisible). The WAL
     record's ALL\_VISIBLE\_CLEARED flag (already parsed by redo) is set from the page bit. The VM-only clear
     stays for a VM bit without its page bit (crash leftovers, the existing corpora).
   - VACUUM's `default:` arm (page no longer all-visible) clears the page bit along with the VM.
   - Test: a page set all-visible by VACUUM, then hit by each DML kind (insert, HOT and non-HOT update on both
     blocks, delete, TOAST insert), has the bit cleared. A VACUUM after the DML sets it again.
2. **Consumer (the perf slice).** `seqScanOp` reads `PDAllVisible` once per page under the page's read lock. When it
   is set and the snapshot is not a standby snapshot (goopg has no `takenDuringRecovery`; use the context's
   in-recovery state), each normal tuple counts as visible without `TupleVisibleSubxact`. Anything the visible path
   does after the check (CTID, decode, the serializable read registration) still runs. The invisible-tuple SSI arms
   (GiST, GIN, the relation-wide conflict-out) cannot trigger, because no tuple on an all-visible page is invisible
   to any snapshot, which is PG's argument for skipping. `seqScanAllVisibleDisabled` is the A/B and test knob.
   - Test: the same scans with the knob on and off over an all-visible table, after DML on some of its pages, and
     inside a transaction that has its own uncommitted rows on an all-visible page (those pages lose the bit at the
     write, so they take the per-tuple path).
3. **Corpora.** The shipped corpora carry VM bits but no page bits, and VACUUM skips VM-all-visible pages
   (`vacuum.go:171`), so a plain VACUUM never backfills them. Measurement uses a private copy of a clone, vacuumed
   so the page bit is set for every VM-all-visible page. The way to do that is VACUUM (DISABLE\_PAGE\_SKIPPING) if
   goopg supports it; otherwise a one-off pass. Plans on that copy must stay identical to the uncopied clone's.
   Bringing the shipped `:65436` / `:65437` / `:65433` corpora up to the bit is an owner-run rebuild or VACUUM
   decision. The loop escalates it; it never touches those clusters.

## Target and prediction

TPC-DS SF0.25 **Q39**, instructions:u per query (`tools/ds-instr.sh`, 3 runs × 2 alternating, fresh A/B) on the
vacuumed private copy. Predicted ~4 % (the 4.4 % sampled share above, less a per-page bit test). The same skip
applies to every seq scan over an all-visible page, so other top-10 queries (Q78, Q23, H Q1-style scans) should move
too.

## Risks

- A DML path that clears the VM but not the page bit would make a scan skip visibility on a page holding an
  uncommitted or deleted tuple: **wrong results**. Slice 1 must cover every `VM.ClearBlock*` caller, and its test
  must exercise each one. Any heap write that bypasses those callers is a hole; grep the `storage` heap writers for
  callers outside the executor.
- A concurrent writer must clear the bit before it releases the page's write lock, and the scan must read it under
  the read lock. PG gets the same guarantee from the buffer content lock.
- Redo already maintains the bit, so a standby's scans could use it. PG refuses for recovery snapshots
  (`takenDuringRecovery`) because a standby's VM and page bits can run ahead of its snapshot's horizon. Keep the
  skip off in recovery until that is reasoned through.

## Slice 1 — landed (loop #233)

The invalidation went into the storage layer rather than the ~13 executor sites, because every heap tuple add and
xmax stamp lands in `internal/storage/heap.go`. An executor path that forgot its VM clear would then still clear
the page bit.

- `storage.PageIsAllVisible` / `PageSetAllVisible` / `PageClearAllVisible` (page.go) are bufpage.h's macros.
- **Cleared:**
  - `PageAddHeapTuple`, on both success paths (heap\_insert, heap\_update's new page);
  - `PageSetHeapTupleXmax` (heap\_delete / heap\_update);
  - `PageSetHeapTupleMovedPartition`;
  - `PageStampHotOldTuple`, `PageStampUpdatedOldTuple`, `PageStampHotOldTupleMulti`;
  - `PageSetHeapTupleXmaxMulti`, only when the multi has an updater.
- **Kept:**
  - `PageSetHeapTupleLockOnly` and a lock-only multi (heap\_lock\_tuple keeps the bit);
  - the raw item writers, which serve index pages, redo and the two in-place catalog updates (heap\_inplace\_update
    keeps it);
  - the `PageApply*Redo` functions, where redo already applies ALL\_VISIBLE\_CLEARED.
- Every clear runs before the caller's WAL emission, so a full-page image shows the bit cleared, as in PG. The
  record's ALL\_VISIBLE\_CLEARED flag is still derived from the VM bit at log time (unchanged). VACUUM sets the page
  bit together with the VM bit, so the two agree.
- VACUUM: `setPageAllVisible` (vacuum.go) sets the bit and dirties the page in the ALL\_FROZEN and ALL\_VISIBLE
  arms. The not-all-visible arm clears it with the map.
- Tests:
  - `TestPageAllVisibleClearedByHeapChanges` (storage, every stamper);
  - `TestPageAllVisibleFollowsVM` (executor: VACUUM sets both bits; insert, HOT update, non-HOT update and delete
    clear the page bit with the VM bit; FOR UPDATE keeps both; VACUUM re-sets).
  - Mutation checks: dropping the delete-stamp clear fails both tests; dropping VACUUM's set fails the second.
- Gates: units, TestE2E\_Physical\* family, the pg\_waldump VM-cleared / vacuum-prune / crash-start testport tests,
  tpch-spotcheck, sf025 PASS=99 (plans 99/99 same).

**Finding (pre-existing, recorded in the ledger).** `storage.PageAllVisible` skips every non-normal line pointer,
LP\_DEAD included. PG's heap\_page\_prune\_and\_freeze refuses all-visible while any LP\_DEAD item remains
(`pruneheap.c`: `all_visible && lpdead_items == 0`) and sets the VM only after the second heap pass makes them unused.
In the executor fixture, a DELETE plus VACUUM leaves the VM all-visible with the LP\_DEAD item still on the page.
The seq scan never returns an LP\_DEAD item, so slice 2 is unaffected. An index-only scan that trusts the VM while
the dead item's index entry survives would be. The probe returned no row (the entry was gone or the plan was not
index-only), so wrong results are not demonstrated.

## Slice 2 — blocked on P0-E15 (loop #234)

The safety review before the scan skip asked whether goopg's VACUUM ever marks a page all-visible over a tuple that
some snapshot must not see. It does, for a rolled-back INSERT:

- `storage.PageAllVisible` requires only xmin < horizon, not a committed xmin. The executor fixture shows an aborted
  tuple left on a page that the VM (and now PD\_ALL\_VISIBLE) marks all-visible, and an Index Only Scan returns it.
- On a real server it is worse. `PageFreezeOldTuples` freezes the aborted xmin, and the freeze cutoff collapses to
  OldestXmin on a young cluster (uint32 wrap plus a non-circular clamp). After VACUUM even a plain Seq Scan counts the
  rolled-back row: 2001 against PG's 2000 (`analysis/p0e15/`).

A scan that trusts PD\_ALL\_VISIBLE would extend the Index Only Scan's wrong result to every seq scan, so slice 2
waits for P0-E15. Filed under M-NIGHTLY with an S2 escalation. P0-E15's fix makes PageAllVisible demand a committed
xmin, the prune remove aborted inserts and the freeze refuse uncommitted xmins. Slice 1 stays landed: nothing reads
the page bit yet, and redo already set it on standbys.
