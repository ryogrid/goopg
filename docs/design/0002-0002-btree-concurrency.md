# Concurrent B-tree (Milestone 0002)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-28                                             |
| Milestone  | 0002 — Production-Grade Checkpointing & Concurrent B-tree |
| Refines    | [root-0009-btree.md](root-0009-btree.md)               |
| Supersedes | —                                                      |

## Problem

`internal/access/btree` (M1, single-column int4 B-tree) protects the
entire tree behind one `sync.Mutex`. That serialises every Insert,
Search, and RangeScan: a SELECT against pgbench_accounts contends
with the very next SELECT on the same connection because both have to
take the same mutex.

Milestone 0002 §B-tree promises a Lehman-Yao concurrent design with
PG modifications — per-page latches, right-link descent, atomic
splits — and `pgbench -c 32 -j 8` showing no global B-tree mutex as
the dominant bottleneck. That's a substantial body of work. This
doc owns the multi-loop plan for getting there.

## Upstream reference

- `postgres/src/backend/access/nbtree/README` — the canonical
  description of Lehman-Yao with PG's modifications. Highest-leverage
  document for this subsystem.
- `postgres/src/backend/access/nbtree/nbtree.c` — entry points
  (`btinsert`, `btgettuple`).
- `postgres/src/backend/access/nbtree/nbtinsert.c` — descend with
  right-link recovery, split logic.
- `postgres/src/backend/access/nbtree/nbtsearch.c` — leaf descent and
  rightmost-key handling under concurrent splits.

## Strategy: stage the concurrency in three landings

A "drop everything and ship Lehman-Yao" loop would touch every
function in `btree.go`, every test, and would risk a long debugging
tail. We split the work into three discrete, testable landings:

### Landing 1 — RWMutex parallelism (this loop)

Replace `BTree.mu sync.Mutex` with `sync.RWMutex`:

- `Search` / `RangeScan` take `mu.RLock()`. Multiple readers run
  concurrently.
- `Insert` / `Create` / `clearRootFlag` / `createNewRoot` /
  `updateRootMeta` take `mu.Lock()`. Writers still serialise.

Per-page mutation continues to run under `Slot.Lock()` from the
buffer pool's content lock, which already guards page-byte tearing
between readers and writers (see
[root-0005-buffer-manager.md](root-0005-buffer-manager.md)).

What this buys: pgbench-SELECT-ONLY scales with cores even with
hot-page reads, and `pg_dump`/catalog probes parallelise. What it
does NOT buy: writer parallelism. That needs Landing 2.

This is the **minimum viable change** that makes the read side
correct under concurrency without changing the on-disk format or
the page-mutation invariants.

### Landing 2 — Per-page latches with right-link descent (next M0002 loop)

Replace the tree-wide `RWMutex` with per-page latches. Use the
buffer pool's existing `Slot.RLock()/Lock()` directly.

Lehman-Yao baseline:

1. **Descent (read or write).** Pin the metapage, read root, drop
   metapage. Pin root with shared latch. Walk down the level: at
   each internal page take a *shared* latch, find the child, drop
   the latch, take the child's latch. Hold at most one page's
   latch at a time.
2. **Right-link recovery.** If the search key is greater than the
   page's high key (the maximum key the page covers), follow
   `op.Next` and re-test. This is what makes "drop the parent
   before latching the child" safe: a concurrent split that moved
   keys to the right sibling is recoverable by following the
   right-link.
3. **Split.** Perform the split under exclusive latches on the
   left page and the freshly-extended right page. Update the
   right-sibling pointer of the *previous* op.Next under exclusive
   latch. Stamp the high key into the left page. Then drop the
   leaf-level latches and walk up to insert the separator into
   the parent — this is now a separate critical section, and a
   reader descending through the parent during the gap will hit
   the right-link recovery path.

The B-tree page format already has Prev/Next pointers in
`BTPageOpaque` (see [root-0009-btree.md](root-0009-btree.md)). We
need to add **high keys**: the rightmost item on each non-rightmost
page is reserved as the "this page covers keys ≤ this value"
sentinel. Upstream uses the same convention
(`P_HIKEY = 1`, line-pointer slot 1 holds the high key).

### Landing 3 — Atomic splits with WAL + page-deletion (later)

- WAL records for splits and merges so crash recovery replays them
  atomically. v0 already emits FPI-on-first-dirty (see
  [0002-0001-checkpointing.md](0002-0001-checkpointing.md)),
  which gives us *durability* for half-split states; this landing
  adds *redo logic* so torn splits replay to a consistent state.
- Page deletion + recycling integrated with VACUUM and MVCC
  visibility (`internal/vacuum`).
- Index-only scans where the visibility map permits.

## On-disk format

Landing 1 changes nothing on disk. The format pinned in
[root-0009-btree.md](root-0009-btree.md) is unchanged.

Landing 2 introduces high keys but is backward compatible: existing
non-rightmost pages without a high key are treated as "covers all
keys ≥ leftmost". A future `goopg ctl reindex` (or a one-shot
upgrade pass) populates them lazily.

## Test strategy

Landing 1:

- Existing `internal/access/btree/btree_test.go` covers
  insert/search/range/split. Re-run; no functional change expected.
- Add a goroutine-stress test that fires N parallel `Search` calls
  against a tree to confirm the contention test exists at all
  (pre-Landing 1 it would block; post-Landing 1 it parallelises but
  is still correct).
- Run `pgbench -S -c 8 -j 4` against a live `goopg` and verify
  results stay consistent.

Landing 2:

- Targeted concurrent insert tests that drive splits across
  multiple goroutines and assert no item is lost.
- pgbench `-c 32 -j 8` with the default mixed workload as the
  milestone-0002 acceptance gate.

Landing 3:

- Crash-recovery tests under a partial split.

## Out of scope for this milestone

- `CREATE INDEX CONCURRENTLY`. The build path stays single-threaded.
- Parallel B-tree scans (intra-query parallelism).
- Other index types (GIN, GiST, BRIN, hash, SP-GiST).

## Open questions

- High-key encoding when the index is multi-column or
  variable-length. M0002's btree is still single-column int4, so
  high keys are 4 bytes. Multi-column will need a follow-up.
- Whether to expose a `vacuum`-driven page-deletion path before or
  after Landing 3's WAL records. Upstream couples them, but goopg's
  v0 vacuum already does heap-only pruning.

## Cross-references

- Milestone definition:
  [`docs/milestones/0002-durability-and-concurrent-storage.md`](../milestones/0002-durability-and-concurrent-storage.md).
- v0 B-tree:
  [root-0009-btree.md](root-0009-btree.md).
- Buffer-pool latching primitives:
  [root-0005-buffer-manager.md](root-0005-buffer-manager.md).
