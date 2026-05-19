# M0107-0002 — Datum 48 B: ArenaID + KindArena Merge

**Status**: accepted  
**Milestone**: M0107 Performance Optimization Refactor  
**Filed**: 2026-05-20

## Summary

Reduce `Datum` struct from 64 B to 48 B by:
1. Changing `DatumKind` from `int` (8 B) to `uint8` (1 B)
2. Replacing `mctx *mctx.Context` (8 B pointer) with `ArenaID mctx.ContextID` (2 B uint16)
3. Removing `Big *big.Int` (8 B pointer) — big numerics stored in mctx
4. Adding `Flags uint8`, `Hi uint64` fields for future use
5. Merging `KindStringArena`/`KindBytesArena` into `KindString`/`KindBytes`

The key GC improvement: hot-path arena-backed KindString Datums (produced by
seqScan/indexScan via `DecodeRowIntoMctx`) had `mctx != nil` → 1 GC-traced
pointer per Datum. After this change, `ArenaID != 0` (uint16, not a pointer)
→ **0 GC-traced pointers** for the hot path. Per-row pointer count drops from
~1 pointer/string to 0. Cold-path `NewStringDatum(s)` still uses `Buf []byte`
(ArenaID=0) which has 1 GC-traced pointer, but that path is not on the
pgbench critical path.

## Before (64 B)

```go
type DatumKind int   // 8 B

type Datum struct {
    Kind  DatumKind       // 8 B
    Int   int64           // 8 B
    Buf   []byte          // 24 B (GC-traced pointer)
    Big   *big.Int        // 8 B  (GC-traced pointer)
    Scale int16           // 2 B
    mctx  *mctx.Context   // 8 B  (GC-traced pointer)
    // 6 B padding → 64 B
}
// 3 GC-traced fields per Datum
```

## After (48 B)

```go
type DatumKind uint8  // 1 B

type Datum struct {
    Kind    DatumKind      // 1 B @ 0
    Flags   uint8          // 1 B @ 1
    ArenaID mctx.ContextID // 2 B @ 2  (uint16; 0 = no mctx payload)
    Scale   int16          // 2 B @ 4
    _pad0   [2]byte        // 2 B @ 6
    Int     int64          // 8 B @ 8
    Buf     []byte         // 24 B @ 16 (nil for arena-backed)
    Hi      uint64         // 8 B @ 40
    // Total: 48 B
}
// GC-traced fields: Buf only (nil for hot-path arena rows → 0 scans)
```

## KindString/KindBytes encoding (merged arena variants)

| Condition | Meaning | Buf | ArenaID | Int |
|-----------|---------|-----|---------|-----|
| `ArenaID == 0` | Buf-backed (cold path) | valid slice | 0 | 0 |
| `ArenaID != 0` | mctx-backed (hot path) | nil | context ID | `offset<<32\|length` |

`KindStringArena` and `KindBytesArena` constants are **removed**. All
arena-backed strings now use `KindString` with `ArenaID != 0`. This simplifies
all `switch d.Kind` sites that previously needed both cases.

## Big Numeric encoding

`Big *big.Int` field removed. Big-numeric datums use:
- `Flags |= flagBigNumeric`
- `ArenaID` = mctx context ID (uses `mctx.Perm()` for now)
- `Int = offset<<32 | length` — points to sign(1B) + BE-magnitude in mctx

`NumericBigValue()` decodes on demand: `mctx.Lookup(d.ArenaID).Bytes(...)` →
`new(big.Int).SetBytes(magnitude)` with sign restoration. Rare path; hot
numeric arithmetic uses the int64 fast path (no Big involvement).

## Files changed

- `internal/executor/datum.go` — struct definition, all constructors, all accessors
- `internal/executor/codec.go` — KindStringArena/KindBytesArena → KindString/KindBytes
- `internal/executor/expr.go` — Big negation path, Arena Kind references
- `internal/executor/numeric.go` — big numeric creation → mctx.Perm()
- `internal/executor/spill.go`, `toast.go`, `copy_binary.go`, `copy_text.go` — Arena Kind refs
- `internal/executor/operators_ddl.go`, `operators_join_agg.go`, `plpgsql_runtime.go` — Arena refs
- `internal/executor/applyworker.go` — Arena Kind refs
- `internal/server/dispatch.go` — KindStringArena reference
- Tests: `datum_arena_test.go`, `codec_projection_arena_test.go`, `numeric_*_test.go`

## Verification

- `go test -count=1 -race ./internal/executor/ ./internal/storage/ ./internal/server/ ./internal/mvcc/ ./internal/wal/ ./internal/planner/ ./internal/parser/ ./internal/analyzer/ ./internal/mctx/ ./internal/access/btree/` → all PASS
- `unsafe.Sizeof(Datum{}) == 48` asserted in datum.go
- `make ralph-state-guard` → OK

## Pre-existing failures (not caused by this change)

- `internal/initdb/` — several tests fail because `bootstrapPgTypeTuples` (M0106)
  overwrites block 0 with PG-format rows while `TestBootstrappedPGTypeRowsReadable`
  expects the legacy custom-format rows. Pre-existing since M0106 work; the tests
  couldn't run before because `wal/checkpointer.go` had an unstaged fix.
- `internal/testutil/tpch/` parity tests — pre-existing `decodePhysicalPGValueMctx`
  missing "numeric" case; unrelated to this change.
