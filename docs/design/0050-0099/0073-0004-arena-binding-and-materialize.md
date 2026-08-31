# Design 0073-0004 — Producer arena binding + Materialize promotion

**Milestone:** M0073-0004
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0073-0001 (Datum.arena field +
KindStringArena/BytesArena variants); M0073-0002
(DecodeRowInto arena param); M0072-0004 commit
`b081767` (Arena type).

## Context

After M0073-0001 + M0073-0002 land, the type surface
supports arena-backed Datums end to end. This design
wires the producer side: `seqScanOp` and `indexScanOp`
own per-batch arenas, bind them to `DecodeRowInto`, and
`Reset()` at natural batch boundaries. The retention
side (`MaterializedSlot.Materialize()`) promotes
arena-backed Datums to owned `[]byte` so consumers that
hold rows across batches see independent storage.

The Q5 / Q9 motivation:
- Q5 `acquireRow` 25.31 % cum heap → target ≤ 5 %.
- Q9 wall time 1030 s → target ≤ 600 s.
- Total Q5 heap 1.46 TB → target ≤ 1.0 TB.

This commit closes the Q5 GC residual story that started
with M0071's slot pipeline and continued through
M0072-0001's BindOuter refactor.

## Goals

- `seqScanOp` owns an `arena *Arena` field; `Reset()` on
  per-block boundary (when `o.curBlock++` advances).
- `indexScanOp` owns an `arena *Arena` field; `Reset()`
  on `Rescan` entry (per-outer probe).
- Both pass `o.arena` to `DecodeRowInto`; varchar / bytes
  Datums emerge arena-backed.
- `MaterializedSlot.Materialize()` walks the row; for
  any arena-backed Datum, deep-copy bytes out of the
  arena into a fresh `[]byte` and flip Kind to
  `KindString` / `KindBytes`.
- `cloneRow` (the producer-side append helper) keeps
  shallow semantics — arena pointer aliased.
  Correctness depends on the consumer's discipline:
  read or Materialize before the next Reset.

## Non-goals

- **Per-page arena lifetime in indexScanOp.** Per-Rescan
  is coarser but simpler; mid-Rescan retained slots stay
  valid until the next Rescan. Per-page would require
  the BTree iterator to surface the page boundary.
- **windowOp / sortOp arena-aware retention.** They
  call `slot.Materialize()` already; the new promotion
  path is transparent.
- **Per-Rescan arena reuse across NLI outer rows.** Each
  Rescan resets the arena entirely; a more sophisticated
  bump-pointer-with-checkpoint scheme is M0074+.

## Producer arena lifecycle

### seqScanOp (per-block boundary)

```go
// internal/executor/operators_storage.go::seqScanOp
type seqScanOp struct {
    // ... existing fields
    arena *Arena
}

func (o *seqScanOp) Open(ctx *Context) error {
    // ... existing setup
    o.arena = NewArena(0)
    return nil
}

func (o *seqScanOp) Next() (TupleSlot, error) {
    // ... walk pages; on cur-block advance:
    //
    // The natural boundary is `o.curBlock++` at l.240.
    // Reset arena there because all in-flight slots from
    // the previous block must have been consumed by the
    // caller (operators that retain across block call
    // Materialize, which deep-copies before the Reset).
    if newBlock {
        o.arena.Reset()
    }
    // ... DecodeRowInto(o.scanRow, o.cols, tuple.Data, o.arena)
}

func (o *seqScanOp) Close() error {
    if o.arena != nil {
        o.arena.Drop()
        o.arena = nil
    }
    // ... existing cleanup
}
```

### indexScanOp (per-Rescan boundary)

```go
// internal/executor/operators_index.go::indexScanOp
type indexScanOp struct {
    // ... existing fields (outerSlot, outerWidth, scanRow, ...)
    arena *Arena
}

func (o *indexScanOp) openPrep(ctx *Context) error {
    // ... existing setup
    o.arena = NewArena(0)
    return nil
}

func (o *indexScanOp) Rescan(outerSlot SlotView, outerWidth int) error {
    o.rows = o.rows[:0]
    o.tids = o.tids[:0]
    o.idx = 0
    o.outerSlot = outerSlot
    o.outerWidth = outerWidth
    o.arena.Reset()  // NEW — per-outer-row boundary

    // ... existing index probe
    // Inside scanFn: DecodeRowInto(o.scanRow, ..., o.arena)
}

func (o *indexScanOp) Close() error {
    // ... existing cleanup
    if o.arena != nil {
        o.arena.Drop()
        o.arena = nil
    }
    return nil
}
```

The `cloneRow on append` in scanFn (the M0072-0001
hot path) becomes:

```go
// scanFn body — varchar Datums in scanRow are now
// arena-backed; cloneRow shallow-copies the arena
// pointer + Int (offset/length packed). The actual
// payload bytes live in o.arena and are valid until
// next Rescan calls o.arena.Reset().
o.rows = append(o.rows, cloneRow(o.scanRow))
```

The shallow `cloneRow` is now near-free (Datum is 64 B,
no per-string `string()` conversion). The arena pages
amortise the variable-length bytes across the entire
Rescan.

### Lifetime rule (the new contract)

**An arena-backed Datum's bytes are valid until the
producer's next `Reset()`.** Consumers that hold the
Datum across a Reset MUST call `slot.Materialize()`
before the next pull from the producer. The 4 retention
sites in the M0071 slot pipeline already do this:

- `internal/executor/executor.go:255` — public Run
  boundary.
- `internal/executor/operators.go::sortOp.Open` (l.312).
- `internal/executor/operators_window.go:42` — windowOp.
- `internal/executor/operators_lockrows.go:240` —
  lockRowsOp.drainAndStamp.

Pipeline operators that pass through (filterOp, limitOp,
projectOp emit-time, NLI emit-time) read the Datum
within a single `Next()` call and don't cross the
Reset boundary; no Materialize needed.

## Materialize promotion

```go
// internal/executor/slot.go::MaterializedSlot.Materialize
func (s *MaterializedSlot) Materialize() *MaterializedSlot {
    if !rowHasArena(s.row) {
        return s  // existing fast path
    }
    // Deep-copy arena-backed Datums into owned []byte.
    s.row = cloneRowOwned(s.row)
    return s
}

// internal/executor/datum.go::cloneRowOwned (NEW)
//
// cloneRowOwned promotes arena-backed Datums to regular
// KindString / KindBytes Datums with their own []byte
// backing. Non-arena Datums pass through unchanged.
//
// Used by Materialize at the 4 retention sites in the
// M0071 slot pipeline.
func cloneRowOwned(src Row) Row {
    dst := acquireRow(len(src))
    for i, d := range src {
        switch d.Kind {
        case KindStringArena:
            offset := int(d.Int >> 32)
            length := int(d.Int & 0xFFFFFFFF)
            buf := make([]byte, length)
            copy(buf, d.arena.Bytes(offset, length))
            dst[i] = Datum{Kind: KindString, Buf: buf}
        case KindBytesArena:
            offset := int(d.Int >> 32)
            length := int(d.Int & 0xFFFFFFFF)
            buf := make([]byte, length)
            copy(buf, d.arena.Bytes(offset, length))
            dst[i] = Datum{Kind: KindBytes, Buf: buf}
        default:
            dst[i] = d
        }
    }
    return dst
}

// rowHasArena returns true if any Datum is arena-backed.
// The fast-path skip in Materialize avoids walking when
// no promotion is needed (most pipeline rows in
// post-M0073 are mid-batch arena Datums; at retention
// boundary Materialize fires the slow path).
func rowHasArena(r Row) bool {
    for _, d := range r {
        if d.Kind == KindStringArena || d.Kind == KindBytesArena {
            return true
        }
    }
    return false
}
```

## Migration plan

Single commit (Commit D in the M0073 plan), combined
with M0073-0002:

1. Update `Arena.Allocate` signature to return
   `(buf, offset)` (per design 0073-0002).
2. Add `Arena.Bytes(offset, length)` accessor (per
   design 0073-0001 — landed in Commit C; here we use it).
3. Update `DecodeRowInto` / `decodeValue` to accept
   `arena *Arena` (per design 0073-0002).
4. Update `seqScanOp` to own + bind + reset arena.
5. Update `indexScanOp` to own + bind + reset arena.
6. Add `cloneRowOwned` helper.
7. Update `MaterializedSlot.Materialize` with the
   `rowHasArena` fast-path skip.
8. Add new tests:
   - `internal/executor/seq_scan_arena_test.go` —
     drive seqScanOp over a synthesized varchar
     table; assert `acquireRow` invocation count
     does not scale with row count (per-batch alloc
     only).
   - `internal/executor/index_scan_arena_test.go` —
     same pattern for indexScanOp.
   - `internal/executor/materialize_arena_test.go` —
     pin Materialize promotion contract: arena-backed
     Datum survives a subsequent Reset of the producer
     arena.
9. Update existing arena test (`arena_test.go`) for
   the `Allocate` signature change.

## Verification

**Pre-commit gate:**
- Q12=2 / Q13=35 / Q21=381 preserved.
- Q9 ≥ 175 rows; Q9 wall ≤ 1100 s (gate); ≤ 600 s
  (compression target).
- `go test ./internal/executor/...` PASS, incl. new
  arena tests.
- 21-query sweep row counts match Phase-4 baseline.

**Q5 heap pprof rerun:**

```sh
mkdir -p pprof-data/m0073-0004
curl -s -o pprof-data/m0073-0004/q5.heap.prof \
    http://127.0.0.1:6060/debug/pprof/heap
go tool pprof -top -cum -sample_index=alloc_space \
    pprof-data/m0073-0004/q5.heap.prof | head -25
```

Acceptance:
- `acquireRow` cum heap ≤ 5 % (was 25.31 %).
- Total Q5 heap ≤ 1.0 TB (was 1.46 TB).
- `btree.RangeScan` continues at ≤ 20 % (the
  M0072-0001 win preserved).

**Q9 wall-time gate:**

```sh
./tpch-runner --queries=9 --per-query-timeout=1200s \
    --cancel-after=1100s
```

Acceptance:
- `Q9: OK elapsed=<wall> rows=175` where wall ≤ 600 s
  (best-effort) or ≤ 2 × 1030 s = 2060 s (hard floor —
  any regression beyond this is revert/bisect).

**Cross-query stability:** Q1, Q3, Q11, Q16 wall time
≤ 110 % of M0072-final.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Slot retained past Reset reads garbage | The 4 retention sites in the slot pipeline already call Materialize; new promotion path deep-copies arena bytes. New `materialize_arena_test.go` pins the contract: retain a slot, Reset the producer's arena, assert StringValue() still returns the original payload. |
| R2 | `cloneRow` aliases arena pointer; mid-batch retention by an unintended consumer corrupts on next Reset | `cloneRow` is producer-internal (accessed via `o.rows = append(o.rows, cloneRow(o.scanRow))`); not part of the public slot API. The public API is `slot.Materialize().Row()` which deep-copies. Documented contract. |
| R3 | DetoastRow returns Buf-backed Datum mid-arena-Datum row | DetoastRow path unchanged; it overwrites the Datum at the column position with a regular `KindString` Datum (Buf-backed). The row has mixed arena + non-arena Datums; cloneRowOwned handles each per-Kind. New `detoast_arena_test.go` pins. |
| R4 | NestedLoopIndexJoin inner Rescan invalidates arena Datums of the OUTER side | NLI's outer is a separate operator; its arena (if any — outer is typically NLI/MHJ which currently doesn't bind arena directly) lives on its own producer. Inner's `arena.Reset()` only affects inner Rescan, not outer. Pipeline contract preserved. |
| R5 | `o.arena.Reset()` on per-block boundary in seqScanOp invalidates rows still being decoded mid-block | Reset fires AT the cur-block-advance moment, AFTER the previous block's last visible tuple has been emitted. Caller already consumed or Materialized. Pin via test that holds a slot across block boundary and asserts Materialize-then-Reset preserves bytes. |
| R6 | Q9 wall time ≥ 600 s after the fix | Compression target is best-effort; hard floor is ≤ 2 × 1030 s = 2060 s. If wall doesn't compress, the bottleneck is `evalExprSlot` per-row CPU which M0074 vectorisation addresses. |
| R7 | Memory growth from arena pages outliving Reset (page reuse not happening) | `Arena.Reset()` rewinds `len(pages[i])` to 0 but keeps capacity (per M0072-0004 design); steady-state memory bounded by the largest page set ever filled. New test pins this. |

## References

- `docs/design/0068-0003-batch-string-arena.md` —
  authoritative arena design.
- `docs/design/0073-0001-datum-arena-field.md` — Datum
  struct + arena variants.
- `docs/design/0073-0002-decode-arena-binding.md` —
  DecodeRowInto / decodeValue signature change.
- `internal/executor/arena.go` (M0072-0004,
  `b081767`) — Arena type.
- `internal/executor/operators_storage.go::seqScanOp`
  (l.~76-246) — target.
- `internal/executor/operators_index.go::indexScanOp`
  (l.~65-275) — target.
- `internal/executor/slot.go:62-64` —
  MaterializedSlot.Materialize.
- `internal/executor/executor.go:255`,
  `operators.go::sortOp.Open` (l.312),
  `operators_window.go:42`,
  `operators_lockrows.go:240` — 4 retention sites.
- `pprof-data/m0072-final/q5.heap.prof` — empirical
  motivation.
