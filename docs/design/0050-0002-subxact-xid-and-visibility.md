# 0050-0002 — Subxact xid allocation and visibility

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0050 — Savepoints and subtransactions
**Supersedes:** —

## Context

Once subxacts can write rows, the snapshot manager has to answer
"is xid X visible to snapshot S?" correctly. With nesting, the answer
depends on the parent xact's status and on whether X (a subxact xid)
was individually rolled back.

## Plan

1. In-memory subxact-to-parent map (the pg_subtrans equivalent): a
   bounded LRU keyed by SubXid → ParentXid. Populated at every subxact
   xid allocation; consulted at every visibility check.
2. New helpers in `internal/mvcc/`:
   - `TopLevelXid(xid) Xid` — walks the subxact-to-parent chain.
   - `SubxactRolledBack(subxid) bool` — checks against the per-top-
     level "rolled back subxacts" bitmap.
3. `XidInProgress` extended:
   - Resolve to top-level xid.
   - If top-level is committed: still in-progress for *this* visibility
     test if `subxid` itself was rolled back (an aborted subxact's row
     stays invisible even after the parent commits). Otherwise visible.
   - If top-level is aborted: invisible.
   - If top-level is in-progress: in-progress.
4. **Snapshot capture.** A snapshot is a tuple (xmin, xmax, xip[], xact
   status accessor). Subxacts inherit the parent's snapshot at
   savepoint time; on `ROLLBACK TO`, the new sibling subxact gets a
   fresh snapshot. The `xip[]` array tracks top-level xids only, as in
   upstream — subxact resolution is via the parent map.
5. Persistence: the in-memory subxact map only needs to survive while
   the top-level xact is in flight. Crash recovery rebuilds it from the
   WAL assignment records (0050-0003); after a clean commit/abort the
   entries can be evicted.

## Definition of Done

- Visibility test matrix in `internal/mvcc/`:
  `(subxact-status, parent-status) × (snapshot-mode)` — every cell
  matches upstream's behaviour.
- Stress test: 2 sessions, one with a 4-deep savepoint chain, the
  other reading; correct rows visible at every commit/rollback step.

## Upstream reference

- `postgres/src/backend/access/transam/subtrans.c` — pg_subtrans SLRU
  (in-memory cache for goopg).
- `postgres/src/backend/utils/time/snapmgr.c` — snapshot fields.
- `postgres/src/backend/access/heap/heapam_visibility.c` —
  `HeapTupleSatisfiesMVCC` / subxact branch.

## goopg references

- `internal/mvcc/snapshot.go`, `internal/mvcc/visibility.go`.
- 0050-0001 — stack provides the parent map population.
