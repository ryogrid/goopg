# 0037-0001 — Spill-to-Disk Hash Join (Grace Hash Join)

**Status:** draft
**Parent milestone:** M0037
**Date:** 2026-05-02

## 1. Objective

When `drainRows` on a child join would exceed a memory budget, spill
intermediate rows to temporary disk files. Read them back one partition
at a time for hash join processing. This eliminates the per-level
`drainRows` deep-copy that causes ~7 GB of accumulated memory in Q2's
4-level join tree.

## 2. Current Problem (M0036 finding)

At each hash join level, `openLazyHashJoin` calls `drainRows` on the build side:

```go
buildRows, err := drainRows(o.right)  // o.right is a child lazy joinOp
```

`drainRows` loops `child.Next()` until EOF, deep-copying every row into a
`[]Row` slice. For Q2:
- L2's build side = L1's output → 800K rows × 12 cols ≈ 1.1 GB copy
- L3's build side = L2's output → 800K rows × 17 cols ≈ 1.4 GB copy
- Total drainRows copies: ~3-4 GB across levels

## 3. Spill File Format

Binary row serialization for temporary files:

```
┌──────────────┬─────────────────────────────────┐
│ len (4 bytes)│ Datum values (len bytes)         │
│   uint32 LE  │                                  │
├──────────────┼─────────────────────────────────┤
│ len          │ row 2                            │
├──────────────┼─────────────────────────────────┤
│ ...          │                                  │
```

Each Datum is encoded as:
- 1 byte: `DatumKind` (KindNull=0, KindBool=1, KindInt=2, KindString=3, KindNumeric=4, KindTime=5)
- For KindInt: 8 bytes int64 LE
- For KindString: 4 bytes len LE + bytes
- For KindNumeric: 8 bytes mantissa LE + 1 byte scale
- For KindTime: 8 bytes nanos LE
- For KindBool: 1 byte (0/1)

## 4. spillWriter / spillReader

```go
type spillWriter struct {
    f    *os.File
    path string
}

func newSpillWriter(dir string) (*spillWriter, error)
func (w *spillWriter) WriteRow(row Row) error    // appends one row
func (w *spillWriter) Close() error
func (w *spillWriter) Path() string

type spillReader struct {
    f    *os.File
    path string
}

func newSpillReader(path string) (*spillReader, error)
func (r *spillReader) ReadRow() (Row, error)     // reads next row, io.EOF on end
func (r *spillReader) Close() error              // removes temp file
```

## 5. Bounded drainRows

```go
func drainRowsBounded(op Operator, maxBytes int64) (spillOperator, error)
```

1. Accumulate rows in `[]Row` slice via `op.Next()`.
2. Track accumulated bytes: `totalBytes += estimatedRowSize(row)`.
3. When `totalBytes > maxBytes`, flush to a `spillWriter`:
   - Write all accumulated rows to spill file.
   - Clear `rows = nil` (frees memory to GC).
   - Continue appending to spill file for subsequent rows.
4. Return a `spillOperator` that wraps `spillReader`:
   - `Open()`: opens the spill file for reading.
   - `Next()`: reads one row from the spill file.
   - `Close()`: deletes the spill file.

If `totalBytes` never exceeds the budget, return a regular in-memory
operator (wrapping the `[]Row` slice). No disk I/O incurred.

## 6. Integration into joinOp

```go
func (o *joinOp) openLazyHashJoin(ctx *Context) error {
    // Build side...
    if buildChildIsJoin {
        // Use bounded drainRows — spill if exceeds work_mem
        spillOp, err := drainRowsBounded(o.right, workMem)
        o.lazyHash = buildHashTableFromSpill(spillOp)  // reads rows
    } else {
        buildRows, _ := drainRows(o.right)
        o.lazyHash = buildHashTable(buildRows)
    }
    ...
}
```

When the build side IS a child join (not a SeqScan), use `drainRowsBounded`
with the `work_mem` budget. The rows are read back via `spillOp.Next()`
during hash table construction, then the spill file is freed.

## 7. Grace Hash Join (Phase B)

For Q2's scale, a single spill may not be enough — the hash table itself
needs to fit in memory. The Grace algorithm partitions both sides:

1. Determine N partitions: `N = max(1, ceil(totalBuildRows * rowSize / workMem))`.
2. For each row in build side: compute `partition = hash(key) % N`, write to
   spill file `partition_i`.
3. For each row in probe side: same partition function, write to spill file.
4. For each partition i (0..N-1):
   - Read build partition i → build in-memory hash table.
   - Read probe partition i → probe hash table → emit joined rows.

This guarantees the hash table for each partition fits in `work_mem`.

## 8. `work_mem` GUC

```go
// internal/config/defaults.go
r.MustRegister(NewVariable(Variable{
    Name: "work_mem", Type: TypeInt, Unit: UnitKB, BootVal: "512MB",
    MinVal: 64, MaxVal: 1 << 40,
    Context: ContextUser,  // can be changed per-session
    Scope:   ScopeSession,
}))
```

Threaded through `executor.Context.WorkMem`. Default 512 MB — sufficient
for Q2's largest hash table (partsupp at 800K rows × 5 cols ≈ 400 MB).

## 9. Implementation Plan

| Step | Files | Description |
|------|-------|-------------|
| 1 | `internal/executor/spill.go` | `spillWriter`, `spillReader`, binary Datum codec |
| 2 | `internal/executor/spill.go` | `drainRowsBounded` — wraps existing `drainRows` with spill fallback |
| 3 | `internal/executor/operators_join_agg.go` | `openLazyHashJoin` uses `drainRowsBounded` for child join build sides |
| 4 | `internal/config/defaults.go` | `work_mem` GUC registration |
| 5 | `internal/executor/context.go` | `WorkMem` field threaded from session |
| 6 | `internal/server/dispatch.go` | `work_mem` GUC value → executor Context |
| 7 | `internal/executor/spill_test.go` | Unit tests for spill codec, writer/reader round-trip |
| 8 | `analysis/tpch-spill-hash-join-results.md` | TPC-H Q2 verification with `work_mem=512MB` |

## 10. Verification

- `TestSpillRoundTrip`: Write 100K rows, read back, verify byte-for-byte match.
- `TestDrainRowsBoundedNoSpill`: 100K small rows fit in budget → no spill.
- `TestDrainRowsBoundedSpill`: 100K large rows exceed budget → spill to disk.
- `TestSpillQ2Plan`: Q2 plan builds with spill-aware joinOp.
- Regression: 22/22 TPC-H queries unchanged.

## 11. Reference

- `internal/executor/operators_join_agg.go:675-695` — `drainRows`
- `internal/executor/operator.go:9-29` — `Operator` interface
- `internal/config/defaults.go:120-132` — GUC registration
- PostgreSQL: `postgres/src/backend/executor/nodeHash.c`
- `analysis/tpch-lazy-hash-join-results.md` — parent drainRows bottleneck
