# REF-014: UPSERT (ON CONFLICT)

## Overview

`INSERT … ON CONFLICT DO NOTHING / DO UPDATE SET …` (UPSERT) atomically inserts a row or updates an existing row if a conflict occurs on a unique index or exclusion constraint.

## goopg Implementation

**Package:** `internal/executor/operators_upsert.go`

### Key Types

- `upsertOp` — handles the DO UPDATE path.
- `onConflictDoNothingOp` — handles the DO NOTHING path.

### DO NOTHING Path

```
onConflictDoNothingOp.Next()
  ├─ Insert the row via writeHeapRow.
  ├─ If no unique-violation error → return (row inserted).
  └─ If unique-violation error → swallow the error, return EOF.
```

### DO UPDATE Path

```
upsertOp.Next()
  ├─ Insert the row via writeHeapRowReturning (returns ItemPointer).
  ├─ If no conflict → update the index, return.
  └─ If conflict (unique-violation error):
       ├─ Resolve the arbiter index (ON CONFLICT (col)).
       ├─ Re-read the conflicting tuple via the index.
       ├─ Stamp xmax on the existing tuple.
       ├─ writeHeapRowReturning for the new tuple.
       ├─ Update the arbiter index pointer.
       └─ If ON CONFLICT ON CONSTRAINT, validate constraint name.
```

### Planner Integration

The planner produces:
- `Insert` with `OnConflict` field containing the conflict target and action.
- For `ON CONFLICT DO NOTHING`, the executor builds `onConflictDoNothingOp`.
- For `ON CONFLICT DO UPDATE`, the executor builds `upsertOp`.

### Limitations

- `ON CONFLICT ON CONSTRAINT name` is partially supported (parsed and basic validation).
- `EXCLUDED` row reference is supported via the `upsertOp`'s excluded-row tracking.

## PostgreSQL Implementation (Deep Dive)

### Speculative Insertion

PostgreSQL uses **speculative insertion** for UPSERT. The INSERT
is performed "speculatively": the tuple is inserted but marked
as "potentially conflicting". If no conflict occurs, the
speculation is confirmed. If a conflict is detected, the
speculative tuple is removed and the ON CONFLICT DO UPDATE path
is taken.

This approach has two advantages:
1. The INSERT and the conflict check are atomic (no TOCTOU race).
2. Concurrent UPSERTs on the same key are correctly serialised
   because the speculation holds the necessary locks.

goopg's approach is different: it tries the INSERT, and if it
fails with a unique-violation error, it falls back to the UPDATE
path. This is not atomic — between the INSERT failure and the
UPDATE, another transaction could modify the tuple.

### Arbiter Index Resolution

PostgreSQL resolves the arbiter index at planning time by
matching the conflict target (`ON CONFLICT (col)`) against
existing unique indexes. If the target columns match a partial
index (`WHERE ...`), the conflict detection uses the partial
index's predicate.

goopg resolves the arbiter index at execution time by matching
the column name against the table's unique indexes. Partial
indexes are not supported.

### EXCLUDED Row

PostgreSQL provides the virtual `EXCLUDED` table in the DO
UPDATE expression. `EXCLUDED.col` refers to the value that was
proposed for insertion. goopg also supports `EXCLUDED` via the
`upsertOp`'s excluded-row state.

### Multiple Conflicts

When INSERTing multiple rows, PostgreSQL checks conflicts for
each row individually. If a row conflicts, the DO UPDATE path
is taken for that row; if not, the row is inserted. goopg
processes one row at a time via the INSERT executor, so the
multi-row case is handled naturally.

## goopg Improvement Analysis

### P0: Speculative Insertion (Concurrency Fix)

Implement speculative insertion:
1. Acquire a speculative lock on the target page.
2. Insert the tuple as "speculative".
3. If no conflict: confirm (mark as non-speculative).
4. If conflict: remove the speculative tuple, run DO UPDATE.

**Impact:** Correctness for concurrent UPSERTs. Currently, two
concurrent UPSERTs on the same key may both succeed (INSERT)
or both fail (UNIQUE violation → retry loop).

### P1: Arbiter Resolution at Plan Time

Move arbiter index resolution from the executor to the planner.
Store the resolved index OID in the `OnConflict` plan node.

**Impact:** Slightly faster execution (no runtime catalog lookup
for the arbiter index).

## References

- goopg: `internal/executor/operators_upsert.go`
- PG UPSERT: `postgres/src/backend/executor/nodeModifyTable.c`
  (`ExecOnConflictUpdate`, `ExecOnConflictNoUpdate`)
- PG speculative insertion: `postgres/src/backend/access/heap/heapam.c`
  (`heap_insert` with `HEAP_INSERT_SPECULATIVE` flag)
