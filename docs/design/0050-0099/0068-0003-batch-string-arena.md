# Design 0068-0003 — Per-Batch String Arena

**Milestone:** M0068-0003
**Status:** draft
**Owner:** TBD
**Branch:** `perf-analysis`
**Depends on:** M0068-0001 (compact Datum), M0068-0002
(TupleSlot pipeline) — Datum needs the `arena unsafe.Pointer`
field added.

## Context

After M0068-0001 shrinks Datum to ≤ 48 bytes with 1 pointer
(`arena`), the remaining GC mark cost on Q5-class workloads
is **the arena pointer itself, multiplied by Datum count**.
For Q5's 96 M Datums, that's still 96 M pointers — better
than 384 M (4 ptrs × 96 M Datums) but not great.

The deeper win is **eliminating the ALLOCATIONS** that back
each `String` / `Bytes` value: every scan-time decode of a
varchar column currently produces a `string` (separate heap
allocation) or `[]byte` (slice header + backing). For 6 M
lineitem rows × 5 varchar/char cols = 30 M short-string
allocations.

Per `practice/go_gc_optimized_programming.md` §9 ("Separate
Metadata From Payload"):
> "Store payloads in arenas, slabs, pooled buffers, page
> allocators."

A per-batch byte arena holds the actual payload bytes; the
Datum carries `(arena, offset, length)`. The arena is a
single `[]byte` per batch (or per scan iteration), reset
on batch boundary. **One backing slice = one GC pointer**
even for thousands of strings.

## Goals

- Variable-length payload (`String`, `Bytes`, large
  `Numeric`) lives in a per-batch arena.
- Datum holds `(arena, offset, length)`, NOT a Go `string` /
  `[]byte` per value.
- Arena reset at batch boundary (operators that consume a
  batch must materialize-or-discard before the next batch
  arrives).
- Zero allocation per varchar value during scan-time decode
  (amortized — arena grows in chunks).

## Proposed design

```go
// Arena is a growable byte buffer that hands out slices.
// Reset by the producer at batch boundaries; consumers MUST
// either consume immediately or call slot.Materialize() to
// copy out before reset.
type Arena struct {
    pages [][]byte
    cur   int  // index into pages
    used  int  // bytes used in pages[cur]
    free  int  // total free bytes across all pages
}

// Allocate returns a writable slice of `n` bytes inside the
// arena. The returned slice is valid until Reset() is called.
func (a *Arena) Allocate(n int) []byte

// Reset truncates all pages back to length 0 (keeps capacity).
func (a *Arena) Reset()

// Bytes returns the byte slice for (offset, length). Cross-page
// references are resolved internally.
func (a *Arena) Bytes(offset, length int) []byte
```

Page size: 64 KB (configurable). Typical scan emits ≤ 1 page
per batch of 1024 rows × ~50-byte avg string = 50 KB.

### Datum integration

`Datum.arena` becomes `*Arena` (Go pointer; just one ptr per
Datum from the GC POV). For a string value:

- `inline = offset_in_arena_pages` (low 32 bits) +
  `cur_page` (high 32 bits, encoded as part of the offset).
- `extra = length`.
- `arena = the *Arena`.

`d.AsString()` reads `d.arena.Bytes(d.inline, int(d.extra))`
and returns `unsafe.String(&bytes[0], len(bytes))`. Zero
allocation.

### Batch lifecycle

A producer (e.g. SeqScan) allocates an `Arena`, populates
slot Datums referencing arena offsets, yields the slot, and
when the consumer signals "done with this batch", calls
`Arena.Reset()` and starts the next batch in the same arena.

For the row-at-a-time pipeline (M0068-0002 doesn't introduce
columnar batches yet), the "batch" is one row. The arena
persists across one Next() call. Operators that retain rows
(sort, hash) call `slot.Materialize()` which copies payload
out of the arena into a long-lived MaterializedSlot. The
producer can then Reset.

For columnar batches (future M0069+), batch size = 1024 rows;
arena resets every 1024 rows.

## Migration plan

1. Add `internal/executor/arena.go` with the Arena type.
2. Add `Datum.arena` field (already in M0068-0001 struct
   plan).
3. Update `internal/executor/codec.go`'s decode path to
   write into arena and produce arena-backed Datums.
4. Update SeqScan / IndexScan to own a per-call Arena.
5. Update `slot.Materialize()` to copy strings out of the
   arena.
6. Update Datum accessors to read from arena when present.

## Sizing & overhead

For Q5 SF=1:
- 6 M lineitem rows × 5 varchar cols × 50 bytes avg = ~1.5 GB
  of string payload.
- With 64 KB arena pages, ~24 K pages allocated over the
  query lifetime; at any moment, ~2-4 pages live (1024-row
  batch × 50 bytes = 50 KB ≈ 1 page).
- GC sees ~4 arena pointers (one per live batch) instead of
  ~30 M individual string pointers.

CPU overhead: 1 array index + 1 pointer arithmetic per
`AsString()` call vs. direct `string` field read. Negligible
on the hot path.

## Verification

- Unit tests: arena allocate / read / reset / multi-page
  growth.
- Decode round-trip: decode a varchar column from disk,
  read via `AsString()`, compare with reference.
- Q5 SF=1 pprof:
  - `inuse_space` shows arena pages dominate string memory
    instead of millions of separate strings.
  - GC mark CPU share drops further (≥ 5 pp on top of
    M0068-0001 + M0068-0002 wins).
- 22-query row-count parity preserved.

## Risks

- **Arena lifetime bugs.** Forgetting to Materialize before
  Reset corrupts retained slots. Mitigation: debug-build
  bit-set in the arena tracks `Reset` generation; reading
  a Datum whose generation doesn't match panics with
  context.
- **Cross-batch references.** A VirtualSlot referencing the
  PRIOR batch's arena after Reset is invalid. Mitigation:
  same generation check; pipeline operators are required to
  consume before Next() returns.
- **`unsafe.String` semantics.** Returning a Go string
  backed by arena bytes is fine as long as the arena page
  isn't recycled while the string is live. The pipeline
  contract enforces this; the unsafe is safe under the
  invariant.
- **Concurrent access.** Arenas are NOT thread-safe. Each
  operator owns its arena. Mitigation: documented contract;
  no shared arenas between goroutines.

## References

- `practice/go_gc_optimized_programming.md` §9 (Separate
  Metadata From Payload).
- `internal/executor/codec.go` — decode entry point.
- `internal/executor/operators_storage.go` — SeqScan.
- `postgres/src/backend/utils/mmgr/aset.c` — PostgreSQL's
  AllocSetContext (similar concept, but ours is simpler
  because we don't need typed allocation).
