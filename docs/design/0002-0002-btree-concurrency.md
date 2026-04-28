# Concurrent B-tree (Milestone 0002)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted (L1+L2+L3a); Landing 3b still planned         |
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

### Landing 2 — High keys, right-link descent, per-page latches (this loop)

Concrete scope:

1. **Page format.** Grow `BTPageOpaque` from 16 to 24 bytes to add a
   fixed-width `HighKey` field (4 bytes for v0's int4 keys) and a
   `BTHasHighKey` flag bit. `btSpecialOffset` moves accordingly. The
   format version bumps to 2; old on-disk indexes are unreadable.
   Tests create fresh indexes; `pgbench -i` recreates them.
2. **High key semantics.** A page with `BTHasHighKey` set claims to
   cover keys ≤ `HighKey`. Pages without the flag (rightmost
   pages, including a freshly-created root that hasn't split) cover
   all remaining keys. After a split, the left page's HighKey is
   set to the smallest key of the right page; the right page
   inherits whatever HighKey the original page carried (or stays
   without one if original was rightmost).
3. **Right-link recovery.** Both descent and leaf scan check, at
   each page, whether `key > HighKey && Next != Invalid`. If so,
   they jump to `Next` and re-test. This is the core Lehman-Yao
   safety net that makes "drop the parent latch before taking the
   child latch" correct.
4. **Per-page latches.** Reads use `Slot.RLock()`; mutations use
   `Slot.Lock()`. A reader's descent does NOT crab-couple — it
   takes one shared latch at a time and relies on right-link
   recovery to fix mid-flight splits. Writers take the leaf's
   exclusive latch only when ready to mutate it; ancestor latches
   are dropped well before that.
5. **Single writer per tree.** `BTree.mu sync.Mutex` is retained
   to serialise inserts. Concurrent readers no longer take
   `bt.mu` at all — their only synchronisation is per-page
   `Slot.RLock`. This produces the read-vs-write parallelism that
   was missing under Landing 1's `RWMutex`. Landing 3 will replace
   `bt.mu` with proper write-side concurrency once split WAL
   records are in.

Why keep `bt.mu` at this stage? Two concurrent writers would need
to coordinate on per-page exclusive latches across split sequences
that span multiple pages (left, right, parent). That coordination
is correct under Lehman-Yao but adds new failure modes (write
amplification on retry, sibling-pointer maintenance under
concurrent splits) that pair naturally with crash-safe split WAL
records — which is Landing 3 territory. Keeping `bt.mu` for this
landing isolates the change set to "readers no longer block on
writers" without buying the rest of the multi-writer story.

What this buys:

- Multiple readers truly parallel — no tree-wide latch.
- Readers run concurrently with writers — they only block
  transiently on the per-page `Slot.Lock` a writer holds during
  its mutation.
- Writers still single-thread per tree, so split correctness is
  preserved by the existing top-down logic.

### Landing 3a — Atomic split WAL records (this loop)

The M0002 checkpointing landing emits FPI-on-first-dirty per page
(see [0002-0001-checkpointing.md](0002-0001-checkpointing.md)).
That covers single-page mutations but not multi-page atomic
operations: a split mutates the left page AND extends a brand-new
right page that has no prior on-disk image. With independent FPIs,
nothing guarantees both records reach durability together.
Concretely, the buffer pool can flush left's data file once
FPI(left) is durable, even if FPI(right) hasn't been fsync'd yet —
a crash between the two leaves a torn state where left advertises
a high key + right-link to a right page whose disk image is the
empty `InitPage` produced by smgr.Extend.

Landing 3a introduces a single atomic record covering both pages:

- New record kind `RecordKindBtreeSplit` (3).
- Payload: `kind | rel(9 bytes) | leftBlk | rightBlk | leftPage | rightPage`.
- Emitted from the writer's split sequence after both pages are
  fully populated in memory; both pages get `pd_lsn = endLSN` of
  this record via `Pool.MarkDirtyWithLSN`, which also sets
  `fpiSinceCheckpoint` so the per-epoch FPI hook does not emit a
  redundant record for either page.
- Replay (`internal/wal/recovery.go`) decodes the record and
  applies both page images via `WriteBlock`/`Extend`, in the
  order left-then-right, so the right page exists on disk before
  any reader following left's right-link gets there.
- The record is sized at ~16 KB (two `BlockSize` images plus a
  small header). Splits are infrequent under steady-state load
  so the WAL volume cost is acceptable; the alternative is a
  diff-style record, which is more code but minor savings for v0.

The parent-pointer update that follows the leaf split is NOT
included in the same record. If a crash interrupts between the
leaf split and the parent update, replay leaves the leaf in its
post-split state with no parent entry pointing to the right
sibling — readers find the right keys via Lehman-Yao right-link
recovery, so reads stay correct. A future maintenance pass (or
an explicit "incomplete split" flag, à la upstream) finishes the
parent insertion; that work is deferred.

### Landing 3b — Multi-writer concurrency (later)

- Drop `bt.mu` and let two writers descend in parallel. Splits
  serialise per-page on `Slot.Lock`, but un-split inserts on
  different pages run unblocked.
- Concurrent inserts on adjacent leaves can race on sibling-
  pointer maintenance; v0 sidesteps it by NOT updating the
  Prev pointer of the old `op.Next` neighbour during split
  (Prev is unused by reads — only forward `Next` is followed).
  Landing 3a keeps that simplification.
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
