# 0047-0003 — B-tree leaf deduplication

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0047 — B-tree maturation
**Supersedes:** —

## Context

Non-unique secondary indexes today store one leaf entry per row, even
when many rows share the same key. A TPC-H SF1 index on
`lineitem.l_shipmode` (5 distinct values × 6M rows) carries ~6M leaf
entries. Upstream "dedup" packs them into one entry per distinct key
with a *posting list* of TIDs; the same data needs ~5 entries per page,
~12 KB per page, instead of the current ~96 B × 6M.

Two- to ten-times reduction in index size on low-cardinality
non-unique indexes is the headline benefit. The on-disk format change
is local to the leaf and the index-fetch loop.

## Plan

1. Extend the leaf-item header. A leaf entry today is a fixed shape
   `[TID(6) || key(K)]`. With dedup, the entry becomes either:
   - `BT_TID_KIND` (single TID, current shape), or
   - `BT_POSTING_KIND` (`[count(2) || key(K) || TID[count](6 each)]`).
   Two bits in the line-pointer's flags discriminate.
2. `BulkBuild` (0047-0001) emits posting-list entries when ≥ 2 input rows
   share the same key.
3. `Insert` path:
   - On insert, locate the leaf as today.
   - If the leaf has a posting-list entry for the same key, append the
     TID to it (in-place if there's room, or split-and-grow if not).
   - If the leaf is about to split because a single key would push past
     capacity, *first* try to dedup all duplicates on the page; only
     split if the dedup pass fails to free enough space.
4. `Scan` (index-fetch) returns one TID at a time from a posting-list
   entry. Cursor state grows a per-entry sub-position.
5. `VACUUM` (page-dedup pass): when freeing a TID from a posting-list
   entry, decrement the count; when count reaches 1, demote back to a
   single-TID entry; when count reaches 0, line-pointer becomes
   reusable.
6. WAL:
   - `XLOG_BTREE_DEDUP` (mirror upstream `xl_btree_dedup`) — emitted
     when a pre-split dedup pass runs.
   - Existing `XLOG_BTREE_INSERT` extended with the posting-list shape
     bit when the inserted entry is grown rather than added.

## Definition of Done

- TPC-H SF1 `lineitem_l_shipmode` index size ≤ 25% of pre-dedup
  baseline.
- Existing index-scan tests (single-TID kind) still green.
- New tests:
  - Bulk build with 1k duplicates packs into one posting list.
  - Insert into a posting-list entry round-trips.
  - VACUUM partially-removing TIDs from a posting list works.
  - Crash + replay round-trip of `XLOG_BTREE_DEDUP`.

## Upstream reference

- `postgres/src/backend/access/nbtree/nbtdedup.c` —
  `_bt_dedup_pass`, `_bt_form_posting`.
- `postgres/src/include/access/nbtree.h` —
  `IndexTuple` posting-list bit.
- `postgres/src/backend/access/nbtree/nbtxlog.c` — replay.

## goopg references

- `internal/access/btree/page.go`, `scan.go`.
- 0047-0001 — bulk path emits dedup-shape entries directly.
