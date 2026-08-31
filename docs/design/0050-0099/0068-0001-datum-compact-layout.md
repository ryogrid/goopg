# Design 0068-0001 — Datum Compact Layout

**Milestone:** M0068-0001
**Status:** draft
**Owner:** TBD
**Branch:** `perf-analysis`

## Context

`internal/executor/datum.go::Datum` is currently a union-style
struct with **all** value-type fields populated regardless of
`Kind`. For TPC-H Q5 SF=1 with 6 M lineitem × 16 cols, that's
~96 M Datum values in the live heap — each carrying 4 GC-traced
pointers (`String`, `Bytes`, `Time.loc`, `NumericBig`) and ~120
bytes of payload. The GC mark phase scans ~384 M pointers per
query and `gcBgMarkWorker` consumed 65 % of CPU pre-PIVOT
(M0065), 30 % post-PIVOT (M0066).

Per `practice/go_gc_optimized_programming.md` §8 (Pointer
Density Is Extremely Important):
> "Go GC can skip pointer-free regions efficiently."

The fastest path to mark-cost reduction is to make the typical
Datum **pointer-free** (KindInt, KindBool, KindTime as
int64 nanos, KindNumeric int64 mantissa) and indirect only the
genuinely variable-length cases (KindString, KindBytes,
KindNumericBig overflow).

## Goals

- Datum size: **≤ 48 bytes** (current: ~120 bytes).
- Pointers per Datum: **≤ 1** (current: 4).
- Maintain SQL-visible behavior (no semantic change in
  `evalExpr`, `compareDatum`, `Format`).
- Provide a clean accessor surface so call sites don't read raw
  fields directly (eases future representations).

## Non-goals

- Changing how Datums are produced from disk (decode/codec
  paths). Those land in M0068-0003 (string arena).
- Slot polymorphism. That's M0068-0002.

## Proposed layout

```go
type Datum struct {
    Kind   DatumKind   // 1 byte (uint8)
    flags  uint8       // 1 byte: bit 0 = isNull; reserved bits
    scale  int16       // 2 bytes: NumericScale, IntervalDays high16
    inline int64       // 8 bytes: KindInt, KindBool (low bit), KindTime
                       //   (unix nanos), KindNumeric (int64 mantissa),
                       //   KindInterval (months<<32 | days)
    extra  int64       // 8 bytes: bytea/string length, NumericBig
                       //   length, type-specific spillover
    arena  unsafe.Pointer // 8 bytes: arena base for variable-length
                       //   payload (M0068-0003). nil for inline kinds.
    _      uint32      // 4 bytes padding for 32-byte struct alignment
}
```

Total: **32 bytes** (8 byte aligned), 1 pointer (`arena`).

Field semantics by `Kind`:

| Kind | inline | extra | arena | Notes |
| ---- | ------ | ----- | ----- | ----- |
| `KindNull` | 0 | 0 | nil | flags=isNull |
| `KindBool` | 0/1 | 0 | nil | low bit |
| `KindInt` | int64 | 0 | nil | full int64 |
| `KindTime` | unix nanos | offset (sec from UTC) | nil | UTC normalized |
| `KindNumeric` (int64 fast path) | mantissa | 0 | nil | scale in `scale` |
| `KindNumeric` (big) | 0 | bigInt byte length | arena base of big.Int bytes | scale in `scale`; rare path |
| `KindString` | offset | length | arena base | UTF-8 bytes |
| `KindBytes` | offset | length | arena base | raw bytes |
| `KindInterval` | months<<32 \| days (signed) | 0 | nil | sub-day rejected at parse |
| `KindToastPointer` | offset | length | arena base | 12-byte TOAST pointer |

## Accessor surface

To allow future representations (e.g. column-batch slots) to
return Datums without storing every field, add accessor
methods:

```go
func (d Datum) AsInt() int64
func (d Datum) AsBool() bool
func (d Datum) AsTime() time.Time
func (d Datum) AsString() string
func (d Datum) AsBytes() []byte
func (d Datum) AsNumeric() (int64, int16, *big.Int)
func (d Datum) IsNull() bool
```

Convert raw field reads (`d.String`, `d.Time`, `d.NumericBig`)
to accessor calls in a follow-up cleanup commit.

## Compatibility with arena (M0068-0003)

When `arena == nil` and `Kind` is a variable-length kind, the
Datum holds an **inline-zero** payload (empty string / empty
bytes). When `arena != nil`, `String` / `Bytes` / `NumericBig`
are reconstructed by reading `extra` bytes starting at
`(*byte)(arena)+inline` (the `inline` field carries the offset
within the arena page).

For Datums constructed BEFORE M0068-0003 lands, the `arena`
pointer points at a small heap-allocated `[]byte` (one
allocation per string). M0068-0003 replaces this with a shared
per-batch arena, removing the per-string allocation entirely.

## Migration plan

1. Add the new struct alongside the old as `compactDatum`
   (separate package) for unit tests.
2. Define accessor methods + helpers (`NewIntDatum`,
   `NewStringDatum`, etc.).
3. Flip `executor.Datum = compactDatum` in one commit,
   updating all field-read sites to accessor calls.
4. Run `go test ./...` and the 22-query SF=1 sweep.

## Verification

- Unit tests: per-Kind round-trip (construct via helper →
  read via accessor → equality).
- `go test ./...` PASS.
- Q5 SF=1 pprof: live heap (`go tool pprof -inuse_space`)
  shows ≥ 50 % reduction in `Datum`-related allocations; GC
  mark CPU share drops ≥ 30 % vs M0066.
- 22-query row-count parity preserved (or documented
  delta).

## Risks

- **Field-read sites**: ~30+ call sites read `d.String`,
  `d.Time`, etc. directly. The accessor migration must be
  comprehensive; missing one yields a compile error or
  silent zero-value bug.
- **Time normalization**: storing time as int64 unix nanos
  loses zone info. Mitigation: all times are UTC at this
  layer (already a contract per M0050-era work).
- **Big numeric**: rare path. Verify `NumericBig` round-trip
  through arena indirection.
- **Endianness**: arena bytes are stored as Go-native big.Int
  serialization; cross-platform compatibility is a
  non-issue (single-host execution).

## References

- `internal/executor/datum.go` — current implementation.
- `internal/executor/codec.go` — row decode entry points.
- `internal/executor/expr.go` — expression evaluation.
- `practice/go_gc_optimized_programming.md` §8.
