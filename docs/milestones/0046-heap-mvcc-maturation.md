# Milestone 0046 — Heap & MVCC maturation

**Status:** planned
**Depends on:** Milestone 0019 (autovacuum scaffolding), Milestone 0030 (catalog persistence — VM/FSM forks ride alongside the heap fork in the same RelFileNode), Milestone 0002 (durability — VM/FSM updates are WAL-logged).
**Drives:** Bring goopg's heap access method to parity with PostgreSQL's everyday-correctness primitives so that long-lived workloads do not bloat, do not exhaust XID space, and can run index-only scans. Specifically: HOT updates, opportunistic page pruning, Free Space Map, Visibility Map, tuple freezing & XID wraparound prevention, and TOAST out-of-line storage.

## 1. Context

`docs/reference/ref-007-heap-mvcc.md` enumerates six concrete differences between goopg's heap and the upstream implementation that all become real correctness or operability issues once a workload runs for more than a few hours:

| Gap | Symptom on goopg today |
|---|---|
| **No HOT** | Every UPDATE writes a new index entry per index, even when no indexed column changed. Indexes bloat, vacuum cost grows linearly with churn. |
| **No heap page pruning** | Dead tuples are reclaimed only when VACUUM runs. Pages grow until a full prune. Buffer cache is wasted on dead rows. |
| **No FSM** | Every INSERT extends the relation (`smgrextend`), even though earlier pages have free space. Hammers the smgr lock and drives unnecessary file growth. |
| **No Visibility Map** | Index-only scans are impossible. VACUUM has to scan every page even when nothing has changed. |
| **No tuple freezing / anti-wraparound** | XIDs are 32-bit; without `FrozenTransactionId` rewriting old `xmin`s, a long-lived deployment will eventually hit wraparound and lose data visibility. |
| **No TOAST** | Wide columns are stored inline. A row > 8 KB cannot be inserted at all (heap insert errors out). |

This milestone closes those gaps. It is structurally large but each piece is independent and lands behind its own design doc.

## 2. Required Design Docs

1. `docs/design/0046-0001-hot-updates.md` — Heap-Only Tuples. Detect "no indexed column changed", chain updated tuples on the same page via `t_ctid`, mark the redirect with `HEAP_HOT_UPDATED` / `HEAP_ONLY_TUPLE`, follow chains in seqscans / index fetches.
2. `docs/design/0046-0002-page-pruning.md` — Opportunistic page pruning during reads. Mirror upstream's `heap_page_prune_opt` / `heap_page_prune` based on the snapshot manager's oldest-xmin horizon. Wired into the buffer-pin / read path, not VACUUM.
3. `docs/design/0046-0003-free-space-map.md` — Per-relation FSM fork (`<relfilenode>_fsm`), tree-of-pages summarising free bytes per heap page, lookup integrated into the heap-insert target-page selection so we extend only when no existing page has space.
4. `docs/design/0046-0004-visibility-map.md` — Per-relation VM fork (`<relfilenode>_vm`) with `ALL_VISIBLE` and `ALL_FROZEN` bits per heap page. Index-only scans skip heap fetches when bit is set; VACUUM skips ALL_FROZEN pages on subsequent passes.
5. `docs/design/0046-0005-tuple-freezing-and-wraparound.md` — Replace ancient `xmin` with `FrozenTransactionId` during VACUUM. Track `relfrozenxid` per relation in the catalog. Compulsory anti-wraparound autovacuum at the freeze-age threshold (M0019 hooks into this).
6. `docs/design/0046-0006-toast.md` — Out-of-line storage for wide columns. TOAST relation auto-created per user table that has a TOAST-able type, varlena 1-byte / 4-byte length headers, in-place vs out-of-line decision based on page-fill threshold.

`0046-0001` and `0046-0002` are foundational and should land first; `0003`–`0006` are independent and can be parallelised once the prune helper is in place.

## 3. Definition of Done

### 3.1 HOT updates
- UPDATE that does not modify any indexed column produces a new heap tuple on the same page (when there is room) and **no** index entries.
- The redirect chain is followed by the index-fetch path (`heap_hot_search_buffer` equivalent in `internal/access/heap`).
- Regression test: pgbench-style UPDATE-heavy workload at 100k transactions shows index size growth ≤ 10% of pre-HOT baseline.

### 3.2 Page pruning
- `prune_page` runs from the buffer-pin path when `page_is_prunable(snap.OldestXmin)` returns true. No new VACUUM-only call sites.
- Regression test: tight UPDATE loop in a single transaction does not grow the page beyond a steady-state size.

### 3.3 Free Space Map
- New FSM fork written through the buffer pool (its own `RelFileForkNode` constant).
- `heapInsertTargetBlock` consults the FSM before extending the relation.
- Regression test: 100k INSERTs interleaved with 50k DELETEs shrinks back to a single-page heap after one VACUUM.

### 3.4 Visibility Map
- New VM fork written through the buffer pool.
- VACUUM sets `ALL_VISIBLE` / `ALL_FROZEN` bits as it scans.
- Index scan operator can skip heap fetch when an `IndexOnlyScan` plan is chosen and the VM bit is set; planner picks `IndexOnlyScan` when the index covers all referenced columns.
- Regression test: `EXPLAIN (ANALYZE, BUFFERS)` on a covered query reports zero heap reads after VACUUM.

### 3.5 Tuple freezing / anti-wraparound
- VACUUM rewrites tuple `xmin` to `FrozenTransactionId` when `xmin` is older than the freeze threshold.
- `pg_class.relfrozenxid` (or goopg's equivalent) is updated and persisted.
- Autovacuum (M0019) honours the anti-wraparound trigger and runs even when the dead-tuple threshold is not met.

### 3.6 TOAST
- Heap insert with a row that would not fit on a page automatically TOASTs the largest TOAST-able column out of line.
- TOAST detoasting on read is transparent to the executor (`Datum` boundary handles it).
- Regression test: insert + select round-trip of a 1 MiB `text` column.

### 3.7 No regression
- `TestRunTPCHQueriesAgainstSyntheticData` 22/22 unchanged.
- `TestTPCHResultParity` identical=22 divergent=0 errored=0 unchanged.
- `make ralph-state-guard` green.

## 4. Out of scope

- BRIN, GIN, GiST forks — only FSM and VM in this milestone.
- TOAST compression algorithms beyond pglz (LZ4 / ZSTD: separate milestone).
- HOT redirect compaction (`PD_PAGE_FULL` driven full-line-pointer rewrite) — only the steady-state HOT path is in scope.

## 5. Reference

- `postgres/src/backend/access/heap/heapam.c` — `heap_update`, HOT decision logic (`HeapDetermineModifiedColumns`, `HeapTupleSatisfiesUpdate`).
- `postgres/src/backend/access/heap/pruneheap.c` — opportunistic prune.
- `postgres/src/backend/access/heap/heaptoast.c` and `detoast.c` — TOAST insert / fetch.
- `postgres/src/backend/storage/freespace/freespace.c`, `fsmpage.c` — FSM tree.
- `postgres/src/backend/access/heap/visibilitymap.c` — VM.
- `postgres/src/backend/commands/vacuumlazy.c` — freeze pass (`heap_prepare_freeze_tuple`).
- `docs/reference/ref-007-heap-mvcc.md` — gap inventory this milestone closes.
- `docs/design/root-0006-storage-format.md`, `root-0007-mvcc-and-snapshots.md`, `root-0016-vacuum-and-analyze.md` — current goopg invariants.
