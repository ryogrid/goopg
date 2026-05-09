# Design 0074-0003 — Datum struct packed layout (52 B exact)

**Milestone:** M0074-0003
**Status:** draft (HIGH RISK — wholesale refactor)
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0073-0001 (commit `c9a34b0`) — Datum.arena
field; M0073-0002+0004 (commit `d0bfe99`) — arena wiring;
all other M0074 sub-milestones (lands LAST in M0074
session).

## Context

`Datum` (datum.go:101-122) is currently 64 B exactly:

```
Kind  DatumKind  // 8 B
Int   int64      // 8 B (arena-packed: (offset<<32)|length)
Buf   []byte     // 24 B (slice header; nil for arena variants)
Big   *big.Int   // 8 B
Scale int16      // 2 B
arena *Arena     // 8 B (M0073-0001)
              // 6 B padding → 64 B exact
```

`const _ uintptr = 64 - unsafe.Sizeof(Datum{})` enforces
the bound. M0073-0001's `arena *Arena` consumed all 8 B
of post-M0072 padding. **Future field additions are
blocked.** Datum may need:
- Nullable flag bits (avoid Kind == KindNull check)
- Per-Datum lifetime tag (per-batch vs permArena)
- Type-system flags (post-coerce, materialised, etc.)

The proposed packed layout replaces `Buf []byte` (24 B)
with `(ArenaRef int32, Offset int32, Length int32)`
(12 B), saving 12 B. The `arena *Arena` field also goes
away — replaced by `ArenaRef int32` indexing into a
process-global registry.

**Critical blocker** (research finding 2026-05-10):
- 38 non-test `NewStringDatum`/`NewBytesDatum` sites
  across 10 modules construct Datums with NO arena
  context (literal strings in expr.go, metadata strings
  in operators_ddl.go, detoasted bytes in toast.go, etc.)
- DetoastValue produces fresh heap `[]byte`; no arena
  binding.

Solution: **per-process permanent arena** for literals +
detoasted values. `permArena` lives for process lifetime,
never resets. Decoupled from per-batch arena lifetime.

## Goals

- Datum struct = 52 B exact (12 B saved).
- `arena *Arena` field replaced by `ArenaRef int32`
  registry index.
- `Buf []byte` field removed.
- `(Offset int32, Length int32)` triplet (with ArenaRef)
  encodes the byte payload.
- All existing callers of NewStringDatum/NewBytesDatum
  continue to work transparently — the constructor
  allocates from permArena.
- Per-batch arena Datums (from seqScan/indexScan) keep
  their existing semantic; only the field encoding
  changes.
- DetoastValue + spill + COPY paths bound to permArena
  (or operator-scoped arena where appropriate).

## Non-goals

- **Per-connection permArena scoping.** Process-global
  is sufficient for goopg's current single-tenant
  scenario. M0075 candidate for multi-tenant production
  with bounded LRU eviction.
- **arenaRegistry resize.** Fixed 256 slots — sufficient
  for goopg's per-query operator count. If exhausted,
  abort with a clear error; M0075 candidate for grow
  semantics.
- **Datum field additions.** This sub-milestone frees
  12 B of headroom; consumers (flag bits, type tags)
  arrive in M0075+.
- **Removing Materialize promotion.** Per-batch arena
  Datums still must promote to permArena at retention
  boundaries (executor.Run, sortOp.Open, windowOp.Open,
  lockRowsOp.drainAndStamp, aggregateOp.evalGroupKey/
  applyAgg, drainRowsCtx, drainRowsBounded).

## Proposed implementation

### Arena registry (NEW file: `internal/executor/arena_registry.go`)

```go
package executor

import "sync/atomic"

const arenaRegistrySize = 256

// arenaRegistry is the process-global table of live
// Arenas. Each Arena registers at NewArena() and
// unregisters at Drop().
var arenaRegistry [arenaRegistrySize]*Arena

// arenaRegistryNext is the round-robin slot allocator.
// Contention is rare in single-process workloads; atomic
// CAS suffices.
var arenaRegistryNext int32

// permArena is the permanent process-global arena for
// literal Datums and detoasted values. Never resets.
// Always at slot 0.
var permArena = newPermArena()

const permArenaSlot int32 = 0

func newPermArena() *Arena {
    a := NewArena(64 * 1024)
    a.permanent = true
    a.registryIdx = permArenaSlot
    arenaRegistry[permArenaSlot] = a
    return a
}

// registerArena assigns a registry slot and stores the
// arena. Skips slot 0 (permArena).
func registerArena(a *Arena) int32 {
    for {
        idx := atomic.AddInt32(&arenaRegistryNext, 1) %
               (arenaRegistrySize - 1)
        slot := idx + 1 // skip slot 0 (permArena)
        // Only claim if slot is currently free.
        if atomic.CompareAndSwapPointer(
            (*unsafe.Pointer)(unsafe.Pointer(&arenaRegistry[slot])),
            nil, unsafe.Pointer(a),
        ) {
            return slot
        }
    }
}

func unregisterArena(slot int32) {
    if slot == permArenaSlot {
        return // permArena never unregisters
    }
    arenaRegistry[slot] = nil
}
```

### Arena type extension (`internal/executor/arena.go`)

```go
type Arena struct {
    pages       [][]byte
    cur         int
    pageSize    int
    permanent   bool   // NEW: skip Reset
    registryIdx int32  // NEW: slot in arenaRegistry
}

func NewArena(pageSize int) *Arena {
    if pageSize == 0 {
        pageSize = 64 * 1024
    }
    a := &Arena{pageSize: pageSize}
    a.registryIdx = registerArena(a)
    return a
}

func (a *Arena) Reset() {
    if a.permanent {
        return // permArena never resets
    }
    // existing reset logic
    ...
}

func (a *Arena) Drop() {
    unregisterArena(a.registryIdx)
    a.pages = nil
}

// AllocateString convenience helper — copy s into arena;
// returns (offset, length). Caller stores both into Datum.
func (a *Arena) AllocateString(s string) (offset, length int32) {
    buf, off := a.Allocate(len(s))
    copy(buf, s)
    return int32(off), int32(len(s))
}

// AllocateBytes — same for []byte.
func (a *Arena) AllocateBytes(b []byte) (offset, length int32) {
    buf, off := a.Allocate(len(b))
    copy(buf, b)
    return int32(off), int32(len(b))
}
```

### Datum struct flip (`internal/executor/datum.go`)

```go
type Datum struct {
    Kind     DatumKind  // 8 B
    Int      int64      // 8 B (numeric mantissa lane / interval / time)
    Big      *big.Int   // 8 B
    ArenaRef int32      // 4 B (registry slot; 0 = permArena)
    Offset   int32      // 4 B (byte offset in arena pages)
    Length   int32      // 4 B (byte length)
    Scale    int16      // 2 B
    // 6 B padding → 52 B exact
}

const _ uintptr = 52 - unsafe.Sizeof(Datum{})
```

### Accessor flip (`StringValue` / `BytesValue`)

```go
func (d Datum) StringValue() string {
    a := arenaRegistry[d.ArenaRef]
    if a == nil {
        return "" // defensive — Datum's arena was dropped
    }
    buf := a.Bytes(int(d.Offset), int(d.Length))
    if len(buf) == 0 {
        return ""
    }
    return unsafe.String(&buf[0], len(buf))
}

func (d Datum) BytesValue() []byte {
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
    off, length := permArena.AllocateString(s)
    return Datum{Kind: KindString,
                 ArenaRef: permArenaSlot,
                 Offset: off, Length: length}
}

func NewBytesDatum(b []byte) Datum {
    off, length := permArena.AllocateBytes(b)
    return Datum{Kind: KindBytes,
                 ArenaRef: permArenaSlot,
                 Offset: off, Length: length}
}
```

### Detoast wiring (`internal/executor/toast.go`)

`DetoastValue` (l.230) currently produces fresh
`make([]byte, 0, totalLen)` and `NewStringDatum` /
`NewBytesDatum` it. Under packed layout: detoast directly
into permArena.

```go
func DetoastValue(...) Datum {
    // Existing: chunks reassembled into make([]byte, ...)
    // New: total size known up front; allocate from
    // permArena and copy chunks in directly.
    buf, off := permArena.Allocate(totalLen)
    // ... copy chunks into buf ...
    return Datum{Kind: KindString,
                 ArenaRef: permArenaSlot,
                 Offset: int32(off),
                 Length: int32(totalLen)}
}
```

### Spill / COPY paths

`internal/executor/spill.go` reader allocates spilled
Datum payload via `NewStringDatum / NewBytesDatum` — auto-
permArena via the constructor flip. No additional change.

`internal/executor/copy_text.go` /
`internal/executor/copy_binary.go` — same pattern; the
constructors handle arena allocation.

### cloneRow / cloneRowOwned

```go
// cloneRow shallow-copies the triplet; Datums alias the
// same arena bytes. Valid until the source arena's next
// Reset (per-batch) or never (permArena).
func cloneRow(src Row) Row {
    dst := acquireRow(len(src))
    copy(dst, src)
    return dst
}

// cloneRowOwned — Materialize boundary; copies any per-
// batch arena Datums into permArena, rewriting ArenaRef.
func cloneRowOwned(src Row) Row {
    dst := acquireRow(len(src))
    for i, d := range src {
        if d.Kind == KindString || d.Kind == KindBytes ||
            d.Kind == KindStringArena || d.Kind == KindBytesArena {
            // The bytes reside in arenaRegistry[d.ArenaRef];
            // promote to permArena.
            srcArena := arenaRegistry[d.ArenaRef]
            if srcArena == nil || srcArena.permanent {
                dst[i] = d
                continue
            }
            srcBytes := srcArena.Bytes(int(d.Offset), int(d.Length))
            off, length := permArena.AllocateBytes(srcBytes)
            dst[i] = Datum{Kind: d.Kind,
                            ArenaRef: permArenaSlot,
                            Offset: off, Length: length,
                            Int: d.Int, Big: d.Big, Scale: d.Scale}
        } else {
            dst[i] = d
        }
    }
    return dst
}
```

Note: under the packed layout, KindStringArena and
KindString become functionally indistinguishable — both
encode their bytes via (ArenaRef, Offset, Length). The
Kind variants can be unified after the flip is stable
(M0075 candidate: simplify to KindString/KindBytes only).
For the M0074 transition, retain both Kind values to
preserve the M0073 cross-Kind compareDatum/compareEq
test contract.

### Compile-time assertion

```go
const _ uintptr = 52 - unsafe.Sizeof(Datum{}) // compile error if > 52
```

## Verification

Pre-commit gate (M0074 STRICT post-F):
- `unsafe.Sizeof(Datum{}) == 52` (compile + runtime).
- Q12=2, Q13=35, Q21=381, Q22=7, Q9=175.
- 21-query sweep: zero row-count change.
- `go test ./...` PASS (full repo).
- Q5 CPU pprof: no regression vs Commit E.
- Q5 heap pprof: total heap ≤ 500 GB (was 404 GB at
  M0073-final; permArena adds bounded permanent
  allocation; should stay well under 500 GB).

New tests in
`internal/executor/datum_packed_layout_test.go`:
- `TestDatumPackedSize` — `unsafe.Sizeof(Datum{}) == 52`.
- `TestPermArenaLiteralDatum` — NewStringDatum allocates
  from permArena; StringValue round-trips.
- `TestBatchArenaToPermArenaPromotion` — Materialize on
  a batch-arena Datum copies bytes into permArena; result
  Datum.ArenaRef == permArenaSlot.
- `TestArenaRegistrySlotReuse` — register / unregister
  300 arenas; permArena slot 0 stays stable; slots 1..255
  are reused round-robin.
- `TestDetoastIntoPermArena` — DetoastValue produces a
  permArena-backed Datum.
- `TestSpillReaderPermArena` — spill round-trip
  preserves Datum content via permArena.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | arenaRegistry concurrency on connection-pool servers → torn pointer reads | Atomic register/unregister via CAS; permArena pre-installed at init; per-arena slot stable until Drop(). |
| R2 | permArena unbounded growth on long-running connection → OOM | Bounded by literal Datum / detoasted value count per session. TPC-H workload: literals are query-text-bounded (~1 KB per query). M0075 candidate for per-connection scoping. |
| R3 | cloneRowOwned misses a retention site → stale arena bytes after Reset | Audit checklist (mirrors M0073-0001 + M0073-0004); integration tests cover all 7 retention sites. |
| R4 | Detoast / spill / COPY paths miss arena binding → stale ArenaRef | Audit checklist in this design; integration tests cover detoast + spill + COPY; commit gate runs full SF=1 sweep. |
| R5 | arenaRegistry slot 0 collision (permArena vs newly-registered arena) | registerArena explicitly skips slot 0; permArena pre-installed at init. |
| R6 | Q5 heap regression > 500 GB after permArena growth | If observed, document permArena allocation breakdown; M0075 immediately addresses. |
| R7 | Test fixture builds Datum literals like `Datum{Kind: KindString, Buf: []byte("foo")}` directly | Find via grep for `Buf:` post-flip; convert to `NewStringDatum` constructor calls. |
| R8 | unsafe.Pointer CAS pattern in registerArena triggers race detector false positive | Test under `-race` flag in CI; revisit if false positives surface. |

## Migration plan (single big commit, F)

1. Land `arena_registry.go` infrastructure + permArena
   init.
2. Add `permanent bool` + `registryIdx int32` to Arena;
   register at NewArena, unregister at Drop.
3. Add `AllocateString` / `AllocateBytes` convenience
   helpers.
4. Migrate `NewStringDatum` / `NewBytesDatum`
   constructors to permArena allocation.
5. Migrate DetoastValue + DetoastRow.
6. Migrate spill reader Datum reconstruction.
7. Migrate COPY FROM payload allocation.
8. Flip Datum struct: remove `Buf`, `arena`; add
   `ArenaRef int32`, `Offset int32`, `Length int32`.
9. Update `StringValue` / `BytesValue` to consult
   `arenaRegistry[d.ArenaRef].Bytes(...)`.
10. Update `cloneRow` / `cloneRowOwned`.
11. Update size assertion to 52 B.
12. Sweep through compile errors — 38 + ~20 sites
    should compile transparently via constructor flip;
    any direct `Buf:` literal in tests gets converted.
13. Run full `go test ./...` + 21-q sweep.

If any sweep query loses rows or any test fails: revert
immediately. Datum layout is too central to debug under
pressure.

## References

- `docs/design/0068-0001-datum-compact-layout.md` —
  Datum struct change discipline (precedent).
- `docs/design/0073-0001-datum-arena-field.md` —
  Datum.arena field landed in M0073-0001; sets the
  pattern for M0074-0003's flip.
- `internal/executor/datum.go:101-122` — current Datum
  struct.
- `internal/executor/arena.go` — Arena type (M0072-0004
  + M0073-0001).
- `internal/executor/toast.go:230-324` — DetoastValue +
  DetoastRow.
