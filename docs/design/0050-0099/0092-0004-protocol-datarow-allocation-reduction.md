# Design 0092-0004 — protocol-layer DataRow allocation reduction

**Status:** authoritative for M0092-0004 implementation.
**Milestone:** [M0092](../../milestones/0092-lazy-row-emission-in-scan-and-project.md).

## Problem

Per-row allocations in the simple-query result loop sum to
~87 B / row across 4 sites:

- `cells := make([][]byte, len(row))` in `dispatch.go:366`
  — 24 B header per row.
- `strconv.FormatInt(d.Int, 10)` inside `Datum.Format` —
  ~10 B per int Datum.
- `[]byte(d.Format())` in `dispatch.go:372` — 24 B header +
  data per column.
- `payload := make([]byte, 0, size)` in
  `protocol/messages.go:314` (WriteDataRow) — 24 B header +
  per-payload capacity.

At ~437 TPS × 1 row × 1 col × ~87 B = ~38 KB/s plus its GC
overhead. At higher concurrency or wider rows the impact
compounds.

## Approach

Three orthogonal fixes:

### (a) Hoist the `cells` slice

`dispatch.go::executeOneSimpleStmt` (around lines 355-379)
allocates `cells := make([][]byte, ncols)` inside the
per-row loop. The slice is identical-sized across rows;
hoist out of the loop, reuse via `cells = cells[:0]`.

Apply to both simple-query and extended-query paths.

### (b) Pool the WriteDataRow payload buffer

`internal/protocol/messages.go::WriteDataRow` allocates
`payload := make([]byte, 0, size)` per call. Add an
overload (or refactor) that accepts a caller-provided buffer:

```go
// WriteDataRowReuse writes a DataRow using the caller-provided
// scratch buffer. The buffer's contents are overwritten; the
// returned buffer (possibly grown) should be passed back on the
// next call for amortisation. Saves ~33 B / row vs WriteDataRow.
func (fw *FrameWriter) WriteDataRowReuse(cells [][]byte, scratch []byte) (newScratch []byte, err error)
```

dispatch.go keeps a session-level `payloadBuf []byte`, passes
it to WriteDataRowReuse, stores the (possibly grown) return.

### (c) Stream int → wire bytes without string intermediate

Add `Datum.AppendValueText(dst []byte) []byte` that emits
the wire-text representation directly into `dst`:

```go
// AppendValueText appends d's text-format representation to
// dst and returns the extended slice. Avoids the
// `strconv.FormatInt → string → []byte` allocation chain
// in the protocol-layer hot path (M0092-0004).
func (d Datum) AppendValueText(dst []byte) []byte {
    switch d.Kind {
    case KindInt:
        return strconv.AppendInt(dst, d.Int, 10)
    case KindString:
        return append(dst, d.Buf...)
    case KindBytes:
        return append(dst, d.Buf...)
    // ... etc per type
    case KindNullSentinel / d.IsNull():
        return dst  // caller handles nil separately
    }
}
```

dispatch.go's result loop becomes:

```go
// cells / payloadBuf hoisted above the loop
for {
    slot, err := op.Next()
    if err == EOF { break }
    row := slot.Row()
    cells = cells[:0]
    for _, d := range row {
        if d.IsNull() {
            cells = append(cells, nil)
            continue
        }
        startLen := len(payloadBuf)
        payloadBuf = d.AppendValueText(payloadBuf)
        cells = append(cells, payloadBuf[startLen:])
    }
    payloadBuf, err = w.WriteDataRowReuse(cells, payloadBuf)
    if err != nil { return err }
    // Truncate payloadBuf for next iter — WriteDataRowReuse
    // copied cells into the wire frame so we don't need to
    // preserve them.
    payloadBuf = payloadBuf[:0]
}
```

Total expected saving: ~80 B / row.

## Risk

- `Datum.Format` (the existing string-returning method) is
  used by many callers (logging, error messages, EXPLAIN).
  We keep it — `AppendValueText` is an additive new method
  used only by the protocol hot path.
- WriteDataRowReuse is a NEW method; WriteDataRow stays for
  callers that don't have a reusable buffer (rare in
  practice).
- The `cells` slice now retains references into payloadBuf
  across the row emission. If WriteDataRowReuse writes
  payloadBuf data to the wire and returns, payloadBuf is
  safe to truncate. Document that contract.

## Test coverage

- Existing dispatch + protocol tests must continue to pass.
- New benchmark `BenchmarkDispatchDataRowInt` that drives
  the loop with a fixed int Row; assert allocs/op ≤ 1
  post-fix (down from 4+ pre-fix).

## Expected impact

- Per-row alloc: ~87 B → ~0 B (steady-state, after pool
  warm).
- GC pressure drops proportionally.
- Per-query CPU should improve (less GC pause, faster
  alloc).
