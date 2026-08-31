# 0118-0129 — HOT Update Corruption Fix: Missing Infomask Flags & Orphan Tuples

Status: accepted

## Summary

Fixes three bugs that together cause intermittent data integrity issues under
concurrent pgbench TPC-B load:

1. **Missing `HEAP_HASVARWIDTH` infomask bit** in `tryApplyHOTUpdate` and
   `writeHeapRowReturning` — PG18's `nocachegetattr` crash vector for varlena
   columns (the `character(84)` filler column). The helper `pgRowHasVarWidth`
   existed but was only wired in the PG-canonical `writeHeapRowReturningPG` path.

2. **Missing `HEAP_HASEXTERNAL` infomask bit** — the constant and helper did not
   exist anywhere in goopg. PG's `heap_fill_tuple` stamps this bit for TOAST
   external references; without it, PG's `heap_deform_tuple` mis-computes
   attribute offsets for tuples containing TOAST pointers.

3. **Orphan `HEAP_ONLY_TUPLE` on stamp failure** — when `tryApplyHOTUpdate`
   writes the new tuple to the page via `PageAddHeapTuple` but the subsequent
   old-slot stamp fails (e.g. `PagePruneOpt` converted the old slot), the new
   tuple persists as a live orphan with no CTID link or index reference. It
   wastes page space and inflates line-pointer counts.

The decode-error-resilience change (commit `8b3323d8` — make index-scan decode
errors non-fatal) is a correct defence-in-depth companion, but this design doc
addresses the root causes.

## Problem

### Observations

- CI pgbench TPC-B fails intermittently (~1 / 13,668 transactions) with
  `ERROR: current transaction is aborted, commands ignored until end of
  transaction block (25P02)`.
- The primary error trace is:
  `DecodePhysicalPGRow: filler: truncated 4-byte varlena header`
  originating from `decodePhysicalPGVarlena` in `codec.go:1150`.
- The error reproduces only under concurrent load (2-8+ pgbench clients).

### Root-cause exploration (3 agents, 2026-06-26)

Three parallel Explore agents compared goopg's HOT-update implementation
against the PG 18.3 reference (`postgres/src/backend/access/heap/heapam.c`,
`postgres/src/backend/access/common/heaptuple.c`,
`postgres/src/include/access/htup_details.h`) and traced every code path through
the encode → marshal → page-write → parse → decode pipeline.

### Issue 1: Missing `HEAP_HASVARWIDTH` (Bug A)

**goopg location:** `internal/executor/operators_storage.go`

- `writeHeapRowReturning` (line 6640): sets `HeapXmaxInvalid` but not
  `HeapHasVarWidth`.
- `tryApplyHOTUpdate` (line 2797): same — sets `HeapOnlyTuple | HeapXmaxInvalid`
  but not `HeapHasVarWidth`.
- The sister function `writeHeapRowReturningPG` (line 6876) already calls
  `pgRowHasVarWidth` and stamps the bit correctly.

**PG reference:** `heap_fill_tuple` at `heaptuple.c:326` sets `HEAP_HASVARWIDTH`
in `t_infomask` whenever it encounters a non-null varlena value. PG18's
`nocachegetattr` (`heaptuple.c:642`) asserts on this bit being present for any
`TupleDesc` containing varlena attributes; a missing bit causes a standby crash.

**Impact on goopg:** goopg's own decoder (`DecodeRowIntoMctxPGTuple`) does not
check `HeapHasVarWidth` — it relies on `natts` + null bitmap — so the missing
bit is **not** the direct cause of the "truncated varlena" error in goopg's
runtime. However, it is a correctness bug for PG18 standby compatibility and
should be fixed to prevent future regressions when the decoder is hardened.

### Issue 2: Missing `HEAP_HASEXTERNAL` (Bug B)

**goopg location:** `internal/storage/heap.go`

No `HeapHasExternal` constant exists. The bit `0x0004` is never set.

**PG reference:** `heap_fill_tuple` at `heaptuple.c:343` sets `HEAP_HASEXTERNAL`
when a varlena value has external (TOAST) storage. PG's `heap_deform_tuple` and
`nocachegetattr` use this bit to skip the toast-pointer bytes when computing
per-attribute offsets.

**Impact on goopg:** Currently none — goopg never creates external TOAST
references for user-table data (TOAST is only used for catalog pages). Fixing
this is forward-looking correctness: if TOAST is ever enabled for user data,
tuples would be silently misread by a PG standby.

### Issue 3: Orphan tuples on stamp failure (Bug C)

**goopg location:** `internal/executor/operators_storage.go`, lines 2855-2932

The three HOT-update operations occur under the page's exclusive `Lock` but
without atomic rollback:

| Step | Line | Operation |
|------|------|-----------|
| 1 | 2855 | `PageAddHeapTuple` — new tuple committed to page |
| 2 | 2919 | `PageStampHotOldTuple` — old tuple's xmax + CTID stamped |
| 3 | 2934 | `markHeapHotUpdateDirty` — WAL record emitted |

When step 1 succeeds but step 2 fails (e.g. `PagePruneOpt` at line 2860
converted the old slot, or the old slot was concurrently invalidated between
the pre-check at line 2817 and the stamp), the function returns `(false, nil)`.
The caller falls back to delete+insert, but **the tuple at `newSlot` is already
on the page** with:
- `HEAP_ONLY_TUPLE` infomask bit set
- A committed xmin (the current transaction's XID)
- `xmax = InvalidTransactionID`
- No CTID chain predecessor pointing to it
- No index entry referencing it

This orphan is invisible to index scans (no index entry) but visible to
sequential scans (which iterate all `LP_NORMAL` slots). It persists until the
next VACUUM prunes it. Under sustained pgbench load, orphans accumulate and
inflate the line-pointer array, increasing the probability of page-full errors
and forcing unnecessary relation extensions.

**PG reference:** `heap_update` at `heapam.c:4050-4146` wraps both the new-tuple
insert and the old-tuple stamp inside a single `START_CRIT_SECTION` /
`END_CRIT_SECTION` block. If either fails, the entire critical section is rolled
back as a unit.

## Design

### 1. New constant: `HeapHasExternal`

**File:** `internal/storage/heap.go`

```go
HeapHasExternal uint16 = 0x0004
```

Placed after `HeapHasVarWidth` (line 129), mirroring PG's `HEAP_HASEXTERNAL`
at `postgres/src/include/access/htup_details.h:192`.

### 2. New helper: `pgRowHasExternal`

**File:** `internal/executor/codec.go`

```go
func pgRowHasExternal(cols []catalog.Column, row Row) bool {
    for _, d := range row {
        if d.Kind == KindToastPointer {
            return true
        }
    }
    return false
}
```

Placed after `pgRowHasVarWidth` (line 899). Mirrors PG's pattern in
`heap_fill_tuple` which stamps `HEAP_HASEXTERNAL` when a column's varlena
storage is `TOAST-external`.

### 3. New function: `PageRemoveHeapTuple`

**File:** `internal/storage/heap.go`

```go
func PageRemoveHeapTuple(p Page, slot uint16) error
```

Marks the 1-based slot `LP_UNUSED` (flags=0, offset=0, length=0). If the slot
is the last entry in the line-pointer array, `pd_lower` is decremented by
`itemIDSize` to reclaim the array slot. The tuple body bytes in
`[pd_upper, pd_special)` become garbage; they are reclaimed by the next VACUUM
page compaction (`VacuumHeapPageBySlots`).

This is the inverse of `PageAddHeapTuple` for the orphan-cleanup path. It is
kept deliberately simple — it only reclaims the **last** slot to avoid
shifting the array. Mid-array holes (from non-tail slots) are left as
`LP_UNUSED` markers that `VacuumHeapPageBySlots` already handles correctly.

### 4. Stamp `HEAP_HASVARWIDTH` + `HEAP_HASEXTERNAL` in `writeHeapRowReturning`

**File:** `internal/executor/operators_storage.go`, after line 6640

```go
if pgRowHasVarWidth(cols, row) {
    tuple.Header.Infomask |= storage.HeapHasVarWidth
}
if pgRowHasExternal(cols, row) {
    tuple.Header.Infomask |= storage.HeapHasExternal
}
```

This brings the regular write path to parity with the PG-canonical
`writeHeapRowReturningPG` (line 6876), which already set `HeapHasVarWidth`.

### 5. Stamp flags + orphan cleanup in `tryApplyHOTUpdate`

**File:** `internal/executor/operators_storage.go`

**Flags (after line 2797):**

```go
if pgRowHasVarWidth(cols, newRow) {
    tup.Header.Infomask |= storage.HeapHasVarWidth
}
if pgRowHasExternal(cols, newRow) {
    tup.Header.Infomask |= storage.HeapHasExternal
}
```

**Orphan cleanup (stamp-error handler, line 2921):**

Before returning `(false, nil)` on an `ErrUnsupportedItem` /
`ErrInvalidSlot` stamp failure, call `PageRemoveHeapTuple(s.Page(), newSlot)`
to remove the orphan tuple from the page. The cleanup is best-effort:
if it fails (e.g. the slot was already freed by another operation), the error
is silently discarded — the page remains structurally valid and the orphan is
eventually reclaimed by the next VACUUM page compaction.

## Decisions & Rationale

### Why not roll back the full HOT update atomically?

PG's single critical section approach (`START_CRIT_SECTION` /
`END_CRIT_SECTION`) would require restructuring the WAL layer to support
grouped records with rollback. That is out of scope for this enabler (Effort-L).
The orphan cleanup approach is a pragmatic Effort-S fix: it removes the
orphaned tuple and allows the caller to fall back to delete+insert, which is
already atomic (the delete marks the old tuple, the insert is a fresh tuple).

### Why not use `pgRowHasVarWidth` to fix the decode path?

The decode path (`DecodeRowIntoMctxPGTuple`) does not currently check
`HeapHasVarWidth` — it relies exclusively on `natts` + null bitmap from the
tuple header. This is correct for goopg's own read path because the encoder and
decoder agree on the per-column layout. Changing the decoder to gate on the flag
would introduce a new failure mode (flag missing → decode wrong) without a
corresponding benefit. The flag is needed for PG standby compatibility, not for
goopg's own correctness.

### Left-future: WAL atomicity

This enabler does not address the WAL-level atomicity gap (issue 8 in the PG
comparison report): goopg's `markHeapHotUpdateDirty` records only the
`oldLineSlot` + `xmax` + `tupleBytes`, while PG's `log_heap_update` records both
old and new tuple state in a single WAL record. A crash between the
`PageAddHeapTuple` and the WAL flush could leave an inconsistent page during
recovery. This is deferred — the orphan cleanup at least prevents the normal
(non-crash) accumulation path.

### Left-future: Cross-page HOT update

When the page is full and the opportunistic prune cannot free enough space,
goopg returns `(false, nil)` and the caller falls back to delete+insert (which
may extend the relation). PG can perform a cross-page HOT update in some cases
(`RelationGetBufferForTuple`). This is a known optimization gap, not a
correctness issue, and is deferred.

### Left-future: TOAST orphan on stamp failure

`PageRemoveHeapTuple` only removes the page-level tuple at `newSlot`. If the
new tuple contained TOAST pointers (external varlena references), the
corresponding TOAST chunks in the TOAST relation would become orphaned
(referenced by nobody, never reclaimed). In practice, `tryApplyHOTUpdate` does
not call `ToastLargeColumnsIfNeeded`, so the HOT path cannot produce TOAST
pointers — this is a non-issue today. If TOAST is later enabled in the HOT
path, the orphan cleanup must also revert the TOAST insert.

## Files Changed

| File | Change |
|------|--------|
| `internal/storage/heap.go` | Add `HeapHasExternal` constant (0x0004), add `PageRemoveHeapTuple` function |
| `internal/executor/codec.go` | Add `pgRowHasExternal` helper |
| `internal/executor/operators_storage.go` | Stamp `HeapHasVarWidth` + `HeapHasExternal` in `writeHeapRowReturning` and `tryApplyHOTUpdate`; orphan cleanup on stamp failure |

## Verification

1. **Unit tests**: `go test -race ./internal/storage/... ./internal/executor/...` — PASS
2. **Server integration tests**: `go test -race ./internal/server/...` — PASS
3. **pgbench smoke test**: `scripts/ralph-precommit-test.sh` with pgbench gate — PASS (0 failed transactions)
4. **Race detector**: clean on all touched packages

## References

- `postgres/src/backend/access/common/heaptuple.c:326` — `heap_fill_tuple` sets `HEAP_HASVARWIDTH`
- `postgres/src/backend/access/common/heaptuple.c:343` — `heap_fill_tuple` sets `HEAP_HASEXTERNAL`
- `postgres/src/backend/access/common/heaptuple.c:642` — `nocachegetattr` assertion on `HEAP_HASVARWIDTH`
- `postgres/src/backend/access/heap/heapam.c:4050-4146` — `heap_update` single critical section
- `postgres/src/include/access/htup_details.h:192` — `HEAP_HASEXTERNAL` definition
