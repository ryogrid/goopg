# 0047-0002 — B-tree page deletion

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0047 — B-tree maturation
**Supersedes:** —

## Context

A B-tree page that becomes empty (every entry has been `VACUUM`-removed)
stays in the tree forever today. Concurrent readers are not impacted —
they just skip-and-traverse via the right-link — but the file grows
without bound. A 1B-row UPDATE-heavy workload accumulates terabytes of
dead leaf pages over weeks even when the live size is tiny.

Upstream `_bt_pagedel` deletes the page in two phases:

1. **Mark pending.** Set `BTP_HALF_DEAD` on the page; remove the leaf
   tuple in the parent that points to it. Concurrent readers can still
   traverse via the right-link.
2. **Unlink.** Once no transaction old enough to need the page can still
   be in flight, set `BTP_DELETED` and unlink the page from the right-link
   chain. The page goes back to the FSM (relation level) for reuse.

The two-phase split is critical for the right-link invariant the
M0002-0002 Lehman-Yao concurrent-descent code relies on.

## Plan

1. New flags `BTP_HALF_DEAD`, `BTP_DELETED` on `BTPageOpaque`.
2. `internal/access/btree/pagedel.go` —
   - `markPagePending(buf)` — phase 1.
   - `unlinkPage(buf, oldestXact)` — phase 2.
3. VACUUM driver:
   - For each leaf with zero live entries: try `markPagePending`. If the
     parent is split-in-progress (M0002-0002's stable-condition), retry
     after the next descent.
   - For each `BTP_HALF_DEAD` page whose `safeXmin` < `OldestXmin`: run
     `unlinkPage`.
4. WAL:
   - `XLOG_BTREE_MARK_PAGE_HALFDEAD`.
   - `XLOG_BTREE_UNLINK_PAGE`, `XLOG_BTREE_UNLINK_PAGE_META` (when
     unlinking the page leaves the metapage as the only entry).
5. Concurrent reader: when traversal lands on `BTP_HALF_DEAD` or
   `BTP_DELETED`, follow the right-link (same as today's missing-key
   path). The Lehman-Yao invariant is preserved.

## Definition of Done

- 100k INSERT → 100k DELETE → VACUUM yields a one-page B-tree.
- M0002-0002's concurrent-descent test still passes — readers crossing
  through a deletion-in-progress page traverse without error and without
  visibility skew.
- Crash recovery: a partially-completed phase-1 mark is idempotent;
  partial unlink leaves the page in a recoverable state.

## Upstream reference

- `postgres/src/backend/access/nbtree/nbtpage.c` —
  `_bt_pagedel`, `_bt_unlink_halfdead_page`.
- `postgres/src/include/access/nbtxlog.h` —
  `xl_btree_mark_page_halfdead`, `xl_btree_unlink_page`.

## goopg references

- `internal/access/btree/page.go` — `BTPageOpaque` flag bits.
- `docs/design/0002-0002-btree-concurrency.md` — descent invariant.
