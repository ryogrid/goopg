# B-tree index bloat: reclaiming dead entries without defeating deduplication

Status: **COMPLETE — root cause corrected mid-investigation; pkey growth now 0 B/txn, matching PostgreSQL**

> **The initial hypothesis in this document was wrong, and §2a records why.** The
> pkey growth is *not* dominated by unreclaimed dead entries; it is split
> amplification caused by packing bulk-built leaves to 100%. Sections 1–7 are
> preserved as the investigation record; §2a, §7a and §9 carry the corrected
> understanding and the measured outcome.
Owner doc for candidate **G** of
[`../perf-optimize-take3/07-remaining-candidates.md`](../perf-optimize-take3/07-remaining-candidates.md).

- Base commit: `7122d3d2b` (branch `perf-opt-take3`)
- Oracle: PostgreSQL 18.3 under `./postgres/` (read-only)

## 1. The problem

Under `pgbench -N` at scale 100, c=50:

| relation | goopg | PostgreSQL 18.3 |
|---|---:|---:|
| `pgbench_accounts_pkey` | **+202.6 MB (+104.3 B/txn)** | **+0 B (0.0 B/txn)** |
| `pgbench_accounts` (heap) | +9.7 MB (4.98 B/txn) | +0.8 MB (0.36 B/txn) |
| `pgbench_history` | +101.3 MB (52.18 B/txn) | +112.9 MB (52.30 B/txn) |

`pgbench_history` grows at the same rate on both engines, so the heap layout is
right; `abalance` is not indexed, so a correct engine need not touch the pkey at
all. goopg's primary key roughly doubles over a two-minute run and keeps
growing. This is a space and long-run-stability defect, not a throughput one at
this horizon.

## 2. Root cause (traced, not inferred from the symptom)

### 2.1 Where the entries come from

`abalance` is unindexed, so an update that stays on its heap page is HOT and
inserts no index entry (`tryApplyHOTUpdate`). When the heap page has no room the
new version lands elsewhere, the update is **non-HOT**, and a new pkey entry is
inserted for the same `aid` pointing at the new TID. The old entry survives,
pointing at a now-dead tuple version.

### 2.2 Why the dead entries are never reclaimed

> Accurate as a description of the code, but **not** the dominant cause of the
> growth — see §2a.


goopg represents a dead index entry as an `ItemIDDead` line-pointer flag plus
the `BTHasGarbage` opaque hint (`btree.go:76-80`, `pgpage.go:126`). The complete
lifecycle today is:

| stage | code | reality |
|---|---|---|
| **set** | `BTree.KillItems` (`lpdead_kill.go:31`) | only from the **read-path** index scan; explicitly skips posting lists |
| **consumed** | index scans skip dead slots | a read hint only |
| **cleared** | `VacuumIndexPages` (`btree_vacuum.go:163`) | full VACUUM only |

**There is no pre-split dead-item removal and no heap-verified deletion.** Dead
entries therefore accumulate until a full VACUUM runs, and every page that fills
up splits — permanently doubling the index — rather than first reclaiming the
dead entries it already holds.

### 2a. CORRECTED root cause: split amplification from 100%-packed leaves

The dead-entry hypothesis was tested directly and **refuted**. With the purge
implemented and wired, instrumentation on a live `pgbench -N` run reported:

```
PURGEDIAG calls 4800  tids 1953194  dead 0
PURGEDIAG2 oldestXmin 4696  lpflags 1 (ItemIDNormal)  dead false
```

**4,800 purge invocations, 1.95 M index entries examined, ZERO classified
dead.** `oldestXmin` was advancing normally (506 → 4,696), and the sampled
entries pointed at `ItemIDNormal` — *live* — heap tuples. The pages being split
are full of live entries, not garbage.

The arithmetic then identifies the real mechanism exactly:

| quantity | value |
|---|---:|
| splits observed | 4,800 in 40 s at 6,256 tps (250,240 txns) |
| splits per transaction | 0.0192 |
| × 8 KB per new page | **157.1 B/txn** |
| **measured pkey growth** | **152.3 B/txn** |

The growth *is* the split rate times the page size. And the reason nearly every
leaf splits is that goopg's bulk index build packs leaves until they are
physically full — `bulkload.go` flushed a page only when
`free < itemIDSize + MaxAlign(len(raw)) + separatorReserve`, i.e. at ~100%
occupancy. An index built by `CREATE INDEX` / `pgbench -i` therefore has **no
room for a single later insert**, so the first insert into any leaf splits it.
That is why the index doubles under `-N` and then stops: every page splits once.

goopg had **no fill factor at all** (`grep -rn fillfactor internal/access/nbtree`
returned nothing). Upstream defines `BTREE_DEFAULT_FILLFACTOR = 90`
(`postgres/src/include/access/nbtree.h:201`) and applies it in `_bt_buildadd` via
`BTGetTargetPageFreeSpace`, leaving 10% of every bulk-built leaf free. **That,
not dead-entry reclamation, is why PostgreSQL's pkey grows 0 B/txn on this
workload.**

### 2.3 What PostgreSQL does instead

Upstream reclaims on the insert path, before ever splitting, in
`_bt_delete_or_dedup_one_page` (`postgres/src/backend/access/nbtree/nbtinsert.c:2683`).
The ordering is the whole point:

1. Collect every `ItemIdIsDead` offset and, if there are any, delete them
   (`_bt_delete_itemids`) — the page now has room and **does not split**.
2. Otherwise try `_bt_simpledel_pass` (`nbtinsert.c:2812`) — *bottom-up index
   deletion*, which visits the **heap** to discover which TIDs are dead. It needs
   no LP_DEAD hints at all.
3. Otherwise try `_bt_dedup_pass` (`nbtdedup.c:58`).
4. Only if none of those freed enough space does the page split.

Deletion and deduplication are **sequenced**, never competing. That is why PG's
pkey grows 0 bytes on this workload.

## 3. Why the previous LP_DEAD attempt failed

The reverted work is `bdaa325a4` → `4998c81b9` (2026-07-14). It migrated the
read-path kill collector onto the UPDATE probe so `updateViaIndex` would mark
dead-pointing entries as it located rows.

**First, a correction to the record.** The "~18×" that got it reverted was
**index growth, not throughput**. The revert message states throughput was
neutral — *14,766 vs 14,882 tps*. Two distinct results:

| workload | outcome |
|---|---|
| uniform `pgbench -N`, 600 s soak | pkey doubled **byte-for-byte identically** to baseline (166.5 → 333.4 MB) — **zero benefit** |
| re-probe-heavy (1000 hot keys, c=1) | fixed pkey **+24.5 MB** vs baseline **+1.3 MB** — **~18× worse space** |
| throughput | **neutral** (14,766 vs 14,882 tps) |

**Why zero benefit on uniform `-N`:** each `aid` is touched ~0.4× over 10 M
rows, so a dead-pointing entry is almost never re-probed before its leaf splits.
On-probe kills essentially never fire. The mechanism was aimed at a re-probe
pattern the benchmark does not have.

**Why it actively hurt when re-probes did happen:** `KillItems` refuses to touch
a posting list —

```go
if isPostingRaw(raw) {
    continue // O-C3-1: posting kills deferred
}
```

— because a posting list has one line pointer for many TIDs and no per-TID dead
bit, so PG only marks a posting tuple dead when *all* its TIDs are dead. Mean-
while goopg's deduplicator (`deduplicateToRawItemsWithSpans`, `bulkload.go:602`)
merges runs purely on key equality and has **no notion of deadness whatsoever**.

The two mechanisms therefore cannot cooperate, in either direction:

- entries that consolidate into a posting become permanently unkillable by the
  kill path, and
- a dead mark that does exist is silently absorbed and lost the moment its run is
  merged into a posting by the next page rewrite.

For duplicate-heavy hot-key churn — exactly the re-probe workload — dedup is the
strategy that actually wins, so hint-based killing both failed to reclaim and
disturbed the consolidation that was doing the real work. **This is inherent to
the design, which is why the approach is abandoned rather than tuned.**

## 4. Constraints

1. **Do not resurrect the reverted mechanism.** No LP_DEAD marking on the hot
   path, on the UPDATE probe or anywhere else.
2. **Do not sacrifice deduplication.** Posting-list consolidation must remain
   fully effective; it is measurably the stronger space strategy for duplicates.
3. **No significant per-transaction overhead.** Reclamation must not run in the
   statement hot path.
4. Preserve correctness: an entry may be removed only when its heap tuple is
   dead to *all* transactions, and the index must remain a faithful PG-format
   B-tree (a real PostgreSQL must still be able to read it).
5. No benchmark-specific special-casing.

## 5. Success criteria

| criterion | target |
|---|---|
| `pgbench_accounts_pkey` growth under `-N` | substantially reduced; ideally approaching PG's 0 B/txn |
| deduplication | posting lists still formed; a dedup-effectiveness test must pass |
| throughput | no significant regression on `-S` or `-N`; **nothing remotely like an 18× space regression on a re-probe workload** |
| correctness | units, race-gate, TPC-H spot-check, parser goldens all green |

## 6. Candidate approaches

### A. Dedup-aware purge at page rewrite (**selected**)

goopg already rewrites an entire leaf through `refillDeduplicated`
(`btree.go:3480`) on refill (`:2846`) and on both halves of a split
(`:2980`, `:2986`), and that function is the *only* place posting lists are
formed. Adding a purge step **inside that rewrite, before the dedup merge**,
gives PG's delete-then-dedup ordering for free:

```
pageItems (expanded, one entry per TID)
   │
   ├─ NEW: drop entries whose heap tuple is dead to all      ← purge
   │
   └─ deduplicateToRawItemsWithSpans  (merges surviving runs) ← dedup
```

Why this cannot fight deduplication: the purge operates on the **expanded**
item list, before any run is merged. Dead TIDs are gone before
`deduplicateToRawItemsWithSpans` ever sees them, so postings are formed over
surviving live TIDs only. There is no dead bit to lose, no posting to refuse to
touch, and no competition — this is structurally the same sequencing PG uses.

It is also *heap-verified*, like `_bt_simpledel_pass`, so it needs no LP_DEAD
hints and works on the uniform `-N` pattern where on-probe kills never fired.

Cost is confined to the split/refill path, which is cold relative to the
statement path, and the page's items are already in memory there.

**Known design problems to solve** (see TODO):

- *Layering*: the heap-visibility oracle (`heapChainDeadToAll`,
  `internal/executor/operators_index.go:127`, and `storage.TupleDeadToAll`) lives
  above `internal/access/nbtree`. The purge needs an injected callback, in the
  style of the existing `Pool.LogBtreeVacuum` / `WALFrontier` hooks, not an
  import cycle.
- *Cost bound*: a rewrite must not fault in an unbounded number of heap pages.
  Needs a budget and/or a "only when the page is about to split" gate.
- *WAL/recovery*: the rewrite is already WAL-logged as a page image; removing
  items must not break replay or `pg_waldump`/standby parity.
- *Correctness*: `OldestXmin` must be conservative, and a purge must never
  remove an entry a concurrent scan could still need.

### B. Background B-tree vacuum

Run `VacuumIndexPages` (`btree_vacuum.go:33`, already implemented and WAL-logged)
periodically from a background goroutine over TIDs known dead.

Rejected as the *primary* fix, kept as a possible complement:

- it does not prevent the split that causes the growth — it reclaims after the
  damage, so the index still doubles and then has to shrink;
- it needs a source of dead TIDs (a heap scan) and so re-implements much of
  VACUUM;
- it adds a scheduler, contention and memory that criterion 3 asks to avoid;
- it duplicates work autovacuum should be doing.

### C. PG-faithful `_bt_delete_or_dedup_one_page` port

The most upstream-faithful option, but it presumes a populated supply of
`ItemIdIsDead` hints, which goopg does not have (§2.2) — its only producer is the
read path and it skips postings. Approach A is the same *ordering* without the
dependency on hints, so A is a strict subset of the work with none of the
hint-supply problem. Revisit if a hint producer ever lands.

## 7. Investigation record

Established by reading code, not inferred:

- Dedup is unconditional in goopg and happens only in `refillDeduplicated`
  (`btree.go:3480`), unlike PG's lazy, split-triggered `_bt_dedup_pass`.
  `initMetaPage` (`btree.go:2073`) documents the unconditional choice and the
  `allequalimage` consequence.
- `KillItems` skips posting lists (`lpdead_kill.go`, the `isPostingRaw` guard).
- `deduplicateToRawItemsWithSpans` merges on key equality only, with no
  dead-entry awareness (`bulkload.go:602-640`).
- `BTHasGarbage` has exactly one consumer that clears it: `VacuumIndexPages`
  (`btree_vacuum.go:163`).
- Reusable primitives: `VacuumIndexPages`, `heapChainDeadToAll`,
  `storage.TupleDeadToAll`, `TxnMgr.OldestXmin()`, `refillDeduplicated`.
- The reverted commit targets the pre-move layout (`internal/access/btree`,
  `internal/mvcc`), so it cannot be cherry-picked onto HEAD for a three-way
  benchmark without re-porting it — which constraint 1 forbids. Its numbers are
  taken from the revert message instead and labelled as such.

## 7a. Second root cause found during implementation: a leaked block per dedup-recovery

The first end-to-end test of the purge **failed**: the index got *bigger* with
the purge enabled (134 vs 125 pages). Investigating rather than tuning the test
exposed a second, independent defect that had nothing to do with dead entries.

`insertIntoBlock` allocates the right sibling **before** it knows whether the
split is needed:

```go
rightSlot, rightBlk, err := bt.pinNewOrRecycled()   // btree.go:2767 — extends the relfile
...
allItems = dedupConsolidate(...)
if fits(allItems) {                                 // "dedup-recovery": no split after all
    resetPageItems(...); refillDeduplicated(...)
    rightSlot.Unlock(); bt.pool.Unpin(rightSlot)    // block abandoned, never freed
```

The abandoned block was neither linked into the tree nor returned to the free
list, so **every dedup-recovery grew the relfile by one page even though the
page's logical content had just shrunk**. The purge made recovery succeed far
more often, which is why enabling it made the file grow faster — the purge was
working, and each success leaked a block.

Fix: `bt.recycleBlock(rightBlk)` on both recovery exits (the block is unlinked
and unreachable, so returning it to the free list is trivially safe).

Measured on the synthetic duplicate-heavy test (`TestPurgeReclaimsIndexGrowth`):

| configuration | index pages |
|---|---:|
| before the leak fix, purge off | 125 |
| before the leak fix, purge on | 134 (worse — the leak, amplified) |
| **after the leak fix, purge off** | **11** |
| **after the leak fix, purge on** | **3** |

So the two causes are independent and both real: the leak dominated this
workload, and the dead-entry purge gives a further 3.7×. This is why the first
result had to be investigated instead of explained away.

## 8. Plan

1. Reproduce the growth in a deterministic test (no server needed).
2. Add the injected dead-TID oracle to the btree, defaulting to nil (no-op).
3. Purge inside the rewrite, before dedup; bound the heap I/O.
4. Tests: growth reclaimed, dedup still effective, correctness under concurrency.
5. Benchmark `-N` growth + `-S`/`-N` throughput against baseline.
6. Update this doc with the measured outcome, including any negative result.

## 9. Final implementation and measured outcome

Three changes landed. They are independent, and the investigation order matters:
the first two were built against the original hypothesis, the third is the
root-cause fix that the failed measurement of the first two exposed.

### 9.1 What was implemented

| # | change | file(s) | why |
|---|---|---|---|
| 1 | **Leaf fill factor** (the fix) | `nbtree/bulkload.go` — `BTreeDefaultFillFactor = 90`, `leafFillFactorReserve` | Bulk-built leaves keep 10% free, so a later insert does not have to split the page. Upstream parity (`BTREE_DEFAULT_FILLFACTOR`, `_bt_buildadd`). |
| 2 | **Right-page leak fix** | `nbtree/btree.go` — `bt.recycleBlock(rightBlk)` on both dedup-recovery exits | The split path extends the relfile *before* deciding to split; abandoning the split leaked that block forever. §7a. |
| 3 | **Dedup-aware dead-entry purge** | `nbtree/dead_purge.go`, `executor/pgindex_dead_purge.go`, `Options.DeadTIDs` | Heap-verified reclamation on the split path, ordered *before* dedup so it cannot fight posting-list consolidation. Regime-specific — see 9.3. |

### 9.2 Measured result — the target workload

`pgbench -N`, scale 100, c=50, 120 s, fresh cluster and fresh load per arm:

| build | pkey after load | pkey growth | B/txn | tps |
|---|---:|---:|---:|---:|
| baseline `7122d3d2b` | 202.3 MB | +202.5 MB | 152.3 | 11,085 |
| + leak fix + purge | 202.3 MB | +202.5 MB | 151.1 | 11,169 |
| **+ fill factor** | **225.0 MB** | **0 B** | **0.0** | **11,583** |
| *PostgreSQL 18.3* | — | *0 B* | *0.0* | *11,994* |

**pkey growth is now 0 B/txn — exactly PostgreSQL's behaviour.** Throughput did
not regress (it measured slightly higher, within `-N`'s ~8% noise floor).

The trade is the one upstream already makes: the index is **11.2% larger at
build time** (202.3 → 225.0 MB) because 10% of every leaf is reserved. After
only two minutes of `-N` the new build is already **44% smaller** in absolute
terms (225 MB vs 404 MB), and unlike the baseline it has stopped growing.

### 9.3 Honest scope of the purge (change 3)

On this workload the purge **does nothing**: 1.95 M entries examined, zero dead
(§2a). It is retained because it is correct, off the hot path, and demonstrably
effective in the regime it was designed for — a duplicate-heavy synthetic
workload where it takes the index from 11 pages to 3 (`TestPurgeReclaimsIndexGrowth`)
— and because with the fill factor in place splits are now rare, so its cost is
correspondingly rare. It is **not** claimed as the fix for pgbench, and anyone
tuning this area should read §2a before assuming dead entries are the problem.

### 9.4 Why this preserves deduplication

Deduplication is untouched by changes 1 and 2. Change 3 cannot fight it by
construction: it filters the **expanded** item list (one entry per heap TID)
*before* `dedupConsolidate` merges runs, so dead TIDs are gone before any
posting list is formed and survivors are fed straight back through the merger.
There is no dead bit for a posting to lose and no posting for the purge to
refuse. `TestPurgePreservesDeduplication` pins this: after purging half the
entries the survivors still form posting lists and never require *more* line
pointers than the unpurged page — the precise failure mode that reverted the
LP_DEAD approach.

### 9.5 Limitations and follow-up

- **`fillfactor` is not yet a storage parameter.** Upstream exposes it per index
  (`WITH (fillfactor = ...)`); goopg hard-codes 90 for bulk-built leaves. Making
  it a reloption is the natural follow-up.
- **Internal levels are unaffected.** Upstream also applies
  `BTREE_NONLEAF_FILLFACTOR = 70` to non-leaf levels during build; this change
  deliberately touches leaves only, where the split amplification is.
- **Incremental inserts still pack to 100%.** The fill factor applies to the
  bulk build path. A tree grown entirely by `INSERT` has the same
  first-insert-splits property on its own pages; upstream has the same
  characteristic, so this is parity rather than a gap.
- **The purge finds nothing on uniform update workloads** (§9.3). If dead-entry
  reclamation is ever needed for real, the missing piece is a source of dead
  TIDs at split time — which is what upstream's `_bt_simpledel_pass` heuristics
  supply, and which this implementation approximates only crudely.
- **The purge costs one small closure per index open.** `indexBTreeOptions`
  builds the filter (a closure plus a `Catalog.RelFileNode` lookup) on every
  index open, which is per statement per index. Measured throughput did not
  regress (`-N` 11,085 → 11,169 tps with the purge, before the fill factor), but
  it is a real per-statement allocation in a codebase that has been removing
  them; if the purge is ever shown to be dead weight, this is the first reason
  to drop it.
- **Existing indexes keep their old density.** The fill factor applies at build
  time, so an index built before this change still doubles once. `REINDEX`
  rebuilds it with headroom.
