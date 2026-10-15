# M0146-0195 — B-tree: dedup before the split allocates its page

Status: open — the root-cause fix landed 2026-10-15. The plans move only once the
TPC-H indexes on `:65433` are rebuilt; that is an owner action.

## Problem

The 2026-10-15 S9 census routed TPC-H Q3 and Q7 (join-method) and Q5
(scan-type) to one divergence. PG probes lineitem through
`idx_lineitem_orderkey_fkidx` in a parameterised Nested Loop, and goopg does not.
The probe prices pointed at index size. On the realigned corpus goopg's indexes
were many times PG's while the heaps matched:

| relation | PG pages | goopg pages | goopg before the realign |
|---|---|---|---|
| `idx_lineitem_orderkey_fkidx` | 16468 | 117050 | 9549 |
| `order_customer_fkidx` | 4121 | 28617 | 1563 |
| `lineitem_pk` | 23101 | 41512 | 23078 |
| `lineitem` (heap) | 129346 | 129214 | — |

Reading the goopg files shows what the extra pages are:

- in the two fk indexes, 100409 of 117050 and 26397 of 28617 leaf blocks hold
  no item at all, not even a high key;
- they are allocated pages that nothing links in.

## Cause

The insert split path in `insertIntoBlock` allocated the new right page
(`pinNewOrRecycled`, extending the file) before it tried to make room:
dedup, then the dead-entry purge. When either made room, the split was
abandoned and the page went to the free list. But the page was live and never
deleted, so `popRecyclableBlock` rejects it whenever a recycle horizon is wired,
which every server does (`PGPageIsRecyclable` accepts only `BTP_DELETED` pages
past their safexid). The next split extended the file again.

The size of the leak depends on the key pattern:

- 4-way duplicate keys inserted in order leave 8 of every 9 leaf blocks empty:
  400000 rows give 5423 blocks against 607 for CREATE INDEX;
- unique, random and CREATE INDEX builds are not affected.

The realign reloaded `:65433` by batched INSERTs into the existing indexes, so
both fk indexes came out this way. `lineitem_pk`'s 1.8× is a different
mechanism: its pages are half full, because the localized-monotonic arm of
`_bt_findsplitloc` is missing (M0146-0178 residual).

PG's order is the reverse. `_bt_findinsertloc` calls
`_bt_delete_or_dedup_one_page` when the page is full
(`./postgres/src/backend/access/nbtree/nbtinsert.c:998`), and only a page still
full afterwards reaches `_bt_split`, which allocates the right page
(`nbtinsert.c:1720`).

## Change

The allocation, and the right page's high key and opaque area, move after the
dedup and purge decision. A no-split rewrite no longer allocates anything, so
there is no abandoned page to recycle.

**Test.** `TestDuplicateInsertsLeakNoPages` inserts 4-way duplicates with a
recycle horizon wired, and requires that no leaf block is left empty. On the old
code, 480 of 541 leaf blocks were empty.

**Live scratch server** (400000 rows):

| build | blocks before | blocks after |
|---|---|---|
| numeric, 4-way duplicates, insert-built | 5423 | 607 |
| CREATE INDEX build of the same data | 607 | 607 |
| int4, 15-way duplicates, insert-built | — | 428 |

## Residuals

- **Existing files keep their empty pages.** `:65433`'s fk indexes stay bloated
  until rebuilt (REINDEX or a reload), which is an owner action on a reference
  cluster. Until then no TPC-H plan moves.
- **Pages vacuum frees are not reused across handles.** Delete, VACUUM and
  reinsert still doubles an index (607 → 1212 blocks). The free list belongs to
  the handle whose vacuum freed the pages; PG records them in the shared FSM
  (`RecordFreeIndexPage`, `_bt_allocbuf`). This is M0122-0010a.
