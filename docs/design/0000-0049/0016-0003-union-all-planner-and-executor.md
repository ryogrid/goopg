# 0016-0003 — UNION ALL planner and executor

## Status

draft

## Goal

Implement `UNION ALL` set operations in goopg's planner and executor,
unblocking recursive CTE fixpoint execution (M0016-0003).

## Background

goopg's parser already accepts `UNION ALL` syntax and produces
`*parser.SetOpClause` nodes on `SelectStmt`. The analyzer currently
rejects ALL set operations with `0A000 "set operations are not
supported in v0 planner"`.

This doc scopes `UNION ALL` only. `UNION DISTINCT`, `INTERSECT`,
and `EXCEPT` remain rejected.

## Analyzer Changes

### Scope

Replace the blanket rejection in `analyzeSelectWithParent` with a
clause that passes through `UNION ALL` and rejects everything else:

```go
if s.SetOp != nil {
    if s.SetOp.Type != parser.SetOpUnion || !s.SetOp.All {
        return analyzeError(s.SetOp.Pos(), "0A000",
            "set operations are not supported in v0 planner")
    }
    // UNION ALL: analyze both sides independently.
}
```

### Analysis

For `UNION ALL`, analyze the left and right `SelectStmt`
independently. No type coercion is performed in v0 — both sides must
produce the same number of columns and column types are taken from the
left side.

```go
if err := analyzeSelectWithParent(s, cat, parent); err != nil {
    return err
}
if err := analyzeSelectWithParent(s.SetOp.Right, cat, parent); err != nil {
    return err
}
```

## Planner Changes

### Plan node

Add a `SetOp` plan node:

```go
type SetOp struct {
    pos   int
    Left  Node
    Right Node
    All   bool // true for UNION ALL
}
```

### Planning

In `planSelect`, after handling the primary SELECT, check for a
trailing `SetOp`:

```go
if s.SetOp != nil && s.SetOp.All {
    left := planSelect(s, cat) // already planned
    right := planSelect(s.SetOp.Right, cat)
    if _, ok := left.(*Project); !ok {
        left = &Project{pos: left.Pos(), Input: left, Cols: left.Output()}
    }
    if _, ok := right.(*Project); !ok {
        right = &Project{pos: right.Pos(), Input: right, Cols: right.Output()}
    }
    return &SetOp{pos: s.Pos(), Left: left, Right: right, All: true}
}
```

Both sides must be wrapped in a `Project` node so they have consistent
column schemas for the executor.

## Executor Changes

### Operator

Add a `setOp` executor operator:

```go
type setOp struct {
    plan       *planner.SetOp
    left       Operator
    right      Operator
    leftDone   bool
    rightDone  bool
}
```

### Execution

`Next()` drains the left side first, then the right side:

```go
func (o *setOp) Next() (Row, error) {
    if !o.leftDone {
        row, err := o.left.Next()
        if err == EOF {
            o.leftDone = true
            o.left.Close()
        } else if err != nil {
            return nil, err
        } else {
            return row, nil
        }
    }
    if !o.rightDone {
        row, err := o.right.Next()
        if err == EOF {
            o.rightDone = true
            o.right.Close()
        } else {
            return nil, err
        }
    }
    return nil, EOF
}
```

### Schema

The output schema is the left side's schema (column names and types
from the first SELECT in the UNION).

## Out of Scope

- `UNION DISTINCT` (deduplication) — requires hash or sort
- `INTERSECT` / `EXCEPT` — full set operation support
- Recursive CTE fixpoint execution — separate slice (M0016-0003)
- Type coercion across UNION limbs — v0 assumes compatible types
- Column name resolution for ORDER BY / LIMIT on UNION — deferred

## References

- PostgreSQL `UNION` clause: https://www.postgresql.org/docs/current/queries-union.html
- Postgres executor: `postgres/src/executor/nodeSetOp.c`
- `docs/design/0016-0001-with-parser-ast-and-name-resolution.md`
- `docs/design/0016-0002-nonrecursive-cte-planner-executor.md`
