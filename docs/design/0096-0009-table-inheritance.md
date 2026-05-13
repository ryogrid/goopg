# 0096-0009 — Table Inheritance (`INHERITS`)

**Status:** accepted  
**Date:** 2026-05-12  
**Milestone:** M0096-0009

## Problem

The `eval-plan-qual` and `eval-plan-qual-trigger` isolation specs set up
parent/child table hierarchies using `CREATE TABLE child () INHERITS (parent)`.
Without inheritance support:
- Child tables were created with zero columns (M0096-0008 fixed syntax
  acceptance, but columns were not actually copied).
- `SELECT * FROM parent` returned only parent rows, missing child rows.
- INSERT INTO child SELECT … failed due to column count mismatch.

## Solution

### Catalog (internal/catalog/catalog.go)

- `InMemory.inheritanceChildren map[uint32][]uint32` — maps parent OID to
  slice of child OIDs, parallel to `partitionChildren`.
- `RegisterInheritanceChild(parentOID, childOID uint32)` — called by the DDL
  executor after `CreateTable` assigns a real OID to the child.
- `InheritanceChildren(parentOID uint32) []*Table` — returns child `*Table`
  pointers for use by the planner.

### Executor DDL (internal/executor/operators_ddl.go)

`execCreateTable` is extended with a two-phase approach:

1. **Column merging** (before `CreateTable`): if `s.Inherits` is non-empty,
   each parent table is looked up and its columns are prepended to the child's
   column list, followed by any additional columns declared in the child body.
   This implements PostgreSQL's "copy-on-create" semantics where `()` means
   "inherit all parent columns with no additions".

2. **Post-creation registration** (after `CreateTable`): once the child table
   has an assigned OID, `RegisterInheritanceChild` is called for every parent
   so the planner can later discover the relationship.

### Planner (internal/planner/planner.go)

`planScanRangeVar` gains an inheritance-aware scan block after the existing
partition scan block:

```
if InheritanceChildren(tbl.OID) non-empty:
    root ← SeqScan(parent)
    for each child:
        root ← SetOp{UNION ALL, root, SeqScan(child)}
    return root
```

Unlike partitioned tables (where the parent itself holds no rows), an inherited
parent may contain rows from direct inserts, so the parent SeqScan is always
included at the front of the UNION ALL chain.

## Limitations

- Multi-level inheritance (grandchild tables) is not supported in v0 — the
  scan only walks one level of children.
- Column overrides and NOT NULL constraint inheritance are not propagated.
- UPDATE / DELETE on the parent do not cascade to children (EvalPlanQual
  requires M0096-0004 row-recheck semantics, not multi-table fan-out).
- ONLY keyword (suppress child-table scan) is not parsed.

## Effect

`CREATE TABLE c1 () INHERITS (p)` now creates c1 with the same columns as p.
`INSERT INTO c1 SELECT ...` correctly inserts rows into c1.
`SELECT * FROM p` now returns rows from p, c1, c2, and c3 via UNION ALL.
All core unit tests pass.
