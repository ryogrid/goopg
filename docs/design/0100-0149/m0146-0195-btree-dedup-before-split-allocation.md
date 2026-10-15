# M0146-0195 — B-tree: dedup before the split allocates its page

Status: open. The storage fix landed 2026-10-15. The same day, a private-clone
measurement showed that index size is NOT what drives the Q3/Q5/Q7 divergence
(see "Measurement" below).

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

## Measurement on a private clone (2026-10-15)

`scripts/tpch-estimate-audit-arm.sh` gained `PRE_AUDIT_SQL`, which runs SQL on
the arm's private clone before the audit. Two plan-only captures were taken in
parallel mode, with one HEAD binary (`7b356ec04`), against PG's
`routing-realign-20261015` plans (`analysis/m0146/m0146-0195/`):

| arm | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| clone as-is (bloated fk indexes) | 12 / 22 | join-order 5, join-method 5, scan-type 8, parallelism 3 |
| clone with compact twins of the 8 fk indexes | 11 / 22 | join-order 7, join-method 6, scan-type 10, parallelism 4 |

In the second arm, `CREATE INDEX <name>_rb`, with the names mapped back for the
diff.

What the second arm shows:

- **Q3, Q5 and Q7 do not move.** Q5's `orders` probe keeps the old
  `order_customer_fkidx` at the same 2.29 per probe, where PG charges 1.64. A
  single-row equality probe barely depends on index size, so the gap is in
  another `cost_index` term.
- **Q11 loses its match, and Q18 and Q21 diverge further.** They take the
  compact twins.

So the hypothesis behind this task's first step is refuted for its three
witnesses, and an index rebuild on `:65433` is not a fix for them.

Index sizes also deviate in both directions, so neither arm is PG's state:

- `:65433`'s fk indexes are 6–8× PG's (`supplier_nation_fkidx` 172 vs 30,
  `partsupp_supplier_fkidx` 14775 vs 2199);
- goopg's bulk build is denser than PG's (the lineitem fk index before the
  realign: 9549 vs 16468).

Q11's match today rests on the bloated sizes.

**New defect.** A rebuild cannot be done through goopg in `tpch` anyway: in a
CREATE DATABASE'd database, `REINDEX TABLE/INDEX` and `DROP INDEX` report
"does not exist" for relations that SELECT and CREATE INDEX resolve. Filed as
M0146-0196.

## Residuals

- **Existing files keep their empty pages.** `:65433`'s fk indexes stay bloated
  until rebuilt (REINDEX or a reload), which is an owner action on a reference
  cluster. Until then no TPC-H plan moves.
- **Pages vacuum frees are not reused across handles.** Delete, VACUUM and
  reinsert still doubles an index (607 → 1212 blocks). The free list belongs to
  the handle whose vacuum freed the pages; PG records them in the shared FSM
  (`RecordFreeIndexPage`, `_bt_allocbuf`). This is M0122-0010a.
