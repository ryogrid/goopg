# Design 0073-0002 — DecodeRowInto arena-aware path

**Milestone:** M0073-0002
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0073-0001 (Datum struct extension);
M0072-0004 (Arena type).

## Context

After M0073-0001 lands the type surface, the producer
side still emits non-arena Datums — `decodeValue` allocates
each varchar's bytes via `string(data[off:off+n])` which
goes through Go's runtime allocator. For Q5 / Q9 with
millions of decoded varchar columns, this is the dominant
heap source (`acquireRow` is 25.31 % cum heap on Q5; the
underlying `string()` conversions feed both `acquireRow`
in the Row pool and per-string allocations).

This design adds the arena parameter to `DecodeRowInto`
+ `decodeValue` so producers (seqScan / indexScan) can
bind a per-batch arena. Arena-backed Datums move all
varchar / bytes payload into the arena's `[]byte` page
slabs; one allocation per page (typically 64 KiB)
amortises across thousands of strings.

`DecodeRow` (the original API) stays unchanged for
backward compatibility with `internal/executor/copy_text.go`
and other non-bench callers — they continue to allocate
fresh Buf bytes.

## Goals

- Add optional `arena *Arena` parameter to
  `DecodeRowInto(dst Row, cols []catalog.Column, data
  []byte, arena *Arena) error`.
- Update `decodeValue` to accept an arena; varchar / char /
  bytea paths use `arena.Allocate(n)` + `copy` when arena
  != nil; emit `KindStringArena` / `KindBytesArena` Datum.
- When arena is nil, behaviour byte-for-byte unchanged.
- `DecodeRow` (the make-Row variant) wraps
  `DecodeRowInto(make(Row, ...), cols, data, nil)`.

## Non-goals

- **Arena binding in producers** (seqScan / indexScan).
  M0073-0004 wires the per-batch lifecycle.
- **Numeric / int / time arena backing.** Those columns
  store fixed-width data inline in Datum.Int / Big; no
  variable-length payload.
- **TOAST pointer arena backing.** TOAST pointers are
  12-byte fixed; no payload to arena-back. The actual
  detoasted bytes (when DetoastRow runs) come via the
  legacy heap path; arena Datums auto-promote on detoast.

## Proposed interface

```go
// DecodeRowInto decodes a row from the on-disk wire
// format into dst. When arena != nil, varchar / char /
// bytea columns are emitted as KindStringArena /
// KindBytesArena Datums backed by arena pages; this is
// the GC-friendly path used by seqScan / indexScan. When
// arena == nil, varchar columns are KindString backed by
// fresh make([]byte) — preserves backward compat for
// copy_text.go etc.
func DecodeRowInto(dst Row, cols []catalog.Column,
                   data []byte, arena *Arena) error
```

Inside `decodeValue` the varchar path:

```go
// Old:
val := string(data[off : off+n])
return NewStringDatum(val), n, nil

// New:
if arena != nil {
    buf := arena.Allocate(n)
    copy(buf, data[off:off+n])
    offset := arena.lastAllocateOffset()  // helper
    d := Datum{
        Kind:  KindStringArena,
        Int:   int64(offset)<<32 | int64(n),
        arena: arena,
    }
    return d, n, nil
}
val := string(data[off : off+n])
return NewStringDatum(val), n, nil
```

The `Arena.lastAllocateOffset()` helper (NEW with this
design) returns the absolute offset of the most recent
`Allocate` call within the arena's offset space. The
design exposes this so `decodeValue` can encode the
position into Datum.Int without exposing arena internals
to every caller.

Alternative: have `Allocate` return both `[]byte` and
`int` (offset). This is cleaner; the design picks this
form:

```go
// Allocate returns the slice + the absolute offset.
func (a *Arena) Allocate(n int) (buf []byte, offset int)
```

This is a breaking change to the M0072-0004 signature.
Mitigated by: `Allocate` is only called from M0073-0002
+ tests; the arena_test.go from M0072-0004 needs
updating to capture the new return.

## Migration plan

Single commit (Commit D of the M0073 plan), combined with
M0073-0004 (the producer wiring):

1. Update `Arena.Allocate` to return `(buf, offset)`.
2. Update `DecodeRowInto(dst, cols, data, arena)`
   signature.
3. Update `decodeValue(t, data, arena)` to dispatch on
   arena-presence per column.
4. Update internal `DecodeRow` to call `DecodeRowInto`
   with `arena=nil`.
5. Update arena_test.go to match the new Allocate
   signature.

## Verification

**Pre-commit gate** (combined with M0073-0004):
- Q12=2 / Q13=35 / Q21=381 / Q9=175 rows preserved.
- Q9 wall ≤ 600 s target (compression).
- `go test ./internal/executor/...` PASS.
- 21-query sweep row-counts unchanged.

**Q5 heap pprof rerun:** `acquireRow` cum ≤ 5 % (was
25.31 %). Total Q5 heap ≤ 1.0 TB (was 1.46 TB).

**TOAST interaction test:** decode a toasted column;
verify `DetoastRow` produces a regular `KindString` /
`KindBytes` Datum (not arena-backed) so the detoasted
bytes outlive the arena's Reset.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | DecodeRowInto signature change breaks copy_text.go / other callers | All callers pass `nil` for arena initially; only seqScan / indexScan opt in (M0073-0004). |
| R2 | Arena.Allocate signature change breaks the M0072-0004 arena_test.go | Update that test atomically with this commit; the test isn't externally consumed. |
| R3 | DetoastRow returns arena-backed Datum (lifetime mismatch) | DetoastRow path unchanged — produces Buf-backed Datum. New test pins the contract. |
| R4 | TOAST pointer column with arena-on causes confusion | TOAST is fixed-width 12 B emitted from `data[off:off+12]`; no varchar path; arena untouched. |
| R5 | Numeric text-string columns (rare path) emit arena Datum but may not roundtrip through later Buf-only callers | Fall back to non-arena path for non-varchar/non-bytea columns; only types whose decoded payload is Buf-only use arena. |

## References

- `internal/executor/codec.go:172-203` — DecodeRowInto.
- `internal/executor/codec.go:293-354` — decodeValue.
- `internal/executor/codec.go:57` — DecodeRow public API.
- `internal/executor/arena.go` — Arena type
  (M0072-0004, `b081767`).
- `docs/design/0068-0003-batch-string-arena.md` — original
  arena design.
- `docs/design/0073-0001-datum-arena-field.md` — Datum
  struct extension that this design produces values for.
