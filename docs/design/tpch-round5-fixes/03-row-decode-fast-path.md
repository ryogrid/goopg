# 03 — Row Decode Fast-Path

| field | value |
| --- | --- |
| priority | HIGH — 24–38% cum CPU across all 5 queries |
| risk | Medium |
| files | `internal/executor/codec.go`, `internal/executor/codec_strategy.go` (new), `internal/executor/operators_storage.go` |
| composes with | [06-numeric-fast-path.md](./06-numeric-fast-path.md) |

## 1. Motivation

After the spill-writer fix (01), row decode becomes the #1 CPU consumer across
all five TPC-H queries:

| Query | `DecodeRowIntoMctxPGTuple` cum CPU | `decodePhysicalPGValueMctx` cum CPU |
| --- | ---: | ---: |
| Q1 | 37.8% | 29.6% |
| Q9 | 29.7% | 23.9% |
| Q4 (residual) | ~7.8% | ~5.7% |
| Q7 (residual) | ~7.8% | ~5.7% |
| Q13 (residual) | ~7.8% | ~5.7% |

The hot path is:

```
DecodeRowIntoMctxPGTuple  (codec.go:755)
  → per-column loop
    → decodePhysicalPGValueMctx(c.Type, data[off:], sctx)  (codec.go:791)
      → strings.ToLower(t.Name) switch  (codec.go:976)
        → per-type decode + Datum construction
```

Each column incurs:
1. `strings.ToLower(t.Name)` — allocates a new string for every call
2. Type-switch dispatch — ~25 cases, compiler-generated jump table
3. `Datum` construction — 48-byte struct, copied by value into the row

For Q1's 6M `lineitem` rows × 16 columns = **96 million** `decodePhysicalPGValueMctx`
calls, each calling `strings.ToLower` on a type name that is known at compile time.

## 2. Current state

### 2.1 The type switch (`codec.go:969-1260`)

```go
func decodePhysicalPGValueMctx(t catalog.Type, data []byte, sctx *mctx.Context) (Datum, int, error) {
    if t.IsArray {
        return decodeArrayValuePG(t, data)
    }
    switch strings.ToLower(t.Name) {
    case "bool", "boolean":
        // 1-byte read → NewBoolDatum
    case "int2", "smallint":
        // 2-byte LE read → NewIntDatum(int64(int16(...)))
    case "int4", "integer", "int", "serial":
        // 4-byte LE read → NewIntDatum(int64(int32(...)))
    case "int8", "bigint", "bigserial":
        // 8-byte LE read → NewIntDatum
    case "float4", "real":
        // 4-byte IEEE-754 → floatTextDatum(PGFloatOut(...))
    case "float8", "double precision", "double":
        // 8-byte IEEE-754 → floatTextDatum(PGFloatOut(...))
    case "numeric", "decimal":
        // varlena text → parseNumericFast or parseNumeric
    case "text", "varchar", "character varying", "bpchar", "character", "unknown":
        // varlena → TOAST check → decodePhysicalPGVarlena → arena or heap alloc
    case "date":
        // 4-byte LE days since PG epoch
    case "timestamp", "timestamptz":
        // 8-byte LE microseconds
    // ... ~10 more type cases ...
    default:
        // unknown type → varlena fallback
    }
}
```

### 2.2 The per-column loop (`codec.go:755-798`)

```go
func DecodeRowIntoMctxPGTuple(dst Row, cols []catalog.Column, data, bitmap []byte,
    storedNatts int, sctx *mctx.Context) error {
    n := len(cols)
    if storedNatts == 0 {
        storedNatts = n
    }
    off := 0
    for i, c := range cols {
        // ALTER TABLE ADD COLUMN check
        if i >= storedNatts { /* ... */ continue }
        // Null bitmap check
        if len(bitmap) > 0 && (bitmap[i/8]>>(uint(i)%8))&1 == 0 {
            dst[i] = NullDatum
            continue
        }
        off = alignPhysicalPGOffset(off, physicalPGTypeAlign(c.Type))
        if off >= len(data) {
            dst[i] = NullDatum
            continue
        }
        v, consumed, err := decodePhysicalPGValueMctx(c.Type, data[off:], sctx)  // ← PER-COLUMN DISPATCH
        if err != nil {
            return fmt.Errorf("DecodePhysicalPGRow: %s: %w", c.Name, err)
        }
        dst[i] = v
        off += consumed
    }
    return nil
}
```

### 2.3 seqScanOp decode path (`operators_storage.go:1622-1677`)

```go
// In seqScanOp.Next():
o.scanRow = acquireRow(len(o.cols))                         // pool lookup + zeroing
err = DecodeRowIntoMctxPGTuple(o.scanRow, o.cols, ...)      // per-column decode
if o.detoastNeeded { DetoastRow(o.scanRow) }                // TOAST expansion
row = cloneRowOwned(o.scanRow)                               // promote arena→heap
```

### 2.4 TPC-H column types

All 8 TPC-H tables use only these types:

| PG type | Columns | Fixed width? |
| --- | --- | --- |
| `int4` / `integer` | `l_linenumber`, `l_orderkey`, `l_partkey`, `l_suppkey`, `o_orderkey`, `o_custkey`, `c_custkey`, `c_nationkey`, `s_suppkey`, `s_nationkey`, `ps_partkey`, `ps_suppkey`, `p_partkey`, `n_nationkey`, `n_regionkey`, `r_regionkey` | Yes (4 bytes) |
| `numeric(15,2)` | `l_extendedprice`, `l_quantity`, `o_totalprice`, `c_acctbal`, `s_acctbal`, `ps_supplycost`, `p_retailprice` | No (varlena text) |
| `numeric(15,4)` | `l_discount`, `l_tax` | No (varlena text) |
| `varchar(N)` | `l_returnflag`, `l_linestatus`, `l_shipinstruct`, `l_shipmode`, `l_comment`, `o_orderstatus`, `o_orderpriority`, `o_clerk`, `o_comment`, `c_name`, `c_address`, `c_phone`, `c_mktsegment`, `c_comment`, `s_name`, `s_address`, `s_phone`, `s_comment`, `ps_comment`, `p_name`, `p_mfgr`, `p_brand`, `p_type`, `p_container`, `p_comment`, `n_name`, `n_comment`, `r_name`, `r_comment` | No (varlena) |
| `char(N)` | `l_returnflag` (some schemas), `l_linestatus` (some schemas) | Semi (bpchar varlena) |
| `date` | `l_shipdate`, `l_commitdate`, `l_receiptdate`, `o_orderdate` | Yes (4 bytes) |
| `text` | (none in TPC-H, but present in pg_catalog) | No (varlena) |

No arrays, no composites, no enums, no timetz, no intervals, no UUIDs, no pg_lsn, no oidvector.

## 3. Design

### 3.1 Pre-computed decode strategy

At `seqScanOp.Open()` time, build a `decodeStrategy` — a per-column table of
specialized decode functions — that replaces the per-column type switch.

**New file: `internal/executor/codec_strategy.go`**

```go
package executor

// columnDecodeFn decodes one column's PG-physical bytes at the given
// offset within data. Returns the Datum, bytes consumed, and any error.
// When sctx is non-nil, variable-length columns allocate into the arena.
type columnDecodeFn func(data []byte, sctx *mctx.Context) (Datum, int, error)

// decodeStrategy is a per-scan immutable table of column decode functions
// and alignment values. Built once at scan Open() time from the table's
// catalog.Column list.
type decodeStrategy struct {
    fns    []columnDecodeFn // len == len(cols)
    aligns []int            // len == len(cols), alignment per column
}

// buildDecodeStrategy pre-computes decode functions and alignments for
// each column. Called once per table scan at Open() time.
func buildDecodeStrategy(cols []catalog.Column) *decodeStrategy {
    ds := &decodeStrategy{
        fns:    make([]columnDecodeFn, len(cols)),
        aligns: make([]int, len(cols)),
    }
    for i, c := range cols {
        ds.fns[i] = selectColumnDecodeFn(c.Type)
        ds.aligns[i] = physicalPGTypeAlign(c.Type)
    }
    return ds
}
```

### 3.2 Per-type specialized decode functions

For each common TPC-H type, create a standalone function. The function signature
is uniform: `func([]byte, *mctx.Context) (Datum, int, error)`.

Example for `int4`:

```go
// decodeInt4Col decodes a PG-physical 4-byte LE int4.
func decodeInt4Col(data []byte, _ *mctx.Context) (Datum, int, error) {
    if len(data) < 4 {
        return Datum{}, 0, fmt.Errorf("truncated int4")
    }
    return NewIntDatum(int64(int32(binary.LittleEndian.Uint32(data[:4])))), 4, nil
}
```

Example for `varchar` (arena-backed):

```go
// decodeVarcharCol decodes a PG varlena text/varchar column.
func decodeVarcharCol(data []byte, sctx *mctx.Context) (Datum, int, error) {
    // TOAST pointer check
    if len(data) >= 13 && data[0] == 0x01 {
        ptr := make([]byte, 12)
        copy(ptr, data[1:13])
        return NewToastPointerDatum(ptr), 13, nil
    }
    payload, n, err := decodePhysicalPGVarlena(data)
    if err != nil {
        return Datum{}, 0, err
    }
    if sctx != nil {
        moff, mlen := sctx.AllocBytes(payload)
        return newStringArenaDatum(sctx, moff, mlen), n, nil
    }
    return NewStringDatum(string(payload)), n, nil
}
```

Example for `numeric(scale)` — closure capturing known scale:

```go
// makeNumericColDecode creates a numeric decode function that tries the
// int64 fast path with the column's declared scale before falling back
// to the big.Int path. See 06-numeric-fast-path.md.
func makeNumericColDecode(declaredScale int) columnDecodeFn {
    return func(data []byte, _ *mctx.Context) (Datum, int, error) {
        payload, n, err := decodePhysicalPGVarlena(data)
        if err != nil {
            return Datum{}, 0, err
        }
        text := string(payload)
        if declaredScale >= 0 {
            if v, scale, ok := parseNumericFastScale(text, int16(declaredScale)); ok {
                return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
            }
        }
        if v, scale, ok := parseNumericFastInt(text); ok {
            return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
        }
        m, s, err := parseNumeric(text)
        if err != nil {
            return Datum{}, 0, fmt.Errorf("decode numeric %q: %w", text, err)
        }
        return newNumeric(m, int(s)), n, nil
    }
}
```

### 3.3 Type selector function

```go
// selectColumnDecodeFn returns the specialized decode function for a
// catalog type. Falls back to the general decodePhysicalPGValueMctx for
// types not covered by specialized functions.
func selectColumnDecodeFn(t catalog.Type) columnDecodeFn {
    if t.IsArray {
        return decodeArrayCol
    }
    switch strings.ToLower(t.Name) {
    case "bool", "boolean":
        return decodeBoolCol
    case "int2", "smallint":
        return decodeInt2Col
    case "int4", "integer", "int", "serial", "oid", "regproc":
        return decodeInt4Col
    case "int8", "bigint", "bigserial":
        return decodeInt8Col
    case "float4", "real":
        return decodeFloat4Col
    case "float8", "double precision", "double":
        return decodeFloat8Col
    case "date":
        return decodeDateCol
    case "timestamp", "timestamptz":
        return decodeTimestampCol
    case "numeric", "decimal":
        if t.Scale >= 0 {
            return makeNumericColDecode(t.Scale)
        }
        return decodeNumericFallbackCol
    case "text", "varchar", "character varying", "bpchar", "character", "unknown":
        return decodeVarcharCol
    case "name":
        return decodeNameCol
    case "bytea":
        return decodeByteaCol
    default:
        // Fall back to the general type-switch decoder for uncommon types.
        return func(data []byte, sctx *mctx.Context) (Datum, int, error) {
            return decodePhysicalPGValueMctx(t, data, sctx)
        }
    }
}
```

### 3.4 Strategy-aware decode

Modify `DecodeRowIntoMctxPGTuple` to accept an optional `*decodeStrategy`:

```go
func DecodeRowIntoMctxPGTuple(dst Row, cols []catalog.Column, data, bitmap []byte,
    storedNatts int, sctx *mctx.Context, strategy *decodeStrategy) error {
    // ... preamble (ALTER TABLE, null bitmap checks unchanged) ...
    off := 0
    for i, c := range cols {
        // ... (ALTER TABLE ADD COLUMN + null bitmap checks unchanged) ...
        if strategy != nil {
            off = alignPhysicalPGOffset(off, strategy.aligns[i])
        } else {
            off = alignPhysicalPGOffset(off, physicalPGTypeAlign(c.Type))
        }
        if off >= len(data) {
            dst[i] = NullDatum
            continue
        }
        var v Datum
        var consumed int
        var err error
        if strategy != nil {
            v, consumed, err = strategy.fns[i](data[off:], sctx)
        } else {
            v, consumed, err = decodePhysicalPGValueMctx(c.Type, data[off:], sctx)
        }
        if err != nil {
            return fmt.Errorf("DecodePhysicalPGRow: %s: %w", c.Name, err)
        }
        dst[i] = v
        off += consumed
    }
    return nil
}
```

### 3.5 Integration with seqScanOp

Add a `strategy *decodeStrategy` field to `seqScanOp`:

```go
type seqScanOp struct {
    // ... existing fields ...
    strategy *decodeStrategy // built once at Open(), immutable for the scan lifetime
}
```

Populate in `Open()` (or at construction time):

```go
func (o *seqScanOp) Open(ctx *Context) error {
    // ... existing Open logic ...
    if o.strategy == nil {
        o.strategy = buildDecodeStrategy(o.cols)
    }
    // ...
}
```

Pass `strategy` to decode in `Next()`:

```go
err = DecodeRowIntoMctxPGTuple(o.scanRow, o.cols, tup.Data, tup.Bitmap,
    natts, o.sctx, o.strategy)  // ← pass strategy
```

### 3.6 Backward compatibility

All existing callers of `DecodeRowIntoMctxPGTuple` that don't pass a strategy
(or pass `nil`) fall through to the existing `decodePhysicalPGValueMctx` path.
The function signature changes but all callers are in-tree.

Callers that need updating to pass `nil` (find all with):
```bash
grep -rn "DecodeRowIntoMctxPGTuple" internal/executor/ --include="*.go" | grep -v "_test.go"
```
This will produce the current list of non-test callers. At the time of writing,
these include sites in `codec.go` (`DecodeHeapTupleRowInto`), `operators_storage.go`
(GiST scan, seq scan, index scan, tid scan paths), `operators_analyze.go` (ANALYZE
sample decode), `sys_pg_database.go` (catalog scan), `operators_ddl_database_acl.go`
(DDL ACL scan), `operators_vacuum_datfrozenxid.go` (VACUUM catalog scan), and
`operators_ddl.go` (DDL paths). All of these are cold paths and can pass `nil` —
no performance impact.

## 4. Implementation steps

1. **Create `internal/executor/codec_strategy.go`** with:
   - `columnDecodeFn` type
   - `decodeStrategy` struct
   - `buildDecodeStrategy` function
   - `selectColumnDecodeFn` function
   - All specialized decode functions (one per TPC-H type)
2. **Add `*decodeStrategy` parameter** to `DecodeRowIntoMctxPGTuple` in `codec.go`.
3. **Update all existing callers** to pass `nil` for the new parameter.
4. **Add `strategy` field** to `seqScanOp` struct in `operators_storage.go`.
5. **Populate strategy** in `seqScanOp.Open()`.
6. **Pass strategy to decode** in `seqScanOp.Next()`.
7. **Run tests:**
   ```bash
   go test ./internal/executor/ -count=1
   go test ./internal/executor/ -run TestDecode -count=1
   ```
8. **Benchmark Q1** before/after with pprof:
   - Expected: `decodePhysicalPGValueMctx` drops out of top 10 CPU consumers
   - Expected: `strings.ToLower` drops out entirely
   - Expected: 10–20% reduction in decode cum CPU

## 5. Risk assessment

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Missing type case in `selectColumnDecodeFn` | Falls back to general decoder — no functional bug, just slower | Exhaustive enum of PG built-in types; unknown types gracefully degrade |
| Schema change mid-scan (ALTER TABLE) | Strategy built for old schema, column count mismatch | Strategy is rebuilt on each `Open()`; a mid-scan ALTER would cause a relation-cache invalidation that triggers re-planning |
| Alignment mismatch in strategy | Corrupted column values | Alignment values come from the same `physicalPGTypeAlign` function used by the legacy path |
| `strings.ToLower` still called in `selectColumnDecodeFn` | The strategy builder still calls `ToLower`, but only once per column at construction time — acceptable (cold path) | Acceptable: N calls instead of N×Rows calls |
| Arena sctx is nil for some scan paths | Varlena columns allocate on heap instead of arena | Same behaviour as current code when sctx is nil; the per-column decode functions already handle `sctx == nil` |

## 6. Verification

1. **Decode correctness:** Add a test that decodes the same tuple data with both the legacy path and the strategy path, asserting byte-identical Datums:
   ```go
   func TestDecodeStrategyParity(t *testing.T) {
       // Build strategy from column list
       strategy := buildDecodeStrategy(lineitemCols)
       row1 := make(Row, len(lineitemCols))
       row2 := make(Row, len(lineitemCols))
       DecodeRowIntoMctxPGTuple(row1, lineitemCols, data, bitmap, natts, nil, nil)
       DecodeRowIntoMctxPGTuple(row2, lineitemCols, data, bitmap, natts, nil, strategy)
       for i := range row1 {
           if !datumEqual(row1[i], row2[i]) {
               t.Errorf("column %d: legacy=%v strategy=%v", i, row1[i], row2[i])
           }
       }
   }
   ```

2. **Profile Q1 before/after:**
   ```bash
   go tool pprof -top -nodecount=20 bench/tpch/pprof/q1_cpu_after.pb.gz
   ```
   Expected: `decodePhysicalPGValueMctx` cum CPU drops from ~29.6% to < 5%.

3. **Run full TPC-H suite:**
   ```bash
   tmp/tpch-runner --port=65433 --queries=all --parallel-workers=0
   ```
   All 22 queries must return correct row counts and no errors.

## 7. Related improvements

- [06-numeric-fast-path.md](./06-numeric-fast-path.md) — the known-scale numeric decode function that the strategy's numeric closure calls. Implement 06 before 03 so the strategy can directly include `parseNumericFastScale`.
- [04-double-clone-elimination.md](./04-double-clone-elimination.md) — the decode strategy's arena-backed path feeds into the double-clone problem; fixing both together gives the largest cumulative allocation reduction.
