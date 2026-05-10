# Design 0073-0001 — Datum.arena field + KindStringArena/BytesArena variants

**Milestone:** M0073-0001
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** `docs/design/0068-0003-batch-string-arena.md`
(arena design); `docs/design/0068-0001-datum-compact-layout.md`
(Datum struct precedent); M0072-0004 commit `b081767`
(Arena type landed but uncalled).

## Context

The Q5 / Q9 heap profile post-M0072-final shows
`acquireRow` at 25.31 % cumulative — a single column-pool
allocation per cloneRow on append in `indexScanOp.scanFn`
(commit `c16f3f2`). Q9's correct row set is ~25 × larger
than pre-fix (175 vs 7 rows), so this allocator scales
linearly with the new workload.

The `Arena` type from `internal/executor/arena.go`
(M0072-0004, `b081767`) provides per-batch byte-slab
allocation but is currently uncalled — its integration
into the Datum pipeline was the deferred half of
M0072-0004.

This design takes step 1: extend `Datum` to carry an
`*Arena` pointer + add new `Kind` variants, **without
producer wiring**. Existing callers see no behavioural
change; the type surface is staged for M0073-0002 +
M0073-0004 to use.

## Goals

- Add `arena *Arena` field to `Datum` struct.
  `unsafe.Sizeof(Datum{}) == 64` exactly (compile-time
  assertion preserved).
- Add `KindStringArena`, `KindBytesArena` to `DatumKind`
  enum.
- Update `Datum.StringValue() / BytesValue()` accessors so
  arena-backed Datums return the same `string` / `[]byte`
  view as the existing `KindString` / `KindBytes` paths
  (callers can't tell which storage is in use).
- Add `Arena.Bytes(offset, length int) []byte` accessor
  (specified in design 0068-0003 but omitted in
  M0072-0004). Resolves the page-relative offset back to
  the byte slice.

## Non-goals

- **Producer wiring.** `DecodeRowInto` is unchanged;
  varchar / bytes columns still emit `KindString` /
  `KindBytes` Datums backed by `make([]byte)`.
  M0073-0002 wires arena.
- **Materialize promotion side effect.** Without producer
  wiring, no Datum in production flow is arena-backed
  yet, so Materialize is unchanged. M0073-0004 flips it.
- **Replacing Buf entirely** (Option B from exploration:
  `Buf []byte` → `(arenaRef int32, offset int32, length
  int32)` packed). Defers to M0074 when Datum needs more
  fields.

## Proposed layout

Current Datum (56 B with 8 B padding, internal/executor/datum.go:85-91):

```go
type Datum struct {
    Kind  DatumKind // 8 B
    Int   int64     // 8 B
    Buf   []byte    // 24 B (slice header)
    Big   *big.Int  // 8 B
    Scale int16     // 2 B
}
```

M0073-0001 layout (64 B exact, 0 B padding):

```go
type Datum struct {
    Kind  DatumKind // 8 B
    Int   int64     // 8 B (also: arena offset/length packed
                    //         when Kind == KindStringArena /
                    //         KindBytesArena — see encoding)
    Buf   []byte    // 24 B (preserved for non-arena variants)
    Big   *big.Int  // 8 B
    Scale int16     // 2 B
    arena *Arena    // 8 B (nil for non-arena Datums)
                    // 6 B padding → 64 B exact
}
```

The compile-time assertion at `internal/executor/datum.go:98`
becomes `64 - unsafe.Sizeof(Datum{}) == 0`. No padding
headroom remains; future field additions trigger the
M0074 packed-layout work.

### Arena offset/length encoding

Arena-backed Datums encode `(offset, length)` into the
existing `Int int64` field:

- High 32 bits = arena offset
- Low 32 bits = byte length

This avoids growing Datum further. The 32-bit limits
match practical TPC-H column widths (single column ≤ 4 GB,
arena page-relative offset ≤ 4 GB).

### Accessor surface

```go
func (d Datum) StringValue() string {
    if d.Kind == KindStringArena {
        offset := int(d.Int >> 32)
        length := int(d.Int & 0xFFFFFFFF)
        buf := d.arena.Bytes(offset, length)
        return unsafe.String(unsafe.SliceData(buf), len(buf))
    }
    return unsafe.String(unsafe.SliceData(d.Buf), len(d.Buf))
}

func (d Datum) BytesValue() []byte {
    if d.Kind == KindBytesArena {
        offset := int(d.Int >> 32)
        length := int(d.Int & 0xFFFFFFFF)
        return d.arena.Bytes(offset, length)
    }
    return d.Buf
}
```

`d.arena` is non-nil when Kind is one of the arena
variants. Callers that read `Datum.Buf` directly (e.g.
`btree.EncodeVarchar([]byte(d.StringValue()))`) work
unchanged because they go through StringValue().

`Arena.Bytes(offset, length)` (NEW in M0073-0001) walks
`a.pages[]` to find the page containing `offset` and
returns the slice. Per the design 0068-0003, oversized
allocations get dedicated pages; the implementation
mirrors `Allocate` in reverse.

## Migration plan

Single commit (Commit C of the M0073 plan):

1. Add `arena *Arena` to `Datum`. Compile-time assertion
   updates from `64 - sizeof` to `64 - sizeof == 0`.
2. Add `KindStringArena`, `KindBytesArena` to
   `DatumKind`. Update `IsString()` / `IsBytes()`
   helpers to include them.
3. Update `StringValue()` / `BytesValue()` to dispatch on
   Kind.
4. Add `Arena.Bytes(offset, length) []byte` accessor.
5. New tests pin (a) struct size, (b) round-trip read,
   (c) Materialize promotion contract (covered by
   M0073-0004 but stub the test now).

No producer or consumer changes — this is type surface
only.

## Verification

**Pre-commit gate:**
- Build server, fresh-restart.
- `./tpch-runner --queries=12,13,21
  --per-query-timeout=400s --cancel-after=380s` —
  Q12=2, Q13=35, Q21≥100. **Unchanged from M0072.**
- `./tpch-runner --queries=9 --per-query-timeout=1200s
  --cancel-after=1100s` — Q9=175 rows, wall ≤ 1100 s.
  **Unchanged from M0072.**
- `go test ./internal/executor/...` PASS, incl. new
  `datum_arena_test.go`.
- 21-query sweep: row counts match Phase-4 baseline.

**Datum size assertion:** `unsafe.Sizeof(Datum{}) == 64`.

**Round-trip test (datum_arena_test.go):**
```go
func TestM0073DatumStringArenaRoundTrip(t *testing.T) {
    a := NewArena(0)
    payload := "hello world"
    buf := a.Allocate(len(payload))
    copy(buf, payload)
    // Find offset within arena
    offset := /* ... */
    d := Datum{
        Kind:  KindStringArena,
        Int:   int64(offset)<<32 | int64(len(payload)),
        arena: a,
    }
    if d.StringValue() != payload {
        t.Errorf("round-trip failed: got %q, want %q",
            d.StringValue(), payload)
    }
}
```

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | StringValue/BytesValue silently materialise from arena via slice-conversion (changing zero-copy contract) | Both paths use `unsafe.String / unsafe.SliceData` to alias the underlying buffer; no copy. New test asserts zero-alloc property. |
| R2 | cloneRow shallow-copy aliases arena pointer beyond Reset boundary | Documented contract (cloneRow valid only until next Reset). M0073-0004 Materialize promotion is the retention boundary; new test pins this. |
| R3 | Datum struct hits 64 B exact (no padding); future field additions blocked | Documented as known constraint; M0074 candidate is the packed `(arenaRef, offset, len)` Option B refactor. |
| R4 | `IsString()` / `IsBytes()` callers don't recognise arena variants | Update predicates to cover both Kind families; compile catches missed call sites via type-discriminating switches. |
| R5 | Arena pointer non-nil when Kind is non-arena (cache pollution) | Constructors set `arena: nil` explicitly; tests assert this. |

## References

- `internal/executor/datum.go:85-125` — Datum struct +
  StringValue / BytesValue (target).
- `internal/executor/arena.go` — Arena type
  (M0072-0004, `b081767`). Add `Bytes(offset, length)`
  accessor.
- `docs/design/0068-0003-batch-string-arena.md` —
  authoritative arena design.
- `docs/design/0068-0001-datum-compact-layout.md` —
  Datum struct change discipline precedent.
- `pprof-data/m0072-final/q5.heap.prof` — empirical
  motivation (`acquireRow` 25.31 % cum).
