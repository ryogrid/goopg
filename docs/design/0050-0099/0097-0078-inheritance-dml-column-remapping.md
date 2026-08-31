# 0097-0078 — Inheritance-Aware UPDATE / DELETE Column Remapping

## Problem

`UPDATE parent SET ...` and `DELETE FROM parent ...` were scanning inheritance
children correctly (the scan loop already enumerated them) but applying
expressions using **parent column ordinals** against **child row layouts**.

### Concrete failure

```sql
CREATE TABLE foo (f1 serial, f2 text, f3 int DEFAULT 42);
CREATE TABLE foochild (fc int) INHERITS (foo);
ALTER TABLE foo ADD COLUMN f4 int8 DEFAULT 99;
-- foochild column layout: [f1=0, f2=1, f3=2, fc=3, f4=4]
-- foo column layout:      [f1=0, f2=1, f3=2,        f4=3]

UPDATE foo SET f4 = f4 + f3 WHERE f4 = 99 RETURNING *;
```

The WHERE predicate `ColumnRef{Index:3} = 99` matched parent column 3 (f4=99).
For the child row, column index 3 is `fc=-123`, so the child row was never
matched. Additionally, the SET expression `f4+f3` evaluated `childRow[3]+childRow[2]`
= `-123+999=876` instead of `childRow[4]+childRow[2]` = `99+999=1098`.

## Fix

Added three helper functions to `internal/executor/operators_storage.go`:

- **`buildInheritColMap(parentCols, childCols)`** — builds `result[i] = child ordinal for parent column i` (by name, case-insensitive)
- **`remapChildRowToParent(childRow, colMap)`** — maps a child-layout row to parent column positions
- **`remapParentRowToChild(parentRow, childRaw, parentCols, childCols)`** — maps parent-space results back to child column order, preserving child-only columns from `childRaw`

### Affected code paths

**`updateOp.Next()` seq-scan path** (simple UPDATE):
- Detects inheritance children via `inheritChildOIDs` set
- For inheritance children: passes `nil` predicate to `scanMatching`, then evaluates
  the predicate manually against the parent-aligned row
- Evaluates SET exprs in parent column space, remaps results back to child layout
- Stores `retRow` (parent-aligned) on `pendingUpdate` for RETURNING evaluation

**`updateWithFrom()` (UPDATE … FROM)**:
- Expands scan targets to include inheritance children
- Remaps child row to parent layout before `FromPred` evaluation
- Builds combined row `[parent_aligned_child, from_cols]` matching the expected column offsets
- Writes `actualNewRow` (child layout) but uses `retNewRow` (parent layout) for RETURNING

**`deleteOp.Next()` seq-scan path** (simple DELETE):
- Same pattern as updateOp — remaps predicate evaluation and stores `retRow` for RETURNING

**`deleteWithUsing()` (DELETE … USING)**:
- Expands scan targets to inheritance children
- Remaps for UsingPred evaluation and RETURNING

## Impact

`returning` regress test: 479 → 394 diff lines (−85). Remaining blockers:
rules/views (ON INSERT/UPDATE/DELETE DO INSTEAD), whole-row RETURNING variables,
and RETURNING OLD/NEW (PostgreSQL 18 syntax).
