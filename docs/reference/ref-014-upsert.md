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

## PostgreSQL Implementation

PostgreSQL's UPSERT (`nodeModifyTable.c`):

- **Arbiter index detection** — PostgreSQL resolves the arbiter
  index at planning time by matching the conflict target columns
  against existing unique/exclusion indexes. goopg's approach
  is similar.
- **Excluded row** — PostgreSQL provides the virtual `EXCLUDED`
  table referencing the proposed insertion values. goopg
  implements this via the `upsertOp`'s excluded-row state.
- **Concurrent UPSERT** — PostgreSQL handles concurrent UPSERTs
  via a "check for unique violation → lock → re-check" retry
  loop. goopg's approach may miss concurrent inserts that commit
  between the check and the insert.
- **Partial unique indexes** — PostgreSQL supports ON CONFLICT
  WHERE clause on partial indexes. goopg does not.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Concurrent safety | No retry loop | Retry loop on serialisation failure |
| Partial indexes | Not supported | ON CONFLICT WHERE on partial indexes |
| EXCLUDED | Supported | Supported |
| ON CONSTRAINT | Basic validation | Full constraint resolution |

## References

- goopg: `internal/executor/operators_upsert.go`
- PostgreSQL UPSERT: `postgres/src/backend/executor/nodeModifyTable.c`
- PG documentation: https://www.postgresql.org/docs/current/sql-insert.html
