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

---

## Addendum (2026-08-19, M0134-0005as) — the descendant-walk taxonomy

The "Limitations" list above is a v0 snapshot; multi-level inheritance and
constraint propagation have since been implemented piecewise, and the pieces did
NOT converge on one walk. goopg still has no single `find_all_inheritors`
equivalent. Four walks exist, each with a deliberately different node set:

| walk | node set | epoch | cycle guard |
|---|---|---|---|
| `collectDMLPartitionLeaves` (`internal/executor/operators_storage.go:2880`) | partitions only, **leaves only** | snapshot | per-node `seen` |
| `allDescendants` (`internal/executor/operators_fk.go:1004`) | inheritance ∪ partitions, all descendants | current | per-**node** visited |
| `collectInheritanceAndPartitionChildren` (`internal/executor/operators_ddl.go:12187`) | inheritance ∪ partitions, **one level** | current | n/a |
| DDL cascades (`cascadeNotNullToChildren`, `cascadeCheckToChildren`, and the DROP-direction twins) | transitive recursion over the one-level primitive | current | per-**EDGE** visited, depth-bounded |

The per-node vs per-edge distinction is not cosmetic. PG's DDL cascades carry
`coninhcount`/`attinhcount` bookkeeping that counts **one increment per inheritance
edge**, so a diamond descendant is legitimately visited once per incoming edge
(`heap.c:2774-2845`). `allDescendants` dedups per node and therefore must never be
substituted into a DDL cascade, even though its reachability set looks identical.

### ALTER TABLE … ADD CHECK (M0134-0005as)

Oracle: `postgres/src/backend/commands/tablecmds.c:ATAddCheckNNConstraint`
(:9911-10049), shared by CHECK and NOT NULL.

1. `is_no_inherit` returns at :10004 **before enumerating any children** — no
   recursion whatsoever. goopg mirrors this with `cascadeCheckToChildren`'s
   leading `if noInherit { return nil }`. Before this slice the omission was
   only accidentally safe: the walk was partitions-only, and NO INHERIT on a
   partitioned table already errors 42P16 earlier, so the child list was always
   empty. Adding inheritance children removed that accident.
2. Children come from `find_inheritance_children` — **one level per call**, then
   DFS recursion (:10028-10046). `pg_inherits` unifies plain inheritance and
   partition attachment, so one call covers both. goopg matches this with
   `collectInheritanceAndPartitionChildren` + explicit recursion, rather than a
   flat descendant set.
3. Flags: every recursion level receives the user's **literal**
   NOT VALID / NOT ENFORCED clause (the same `Constraint` node is reused). This
   is the ALTER-time rule and is deliberately *asymmetric* with the CREATE-time
   rule (`MergeCheckConstraint`, :3219-3220), where validity derives purely from
   enforcement. `TestCheckInheritFlagsCreateTimeAntiSymmetry*` pins the
   difference so the two rules are not "tidied" into one. See M0134-0005ar.
4. The constraint name is resolved **once** at the top (PG fills
   `newConstraint->conname` before recursing) and that resolved name cascades.
   Previously goopg passed the raw `act.ConstraintName`, so an anonymous
   `ADD CHECK (x > 0)` propagated an empty name to every child.

Still unimplemented (see the deferral ledger): the `ONLY`-with-existing-children
rejection (`errmsg("constraint must be added to child tables too")`, :10020-10023),
and the parent's own `AddCheckFull` still registers the raw (possibly empty)
`act.ConstraintName` while its children now get the resolved one.
