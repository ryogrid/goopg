# 0116-0001 — Multi-Column Index-Only Scan Key Decoding

**Status:** draft
**Date:** 2026-05-26
**Milestone:** M0116
**Supersedes:** —

---

## 1. Problem Statement

PostgreSQL's Index-Only Scan (IOS) combines two MVCC optimization layers:

1. **Visibility Map** — confirms all tuples on a heap page are visible to all
   transactions, so no heap fetch is needed for MVCC checks.
2. **Index key coverage** — all projected columns are embedded in the B-tree
   index leaf entry, so no heap tuple read is needed for column values.

When both conditions hold, IOS returns rows directly from the index without
touching the heap at all.

goopg implements the single-column case of IOS
(`internal/executor/operators_indexonly.go`) but `decodeRowFromKey` rejects
composite keys:

```go
// operators_indexonly.go:145
if len(key) != 1 {
    return nil, fmt.Errorf("index-only scan: multi-column key decode not supported yet")
}
```

Tables with composite primary keys or multi-column unique constraints fall back
to heap fetches even when the index key fully covers the projected columns,
negating the Visibility Map benefit for those tables.

Affected TPC-H tables: `lineitem (l_orderkey, l_linenumber)`,
`partsupp (ps_partkey, ps_suppkey)`. Affected pgbench tables: none (single-PK
schema), but user schemas commonly use composite keys.

The current `decodeBTreeKeyToDatum` supports: `int4`, `int8`, `varchar`/`char`,
`timestamp`. The types `float8`, `date`, `bool`, and `name` are not yet handled
even for single-column IOS (they fall through to an error). Extending them is
in scope for M0116-0001 alongside the multi-column loop.

---

## 2. B-tree Key Format

The B-tree key format is established by the encoder in
`internal/storage/btree.go` (or the relevant btree storage package). Each
column value is appended in declaration order with the encoding used by that
column's type:

| Type family | Encoding |
|---|---|
| `int4` | 4-byte big-endian, sign bit flipped (`EncodeInt4`) |
| `int8` | 8-byte big-endian, sign bit flipped (`EncodeInt8`) |
| `float8` | 8-byte big-endian, sign-bit-flip + all-bits-flip for negatives (`EncodeFloat8`) |
| `bool` | 1 byte (0 or 1) |
| `date` | 4-byte big-endian via `EncodeInt4` (days since PG epoch) |
| `timestamp` | 8-byte big-endian via `EncodeInt8` (µs since PG epoch) |
| `text`, `varchar`, `bpchar`, `name` | null-terminated escape-encoded UTF-8 (`EncodeVarchar`) |

The decoder in `decodeRowFromKey` must follow the same order and byte layout.
Multi-column keys are formed by concatenating the per-column encodings in
column declaration order. There is no inter-column separator; for fixed-width
types the byte count is a constant, and for variable-length types the 0x00
terminator marks the end of the field.

---

## 3. Proposed Implementation

### 3.1 `decodeRowFromKey` Refactor

Replace the single-key fast path with a loop over all columns in the index
definition:

```go
func (o *indexOnlyScanOp) decodeRowFromKey(key []byte) (Row, error) {
    // Iterate all index columns in declaration order to track byte offsets,
    // then return only the Covered columns (the projected subset).
    allCols := o.plan.Index.Columns  // []string: column names in key order
    covered  := o.plan.Covered       // []catalog.Column: projected subset

    decoded := make(map[string]Datum, len(allCols))
    off := 0
    for _, colName := range allCols {
        col, ok := o.ctx.Catalog.LookupColumn(o.plan.Table, colName)
        if !ok {
            return nil, fmt.Errorf("IOS key: column %q not in catalog", colName)
        }
        datum, n, err := decodeIndexKeyColumn(key[off:], col)
        if err != nil {
            return nil, fmt.Errorf("IOS key col %q: %w", colName, err)
        }
        decoded[colName] = datum
        off += n
    }
    row := make(Row, len(covered))
    for i, col := range covered {
        row[i] = decoded[col.Name]
    }
    return row, nil
}
```

`decodeIndexKeyColumn` is a new function that dispatches on the column type,
returns the decoded `Datum` and the number of bytes consumed (including any
terminator byte). The existing single-column decoder logic migrates into this
function. For variable-length types, it scans forward to the 0x00 terminator
to determine the byte count (the current `DecodeVarchar` signature takes a full
slice; a thin wrapper returning the consumed byte count is needed).

### 3.2 Planner — Column Coverage Check

`planIndexOnlyScan` (or the rule in the planner that emits `IndexOnlyScan`)
must verify that every column referenced in the query's target list and
predicate is available in the index key. Columns must appear in the same order
as the index definition:

```go
func canUseIndexOnlyScan(query *Select, idx *catalog.Index) bool {
    for _, col := range outputAndFilterColumns(query) {
        if !slices.Contains(idx.Columns, col) {
            return false
        }
    }
    return true
}
```

If this check fails, the planner emits `IndexScan` (heap fetch) as before.

### 3.3 `IndexOnlyScan` Plan Node — Column Metadata

The `IndexOnlyScan` plan node already carries all the information needed:

- `Index *catalog.Index` provides `Index.Columns []string` — the ordered list
  of column names that form the B-tree key, in declaration order.
- `Covered []catalog.Column` — the projected column subset the operator must
  return.

No new fields are required. `decodeRowFromKey` uses `Index.Columns` to decode
the full composite key byte-by-byte, then projects the output using `Covered`.

---

## 4. Type Decoder Notes

### Variable-length columns (text / varchar / bpchar / name)

The existing single-column string decoder (`DecodeVarchar`) reads a
null-terminated, escape-encoded byte sequence (0x00 is the terminator; 0x01 is
the escape introducer). The multi-column path uses the same encoding; scanning
forward to the 0x00 terminator tells the loop how many bytes to advance `off`.

`bpchar` (blank-padded char) uses `EncodeChar`, which strips trailing spaces
before applying `EncodeVarchar`; decoding is identical to `DecodeVarchar`. On
decode, the caller must right-pad to the declared length if needed.

`name` (63-byte max identifier) is encoded with `EncodeVarchar` — no
special-casing is needed.

### Fixed-width numeric types

`int4`, `int8`, `float8`, `date`, `timestamp`, and `bool` have known sizes so
the loop can advance `off` by a constant without a length prefix.

---

## 5. Testing Plan (M0116-0003, M0116-0004)

| Test | Description |
|---|---|
| `TestIOS_CompositeInt4Int4` | 2-column `(int4, int4)` PK; SELECT projecting both columns |
| `TestIOS_CompositeInt4Text` | 2-column `(int4, text)` unique; SELECT projecting both |
| `TestIOS_HeapFallback` | Query projecting a column not in the composite index; assert `IndexScan` chosen (heap fetch) |
| `TestIOS_ExistingSingleColumn` | Regression: existing single-column IOS tests pass unchanged |
| `TestIOS_3Columns` | 3-column composite key; full coverage case |

Integration: wire a `lineitem`-style table with `(l_orderkey int4, l_linenumber int4)`
composite PK and verify IOS is chosen and returns correct results.

### 5.1 Runtime gaps uncovered by tests (M0116-0003, 2026-05-29)

Writing the four tests above against the M0116-0001/0002 code revealed three
runtime gaps that the original design did not explicitly call out. All three
are now fixed:

1. **Fixed-width decoder slice discipline.** `decodeIndexKeyColumn` originally
   passed the whole remaining key slice (e.g. 8 bytes on a `(int4, int4)`
   composite) into `btree.DecodeInt4`, which asserts `len(b) == 4` and rejects
   anything else. From the single-column path this was fine; from the
   multi-column loop it discarded every row. The dispatch now slices
   `key[:width]` per fixed-width branch (int4 / int8 / float8 / timestamp) and
   bounds-checks before delegating.

2. **`Keys` carried through IOS promotion.** `IndexScan.Keys` (the M0054-0006
   composite-equality probe vector) was not copied into the promoted
   `IndexOnlyScan`. The struct gains `Keys []Expr`,
   `tryPromoteIndexOnlyScan` copies it, and `indexOnlyScanOp.Open` adds a
   `len(Keys) > 0` branch that calls a new `lookupKeys` helper mirroring
   `indexScanOp.lookupKeys`. Without this, a full equality probe like
   `WHERE a = 1 AND b = 20` against a composite `(a, b)` index would
   degenerate to a leading-column-only probe plus a residual filter — still
   correct under the *single-column* IOS heuristic, but it would have been
   wrong as soon as the planner started using composite `Keys` for IOS.

3. **No silent decode failures.** The fast-path scan callback used
   `if err == nil { append }`, masking any future decode bug as a missing
   row. It now propagates an XX000 `ExecError` from the scan, matching the
   convention the rest of the executor follows.

These three fixes are landed alongside the M0116-0003 tests so the test
suite actually validates the multi-column IOS end-to-end correctness story
on top of the planner and decoder changes.

---

## 6. Performance Expectations

For queries that project only composite-PK columns from large tables with a
fully-committed, VM-visible heap, IOS avoids all heap I/O:

- Expected improvement: proportional to the heap-to-index page ratio.
  For a narrow table (2–3 int columns, dense packing), index pages are
  ≈ 10× denser than heap pages, so IOS delivers ≈ 10× fewer I/O operations.
- For warm-cache workloads the improvement is in CPU (fewer buffer pin/unpin
  calls, no tuple-header parsing), typically 10–30%.

---

## 7. References

- `postgres/src/backend/executor/nodeIndexonlyscan.c` — `ExecIndexOnlyScan`
- `postgres/src/backend/access/index/indexam.c` — index tuple decoding
- `internal/executor/operators_indexonly.go` — current IOS implementation
- `internal/storage/btree.go` — B-tree key format encoder
- `practice/pg_mvcc_internals.md` §"Visibility Map"
- M0046-0004 design (IOS foundation)
