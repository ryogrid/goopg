# 0016-0004 — Recursive CTE fixpoint execution

## Status

draft

## Goal

Implement `WITH RECURSIVE` fixpoint execution in goopg's planner and
executor, building on the UNION ALL support from 0016-0003.

## Background

`WITH RECURSIVE cte AS (anchor UNION ALL recursive_member) SELECT ...`
requires:
1. Detecting that the CTE is recursive (the `Recursive` flag on
   `WithClause` and the CTE self-reference in the body).
2. Splitting the CTE body into an anchor (the non-recursive term)
   and a recursive member (the term that references the CTE name).
3. Executing a fixpoint: start with the anchor result, feed each
   iteration's output as the "working table" input to the recursive
   member, and accumulate all rows until the working table is empty.

v0 only supports `UNION ALL` recursive CTEs. `UNION DISTINCT`
(de-duplication) and `INTERSECT`/`EXCEPT` recursive forms are
deferred.

## Parser & AST

The parser already handles `WITH RECURSIVE` — `WithClause` has a
`Recursive bool` field. The CTE name is resolved as a table reference
inside the CTE body. When `Recursive=true`, the CTE body must be a
`UNION ALL` with a reference to the CTE name (non-recursive CTE bodies
may not reference themselves).

No parser changes are needed.

## Analyzer Changes

### CTE Self-Reference Detection

When analyzing a `WITH RECURSIVE` CTE, check whether the CTE body
references the CTE name. If it does, it's recursive and must be a
`UNION ALL` between the anchor and recursive member.

```go
func analyzeCTE(def *parser.CommonTableExpr, cat catalog.Catalog, scope *scope) error {
    if def.Query.SetOp == nil || !def.Query.SetOp.All {
        return analyzeError(def.Pos(), "0A000",
            "WITH RECURSIVE requires UNION ALL")
    }
    // Analyze the recursive member with a scope that includes the
    // CTE name as a valid relation, so self-references resolve.
    recScope := &scope{parent: scope, cat: cat}
    recScope.AddRelation(def.Name, &catalog.Table{Name: def.Name})
    // Anchor: analyze left side (no CTE reference in scope).
    if err := analyzeSelectWithParent(def.Query, cat, scope); err != nil {
        return err
    }
    // Recursive member: analyze right side with CTE in scope.
    if err := analyzeSelectWithParent(def.Query.SetOp.Right, cat, recScope); err != nil {
        return err
    }
    return nil
}
```

## Planner Changes

### Plan Nodes

Add a `RecursiveUnion` plan node:

```go
type RecursiveUnion struct {
    pos      int
    Anchor   Node  // non-recursive term
    Recursive Node // recursive term (references CTE)
    Schema   Schema
}
```

The anchor and recursive terms are planned independently. The anchor is
planned with the CTE name NOT resolvable (no self-reference). The
recursive term is planned with the CTE name resolvable via a
`WorkTableScan` node.

### WorkTableScan

Add a `WorkTableScan` plan node that reads from the in-memory working
table:

```go
type WorkTableScan struct {
    pos    int
    schema Schema
}
```

### Planning

In `preplanWithClause`, when a CTE has `Recursive=true`:

1. Clone the CTE body for the anchor (remove the UNION ALL right side
   or the recursive reference).
2. Plan the anchor without the CTE in scope.
3. Plan the recursive member with the CTE in scope as a WorkTableScan.
4. Return a `RecursiveUnion` combining both.

```go
func planRecursiveCTE(def *parser.CommonTableExpr, cat catalog.Catalog) (Node, error) {
    anchor := def.Query
    anchor.SetOp = nil  // strip UNION ALL for anchor
    anchorPlan, err := planSelect(anchor, cat)
    if err != nil {
        return nil, err
    }
    // Recursive member: plan with CTE as WorkTableScan.
    recPlan, err := planSelect(def.Query.SetOp.Right, cat)
    if err != nil {
        return nil, err
    }
    return &RecursiveUnion{
        Anchor:    anchorPlan,
        Recursive: recPlan,
        Schema:    anchorPlan.Output(),
    }, nil
}
```

## Executor Changes

### RecursiveUnion Operator

```go
type recursiveUnionOp struct {
    plan      *planner.RecursiveUnion
    anchor    Operator
    recursive Operator
    working   []Row     // current working table rows
    iterRows  []Row     // rows produced by the current iteration
    output    []Row     // all accumulated output rows
    outIdx    int       // current output position
    done      bool
    ctx       *Context
}
```

### Execution

1. Open both anchor and recursive operators.
2. Drain the anchor into `working` and `output`.
3. Loop:
   a. If `working` is empty, stop.
   b. Feed each row in `working` through the recursive operator
      (via the `WorkTableScan` which returns rows from `working`).
   c. Collect the output into `iterRows` and `output`.
   d. Replace `working` with `iterRows`.
   e. If `iterRows` is empty or max iterations reached, stop.

```go
func (o *recursiveUnionOp) Next() (Row, error) {
    if o.done {
        return nil, EOF
    }
    // First: drain anchor.
    if o.anchor != nil {
        for {
            row, err := o.anchor.Next()
            if err == EOF {
                o.anchor.Close()
                o.anchor = nil
                break
            }
            if err != nil {
                return nil, err
            }
            o.working = append(o.working, row)
            o.output = append(o.output, row)
        }
    }
    // Subsequent iterations: drain recursive through working.
    for len(o.output) == 0 || o.outIdx >= len(o.output) {
        if len(o.working) == 0 {
            o.done = true
            return nil, EOF
        }
        // Set working table and drain recursive operator.
        o.setWorkingTable(o.working)
        o.working = nil
        o.recursive.Reopen() // reset the recursive operator for a new pass
        for {
            row, err := o.recursive.Next()
            if err == EOF {
                break
            }
            if err != nil {
                return nil, err
            }
            o.output = append(o.output, row)
            o.working = append(o.working, row)
        }
        // Reset output index for the new iteration
        o.outIdx = 0
    }
    row := o.output[o.outIdx]
    o.outIdx++
    return row, nil
}
```

### WorkTableScan Operator

```go
type workTableScanOp struct {
    working []Row
    idx     int
}
func (o *workTableScanOp) Next() (Row, error) {
    if o.idx >= len(o.working) {
        return nil, EOF
    }
    row := o.working[o.idx]
    o.idx++
    return row, nil
}
```

## Cycle Safety

v0 limits recursive CTE iterations to 1000 (matching upstream's
`avoid cycle detection at 1000 as a safety limit` default). When the
limit is hit, execution stops and returns the rows accumulated so far.

## Out of Scope

- `UNION DISTINCT` recursive CTEs (deduplication)
- `SEARCH` and `CYCLE` clauses
- Multiple recursive CTEs in the same WITH list
- Non-`UNION ALL` recursive forms

## References

- PostgreSQL `WITH RECURSIVE`:
  https://www.postgresql.org/docs/current/queries-with.html
- Postgres executor: `postgres/src/executor/nodeRecursiveunion.c`,
  `postgres/src/executor/nodeWorktablescan.c`
- `docs/design/0016-0003-union-all-planner-and-executor.md`
