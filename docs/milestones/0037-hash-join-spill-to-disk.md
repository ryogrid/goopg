# Milestone 0037 — Spill-to-Disk Hash Join

**Status:** accepted
**Depends on:** M0036 (lazy hash join — confirmed parent drainRows re-materializes child output)
**Drives:** Break the `drainRows` copy chain across join levels by writing intermediate results to disk when they exceed a memory budget, then reading them back one partition at a time.

## Context

M0036 identified the fundamental bottleneck: `drainRows` on a child joinOp
re-materializes the child's entire output at the parent level. Each join level
deep-copies all rows from the level below, compounding memory linearly with
join depth. For Q2's 4 hash-join levels over 800K-row base tables, this
produces ~7 GB of drainRows copies — and with GC overhead, 30+ GB RSS.

No single-operator optimization (lazy, streaming, etc.) can fix this because
the two-way join model *requires* the build side to be fully present before
the hash table can be constructed.

### How spill-to-disk solves this

When `drainRows` exceeds a configurable memory budget (e.g., 500 MB), the
rows are written to temporary files on disk instead of accumulated in a
`[]Row` slice. The hash table is then built in **multiple passes**:

1. **Partition pass**: Both build and probe sides are partitioned into N
   buckets based on a hash of the join key. Each bucket is written to its
   own temporary file.
2. **Join pass**: One bucket at a time is read back into memory and hash-joined.
   Each bucket is small enough to fit in the memory budget.

This is the **Grace Hash Join** algorithm (also called Hybrid Hash Join when
the first partition stays in memory). It's what PostgreSQL uses when
`work_mem` is exceeded.

### Relation to current two-way model

The spill-to-disk operator wraps the child join's `Next()` output. When
`drainRows` is called:
- If total rows × row size ≤ budget: accumulate in `[]Row` slice (current behaviour).
- If exceeded: write to spill file, return a `spillReader` that yields rows
  on demand. Free the accumulated `[]Row` memory.

This means the parent hash join's build side is now a spill file, not an
in-memory slice. Memory consumed = budget (constant), not child output size.

## Required Design Docs

1. `docs/design/0037-0001-spill-to-disk-hash-join.md` — Spill file format,
   `spillWriter`/`spillReader`, integration into `drainRows`, memory-budget
   GUC (`work_mem` or similar), Grace partitioning.

## Definition of Done

### Phase A: Bounded drainRows with spill

1. **`spillWriter`**: Writes `Row` slices to a temp file. Binary format:
   4-byte little-endian length prefix + Datum values serialized.
2. **`spillReader`**: Implements `Operator` — `Open()` seeks to start,
   `Next()` deserializes one `Row`, `Close()` deletes the temp file.
3. **`drainRowsBounded(op, maxBytes)`**: Replaces `drainRows(op)`.
   Accumulates rows in `[]Row`. When `cap(rows) * avgRowSize > maxBytes`,
   flushes accumulated rows to a `spillWriter`, continues appending to disk.
   Returns a `spillOperator` (implements `Operator` on top of `spillReader`)
   instead of `[]Row`.

### Phase B: Grace hash join

4. **Partition function**: `partitionRows(rows, keyExpr, numPartitions)` →
   N spill files, one per partition. Rows are partitioned by `hash(joinKey) % N`.
5. **Grace hash join executor**: `joinOp` detects when build-side row count ×
   estimated row size > `work_mem`. Switches to grace mode:
   - Partition build and probe sides into N files.
   - For each partition i: read build partition i → build in-memory hash table;
     read probe partition i → probe hash table; emit joined rows.
6. **`work_mem` GUC**: New configuration variable (TypeInt, UnitKB, BootVal 512MB).
   Registered in `internal/config/defaults.go`. Sets the per-operator memory budget.

### Phase C: Integration

7. **`openLazyHashJoin` integration**: When `drainRows` on the build side
   would exceed `work_mem`, use spill reader instead of in-memory `[]Row`.
8. **Regression tests**: All 22 TPC-H queries build and execute.
9. **Memory measurement**: Q2 on partial SF=1 data with `work_mem=512MB`
   completes with peak RSS ≤ 15 GB at `shared_buffers=2048MB`.

## Reference

- `internal/executor/operators_join_agg.go:40-75` — `joinOp.Open()` (lazy hash join)
- `internal/executor/operators_join_agg.go:675-695` — `drainRows` (deep-copies all rows)
- `internal/executor/operator.go:9-29` — `Operator` interface
- `internal/config/defaults.go:120-132` — GUC registration pattern
- PostgreSQL: `postgres/src/backend/executor/nodeHash.c` — Grace hash join
- PostgreSQL: `postgres/src/backend/utils/sort/tuplesort.c` — external sort (spill pattern)
- `analysis/tpch-lazy-hash-join-results.md` — parent drainRows re-materialization bottleneck
