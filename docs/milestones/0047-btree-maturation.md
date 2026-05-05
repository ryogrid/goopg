# Milestone 0047 — B-tree maturation: bulk load, page deletion, deduplication

**Status:** planned
**Depends on:** Milestone 0002 (concurrent B-tree — Lehman-Yao right-link descent already in place; page deletion is the L3b half).
**Drives:** Cut B-tree CREATE INDEX wall time from O(N log N) random-insert to O(N) sort-then-load, prevent unbounded tree growth under UPDATE/DELETE churn, and drop the per-tuple item-id overhead for non-unique indexes via deduplication.

## 1. Context

`docs/reference/ref-002-btree.md` calls out three high-impact gaps in goopg's B-tree:

1. **No bulk load.** `CREATE INDEX` on the pgbench-init `pgbench_accounts` table takes ~31 seconds because every row triggers a top-down B-tree insert with the associated descent and split work. Upstream uses `nbtsort.c` to sort the input, then build the leaf level sequentially and propagate upward, taking <2 s on the same data.
2. **No page deletion.** When a leaf becomes empty after VACUUM removes all dead index entries, goopg leaves it in place and just skips it on traversal. UPDATE-heavy workloads grow the tree forever even though most pages are empty.
3. **No deduplication.** A non-unique index on a low-cardinality column stores one item per tuple. `lineitem_l_shipmode_idx` (5 distinct values × ~6M rows on TPC-H SF1) carries ~6M entries instead of ~5 entries × N TIDs each. Bloats the index 5–10× and inflates buffer-pool pressure.

This milestone closes all three. They are independent enough to land in any order, but bulk-load is the highest-impact and lands first.

## 2. Required Design Docs

1. `docs/design/0047-0001-btree-bulk-load.md` — sort-then-build path triggered by `CREATE INDEX`. External sort over heap rows, build leaf pages sequentially, build internal levels bottom-up. Skip the public `btree.Insert` path entirely. WAL records emitted as full-page-image batches (cheaper than logical insert per tuple).
2. `docs/design/0047-0002-page-deletion.md` — V-Y-style page deletion (the upstream `_bt_pagedel` algorithm): two-phase delete (mark-pending then unlink) with the right-link invariant preserved. Wired into the VACUUM path; never called from runtime queries. Adapts M0002-0002's L3a split-WAL infrastructure.
3. `docs/design/0047-0003-deduplication.md` — leaf deduplication: when a leaf would split because of N tuples sharing the same key, collapse them into a single posting list (`itemPointer[]`) keyed once. New on-disk item shape (variable-length posting list); planner-side index-only-scan path needs to walk the list correctly.

## 3. Definition of Done

### 3.1 Bulk load
- New `internal/access/btree/bulk.go` with `BulkBuild(rel *Relation, tuples Iter) error`.
- `CREATE INDEX` calls `BulkBuild` instead of looped `Insert`.
- pgbench-init wall-time on `pgbench_accounts` (1M rows): ≤ 4 s (was ~31 s).
- Regression test: index built via bulk path is byte-identical (modulo LSNs) to one built via repeated insert plus optimal pack.

### 3.2 Page deletion
- VACUUM removes empty leaves and propagates upward.
- Concurrent reader on the right-link chain sees no broken traversal (the upstream "page-deletion + right-link" invariant test).
- Regression test: 100k INSERT → 100k DELETE → VACUUM yields a one-page B-tree (just the metapage + an empty root).

### 3.3 Deduplication
- New leaf-item header bit / variant marks "posting list".
- Index-fetch path returns one TID at a time from a posting list.
- TPC-H SF1 supplementary index `lineitem_l_shipmode` size: ≤ 25% of pre-dedup baseline.
- Regression test: non-unique index over a 1k-row, 5-distinct-key relation packs into a single page.

### 3.4 No regression
- All of M0002 / M0044 B-tree tests still green.
- `TestRunTPCHQueriesAgainstSyntheticData` 22/22 unchanged.
- `make ralph-state-guard` green.

## 4. Out of scope

- GiST / SP-GiST / GIN — different access methods, separate milestone.
- Index-Skip-Scan / Loose-Index-Scan optimisation — distinct planner work.
- Online (`CONCURRENTLY`) reindex — concurrency-design follow-up.

## 5. Reference

- `postgres/src/backend/access/nbtree/nbtsort.c` — bulk-load build.
- `postgres/src/backend/access/nbtree/nbtpage.c` — page deletion.
- `postgres/src/backend/access/nbtree/nbtdedup.c` — deduplication.
- `postgres/src/include/access/nbtxlog.h` — WAL record shapes (`xl_btree_dedup`, `xl_btree_unlink_page`).
- `docs/reference/ref-002-btree.md` — gap inventory.
- `docs/design/root-0009-btree.md`, `0002-0002-btree-concurrency.md` — current goopg invariants.
