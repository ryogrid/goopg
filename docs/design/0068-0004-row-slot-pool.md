# Design 0068-0004 — Cross-Query Row/Slot Pool

**Milestone:** M0068-0004
**Status:** draft
**Owner:** TBD
**Branch:** `perf-analysis`
**Depends on:** M0068-0002 (TupleSlot pipeline) — pool is
keyed by slot kind + width.

## Context

After M0068-0001 (compact Datum) and M0068-0003 (string
arena), the remaining slot allocation is the
`MaterializedSlot.values []Datum` backing array.
Investigation found **zero `sync.Pool` usage in the
executor**: every operator's `Open()` allocates fresh
buffers; nothing pools across queries.

For TPC-H workloads with consistent row widths (lineitem
16 cols, orders 9 cols, etc.), the same widths recur every
query. A `sync.Pool` keyed by width gives free reuse with
no global state.

## Goals

- `sync.Pool`-backed slot allocation, keyed by width.
- Operators acquire slots in `Open()`, release in `Close()`.
- Cross-query reuse cuts the residual 70-80 % of post-decode
  slot allocations.
- No per-row pooling overhead on the hot path (pool is
  consulted at slot acquisition, not per `Next()`).

## Non-goals

- Pooling Datum payload (handled by arena, M0068-0003).
- Pooling at sub-slot granularity (e.g. per-Datum). The
  arena handles variable-length payload; fixed-width
  Datums are already inline in the slot's backing array.

## Proposed design

```go
package executor

// slotPool maintains sync.Pools keyed by slot width.
// Width-binned pools avoid the "wrong-size" reslice cost
// when a generic pool returns slots of mismatched width.
type slotPool struct {
    byWidth [maxSlotWidth + 1]sync.Pool
}

const maxSlotWidth = 64 // covers TPC-H widest row + safety margin

var globalSlotPool slotPool

func init() {
    for w := 0; w <= maxSlotWidth; w++ {
        width := w  // capture
        globalSlotPool.byWidth[w].New = func() any {
            return &MaterializedSlot{values: make([]Datum, width)}
        }
    }
}

// AcquireMaterializedSlot returns a slot of the given width.
// Caller MUST call ReleaseMaterializedSlot when done.
func AcquireMaterializedSlot(width int) *MaterializedSlot {
    if width < 0 || width > maxSlotWidth {
        return &MaterializedSlot{values: make([]Datum, width)}
    }
    s := globalSlotPool.byWidth[width].Get().(*MaterializedSlot)
    return s
}

// ReleaseMaterializedSlot zero-fills the slot's Datums and
// returns it to the pool. Zero-fill prevents stale Datum
// pointers from extending arena lifetimes.
func ReleaseMaterializedSlot(s *MaterializedSlot) {
    if s == nil || len(s.values) > maxSlotWidth {
        return
    }
    for i := range s.values {
        s.values[i] = Datum{}
    }
    globalSlotPool.byWidth[len(s.values)].Put(s)
}
```

The slot's `Release()` method (from M0068-0002) calls
`ReleaseMaterializedSlot` directly when the implementation is
MaterializedSlot.

### Operator integration

Operators that allocate slots use the pool:

- `MaterializedSlot` returned by `slot.Materialize()` —
  `AcquireMaterializedSlot(width)` then `Release()` when
  done.
- `MHJ` build-side hash table values are
  `*MaterializedSlot` — acquired during build, released
  during `Close()` walk.
- `sortOp` buffer: each row materialized — acquired during
  drain, released during sort-output emit.

### Sizing

`maxSlotWidth=64` covers TPC-H widest row (Q5 6-table MHJ
output ~45 cols) with margin. For widths > 64, fall back to
direct `make` (rare). Cap is configurable.

### Pool warmup

`sync.Pool` clears at GC. With `GOGC=off` (M0066-0001), GC
fires only on `GOMEMLIMIT` pressure, so pool retention is
high. Even under default GC, the pool absorbs the steady-
state width distribution within seconds.

## Migration plan

1. Add `internal/executor/slot_pool.go` with the pool +
   helpers.
2. `MaterializedSlot.Release()` calls
   `ReleaseMaterializedSlot`.
3. `slot.Materialize()` from VirtualSlot allocates via
   `AcquireMaterializedSlot`.
4. MHJ build / sort buffer / aggregate group values all
   acquire from pool.
5. Add metrics counter (per-width acquire / release / new)
   to confirm pool hit rate.

## Verification

- Unit tests: pool-roundtrip per-width, oversize fallback.
- Q5 SF=1 pprof:
  - `alloc_space` shows
    `(*MaterializedSlot).Materialize` allocations drop
    ≥ 70 %.
  - Slot allocation count over 60 s window: ≤ 30 % of
    pre-pool baseline.
- 22-query row-count parity preserved.

## Risks

- **Stale Datum pointers.** A pooled slot holding stale
  arena pointers extends arena lifetime. Mitigation:
  zero-fill on Release (`Datum{}` clears all fields
  including the arena pointer).
- **Pool growth.** `sync.Pool` is bounded by GC, but
  `GOGC=off` makes growth unbounded. Mitigation: cap
  per-width pool depth (Go's stdlib doesn't expose this;
  enforce manually if needed) — defer until profiling
  shows growth issue.
- **False sharing.** Concurrent goroutines may contend
  on a single pool. Go's `sync.Pool` is per-P internally;
  contention is rare. No explicit mitigation needed.
- **Operators that forget to Release.** Leak. Mitigation:
  add a debug-build counter that warns on slot drops
  when the slot has a non-zero generation tag (sentinel
  set on Acquire).

## References

- `internal/executor/operator.go` — slot lifecycle.
- `practice/go_gc_optimized_programming.md` §11 (alloc_space
  vs. inuse_space — pool target is alloc_space reduction).
- `sync.Pool` docs: https://pkg.go.dev/sync#Pool
