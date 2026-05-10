# Milestone 0080 — Heap WAL parity + VM/FSM persistence

**Status:** accepted (2026-05-11)
**Branch:** `try-codex` (commits `2ba63a8`, `0afc743`, `4e621c5`)
**Predecessor:** M0079 (catalog DDL + btree WAL recovery)
**Drives:** PostgreSQL-aligned WAL parity for heap-side records;
persistence of optimisation metadata (Visibility Map + Free
Space Map) across restart.

## Context

After M0079 closed the btree-side gaps, an audit of goopg's
heap WAL surface against PostgreSQL's `heapam_xlog.h` exposed
two structural items:

1. `PageFreezeOldTuples` emitted FPI (8 KiB / page) instead of
   the logical `XLOG_HEAP2_FREEZE_PAGE` PostgreSQL uses.
2. Six PG record kinds had no goopg counterpart at all:
   `XLOG_HEAP_UPDATE` (atomic non-HOT),
   `XLOG_HEAP2_MULTI_INSERT`, `XLOG_HEAP2_VISIBLE`,
   `XLOG_BTREE_REUSE_PAGE`, `XLOG_BTREE_META_CLEANUP`, and
   the second tier of btree records carried over from M0079.

The persistence audit additionally flagged two ❌ gaps: goopg's
in-memory `VisibilityMap` and `FSM` were lost across restart.
PostgreSQL persists both via `pg_visibility` / `pg_freespace`
fork files.

## Sub-milestones

| # | Sub-milestone | Commit | Status |
| - | ------------- | ------ | ------ |
| 0001 | `RecordKindHeapFreeze` (logical vs FPI) | `2ba63a8` | accepted |
| 0002 | 5 new record kinds infra (HeapUpdate, MultiInsert, HeapVisible, BtreeReusePage, BtreeMetaCleanup) | `0afc743` | accepted |
| 0003 | VM persistence (`pg_vm_state.bin` save + load) | `4e621c5` | accepted |
| 0004 | FSM persistence (`pg_fsm_state.bin` save + load) | `4e621c5` | accepted |

## Design references

- `docs/design/0080-0001-heap-freeze-and-multi-insert-wal.md`
- `docs/design/0080-0002-remaining-pg-parity-records.md`

## Definition of Done

- ✅ VACUUM FREEZE emits `RecordKindHeapFreeze` (slot list +
  pd_lsn idempotent) instead of FPI per page.
- ✅ Five new record kinds (27-31) have encode/decode round-
  trip tests + length / wrong-kind guards.
- ✅ `HeapUpdate` + `HeapMultiInsert` replay reads page,
  applies mutation under pd_lsn idempotency, writes back.
- ✅ `HeapVisible` / `BtreeReusePage` / `BtreeMetaCleanup`
  replay routes to no-op (catalog/metadata-only — producers
  pending in M0081).
- ✅ `VisibilityMap` persisted at `<DataDir>/global/pg_vm_state.bin`
  via `os.CreateTemp + Sync + Rename`; loaded on startup.
- ✅ `FSM` persisted at `<DataDir>/global/pg_fsm_state.bin`
  via the same atomic pattern.
- ✅ Save on graceful shutdown (`cmd/goopg/main.go` defer);
  Load on startup (`internal/initdb/open.go::Open`).
- ✅ Deterministic save ordering (sort by (DBOid, RelOid)) so
  two saves of the same state produce byte-identical files.
- ✅ `go test ./...` PASS.

## Persistence audit close

After M0080, every PostgreSQL persistent-metadata surface has
a goopg counterpart:

| PG | goopg |
| -- | ----- |
| pg_xact (clog) | `internal/mvcc/clog.go` |
| pg_catalog heaps | `<DataDir>/global/pg_catalog.json` + heap relfiles + WAL records (M0079-0001) |
| pg_wal | `<DataDir>/pg_wal/...` |
| pg_replslot | `internal/wal/slots.go` (atomic temp+rename) |
| heap / index relfiles | `<DataDir>/base/<DBOid>/<RelOid>` |
| pg_visibility | `<DataDir>/global/pg_vm_state.bin` (M0080-0003) |
| pg_freespace | `<DataDir>/global/pg_fsm_state.bin` (M0080-0004) |
| pg_subtrans | rebuilt from `RecordKindXactAssignment` WAL records |

PostgreSQL features without a goopg counterpart (`pg_multixact`,
`pg_twophase`, `pg_commit_ts`) correspond to features goopg has
not yet implemented at the executor level; tracked as separate
milestones M0083+.

## Out of scope (deferred to M0081 / M0082)

- Producer wiring for HeapUpdate / HeapMultiInsert / HeapVisible
  (heap path) and BtreeReusePage / BtreeMetaCleanup (btree path).
- Per-relation VM / FSM fork files matching PostgreSQL's
  on-disk layout (M0082 candidate).

## References

- `internal/wal/recovery.go::RecordKindHeapFreeze` (26),
  `HeapUpdate` (27), `HeapMultiInsert` (28),
  `HeapVisible` (29), `BtreeReusePage` (30),
  `BtreeMetaCleanup` (31).
- `internal/storage/vm.go::Save / Load / VMStatePath`.
- `internal/storage/fsm.go::Save / Load / FSMStatePath`.
- `internal/initdb/open.go::Runtime.SaveVM / SaveFSM`.
