# Design 0075-0003 — Datum struct full flip (64 B → 40 B)

**Milestone:** M0075-0003
**Status:** draft (MED-HIGH RISK — central-type flip)
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0074-0003 (commit `4d892ac`) —
`arenaRegistry` + `permArena` infrastructure;
M0073-0001 (commit `c9a34b0`) — original Datum.arena
field; M0073-0002+0004 (commit `d0bfe99`) — arena
wiring through codec.

## Context

`Datum` (`internal/executor/datum.go:101-122`) is
currently 64 B exact. M0073-0001's `arena *Arena`
consumed all 8 B of post-M0072 padding. Future field
additions (nullable flag, lifetime tag, type-system
flags) are blocked.

M0074-0003 landed the prerequisite infrastructure:
- `arenaRegistry` (256 slots) at
  `internal/executor/arena_registry.go`
- `permArena` reserved at slot 0 — process-global,
  permanent=true, never resets
- `Arena.AllocateString` / `Arena.AllocateBytes` helpers
- `permanent` + `registryIdx` fields on Arena

Research (Explore agent, 2026-05-10) confirmed the
migration scope is much smaller than originally feared:
- **7 internal `Buf:` literal sites** (all in
  datum.go's constructors and helpers)
- **3 direct `.Buf` reads** (all in datum.go's
  `StringValue` / `BytesValue` accessors)
- **0 test fixtures** with `Buf:` literals
- All production code paths flow through constructors
  (`NewStringDatum` / `NewBytesDatum` /
  `NewToastPointerDatum`) which auto-migrate

Confirmed packed size via `unsafe.Sizeof` math:
**40 B** (24 B saving, not 12 B as the M0074-0003
design doc estimated).

## Goals

- Datum struct = **40 B exact** (`unsafe.Sizeof(Datum{}) == 40`).
- Replace `Buf []byte` (24 B) + `arena *Arena` (8 B)
  with `(ArenaRef int32, Offset int32, Length int32)`
  = 12 B.
- All variable-length payload (string, bytes,
  KindToastPointer's 12-byte pointer) lives in arena
  pages addressed via the registry.
- Constructors transparently allocate from `permArena`;
  callers see no API change.
- Accessors (`StringValue`, `BytesValue`) consult
  `arenaRegistry[d.ArenaRef].Bytes(int(d.Offset),
  int(d.Length))`.
- Per-batch arena Datums (from seqScan / indexScan /
  index-build) preserve their existing semantic — only
  the field encoding changes (registry slot + offset +
  length triplet instead of pointer + packed Int).

## Non-goals

- **Per-connection permArena scoping.** Process-global
  is sufficient for goopg's single-tenant scenario.
  M0076 candidate.
- **Datum field additions.** This sub-milestone frees
  24 B of headroom; consumers (flag bits, type tags)
  arrive in M0076+.
- **Removing `KindStringArena` / `KindBytesArena`
  variants.** Under the flip, these become functionally
  indistinguishable from `KindString` / `KindBytes`
  (both encode via the same triplet), but retaining
  the Kind variants preserves M0073-0001's cross-Kind
  comparison contract. M0076 may consolidate.

## Proposed implementation

### Datum struct

```go
type Datum struct {
    Kind     DatumKind // 8 B
    Int      int64     // 8 B (numeric mantissa lane / interval / time)
    Big      *big.Int  // 8 B
    ArenaRef int32     // 4 B (registry slot; 0 = permArena)
    Offset   int32     // 4 B (arena-relative byte offset)
    Length   int32     // 4 B (byte count)
    Scale    int16     // 2 B
    // 2 B padding → 40 B exact
}

const _ uintptr = 40 - unsafe.Sizeof(Datum{})
```

Field layout offsets (64-bit alignment):
- Kind:     offset 0  (8 B)
- Int:      offset 8  (8 B)
- Big:      offset 16 (8 B)
- ArenaRef: offset 24 (4 B)
- Offset:   offset 28 (4 B)
- Length:   offset 32 (4 B)
- Scale:    offset 36 (2 B)
- padding:  2 B → 40 B

### Accessor flip

```go
func (d Datum) StringValue() string {
    if d.Length == 0 {
        return ""
    }
    a := arenaRegistry[d.ArenaRef]
    if a == nil {
        return "" // defensive — arena was dropped
    }
    buf := a.Bytes(int(d.Offset), int(d.Length))
    if len(buf) == 0 {
        return ""
    }
    return unsafe.String(&buf[0], len(buf))
}

func (d Datum) BytesValue() []byte {
    if d.Length == 0 {
        return nil
    }
    a := arenaRegistry[d.ArenaRef]
    if a == nil {
        return nil
    }
    return a.Bytes(int(d.Offset), int(d.Length))
}
```

### Constructor flip

```go
func NewStringDatum(s string) Datum {
    if s == "" {
        return Datum{Kind: KindString, ArenaRef: permArenaSlot}
    }
    off, length := permArena.AllocateString(s)
    return Datum{Kind: KindString,
                 ArenaRef: permArenaSlot,
                 Offset: off, Length: length}
}

func NewBytesDatum(b []byte) Datum {
    if len(b) == 0 {
        return Datum{Kind: KindBytes, ArenaRef: permArenaSlot}
    }
    off, length := permArena.AllocateBytes(b)
    return Datum{Kind: KindBytes,
                 ArenaRef: permArenaSlot,
                 Offset: off, Length: length}
}

func NewToastPointerDatum(p []byte) Datum {
    // Always 12 bytes; allocate from permArena since
    // the pointer outlives any per-batch arena Reset.
    off, length := permArena.AllocateBytes(p)
    return Datum{Kind: KindToastPointer,
                 ArenaRef: permArenaSlot,
                 Offset: off, Length: length}
}
```

### Arena-backed constructors

```go
// newStringArenaDatum constructs a KindStringArena Datum
// using the arena's registry slot. The arena's bytes
// were already written via Allocate(); this just records
// the (ArenaRef, Offset, Length) triplet.
func newStringArenaDatum(arena *Arena, offset, length int) Datum {
    return Datum{
        Kind:     KindStringArena,
        ArenaRef: arena.registryIdx,
        Offset:   int32(offset),
        Length:   int32(length),
    }
}

func newBytesArenaDatum(arena *Arena, offset, length int) Datum {
    return Datum{
        Kind:     KindBytesArena,
        ArenaRef: arena.registryIdx,
        Offset:   int32(offset),
        Length:   int32(length),
    }
}
```

### MaterializeArena / cloneRowOwned simplification

Under the flip both arena-backed and non-arena Datums
share the same triplet encoding. The two arms of
`MaterializeArena` (KindStringArena / KindBytesArena)
collapse into one:

```go
func (d Datum) MaterializeArena() Datum {
    if d.Kind != KindStringArena && d.Kind != KindBytesArena {
        return d
    }
    if d.Length == 0 {
        // empty payload — flip Kind to non-arena variant.
        if d.Kind == KindStringArena {
            return Datum{Kind: KindString, ArenaRef: permArenaSlot}
        }
        return Datum{Kind: KindBytes, ArenaRef: permArenaSlot}
    }
    src := arenaRegistry[d.ArenaRef]
    if src == nil || src.permanent {
        // Already permanent or arena dropped — pass through.
        if d.Kind == KindStringArena {
            return Datum{Kind: KindString, ArenaRef: d.ArenaRef,
                         Offset: d.Offset, Length: d.Length}
        }
        return Datum{Kind: KindBytes, ArenaRef: d.ArenaRef,
                     Offset: d.Offset, Length: d.Length}
    }
    srcBytes := src.Bytes(int(d.Offset), int(d.Length))
    off, length := permArena.AllocateBytes(srcBytes)
    if d.Kind == KindStringArena {
        return Datum{Kind: KindString, ArenaRef: permArenaSlot,
                     Offset: off, Length: length}
    }
    return Datum{Kind: KindBytes, ArenaRef: permArenaSlot,
                 Offset: off, Length: length}
}
```

`cloneRowOwned` follows the same simplification.

### codec.go::decodeValueArena

Already encodes (offset, length) into Datum.Int. Update
to write the triplet directly via the arena's
registryIdx:

```go
func decodeValueArena(t catalog.Type, data []byte, arena *Arena) (Datum, int, error) {
    // ... varlen length parse ...
    buf, off := arena.Allocate(n)
    copy(buf, data[4:4+n])
    return Datum{
        Kind:     KindStringArena, // or KindBytesArena
        ArenaRef: arena.registryIdx,
        Offset:   int32(off),
        Length:   int32(n),
    }, 4 + n, nil
}
```

### KindToastPointer encoding

Today: `Buf` holds the 12-byte pointer slice (likely
aliasing or copied). After flip: pointer is allocated
from permArena via `NewToastPointerDatum`. Detoast reads
via `BytesValue()` (registry lookup).

Call sites verified in research:
- `datum.go:374` — constructor
- `toast_test.go:169` — test fixture (uses constructor)
- `toast.go:122` — `toastStore` chunking
- `codec.go:136` — `decodeRowProjectionArena` (M0074-0004)
- `codec.go:273` — `decodeRowIntoArena` (M0073-0002)

All sites flow through `NewToastPointerDatum` after the
flip, auto-migrating to permArena.

### Removal verification

Files that need NO change after the flip (production
auto-migrates):
- `toast.go::DetoastValue` / `DetoastRow` — calls
  `NewBytesDatum` / `NewStringDatum`.
- `spill.go` reader path — calls constructors per
  Explore agent report.
- `copy_text.go` / `copy_binary.go` — type-specific
  decoders ultimately call constructors.

## Verification

Pre-commit gate (M0075 STRICT):
- `unsafe.Sizeof(Datum{}) == 40` (compile + runtime).
- Q12=2, Q13=35, Q21=381, Q22=7, Q9=7.
- 21-q SF=1 sweep: zero row-count change.
- `go test ./...` PASS (full repo).
- Q5 inuse_space heap pprof: ≤ 110 % of M0074-final
  (no permArena explosion).

New tests in
`internal/executor/datum_packed_flip_test.go`:
- `TestDatumPackedSize` — `unsafe.Sizeof(Datum{}) == 40`.
- `TestNewStringDatumPermArenaPath` — round-trip via
  StringValue.
- `TestNewBytesDatumPermArenaPath` — same.
- `TestNewToastPointerDatumPermArenaPath` — 12-byte
  payload allocates and round-trips.
- `TestMaterializeArenaToPermArena` — arena-backed
  Datum's bytes copied into permArena post-Materialize.
- `TestStringValueAcrossDifferentArenas` — Datum
  constructed from per-batch arena resolves correctly
  via registry.
- `TestDetoastIntoPermArena` — DetoastValue produces
  a permArena-backed Datum.
- `TestEmptyStringDatumNoAlloc` — empty payload returns
  Datum with Length=0 (no permArena allocation).

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | KindToastPointer 12-byte payload corrupted under permArena migration | Unit test + existing `toast_test.go` integration test surface covers detoast cycles. |
| R2 | permArena unbounded growth on long-running connection → OOM | Bounded by literal Datum / detoasted value count per session; TPC-H workload has bounded literals. M0076 candidate for per-connection scoping. |
| R3 | Materialize / cloneRowOwned misses a retention site → stale arena bytes after Reset | Simplified single-path code reduces the surface area; integration test exercises the 7 retention sites from M0073-0004. |
| R4 | `arenaRegistry[d.ArenaRef]` returns nil after operator Drop, causing accessor to silently return empty string | StringValue / BytesValue return `""` / `nil` (defensive); pre-commit gate (full SF=1 sweep) catches any production-path failure. |
| R5 | Empty-string Datum loses identity (Length=0 with ArenaRef=permArenaSlot vs ArenaRef=0 in old struct) | Compatibility-preserving: empty-string Datum has Length=0, accessor returns `""` regardless of ArenaRef. Pin via `TestEmptyStringDatumNoAlloc`. |

## Migration plan (single big commit, C)

1. Land Datum struct flip in `datum.go`.
2. Update accessors (StringValue, BytesValue).
3. Update constructors (NewStringDatum, NewBytesDatum,
   NewToastPointerDatum, newStringArenaDatum,
   newBytesArenaDatum).
4. Simplify MaterializeArena, cloneRowOwned.
5. Update size assertion to 40 B.
6. Update `codec.go::decodeValueArena` to write the
   triplet.
7. Run full `go test ./...` + 21-q sweep.
8. Land tests.

If any sweep query loses rows or any test fails: revert
immediately. Datum layout is too central to debug
under pressure.

## References

- `docs/design/0074-0003-datum-packed-layout.md` —
  M0074-0003 partial flip (infrastructure only); this
  design completes it.
- `docs/design/0073-0001-datum-arena-field.md` —
  original Datum.arena field design.
- `docs/design/0068-0001-datum-compact-layout.md` —
  Datum struct change discipline.
- `internal/executor/datum.go:101-122` — current Datum
  struct.
- `internal/executor/arena_registry.go` — arenaRegistry
  + permArena (M0074-0003).
- `internal/executor/arena.go` — Arena type with
  `permanent` + `registryIdx` fields.
- `internal/executor/toast.go` — DetoastValue (auto-
  migrates).
