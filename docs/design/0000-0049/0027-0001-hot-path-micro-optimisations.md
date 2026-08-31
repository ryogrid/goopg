# 0027-0001 — Hot-Path Micro-Optimisations

## Status

draft

## Goal

Reduce per-tuple and per-operation CPU/memory overhead in the
executor's scan, decode, index, and WAL-encode paths. Each
change must preserve (or improve) code readability.

## 1. DecodeRowInto — reuse row buffer

### Current Behaviour

`scanMatching` and `indexScanOp` call `DecodeRow(cols, tuple.Data)`
for every visible tuple. `DecodeRow` allocates a new `Row` slice:

```go
func DecodeRow(cols []catalog.Column, data []byte) (Row, error) {
    row := make(Row, len(cols))
    // ... fill row from data ...
    return row, nil
}
```

For a full-table scan of 300K tuples, this is 300K short-lived
`Row` allocations — each ~8 × `Datum` = 8 × 24 bytes ≈ 200 bytes
→ 60 MB/s of allocation pressure on the GC.

### Proposed Change

Add `DecodeRowInto(dst Row, data []byte) error` that fills an
existing `Row` slice. In `scanMatching`, allocate a single `Row`
and reuse it across all tuples in each page:

```go
row := make(Row, len(cols))
for slot := uint16(1); slot <= count; slot++ {
    // ...
    if err := DecodeRowInto(row, tuple.Data); err != nil { ... }
    // Copy row if matching (tuple data is needed after unpin)
    if pred matches {
        match := make(Row, len(cols))
        copy(match, row)
        matches = append(matches, ...)
    }
}
```

For non-matching tuples (the common case), the row bytes are
decoded into the reusable buffer and immediately discarded —
zero allocation.

### Impact

Estimated 10–15 % reduction in scan time for full-table scans.
Negligible for index scans (few tuples scanned). Memory allocation
drops from O(tuples scanned) to O(matching tuples).

## 2. Pre-allocate pending/rows slices

### Current Behaviour

```go
var pending []pendingUpdate            // starts nil, grows via append
```

and

```go
o.rows = append(o.rows, row)           // indexScanOp row collection
```

### Proposed Change

Pre-allocate with capacity 1 (the common case for equality lookups):

```go
pending := make([]pendingUpdate, 0, 1)
```

and:

```go
o.rows = make([]Row, 0, 1)
```

### Impact

Negligible (< 1 %). Eliminates one `append` grow+copy per
UPDATE/DELETE/index-scan.

## 3. sort.Search in B-tree descent

### Current Behaviour

`descendToLeaf` calls `findChildBlock(items, key)` which does a
linear scan of the internal page's item list. With a fanout of
~200 items per internal page, a linear scan adds up to 200
comparisons per level × 3–4 levels = 600–800 comparisons per
index lookup.

```go
func findChildBlock(items []item, key []byte) BlockNumber {
    for i := len(items) - 1; i >= 0; i-- {
        if bytes.Compare(key, items[i].key) >= 0 {
            return items[i].ptr.Block
        }
    }
    return ...
}
```

### Proposed Change

Replace with `sort.Search` (binary search):

```go
func findChildBlock(items []item, key []byte) BlockNumber {
    i := sort.Search(len(items), func(i int) bool {
        return bytes.Compare(key, items[i].key) < 0
    })
    if i == 0 {
        return items[0].ptr.Block
    }
    return items[i-1].ptr.Block
}
```

The internal page items are always in sorted order (enforced by
the B-tree insertion algorithm), so binary search is correct.

### Impact

Estimated 5–10 % reduction in index lookup time. Comparisons drop
from O(items) to O(log items) — about 8 vs 200 comparisons per
internal page.

## 4. CRC-32 Caching for WAL Records

### Current Behaviour

`encodeRecord` computes `crc32.ChecksumIEEE(payload)` on every
call. For short payloads (commit markers, heap delete records,
B-tree insert records) the CRC computation dominates the encoding
time.

### Proposed Change

Since WAL records are write-once, append-only, and never modified,
cache the CRC for the most recent record. When the same payload
bytes are written again (unlikely but possible), the cache match
avoids recomputation.

A simpler and more impactful change: pre-compute the CRC for
fixed-size records. For the common case of short payloads
(< 256 bytes), compute the CRC inline without a function call
overhead.

### Impact

Estimated 2–5 % reduction in WAL append time. Larger effect for
very short records (commit markers, ~20 bytes) where CRC dominates.

## Summary

| # | Change | Est. Impact | Lines Changed | Readability |
|---|--------|-------------|---------------|-------------|
| 1 | `DecodeRowInto` | 10–15 % | +20 | Neutral |
| 2 | Pre-allocate slices | < 1 % | +3 | Slightly better |
| 3 | `sort.Search` in B-tree | 5–10 % | +5 | Better (clearer intent) |
| 4 | CRC-32 caching | 2–5 % | +5 | Neutral |

## References

- `internal/storage/heap.go` — `DecodeRow`
- `internal/executor/operators_storage.go` — `scanMatching`
- `internal/executor/operators_index.go` — `indexScanOp.Open`
- `internal/access/btree/btree.go` — `findChildBlock`, `descendToLeaf`
- `internal/wal/format.go` — `encodeRecord`
