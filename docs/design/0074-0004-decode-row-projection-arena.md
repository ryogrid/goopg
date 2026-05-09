# Design 0074-0004 — DecodeRowProjectionIntoArena variant

**Milestone:** M0074-0004
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0073-0002 (commit `d0bfe99`) —
DecodeRowIntoArena + decodeValueArena landed.

## Context

`DecodeRowProjection` exists at `internal/executor/codec.go:65-127`
but has no arena variant. It accepts a `keep []bool`
column-mask: `keep[i]=true` decodes the column,
`keep[i]=false` skips via `decodeValueSize` to advance
the offset without allocating. The decoded path still
goes through the legacy `decodeValue` (heap-allocated
`make([]byte)` for varchar / bytes columns).

Current call sites: `internal/executor/operators_ddl.go:603,
713` — index-build paths. When building an index on a
single column of a wide table, only the indexed column's
data is decoded, but that column's varchar / bytes payload
hits the heap.

M0073-0002 added `DecodeRowIntoArena` (codec.go ~239) +
`decodeValueArena` for the seqScanOp / indexScanOp data
path. The projection variant was scope-deferred.

## Goals

- Add `DecodeRowProjectionIntoArena(dst Row, cols []catalog.
  Column, data []byte, keep []bool, arena *Arena) error`
  that mirrors DecodeRowProjection's skip semantics but
  routes projected columns through `decodeValueArena`.
- Wire the existing `operators_ddl.go` index-build call
  sites to pass an arena. Reset boundary: per-page during
  index build (mirrors seqScanOp pattern from M0073-0004).
- Preserve byte-for-byte backward compat:
  `DecodeRowProjection(dst, cols, data, keep)` continues
  to work and produces non-arena Datums when called
  without an arena.

## Non-goals

- **Per-batch arena page-size tuning.** Research showed
  the 64 KiB default amortises well at SF=1; per-call-site
  tuning has limited upside without workload variance
  evidence. Carries to M0075 if needed.
- **New call sites beyond index-build.** Other callers
  of `DecodeRowProjection` (if any emerge in future code)
  can adopt the arena variant on-demand.
- **Changing the `keep []bool` semantic.** Skip behaviour
  is identical to DecodeRowProjection.

## Proposed implementation

```go
// DecodeRowProjection — backward-compat wrapper.
func DecodeRowProjection(dst Row, cols []catalog.Column,
                          data []byte, keep []bool) error {
    return DecodeRowProjectionIntoArena(dst, cols, data, keep, nil)
}

// DecodeRowProjectionIntoArena decodes only columns where
// keep[i] is true into the dst Row, skipping non-projected
// columns by advancing the offset via decodeValueSize.
// When arena is non-nil, projected columns emit
// KindStringArena / KindBytesArena Datums backed by arena
// allocation. When arena is nil, behaviour is identical
// to DecodeRowProjection.
func DecodeRowProjectionIntoArena(dst Row, cols []catalog.Column,
                                    data []byte, keep []bool,
                                    arena *Arena) error {
    // mirror of DecodeRowProjection structure but each
    // decodeValue call site dispatches on (arena == nil)
    // to either decodeValue or decodeValueArena.
    ...
}
```

### Call-site wiring (`operators_ddl.go`)

The two index-build sites (l.603, l.713) currently call:
```go
if err := DecodeRowProjection(scratchRow, cols, tuple.Data, keep); err != nil { ... }
```

Replace with:
```go
if err := DecodeRowProjectionIntoArena(scratchRow, cols,
                                         tuple.Data, keep,
                                         o.arena); err != nil { ... }
```

Add `arena *Arena` field to the DDL operator state, with
the same Open/Reset/Close lifecycle as seqScanOp.

## Verification

Pre-commit gate (M0074 standard):
- `go test ./internal/executor/... -run TestIndexBuild`
  PASS.
- 21-query SF=1 sweep: zero row-count change.
- Q12=2, Q13=35, Q21=381, Q22=7.

New tests in
`internal/executor/codec_projection_arena_test.go`:
- `TestDecodeRowProjectionArenaProjectedKindArena` — when
  arena != nil, projected varchar columns emit
  KindStringArena Datums.
- `TestDecodeRowProjectionArenaSkippedColumnsZeroAlloc` —
  when arena != nil, skipped columns produce no Datum
  alloc (Datum.Kind == KindNull or untouched).
- `TestDecodeRowProjectionArenaBackwardCompat` — when
  arena == nil, output matches DecodeRowProjection
  byte-for-byte.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Index-build retains projected Datum past arena Reset → garbage read | Index build is single-pass per page; no slot retention crosses Reset boundary. Reset called at page advance, after page's keys are flushed to btree. |
| R2 | DecodeRowProjectionIntoArena semantic divergence from skipping when arena != nil | Test pinning round-trip: projected columns equal non-arena baseline (after StringValue copy); skipped columns produce zero-value Datums in both modes. |
| R3 | New arena field on DDL operator missed at Close → leak | Add arena.Drop() in operator's Close(); pin via test harness. |

## Migration plan

Single commit (Commit C in M0074):
1. Add `DecodeRowProjectionIntoArena` to codec.go.
2. Wrap `DecodeRowProjection` to call the arena variant
   with arena=nil.
3. Add arena field + lifecycle to operators_ddl.go DDL
   operator(s); update both call sites.
4. Land tests.
5. Verify gate: SF=1 sweep + index-build unit tests.
