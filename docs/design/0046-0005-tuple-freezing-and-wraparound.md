# 0046-0005 — Tuple freezing and anti-wraparound

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0046 — Heap & MVCC maturation
**Supersedes:** —

## Context

goopg uses 32-bit XIDs (mirroring upstream). Without freezing the
oldest-xmin horizon eventually moves more than 2^31 ahead of the oldest
extant `xmin` value, at which point those old tuples either become
invisible or trigger an emergency `xidStopLimit` shutdown. The fix is to
periodically rewrite ancient `xmin` values to `FrozenTransactionId` (a
sentinel meaning "infinitely far in the past — visible to everyone");
tuples thus frozen are immune to the wraparound check.

`relfrozenxid` is the per-relation guarantee that no `xmin` < `relfrozenxid`
remains in the relation. The cluster-wide `datfrozenxid` (per-database)
is `min(relfrozenxid)` over all relations. Autovacuum (M0019) launches a
compulsory anti-wraparound vacuum when `current_xid - relfrozenxid >
autovacuum_freeze_max_age`.

## Plan

1. Add `FrozenTransactionId = 2`, `FirstNormalTransactionId = 3` (mirror
   upstream).
2. New tuple-classifier helper
   `internal/access/heap/freeze.go::ShouldFreezeTuple(t, freezeLimit)`:
   true when `t.xmin < freezeLimit && t.xmin >= FirstNormalTransactionId`.
3. VACUUM's per-page pass:
   - Compute `freezeLimit = OldestXmin - vacuum_freeze_min_age`.
   - For each tuple with `ShouldFreezeTuple(...)`, rewrite `xmin = FrozenTransactionId`,
     set the `HEAP_XMIN_FROZEN` infomask bit.
   - Track per-page `newRelfrozenxid` (min of remaining live `xmin`s); after
     scan finishes, update relation's `relfrozenxid` in the catalog.
4. WAL: `XLOG_HEAP2_FREEZE_PAGE` record carrying offset + cutoff. Replay
   applies the same per-tuple xmin rewrite.
5. Catalog: extend `pg_class` (M0030 catalog persistence) with
   `relfrozenxid xid` column. Persist on each VACUUM.
6. Anti-wraparound trigger in M0019 autovacuum: when
   `currentXid - relfrozenxid > vacuum_freeze_table_age`, autovacuum
   chooses an aggressive vacuum that scans every non-`ALL_FROZEN` page.
7. Cluster-wide protection: dispatcher logs a WARNING when
   `currentXid - datfrozenxid > xidWarnLimit` and refuses new transactions
   above `xidStopLimit`. (Mirror upstream's xid-warn / xid-stop limits.)

## Definition of Done

- VACUUM with default GUCs freezes tuples whose xmin is older than
  `OldestXmin - vacuum_freeze_min_age`.
- `pg_class.relfrozenxid` advances after each VACUUM.
- Autovacuum starts an anti-wraparound vacuum at the
  `autovacuum_freeze_max_age` threshold.
- Stress test simulates 1B XID consumption (XID counter advanced by an
  internal hook): cluster keeps running, tuples remain visible.

## Upstream reference

- `postgres/src/backend/commands/vacuumlazy.c` —
  `lazy_scan_heap`, freezing pass.
- `postgres/src/backend/access/heap/heapam.c` —
  `heap_prepare_freeze_tuple`, `heap_freeze_execute_prepared`.
- `postgres/src/backend/access/transam/varsup.c` —
  `xidStopLimit`, `xidWarnLimit`, `xidWrapLimit`.

## goopg references

- `internal/storage/vacuum.go` — VACUUM driver (gains the freeze pass).
- `internal/mvcc/snapshot.go` — `OldestXmin` source.
- M0019 — autovacuum scheduling integration.
- M0030 — catalog persistence for `relfrozenxid`.
