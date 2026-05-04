# 0046-0004 — Visibility Map (VM)

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0046 — Heap & MVCC maturation
**Supersedes:** —

## Context

Without a Visibility Map, goopg cannot do index-only scans (every index
hit must fetch the heap to check `xmin`/`xmax` visibility) and VACUUM has
to scan every page even when most are unchanged.

The VM is a per-relation fork (`<relfilenode>_vm`) with two bits per heap
page: `ALL_VISIBLE` (every tuple on the page is visible to every active
snapshot) and `ALL_FROZEN` (every tuple has been frozen — visible to
every possible snapshot, ever). Once set, a bit is cleared whenever the
heap page is modified.

## Plan

1. New fork constant `INIT_FORKNUM_VM`.
2. `internal/storage/visibilitymap/` package: 2-bits-per-block bitmap,
   `Set(rel, blk, flags, lsn)` and `Get(rel, blk) flags` operations.
3. WAL: page modifications include the heap page's old VM flags; replay
   clears the corresponding VM bit. (Critical: VM must be conservative
   — false negatives are fine, false positives break MVCC.)
4. VACUUM (M0019 + this milestone's freezing pass) sets `ALL_VISIBLE`
   when a page passes the all-visible test, and `ALL_FROZEN` when every
   `xmin` on the page is `FrozenTransactionId`.
5. Index-only scan path (planner + executor) — when the index covers all
   referenced columns, executor checks the VM bit; if set, returns the
   index entry directly without a heap fetch.
6. VACUUM on subsequent passes can skip pages with `ALL_FROZEN` set
   (the upstream "skip frozen" optimisation).

## Definition of Done

- `EXPLAIN (ANALYZE, BUFFERS)` on a covered query reports zero heap
  reads after a VACUUM that sets `ALL_VISIBLE`.
- VACUUM second-pass on an unchanged table touches zero `ALL_FROZEN`
  pages.
- VM bit cleared correctly by every WAL-logged heap modification
  (regression test stresses INSERT / UPDATE / DELETE / HOT-UPDATE).
- Crash recovery preserves the conservative invariant: replayed page
  modifications clear the VM bit even if the WAL record arrived after a
  checkpoint.

## Upstream reference

- `postgres/src/backend/access/heap/visibilitymap.c` — bitmap layout,
  `visibilitymap_set`, `visibilitymap_get_status`.
- `postgres/src/backend/executor/nodeIndexonlyscan.c` — VM-bit check
  before heap fetch.

## goopg references

- `internal/access/btree/scan.go` — index-scan path that grows the
  IndexOnlyScan branch.
- 0046-0001 (HOT updates clear `ALL_VISIBLE`).
- 0046-0005 (freezing sets `ALL_FROZEN`).
