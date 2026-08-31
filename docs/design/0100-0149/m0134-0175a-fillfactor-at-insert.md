# M0134-0175a — `fillfactor` must reserve space when choosing a heap insert page

**Status:** LANDED 2026-08-29.
**Filed out of:** [M0134-0175 (TABLESAMPLE)](m0134-0175-tablesample.md).
**Touches:** `internal/storage/heap.go`, `internal/executor/operators_storage.go`,
`internal/executor/heap_fillfactor.go`, `internal/executor/context.go`,
`internal/executor/operators_ddl.go`.

## The gap

`fillfactor` existed everywhere except the one place it means something.

`catalog.Table.Fillfactor` was parsed by `CREATE TABLE ... WITH (fillfactor=N)`
and by the partition-leaf path, bounds-checked into `[10,100]`, persisted to
`pg_class.reloptions`, re-emitted by `pg_dump`, settable and resettable through
`ALTER TABLE`, and read by the **cost model** (`internal/optimizer/relsize.go`).
It had **no consumer in the insert path**. `writeHeapRowReturning` moved to a
new page only when the current one was *physically* full:

```go
minFreeBytes := uint16(len(tupleBytes) + 4)   // fits? then use it
```

So the ten ~232-byte rows of the upstream `tablesample.sql` fixture — a table
created `WITH (fillfactor=10)` for the explicit purpose of spanning several
pages from very little data — landed on **one** block where PostgreSQL 18.3
uses **four**. Every block-addressed `TABLESAMPLE` result therefore diverged
from the oracle, despite the sampler arithmetic in M0134-0175 being an exact
port. Correct arithmetic over the wrong page layout.

This is the *declared but unconsumed* pattern again (cf. `client_min_messages`),
and its cost reaches well past one regress case: with no reserve, **`fillfactor`
cannot keep free space on a page for later HOT updates, which is the entire
reason the reloption exists.**

## What PostgreSQL does

`RelationGetBufferForTuple` (`postgres/src/backend/access/heap/hio.c:520-556`)
computes, once per insert, the free space an *existing* page must have before
the tuple may land on it:

```c
len = MAXALIGN(len);
saveFreeSpace = RelationGetTargetPageFreeSpace(relation, HEAP_DEFAULT_FILLFACTOR);
nearlyEmptyFreeSpace = MaxHeapTupleSize - (MaxHeapTuplesPerPage / 8 * sizeof(ItemIdData));
if (len + saveFreeSpace > nearlyEmptyFreeSpace)
    targetFreeSpace = Max(len, nearlyEmptyFreeSpace);
else
    targetFreeSpace = len + saveFreeSpace;
```

with `RelationGetTargetPageFreeSpace(rel, deflt) = BLCKSZ * (100 - fillfactor) / 100`
(`src/include/utils/rel.h:389`) and `HEAP_DEFAULT_FILLFACTOR = 100`. Candidate
pages are then accepted by `targetFreeSpace <= PageGetHeapFreeSpace(page)`
(hio.c:702), and the same `targetFreeSpace` is what the FSM search asks for.

Three properties of that code are load-bearing and each is mirrored here:

1. **The default reserves nothing.** `fillfactor = 100` gives `saveFreeSpace = 0`
   and `targetFreeSpace = MAXALIGN(len)` — arithmetically identical to the old
   "does the tuple physically fit" test. Every table that does not ask for a
   reserve keeps its previous on-disk density, byte for byte.
2. **A freshly extended page is exempt.** After extension upstream checks only
   `len > pageFreeSpace` (hio.c:859), not the target.
3. **`nearlyEmptyFreeSpace` clamps the reserve.** Without it a `fillfactor=10`
   table holding 4 KiB tuples would demand ~11 KiB of free space on an 8 KiB
   page: no page could ever satisfy the target, and the relation would extend
   without bound. Property 2 makes this survivable; the clamp makes it correct
   for the FSM search too.

## What landed

**`internal/storage/heap.go`** — the arithmetic, as pure functions:

- `MaxHeapTupleSize` (8160), `HeapDefaultFillfactor` (100) and the unexported
  `nearlyEmptyFreeSpace` (8016) join the existing `MaxHeapTuplesPerPage` (291).
- `PageGetHeapFreeSpace(p)` mirrors `bufpage.c:988` over `PageGetFreeSpace`
  (`bufpage.c:907`): `(pd_upper - pd_lower) - sizeof(ItemIdData)`. The result is
  directly comparable to a MAXALIGNed tuple length, so it agrees exactly with
  what `PageAddHeapTuple` checks internally. **Deliberate divergence:** upstream
  returns 0 at `MaxHeapTuplesPerPage` line pointers *unless* `PD_HAS_FREE_LINES`
  lets a dead pointer be recycled; goopg's `PageAddHeapTuple` always appends a
  fresh line pointer and never recycles, so the unconditional 0 is the accurate
  answer for this allocator.
- `HeapInsertTargetFreeSpace(tupleLen, fillfactor)` is the hio.c block above,
  with `fillfactor <= 0` meaning "unset" (goopg's `catalog.Table.Fillfactor`
  encodes unset as 0, PG's reloption as -1).

**`internal/executor/operators_storage.go`** — `writeHeapRowReturning` computes
`targetFreeSpace` once, passes it as the new `reserve` argument of
`tryAppendToBlock` for the FSM candidate and tail-block probes, passes `0` for
the freshly batch-extended page (property 2), and raises the FSM search
threshold to `targetFreeSpace + itemIDSize` (the FSM stores `pd_upper-pd_lower`,
one line pointer more than `PageGetHeapFreeSpace` reports).

A page rejected **by the reserve** is not physically full, so — unlike the
`ErrNoSpaceInPage` branch — it records its **actual** remaining free space in
the FSM instead of 0, mirroring upstream's
`RecordAndGetPageWithFreeSpace(rel, blk, pageFreeSpace, targetFreeSpace)`. A
smaller tuple may still legitimately land there later, and since the search
already asks for `targetFreeSpace`, an accurate entry cannot hand the page back
for *this* tuple. Recording 0 would have thrown away that information.

**The sibling was checked, not copied.** `writeHeapRowReturningPG` carries a
near-identical block-selection loop. Its single caller is
`writeHeapRowCanonical`, i.e. system-catalog relations only, and a catalog
relation carries no reloptions — its fillfactor is always 100 and its reserve
always 0, so the gate there would be unreachable code. The divergence is
recorded in a comment at that function, with the instruction to port the
argument across if it ever gains a user-table caller. (Sibling paths must change
together *or* say why they didn't.)

## Where the fillfactor comes from — the relcache stand-in

PG reads `rd_options` off the `Relation` the caller already holds. goopg's
`writeHeapRowReturning` holds only a `storage.RelFileNode`, and
`catalog.InMemory` has **no OID index**: `LookupTableByOIDAllDBs` walks every
namespace's `map[string]*Table`. Resolving on every inserted row would put an
`O(tables)` scan under a shared `RLock` on the hottest write path in the engine
— precisely the shape of the M0107 regression (`ReadMemStats` per query, 4k →
42k TPS once removed).

So `(*Context).heapFillfactor` memoises per relation OID in
`Context.heapFillfactorCache`, following the existing `pgKeyDescCache`
precedent: one resolution per relation per session, an ordinary map hit
thereafter. `ALTER TABLE ... SET/RESET (fillfactor)` drops the entry for its own
session (`invalidateHeapFillfactor`), which is where PG gets relcache
invalidation.

**Known, bounded divergence:** a *concurrent* session that has already memoised
the old value keeps it until it reconnects, where PG would receive the
invalidation. This can never change which rows exist or what they contain — only
how densely that session's subsequent inserts pack pages. Recorded in the
deferral ledger; the honest fix is an OID index on `tableNamespace` plus a
catalog generation counter, which is a larger change than this task.

## Verification

`TestFillfactorReservesSpaceAtInsert` drives the upstream fixture through the
real executor and pins the block layout at **3/3/3/1 over four blocks** — PG
18.3's layout, and the reason `TABLESAMPLE SYSTEM (50) REPEATABLE (0)` returns
ids 3..8 (blocks 1 and 2). `TestDefaultFillfactorPacksTightly` is the control:
the same ten rows with no reloption must still share **one** block, which is what
proves the change is inert for the TPC-H/TPC-DS schemas, the catalog heaps and
pgbench's tables. `TestHeapFillfactorMemoTracksAlter` covers the memo and its
invalidation; `internal/storage/heap_fillfactor_test.go` pins the page-geometry
constants and every branch of the target arithmetic against the oracle values.

Revert-checks (both bite): forcing the lookup to `HeapDefaultFillfactor` returns
the layout to `[10]`, the exact pre-fix symptom; deleting the
`nearlyEmptyFreeSpace` clamp makes a 4 KiB tuple in a `fillfactor=10` table
demand 11468 bytes on an 8 KiB page.

`tablesample.sql`: **304 → 214** diff lines, and the first 63 lines of the case
— every sampled `SELECT` — now match the oracle exactly. `^+ERROR` 6, `^-ERROR`
3, unchanged: the remaining buckets are the already-filed M0134-0175b/c/d and
M0134-0169a, the pre-existing inheritance-child EXPLAIN alias gap, and one new
discovery (below).

Gates: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2 20.9s, Q13 rows=34 8.3s) — the
insert path is the highest-risk surface in the repo and the default-fillfactor
control test is the argument that TPC-H/TPC-DS page density is untouched.

## Discovered here, not fixed here

With the page layout corrected, the `SCROLL CURSOR` half of `tablesample.sql`
resolved into a distinct bug: after the cursor has been scrolled forward, a
second `FETCH FIRST` (and every `FETCH NEXT` after it) returns **0 rows** where
PG restarts the scan and returns 3, 4, 5, …. The first pass through the cursor
matches the oracle, so this is a rewind/restart gap in the scroll-cursor
machinery rather than anything sampling-specific. Filed as **M0134-0175e**.
