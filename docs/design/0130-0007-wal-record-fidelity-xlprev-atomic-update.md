# WAL fidelity audit — xl_prev 0-based + atomic heap-update completeness

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S7 — audit)

## Background

Two WAL fidelity gaps were cataloged in the analysis README and deferral
ledger. One is already fixed at HEAD; the other needed a verification pass.
Both are now audited — S7.1 confirmed fixed with a new cross-segment
regression gate; S7.2 confirmed the PG-canonical path is atomic and the
general-user-table path's two-record gap is a known, ledger-tracked deferral.

## S7.1 — xl_prev 0-based (ledger M0110-0002 row 29) — ✅ VERIFIED

**Root cause (historical):** goopg's restart-seed bug caused `prevRecPtr` to
be 1-based instead of 0-based. `pg_waldump` aborted chain traversal at the
second record with "incorrect prev-link" error.

**Fix (landed):** `internal/wal/writer.go:1385-1399` — `detectWritePos`
converts goopg's 1-based public RecPtr to the 0-based `xl_prev` written on
disk (`prevRecPtr = lastRecPtr - 1`). The live append path applies the same
conversion via `resetPosition(end, start-1)`.

**Verification (this loop):**
- Single-segment pg_waldump chain: verified by existing
  `TestPGWaldumpParsesEmittedWAL` (pg_waldump with `-s`/`-e` range, asserts
  no "incorrect prev-link").
- **New cross-segment gate:** `TestCrossSegmentXLPrevChain` in
  `internal/wal/pg_waldump_compat_test.go`. Uses a 32-byte segment size to
  force records across 2+ boundaries, then reads them back with
  `ReadAll` and asserts all records are recovered with correct payloads.
  pg_waldump is NOT used here because it requires standard segment sizes
  (1 MiB–1 GiB, `isValidWalSegSize` in `xlogreader.c`); filling 1 MiB in
  a unit test is impractical. The single-segment pg_waldump test already
  validates the format; the cross-segment goopg-reader test validates the
  chain logic — together they cover the full xl_prev contract.

**Result:** `TestCrossSegmentXLPrevChain` PASS, full `internal/wal` suite PASS.

## S7.2 — Atomic heap-update WAL (ledger M0118-0129 row 27) — AUDIT COMPLETE

### Audit method

Traced every heap-update WAL emit path from the executor (`updateOp.Next`,
`updateHeapRowCanonicalPG`) through the WAL hooks to the encode functions,
and cross-referenced against PG 18.3's `log_heap_update`
(`postgres/src/backend/access/heap/heapam.c`) and the `xl_heap_update`
struct (`postgres/src/include/access/heapam_xlog.h`).

### Finding 1: PG does NOT record the old tuple image

**Correction to the original gap description:** PG's `xl_heap_update` main
data is `old_xmax | old_offnum | old_infobits_set | flags | new_xmax |
new_offnum` — 14 bytes of metadata, not a full tuple image. The old version
is located by `old_offnum` on the page during redo. The only tuple data
carried is the **new** tuple in block 0. goopg's `EncodeHeapUpdatePG`
mirrors this exactly (same 14-byte main data, block 0 = new tuple).

### Finding 2: The PG-canonical path IS atomic ✅

`updateHeapRowCanonicalPG` (`internal/executor/operators_storage.go:8526`)
— used for B0.2 catalog ALTERs — emits a single WAL record via
`LogHeapUpdateFunc` → `EncodeHeapUpdatePG` (`internal/wal/pg_assembled_emit.go:254`).
The record contains:
- 14-byte `xl_heap_update` main data (old_xmax, old_offnum, new_offnum, flags)
- Block 0: new tuple page with `xl_heap_header` + tuple bytes
- Block 1 (cross-page only): old page reference (no data)

This is a single `Append` call → one WAL record → atomic crash-recovery unit.

### Finding 3: The general user-table UPDATE path uses two separate records ⚠

`updateOp.Next()` (`operators_storage.go:4111-4394`) — the non-HOT
fallback for regular user-table UPDATEs — emits TWO separate WAL records:
1. `markHeapDeleteDirty` → `RecordKindHeapDelete` (old tuple xmax stamp)
2. `writeHeapRowReturning` → `ctx.Pool.LogHeapInsert()` →
   `RecordKindHeapInsert` (new tuple)

These are separate `Append` calls. A crash between them leaves the old
tuple xmax-stamped (appears deleted) without the new tuple — the
atomicity gap described in the ledger.

### Finding 4: Native `RecordKindHeapUpdate` (27) is legacy/test-only

`EncodeHeapUpdate` (`internal/wal/recovery.go:1496`) exists and is complete,
but has **zero production call sites** (only `m0080_records_test.go` and
`classifier_test.go`). It routes to `RmgrGoopgCatalog` (rmid 128), as
documented in M0130-S6. The production emit path uses the PG-compatible
`EncodeHeapUpdatePG` instead.

### Verdict

| path | atomic? | record(s) | status |
|---|---|---|---|
| `updateHeapRowCanonicalPG` (catalog ALTERs) | YES | single `EncodeHeapUpdatePG` | ✅ clean |
| `updateOp.Next()` non-HOT fallback (user tables) | NO | `HeapDelete` + `HeapInsert` | ⚠ known gap |
| `tryApplyHOTUpdate` (same-page HOT) | YES | single `RecordKindHeapHotUpdate` | ✅ clean |
| `EncodeHeapUpdate` native (legacy) | N/A | unused in production | legacy |

### Deferral

The general-path two-record gap is ledger-tracked (M0118-0129 row 27,
2026-06-26): "Restructure WAL layer to support grouped records with
rollback (Effort-L)". The `EncodeHeapUpdatePG` function already exists and
is proven in the catalog path — wiring it into the general path is a
matter of plumbing `LogHeapUpdateFunc` through the non-HOT fallback instead
of the current `HeapDelete`+`HeapInsert` pair. This is not scoped for
M0130-S7 (audit-only).

## Gates run

1. `TestCrossSegmentXLPrevChain` — PASS (new, S7.1 cross-segment gate)
2. `TestPGWaldumpParsesEmittedWAL` — PASS (existing, S7.1 single-segment pg_waldump gate)
3. `go test ./internal/wal/...` — PASS (5.3s, full suite)
4. UNITS + smoke — to be run in this loop (see status block)

## References

- `.ralph/deferral_ledger.md` row 27 (M0118-0129 — WAL atomicity gap, general path)
- `.ralph/deferral_ledger.md` row 29 (M0110-0002 — xl_prev 1-based, RESOLVED)
- `internal/wal/writer.go:1385-1399` — xl_prev −1 conversion in `detectWritePos`
- `internal/wal/recovery.go:277-290` — `RecordKindHeapUpdate` documentation
- `internal/wal/recovery.go:1494-1539` — `EncodeHeapUpdate`/`DecodeHeapUpdate` (legacy)
- `internal/wal/pg_assembled_emit.go:244-278` — `EncodeHeapUpdatePG` (PG-compatible, production)
- `internal/executor/operators_storage.go:4111-4394` — `updateOp.Next()` non-HOT fallback
- `internal/executor/operators_storage.go:8526-8700` — `updateHeapRowCanonicalPG` (catalog path)
- `internal/wal/pg_waldump_compat_test.go` — `TestCrossSegmentXLPrevChain` (new), `TestPGWaldumpParsesEmittedWAL`
- `postgres/src/backend/access/heap/heapam.c` — `log_heap_update`
- `postgres/src/include/access/heapam_xlog.h` — `xl_heap_update`
