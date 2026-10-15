# M0146-0195 — B-tree: dedup before the split allocates its page

Status: done 2026-10-15. On the corpus reloaded with the fix (owner, 419dc5c9e),
TPC-H Q3, Q5 and Q7 match PG; the TPC-H match count is 12 → 15.

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

## Corrected measurement (2026-10-15, same day)

The first measurement was invalid. goopg builds ONE parameterised index path
per parameterisation: `pickIndexCoveringLeadingPrefix` (`nl_index_join.go`)
takes the index that binds the most columns, and on a tie the first one by name
(`IndexesOnTable` sorts by name). PG's `build_index_paths` instead builds a path
for every usable index and lets `add_path` keep the cheapest. The `_rb` twins
tied with the originals, so the bloated originals won without either being
priced.

**The probe cost reproduces exactly.** The gap grows with loop count. With
`order\_customer\_fkidx` probed from ~30000 customers, PG charges 4.98 per probe
and goopg 7.07. The difference, 2.09, is `genericcostestimate`'s index-page
term under loop amortisation (`index_pages_fetched`, costsize.c:906):

| index | pages | index-page cost per probe |
|---|---|---|
| PG's `order_customer_fkidx` | 4121 | 0.55 |
| goopg's, after the realign | 28617 | 2.63 |

A compact twin (1563 pages) named to sort first was chosen at 4.65 per probe,
the predicted figure.

**Re-measured** with twins named `a_<original>` and mapped back for the diff.
Both arms used the HEAD + M0146-0178 slice-2 binary, against PG's
`routing-realign-20261015`:

| arm | match | join-method | scan-type | parallelism |
|---|---|---|---|---|
| clone as-is | 12 / 22 | 5 | 8 | 3 |
| compact fk indexes in use | 14 / 22 | 3 | 5 | 1 |

- Q3, Q5 and Q7 now MATCH; this task's targets.
- Q2 and Q18 lose categories.
- Q10 loses its match. goopg's bulk-built twins (1563 pages) are denser than
  PG's indexes (4121), so they undercut PG's costs there.

What would bring goopg in line:

- **Bring the fk indexes to PG's size.** REINDEX is unavailable in `tpch`
  (M0146-0196), but a reload of `:65433` by INSERT with the 7b356ec04 fix builds
  them without the leak. That is an owner action.
- **Cost every index (M0146-0197).** goopg should build a parameterised path for
  every eligible index, as PG's `build_index_paths` does, so that cost picks
  among them.

## Result on the real corpus (2026-10-15 evening)

The owner restarted `:65433` on a 7e9900a35 build, which includes 7b356ec04, and
redid the INSERT replay (419dc5c9e). The fk indexes came out PG-sized:

| index | before | after | PG |
|---|---|---|---|
| `idx_lineitem_orderkey_fkidx` | 117050 | 16641 | 16468 |
| `order_customer_fkidx` | 28617 | 2220 | 4121 |
| `partsupp_supplier_fkidx` | 14775 | 814 | 2199 |

Parallel plan-only TPC-H capture on the reloaded corpus with a clean-HEAD
binary, against PG's `routing-realign-20261015`
(`analysis/m0146/m0146-0178/residual-workers/m0178r-tpch-head*`):

| measure | before | after |
|---|---|---|
| match | 12 / 22 | 15 / 22 |
| join-method | 5 | 2 |
| scan-type | 8 | 4 |
| join-order | 5 | 3 |
| parallelism | 3 | 1 |

Q3, Q5 and Q7 MATCH. Q10 keeps its match: unlike the clone's bulk-built twins,
insert-built indexes are PG-sized. Q5 completes in about 1 s, where it timed
out at 900 s before.

## Residuals

- **Existing files keep their empty pages.** `:65433`'s fk indexes stay bloated
  until rebuilt (REINDEX or a reload), which is an owner action on a reference
  cluster. Until then no TPC-H plan moves.
- **Pages vacuum frees are not reused across handles.** Delete, VACUUM and
  reinsert still doubles an index (607 → 1212 blocks). The free list belongs to
  the handle whose vacuum freed the pages; PG records them in the shared FSM
  (`RecordFreeIndexPage`, `_bt_allocbuf`). This is M0122-0010a.
