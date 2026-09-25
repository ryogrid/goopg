# M0145-0008k recon: why count(*) is slower than sum() on goopg, and whether a pin-held slot is sound

Status: **RECON COMPLETE 2026-09-25**. No production code changed (C1). Task:
`.ralph/fix_plan.md` M0145-0008k (Kind: recon, Parent: M0145-0008f). Evidence:
`analysis/m0145/m0145-0008k/` (timings, pprof tops). Filed:
**M0145-0008p** (zero-consumer deform) and **M0145-0008q** (cleanup-lock
discipline, the prerequisite for a pin-held slot).

## 1. The count(*) > sum() inversion

TPC-H SF1 lineitem, private clone, serial (`max_parallel_workers_per_gather=0`):

| query | goopg | PG 18.3 |
|---|---|---|
| `count(*)` | ~4.8 s | 0.96 s |
| `sum(l_quantity)` | ~3.2 s | 1.40 s |
| `count(l_quantity)` | ~3.2 s | — |

The plans are identical: `Aggregate -> Seq Scan on lineitem`, both
`width=550`. The CPU profiles differ in one place:
- `count(*)` spends 5.5 s cumulative in `seqScanOp.decodeScanRow` (full-row
  deform) and 4.1 s in `cloneRowOwned` (full-row copy).
- `sum(l_quantity)` goes through `decodeScanRowRange` and
  `cloneRowOwnedPrefix`, deforming and copying only columns 0..4.

**Cause: a sentinel collision in the EX1 deform bound**
(`internal/executor/scan_deform.go`).
- `deformBoundBelow`'s Aggregate arm drops the ancestor bound and restarts
  from `deformBoundNone`, then folds its arms (group keys, aggregate
  arguments, filters, passthroughs). `count(*)` has no arms, so the bound
  stays `deformBoundNone`.
- `effectiveDeformBound` maps `deformBoundNone` to **full width**. That is
  the right answer at the plan root, where a bare scan's rows go to the
  client, and the wrong one here.
- So "this consumer reads zero columns" and "nothing has been folded yet"
  share one value. The only query that reads nothing gets the widest
  deform.

PG deforms nothing for `count(*)`. The aggregate never touches the slot, so
`slot_getsomeattrs` is never called (`execTuples.c`), and `heapgettup`
hands a buffer tuple without copying it.

**Probe (not committed).** Threading bound 0 from a zero-arm Aggregate (deform
column 0 only):
- serial: `count(*)` 4.8 s → **1.55 s**;
- parallel: 1.76 s → **0.61 s**;
- the result is unchanged. `sum` is unchanged.

Filed as **M0145-0008p**:
- give the bound algebra a distinct "zero columns consumed" value that the
  Aggregate arm returns when no arm folds a reference;
- make the scan's survivor window 0 (skip deform and the retention clone,
  keeping visibility and the prefilter path);
- check the sibling leaves (index and bitmap `deformBound` stamps) and the
  parallel worker closure.

## 2. Is a pin-held slot sound under goopg's page mutation paths?

PG hands the parent a slot pointing into a pinned shared buffer
(`ExecStoreBufferHeapTuple`). That is safe because nothing may **move**
tuple bytes on a page someone else has pinned. Every compaction takes a
cleanup lock, which is an exclusive content lock held while the pin count
is 1:
- opportunistic prune: `heap_page_prune_opt` →
  `ConditionalLockBufferForCleanup`
  (`postgres/src/backend/access/heap/pruneheap.c:245`); it skips the page
  if anyone else holds a pin;
- VACUUM: `lazy_scan_heap` takes `ConditionalLockBufferForCleanup` /
  `LockBufferForCleanup` (`postgres/src/backend/access/heap/vacuumlazy.c:1343`,
  `:1373`) before pruning;
- hint-bit setting touches only tuple headers under a share lock, never the
  data area a decoded slot reads.

goopg compacts pages under the **exclusive content lock alone**, with no
pin-count check, on three paths:
- the opportunistic prune in the HOT-update path
  (`internal/executor/operators_storage.go`, `storage.PagePruneOpt` →
  `VacuumHeapPageBySlots`);
- VACUUM (`internal/commands/vacuum/vacuum.go`, `storage.PageVacuumPrune`);
- the index-only scan's on-access prune
  (`internal/executor/operators_indexonly.go`, `storage.PageVacuumPrune`).

The buffer pool does count pins (`bufpool.go` `statePin`), but no
compaction consults the count.

**Verdict: a pin-held slot is unsound today.** A scan that released its
content lock but kept its pin could have the tuple it handed upward
compacted to another offset mid-read. That is why the scan clones at its
retention boundary. The prerequisite is PG's cleanup-lock discipline: a
`LockBufferForCleanup` / `ConditionalLockBufferForCleanup` pair on the
buffer pool, and every compaction site takes it (the opportunistic one
conditionally, skipping the page like PG). Filed as **M0145-0008q**. The
zero-copy slot itself is a follow-up once 0008q lands. The clone then
happens only when a consumer retains the row past the page (Sort,
Hash build, Material), which is PG's `ExecCopySlot` / `ExecMaterializeSlot`
point.

Movement: none (recon).
