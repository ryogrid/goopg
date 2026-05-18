# 02 — Pointer-Free `Datum`

This chapter removes every GC-traced field from the `Datum` value type.
The current `Datum` carries three pointer-shaped fields (`Buf []byte`'s
slice header, `Big *big.Int`, `arena *Arena`); across a 50-column row
that is roughly 150 pointers the GC must scan on every cycle.
[[01-memory-context]] provided the substrate (`mctx`) that holds payload
bytes off the GC heap; this chapter rewrites `Datum` to reference
`mctx` storage via a 16-bit `ContextID` plus packed offset / length.

Cross-references: [[01-memory-context]] (`Datum.ArenaID` resolves via
`mctx.Lookup`), [[03-executor-concrete]] (`Slot.Cells` is `[]Datum` and
becomes a pointer-free slice).

## 1. Current state

Verbatim from `internal/executor/datum.go:101-122`:

```go
type Datum struct {
    Kind  DatumKind // 8B (Go int)
    Int   int64     // 8B (arena-Datum: (offset<<32)|length)
    Buf   []byte    // 24B (slice header; nil for arena variants)
    Big   *big.Int  // 8B
    Scale int16     // 2B
    arena *Arena    // 8B
    // 6B padding → 64B exact.
}
const _ uintptr = 64 - unsafe.Sizeof(Datum{}) // compile-error if > 64
```

Pointer-typed fields:

| Field   | GC implication                                                                |
|---------|-------------------------------------------------------------------------------|
| `Buf`   | 24-byte slice header; the underlying `*byte` is traced and dereferenced.     |
| `Big`   | `*big.Int`; traced and the `big.Int` struct itself is scanned recursively.   |
| `arena` | `*Arena`; traced; the `Arena` struct's `pages [][]byte` is in turn scanned.  |

The GC cost is amplified by `Row = []Datum` (`internal/executor/datum.go:524`)
— each row is a slice of 50–100 Datum values, so the per-row pointer
scan count is `50 × 3 = 150` minimum. At 6 400 rows/sec (c=100 select-
only) that is ~1 million pointer scans per second purely on Datum
fields.

The decomposition is incomplete in two other ways:

- `KindNumeric` allocates `*big.Int` on the heap for any value that
  exceeds int64 mantissa range. Numeric-heavy queries (TPC-H q1,
  pg_catalog views) hit this path frequently.
- The `arena` pointer was introduced in M0073-0001 as a forward-compat
  step toward a packed Datum (M0075), but the flip never landed and
  the field has been holding the slot since. We complete the flip here.

## 2. Target layout

Final shape (subject to fieldalignment lint at implementation time):

```go
// internal/executor/datum.go (post-refactor)

type Datum struct {
    Kind    DatumKind       // 1 B   (uint8 backing; was 8 B int)
    Flags   uint8           // 1 B   (IsNull, IsBigNumeric, IsToast, reserved)
    ArenaID mctx.ContextID  // 2 B   (uint16; resolves via mctx.Lookup)
    Scale   int8            // 1 B   (numeric scale; signed)
    _pad0   [3]byte         // 3 B   pad to 8-byte boundary before Lo
    Lo      uint64          // 8 B   (scalar / (offset<<32)|length / mantissa lo)
    Hi      uint64          // 8 B   (numeric hi mantissa / interval micros / TID lo)
}

// Field layout (offsets): Kind@0, Flags@1, ArenaID@2, Scale@4, _pad0@5..7,
// Lo@8, Hi@16. unsafe.Sizeof(Datum{}) == 24 B, with zero GC-traced fields.
const _ uintptr = 24 - unsafe.Sizeof(Datum{}) // compile-error if not 24

// IsNull is encoded via Flags bit 0 (not a separate Kind) so the
// "null with a kind tag" case (used by IS NULL on typed columns) is
// expressible without losing the Kind.
const (
    flagNull        uint8 = 1 << 0
    flagBigNumeric  uint8 = 1 << 1
    flagToast       uint8 = 1 << 2
)
```

Size: **24 B exact**, half the current 64 B. No GC-traced fields. The
`Row = []Datum` slice header is unchanged (one slice scan root per row),
but the per-row pointer count drops from 150 to **zero**.

Sizing rationale:
- `Kind` was `int` for historical reasons; the enum has < 256 values,
  so `uint8` is sufficient.
- `Scale` was `int16` but PG numeric scale is bounded ([-30, +30]); `int8`
  fits with room to spare.
- `Flags` consolidates `IsNull`, big-numeric, and toast indicators —
  bits that were previously implied by `Kind` switching.
- `Lo` and `Hi` are sized for the largest payload (numeric high
  mantissa, interval micros, TID block+offset).

Smaller Datum has secondary benefits: a 16-column row drops from 1 KB
(`16 × 64 B`) to 384 B (`16 × 24 B`), fitting cache lines better. Row
copies in operators like sort and hash-join consume proportionally
less memmove.

## 3. Per-Kind encoding rules

The **`ArenaID` invariant**: `ArenaID = 0` means *the Datum has no
arena-resident payload*; its value is fully encoded in `Lo` / `Hi` /
`Flags` / `Scale`. Accessors that consult mctx (`StringValue`,
`BytesValue`, `BigIntValue`) check `ArenaID != 0` before calling
`mctx.Lookup`, so the chapter-01 reserved `InvalidContextID = 0`
slot is never dereferenced. The literal-payload path that today
uses `permArena` is migrated to `ArenaID = mctx.PermContextID` (= 1)
([[01-memory-context]] §6), keeping `ArenaID = 0` clean as the
"no payload" sentinel. The accessors below enforce this gate.

| Kind                       | Lo                                       | Hi               | ArenaID            | Flags        |
|----------------------------|------------------------------------------|------------------|--------------------|--------------|
| KindNull                   | 0                                        | 0                | 0                  | flagNull     |
| KindBool                   | 0 or 1                                   | 0                | 0                  | 0            |
| KindInt (int2/int4/int8)   | int64 value                              | 0                | 0                  | 0            |
| KindFloat (float4/float8)  | bits of float64                          | 0                | 0                  | 0            |
| KindString                 | (offset << 32) \| length                 | 0                | own ctx (or Perm)  | 0            |
| KindBytes (bytea)          | (offset << 32) \| length                 | 0                | own ctx (or Perm)  | 0            |
| KindTime (timestamp/tz)    | unix-nanos UTC                           | 0                | 0                  | 0            |
| KindInterval               | (months << 32) \| days (signed)          | micros           | 0                  | 0            |
| KindNumeric (fast path)    | int64 mantissa                           | 0                | 0                  | 0 (Scale set)|
| KindNumeric (big path)     | (offset << 32) \| length of BE mantissa  | 0                | own ctx            | flagBigNumeric |
| KindToastPointer           | toastrelid (uint32) << 32 \| chunk id    | va_extsize       | 0                  | flagToast    |
| KindTID                    | block (uint32) << 32 \| offset (uint16)  | 0                | 0                  | 0            |
| KindUUID                   | low 64 bits                              | high 64 bits     | 0                  | 0            |

All KindString / KindBytes / big-Numeric payloads live in their
context's chunk storage; resolution is
`mctx.Lookup(ArenaID).Bytes(offset, length)`. The lifetime contract is
identical to today's arena-backed Datum: payload is valid until the
owning context is `Reset` or `Release`d. Callers retaining a Datum
past that boundary must `CopyTo` ([[03-executor-concrete]]) the payload
into their own context.

### Numeric encoding without `*big.Int`

The current `KindNumeric` either stores an int64 mantissa in `Int`
(fast path) or holds `*big.Int` (big path). The fast path is unchanged
— `Lo = mantissa`. The big path replaces the `*big.Int` pointer with
the big-endian byte representation stored in mctx:

```go
// Big-numeric encoding into mctx (zero GC-heap allocation):
b := bi.Bytes()                     // big-endian magnitude; one heap alloc inside big.Int
mag := len(b)
buf := ctx.Alloc(1 + mag)           // sign byte + magnitude bytes; mctx-resident
if bi.Sign() < 0 {
    buf[0] = 0x80
}
copy(buf[1:], b)
// Resolve buf's offset/length via the helper that goes through Alloc's
// returned (slice, offset) pair (chapter 01 §5):
offset, length := ctx.OffsetOf(buf)
d.Lo = uint64(offset)<<32 | uint64(length)
d.Flags |= flagBigNumeric
d.ArenaID = ctx.ID()
```

(`OffsetOf(buf []byte) (offset, length uint32)` is a small helper on
`*mctx.Context` that returns the logical offset of `buf`'s first byte
within the context's chunk stream; it is a pointer-comparison against
each chunk plus an arithmetic remainder. Added to [[01-memory-context]]
§5 alongside `Alloc`.)

The residual `bi.Bytes()` allocation is inside `*big.Int` itself — a
caller-provided value — and is unavoidable without rewriting big.Int.
Big-numeric construction is rare enough (constant folding at plan time,
or rare data-type conversion) that the residual cost is tolerable; the
per-row hot path is on the fast path (`int64` mantissa, zero alloc).

`Datum.BigIntValue() *big.Int` rehydrates on demand for arithmetic
that genuinely needs `*big.Int` (compare, multiply, etc.). The
allocation is paid only when the caller demands it; common operations
that don't need the rehydrated form (equality, copy, encode-to-wire)
operate on the packed bytes directly.

**Performance caveat** — workloads dominated by big-numeric arithmetic
(TPC-H q1's sum/avg over `l_extendedprice * (1 - l_discount)`) pay
extra rehydration cost vs the current pointer-keep. The mitigation is
to add `int128`-style packed-arithmetic kernels in the executor for
the common cases (sum, avg) that today decode-then-arith. The kernels
operate on `(Lo, Hi)` pairs directly. Sized in
[[09-migration-and-rollout]] §risk-register.

### TOAST encoding

`KindToastPointer` stays at a 12-byte logical layout (matching PG's
`varatt_external` shape: `va_rawsize`, `va_extsize`, `va_valueid`,
`va_toastrelid`). Pre-refactor it was stored in the 24 B `Buf` slice;
post-refactor it is packed into `Lo` (toastrelid + va_valueid) and `Hi`
(va_rawsize and va_extsize). The on-disk format is untouched.

## 4. NumericArena (sub-region of mctx)

For big-numeric values, a per-context sub-region stores the BE byte
slices. The simplest implementation is to let `mctx.AllocBytes` handle
it directly (the bytes are appended to the same chunk stream as
strings); the offset/length encoding is identical. No separate API
needed; the **NumericArena** name is a docs concept, not a runtime
object.

## 5. Accessors (rewritten for the new layout)

The current accessors at `internal/executor/datum.go:128-228` are
rewritten. Public method shapes preserved so caller-side migration
is mechanical:

```go
func (d Datum) IsNull() bool { return d.Flags & flagNull != 0 }

func (d Datum) BoolValue() bool { return d.Lo != 0 }

func (d Datum) IntValue() int64 { return int64(d.Lo) }

func (d Datum) FloatValue() float64 {
    return *(*float64)(unsafe.Pointer(&d.Lo))
}

func (d Datum) StringValue() string {
    if d.Lo == 0 { return "" }
    ctx := mctx.Lookup(d.ArenaID)
    buf := ctx.Bytes(uint32(d.Lo>>32), uint32(d.Lo))
    if len(buf) == 0 { return "" }
    return unsafe.String(&buf[0], len(buf))
}

func (d Datum) BytesValue() []byte {
    if d.Lo == 0 { return nil }
    return mctx.Lookup(d.ArenaID).Bytes(uint32(d.Lo>>32), uint32(d.Lo))
}

func (d Datum) TimeValue() time.Time {
    return time.Unix(0, int64(d.Lo)).UTC()
}

func (d Datum) IntervalMonths() int32 { return int32(d.Lo >> 32) }
func (d Datum) IntervalDays()   int32 { return int32(d.Lo) }
func (d Datum) IntervalMicros() int64 { return int64(d.Hi) }

func (d Datum) NumericMantissa() int64 {
    return int64(d.Lo)  // valid only when d.Flags & flagBigNumeric == 0
}

func (d Datum) NumericScale() int8 { return d.Scale }

// BigIntValue decodes the packed big-endian mantissa into *big.Int.
// Allocates one *big.Int + backing slice on the GC heap; callers in
// hot paths should prefer the packed kernels.
func (d Datum) BigIntValue() *big.Int {
    buf := d.BytesValue()
    if len(buf) == 0 { return new(big.Int) }
    sign := buf[0] & 0x80
    bi := new(big.Int).SetBytes(buf[1:])
    if sign != 0 { bi.Neg(bi) }
    return bi
}

func (d Datum) TIDBlock()  uint32 { return uint32(d.Lo >> 32) }
func (d Datum) TIDOffset() uint16 { return uint16(d.Lo) }
```

### Constructors

```go
func NewBoolDatum(b bool) Datum {
    d := Datum{Kind: KindBool}
    if b { d.Lo = 1 }
    return d
}

func NewIntDatum(i int64) Datum {
    return Datum{Kind: KindInt, Lo: uint64(i)}
}

func NewFloatDatum(f float64) Datum {
    return Datum{Kind: KindFloat, Lo: *(*uint64)(unsafe.Pointer(&f))}
}

// NewStringDatum copies s into ctx and returns a KindString Datum
// rooted in ctx. The Datum is valid until ctx is Reset/Released.
func NewStringDatum(ctx *mctx.Context, s string) Datum {
    if s == "" {
        return Datum{Kind: KindString}
    }
    off, n := ctx.AllocString(s)
    return Datum{
        Kind: KindString, ArenaID: ctx.ID(),
        Lo: uint64(off)<<32 | uint64(n),
    }
}

func NewBytesDatum(ctx *mctx.Context, b []byte) Datum { /* analogous */ }

// NewBigNumericDatum stores bi's BE bytes (with sign flag) into ctx.
func NewBigNumericDatum(ctx *mctx.Context, bi *big.Int, scale int8) Datum
```

Note the constructor signatures take an explicit `*mctx.Context`. This
is the same discipline as the current `decodeRowIntoArena` path; the
caller must already hold a context to allocate from. The "Datum
factory with no context" cases (today's `NewStringDatum(s string)`
that aliases into the caller's stack-allocated string) are handled by
the special `PermContextID` for literal pool values, or by requiring
the caller to thread a context.

## 6. Wire format

**Unchanged.** The PG-binary and PG-text encoders/decoders for tuple
fields operate on `Datum` as a value; they currently call `d.Buf`,
`d.Big`, etc. — those call sites switch to the new accessors. No
on-disk byte representation changes; no protocol message format
changes. The wire-encode path lives in `internal/protocol/encode.go`
and `internal/executor/codec.go`; both are listed in the API-impact
inventory below.

## 7. API impact (call sites to migrate)

Migration of `d.Buf`, `d.Big`, `d.arena.Bytes(...)`, and direct field
access to the new accessors. The implementer **must** generate the
authoritative inventory immediately before Phase B begins, via:

```bash
grep -RInE '\.Buf|\.Big|\.arena\.Bytes\(' \
    /home/ryo/work/goopg/goopg/internal \
    | grep -v '_test.go' | wc -l
```

and a separate `_test.go` count. The pre-refactor estimate (production
+ tests combined) is in the low-to-mid hundreds; we deliberately do
not freeze a number here because it drifts with day-to-day commits.
The phased per-package rollout in §migration below is sized in
*packages*, not call sites, so the count delta does not affect the
plan.

Package coverage (the packages that have at least one access today):
`internal/executor/`, `internal/planner/`, `internal/wal/`,
`internal/access/heap/`, `internal/initdb/`, `internal/protocol/`,
`internal/server/`, plus their `*_test.go` files.

The implementation strategy is:

1. Land the new `Datum` layout behind a `//go:build datumv2` build
   tag.
2. Add the new accessors alongside the old ones (`StringValue` works
   under both); the old `Buf` field returns nil under the new tag.
3. Migrate packages one at a time, each landing as a small PR that
   compiles cleanly under the new tag.
4. After all packages are ported, drop the build tag and delete the
   `Buf` / `Big` / `arena` fields.

This phasing is Phase B in [[09-migration-and-rollout]].

## 8. PG counterparts

| goopg concept          | PG counterpart                                                   |
|------------------------|------------------------------------------------------------------|
| Pointer-free `Datum`   | `postgres/src/include/postgres.h:286` — `Datum` is `uintptr_t`   |
| Value/pointer-by-tag   | `postgres/src/include/catalog/pg_type.h` — `typbyval`, `typlen`  |
| Numeric mantissa       | `postgres/src/include/utils/numeric.h` — packed digit array      |
| Interval micros        | `postgres/src/include/datatype/timestamp.h` — `Interval` struct  |
| TOAST pointer          | `postgres/src/include/postgres.h:153` — `varatt_external`        |

PG's `Datum` is itself a `uintptr_t` (8 bytes); the type system decides
whether to interpret it as a value or a pointer into a palloc'd region.
Our 24-byte tagged-union is a richer encoding but adheres to the same
"by-value when small, indirect-into-mctx when large" discipline.

## 9. Concurrency and lifetime contract

A `Datum` is a value type; copying it is a 24-byte memmove. Two callers
holding the same `Datum` value see the same payload bytes through the
same `ArenaID + (offset, length)`. Mutating the payload bytes through
either caller is illegal — the contract is read-only.

Lifetime: `d.StringValue()`, `d.BytesValue()` return slices aliasing
the mctx chunk backing array. Those slices are invalid past
`mctx.Lookup(d.ArenaID).Reset()` or `.Release()`. The discipline is
identical to today's arena-backed Datum, just without the inline
`*Arena` pointer.

Cross-statement retention: callers that store a Datum past statement
end **must** `CopyTo` it into a parent context first (the txn or
session context). `Slot.CopyTo` ([[03-executor-concrete]]) does this
in bulk for entire rows.

## 10. Verification

After Phase B of [[09-migration-and-rollout]] ships:

- **Compile-time** — `unsafe.Sizeof(Datum{}) == 24` asserted in
  `internal/executor/datum_test.go`. `grep -RIn 'd\.Buf\|d\.Big\|d\.arena'
  internal/` returns zero outside `datum.go` itself.
- **GC pointer scan** — capture an `inuse_objects` profile at c=10
  select-only; expect zero references to `*big.Int` or `*executor.Arena`
  from any `[]Datum` slice. The `runtime.scanobject` cum% in the cpu
  profile drops from 54.9 % to **< 25 %** at this phase (Phase B
  delivers the bulk of the GC saving; Phase C closes the rest).
- **Per-row pointer count** — a synthetic benchmark
  (`internal/executor/datum_bench_test.go`) shows 0 GC-traced fields
  per `Datum` via `reflect.Type.Field(i).Type.Kind()` enumeration.
- **TPC-H regression** — numeric-heavy queries (q1, q4, q5) run within
  ±10 % of pre-refactor wall-clock. If big-numeric workload regresses
  > 10 %, the packed-arithmetic kernel sub-task (sized in
  [[09-migration-and-rollout]] §risk-register) is escalated and landed
  before Phase C proceeds.
- **pgbench c=10 SO TPS** — combined with Phase A (mctx), the GC
  reduction should lift c=10 SO from 2 307 to **≥ 5 000** TPS. The
  full target (≥ 8 000) lands after [[03-executor-concrete]] eliminates
  the per-row `cloneRowOwned` deep-copy.
