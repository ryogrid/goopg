# M0145-0008q: page compaction takes a cleanup lock (pin count 1)

Status: **LANDED 2026-09-25**. Task: `.ralph/fix_plan.md` M0145-0008q (Kind:
impl, Parent: M0145-0008k). Recon: `m0145-0008k-seqscan-row-copy-recon.md` §2.
Evidence: `analysis/m0145/m0145-0008q/`.

## Why

PG lets a backend keep reading a tuple on a pinned buffer after it drops the
content lock (`ExecStoreBufferHeapTuple`). That is sound only because nothing
moves tuple bytes on a page someone else has pinned: prune and vacuum repack
under a **cleanup lock**, the exclusive content lock held while the caller's
pin is the only one (`postgres/src/backend/storage/buffer/bufmgr.c`
`LockBufferForCleanup`, `ConditionalLockBufferForCleanup`,
`IsBufferCleanupOK`). goopg compacted under the exclusive lock alone, so its
scans had to clone every tuple before unlocking. This slice ports the lock
discipline, the prerequisite for a pin-held (zero-copy) scan slot.

## What changed

- **`storage.Pool`** gains the three bufmgr primitives, on the shared pin
  count in the slot state word:
  - `IsCleanupOK(s)`: the caller holds the exclusive lock and its pin is the
    only one (pin count 1);
  - `ConditionalLockForCleanup(s)`: `TryLock`, then the pin check. On
    failure it returns false holding no lock;
  - `LockForCleanup(s)`: lock, check, and on failure unlock and retry with a
    capped backoff (50 µs doubling to 10 ms). PG sleeps until the last
    other unpinner signals it (`BM_PIN_COUNT_WAITER`); the retry loop has
    the same outcome without a waiter flag.
- **Every runtime compaction site takes it:**
  - **HOT-update opportunistic prune** (`operators_storage.go`, page-full
    arm). The exclusive lock is already held, so this is `IsCleanupOK`. A
    page someone else still has pinned is not pruned, and the update leaves
    HOT.
  - **Index-only scan on-access prune** of temp pages
    (`operators_indexonly.go`): `ConditionalLockForCleanup`, skipping a
    pinned page as `heap_page_prune_opt` does.
  - **VACUUM** (`commands/vacuum/vacuum.go`): `ConditionalLockForCleanup`
    first, as `lazy_scan_heap` does.
    - A **non-aggressive** pass that cannot get the lock does PG's
      `lazy_scan_noprune`. Under the share lock it counts the tuples a prune
      would keep (`storage.PageCountVacuumLive`, the non-mutating twin of
      `pagePruneCore`'s live count), so reltuples stays right. It assumes
      the page non-empty for truncation and does not prune, freeze or set
      VM bits.
    - The page's unfrozen xmins go unexamined, so the pass counts it in
      `Stats.SkippedPinned`. `Stats.RelfrozenxidGuarded` then keeps
      relfrozenxid from advancing, exactly like a VM skip of an all-visible
      page. Both callers (manual VACUUM, autovacuum) use it.
    - An **aggressive** pass waits (`LockForCleanup`), as PG does when
      freezing is required.
- **Redo is unchanged.** The heap prune, vacuum and prune-opt replays compact
  private page copies read through `storage.Manager`, not pool buffers, so
  no other pin can exist there.

## No private refcount

goopg has no per-backend private refcount, so "only pin" means the shared
count is 1. A backend holding two pins on one page itself never gets the
cleanup lock; PG's conditional variant also refuses it. For example, a
seq-scan-driven UPDATE whose scan still pins the page the HOT path wants to
prune gets no lock, and the update goes non-HOT. `LockForCleanup` must never
be called with a second own pin (documented on the method). VACUUM holds
none.

## Tests

- `TestCleanupLockRequiresTheOnlyPin` (storage):
  - a sole pin gets the conditional lock;
  - two pins do not, and the failed attempt leaves no content lock behind;
  - `IsCleanupOK` tracks the count;
  - `LockForCleanup` succeeds once the pin is dropped.
- `TestVacuumPinnedPageNonAggressiveCountsWithoutPruning`: under a foreign
  pin, a non-aggressive pass reclaims nothing but reports Live=1 and
  SkippedPinned=1, guards relfrozenxid, and leaves the dead tuple on the
  page.
- `TestVacuumPinnedPageAggressiveWaitsForCleanupLock`: an aggressive pass
  prunes only after the foreign pin is dropped.
- `TestPort_IsolationVacuumNoCleanupLock` (PG's spec for exactly this
  behaviour, pass-required) still passes.

## Measured

pgbench TPC-B, scale 2, 8 clients, 30 s, fresh cluster per arm, HEAD
`0cda5c573` against the candidate:
- **Throughput:** 626 / 624 / 630 tps on HEAD against 624 / 621 / 626 on the
  candidate. That is within noise, and there are no failed transactions.
- **Table growth after the run** (max heap block): identical for branches
  (83) and accounts (3335); tellers 84 → 90.

The small tables grow to ~80 pages for 2–20 rows **on HEAD as well**. That is
a pre-existing HOT/prune gap, filed separately: PG prunes when a page is
read (`heap_page_prune_opt` in `heap_prepare_pagescan` and
`heapam_index_fetch_tuple`), and goopg prunes only when a HOT update finds
its page full.

## Gates (staged tree)

- units: PASS.
- `go test -race` over the storage and vacuum tests: PASS.
- `tpch-spotcheck`: PASS.
- acceptance arm: 24 MATCH.
- `tpcds-sf025`: 96/96, 99/99 shapes same.
- Isolation family (`TestPort_Isolation*`): two failures,
  `ReadWriteUnique4` and `TemporalRangeIntegrity`. Both fail identically on
  a clean HEAD worktree and are open nightly items.

Movement: none. This is a storage-discipline change and no plan or value
moved.

## Not ported (ledgered)

- The waiter signal (`BM_PIN_COUNT_WAITER` / `ProcWaitForSignal`): replaced
  by capped-backoff polling.
- Per-backend private refcounts (`GetPrivateRefCount`).
- `lazy_scan_noprune`'s `NewRelfrozenXid` tracking and `missed_dead_tuples`
  reporting. goopg guards relfrozenxid instead, and reports no missed-dead
  count.
- B-tree VACUUM's cleanup lock on leaf pages (`btvacuumpage`).
- Pruning on read (`heap_page_prune_opt` at page read), the gap the pgbench
  sizes show.
- The pin-held scan slot itself, which this slice unblocks.
