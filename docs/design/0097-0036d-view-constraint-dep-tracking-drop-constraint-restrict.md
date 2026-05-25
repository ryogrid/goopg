# 0097-0036d — View→Constraint Dependency Tracking for DROP CONSTRAINT RESTRICT

**Milestone**: M0097-0036 / M0097-0003  
**Status**: accepted  
**Date**: 2026-05-25

## Problem

The `functional_deps` regress test had 21 remaining diff lines:

- 5 × 3 = 15 lines from `ALTER TABLE t DROP CONSTRAINT name RESTRICT` failing to raise the expected error when views depend on the constraint for GROUP BY functional dependency.
- 1 line from `EXECUTE foo` failing after a successful DROP CONSTRAINT (the prepared plan references a PK that no longer exists — re-planning produces the error naturally).

goopg had no dependency registry, so `ALTER TABLE articles DROP CONSTRAINT articles_pkey RESTRICT` succeeded even when `fdv1 GROUP BY id` (which uses `articles_pkey` for functional determination) was live.

## Root Cause

Two missing pieces:
1. **No dependency tracking at CREATE VIEW time** — goopg validates the view body by planning it (M0097-0003), but discards the result without recording *which PK constraints* the plan relied on.
2. **DROP CONSTRAINT parsed as no-op** — the `parseAlter` `KwDrop` branch consumed the entire rest of the statement as a no-op; no `AlterTableDropConstraint` action existed.

## Fix

### Parser (`internal/parser/ast.go`, `internal/parser/ddl.go`)

Added `AlterTableDropConstraint AlterTableActionKind` and `Restrict bool` field to `AlterTableAction`. In `parseAlter`, the `KwDrop` branch now:
- Consumes `DROP`
- If next token is `CONSTRAINT`: parses `name [RESTRICT|CASCADE]` and returns a real `AlterTableDropConstraint` action (Restrict=true by default)
- Otherwise (DROP COLUMN, etc.): falls through to the existing no-op consume-rest path

### Catalog (`internal/catalog/catalog.go`)

Added `constraintViewDeps map[string][]string` to `InMemory`, keyed by `"tableOID:constraintName"` → list of view names. New methods:

- `RegisterViewConstraintDep(viewName, tableOID, constraintName)` — idempotent
- `UnregisterViewConstraintDeps(viewName)` — removes all deps for a dropped view
- `ViewsDependingOnConstraint(tableOID, constraintName) []string` — returns dependent view names
- `DropPrimaryKeyConstraint(tableOID, constraintName) bool` — removes the PK index from both `byTable` and `indexes` maps

### Executor (`internal/executor/operators_ddl.go`)

**CREATE VIEW** (`execCreateView`): after successfully creating the view, calls `collectViewPKDeps(s.Query, catalog)` to scan the SELECT AST for GROUP BY functional dependencies, then registers each `(tableOID, constraintName)` pair in the catalog.

`collectViewPKDeps` walks:
- Main SELECT body: for each `RangeVar` in `From`, checks if all PK columns of that table appear in `GroupBy` (case-insensitive, handles table aliases and qualified names)
- UNION/INTERSECT/EXCEPT branches: recurses into `sel.SetOp.Right`
- WHERE subqueries: walks `InExpr.Subquery`, `SubqueryExpr.Inner`, `ExistsExpr.Subquery`

**DROP VIEW** (`execDropView`): calls `im.UnregisterViewConstraintDeps(name.String())` after the view is removed.

**ALTER TABLE** (`execAlterTable`): new `case parser.AlterTableDropConstraint` routes to `execAlterTableDropConstraint`, which:
1. Finds the named PK index on the table (error 42704 if not found)
2. In RESTRICT mode: calls `im.ViewsDependingOnConstraint` — if any views depend on it, raises `2BP01 "cannot drop constraint … because other objects depend on it"` with `DETAIL: view N depends on constraint C on table T` and `HINT: Use DROP … CASCADE …`
3. Otherwise: calls `im.DropPrimaryKeyConstraint` to remove the index

## Test Coverage

- `TestParseAlterTableDropConstraint` (parser): verifies RESTRICT/CASCADE/default parsing
- `TestViewConstraintDepTracking` (catalog): register, dedup, unregister lifecycle
- `TestDropPrimaryKeyConstraint` (catalog): index removed from byTable + indexes maps
- `TestPort_RegressSuite/functional_deps` → **PASS** (was 21 diff lines)

## Sibling-Path Note

The view body validation path (`planner.Plan(s.Query, cat)`) already existed in `execCreateView` (M0097-0003). The new dep-scan (`collectViewPKDeps`) runs a parallel AST walk that mirrors the planner's `isColumnFunctionallyDetermined` logic — kept separately rather than threading state through the planner to avoid coupling. Both must agree on which PKs establish functional dependency (primary key only, not unique).

## Prepared-Plan Invalidation

The last test case (`PREPARE foo … GROUP BY id; EXECUTE foo; ALTER TABLE DROP CONSTRAINT …; EXECUTE foo → fail`) works automatically: goopg's EXECUTE re-plans from the stored AST on every call (`disablePlanCache = true`). After the PK index is dropped, re-planning `GROUP BY id` fails because `isColumnFunctionallyDetermined` returns false — the error is the same `"column must appear in GROUP BY"` that PostgreSQL produces from plan-cache invalidation.
