# WAL fidelity audit — xl_prev 0-based + atomic heap-update completeness

**Status:** draft
**Date:** 2026-08-09
**Milestone:** M0130 (S7 — audit)

## Background

Two WAL fidelity gaps were cataloged in the analysis README and deferral
ledger. One is already fixed at HEAD; the other needs a verification pass.

## S7.1 — xl_prev 0-based (ledger #29) — ✅ FIXED at HEAD

**Root cause (historical):** goopg's restart-seed bug caused `prevRecPtr` to
be 1-based instead of 0-based. `pg_waldump` aborted chain traversal at the
second record with "incorrect prev-link" error.

**Fix (landed):** `internal/wal/writer.go` documents the −1 conversion:
"Without this, the first record appended after a restart/boot inherits a +1
xl_prev and pg_waldump aborts the chain with 'incorrect prev-link'." The
first record's `xl_prev` is set to `InvalidXLogRecPtr` (0) and subsequent
records chain with 0-based LSN values.

**Verification task:** Confirm `pg_waldump` from `./postgres/` oracle parses
a full WAL segment chain across a segment boundary without errors. This is
already verified by the existing `WALPgWaldumpCompat` test family — the
M0130 task adds explicit cross-segment-boundary coverage if missing.

## S7.2 — Atomic heap-update WAL (ledger #27) — AUDIT NEEDED

**Gap (as cataloged):** goopg's heap update path was described as calling
`PageAddHeapTuple` then separately emitting a WAL record. PG records both
old+new tuple data in a single atomic `xl_heap_update` record.

**Status at HEAD:** `internal/wal/recovery.go:277-290` documents
`RecordKindHeapUpdate` as "the M0080-0002 atomic non-HOT …
XLOG_HEAP_UPDATE". The recovery path handles both old and new tuple images
in a single record. The emit path may already produce pre-assembled
envelopes that match PG's `log_heap_update` semantics.

**Verification tasks:**
1. Trace the heap-update emit path from `internal/storage/heap.go` through
   the WAL writer. Confirm the emit order is: build new tuple → emit
   `xl_heap_update` with old+new images → flush WAL → write new tuple to
   page → set old tuple xmax.
2. If the order is correct and both images are in a single record: document
   the verified-clean state; S7.2 is DONE.
3. If a gap remains (separate page mutation and WAL emit, or missing old
   tuple image): scope the fix. PG's `log_heap_update` in
   `postgres/src/backend/access/heap/heapam.c` is the reference.

**Crash consistency test (regardless of audit outcome):**
- Kill server (`SIGKILL`) during heavy UPDATE workload.
- Restart and verify no inconsistent pages (no old xmax set but new tuple
  missing, or vice versa).

## Gates

1. `pg_waldump` chain traversal across a segment boundary — no errors (S7.1).
2. Crash-recovery test: kill during UPDATE, restart, verify clean (S7.2).
3. Standby UPDATE replay: updated rows visible on PG standby.
4. UNITS + `WALPgWaldumpCompat` green.

## References

- `.ralph/deferral_ledger.md` #27 (WAL atomicity gap), #29 (xl_prev 1-based — RESOLVED)
- `internal/wal/writer.go:1385-1395` — xl_prev −1 conversion
- `internal/wal/recovery.go:277-290` — `RecordKindHeapUpdate` documentation
- `internal/storage/heap.go` — heap update path
- `postgres/src/backend/access/heap/heapam.c` — `log_heap_update`
- `postgres/src/include/access/heapam_xlog.h` — `xl_heap_update`
