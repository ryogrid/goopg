# 0097-0043 — ORDER BY NULLS FIRST / NULLS LAST

## Summary

Implements the PostgreSQL `ORDER BY expr [ASC|DESC] [NULLS FIRST|NULLS LAST]`
extension, and corrects the default NULL placement to match PostgreSQL semantics.

## Problem

Before this change, goopg had two NULL-ordering bugs:

1. **Missing syntax**: `ORDER BY col NULLS FIRST` produced a parser error
   (`syntax error at or near "nulls"`). The `NULLS` keyword was not recognized
   in the `parseSortItem` path.

2. **Wrong default behavior**: The `lessRows` comparator in the sort executor
   treated NULLs as "less than" any non-NULL value regardless of direction.
   This produced:
   - ASC: NULLs appear **first** (wrong — PostgreSQL puts them **last**)
   - DESC: NULLs appear **last** (wrong — PostgreSQL puts them **first**)

## PostgreSQL semantics

| Sort clause                   | NULL placement   |
|-------------------------------|-----------------|
| `ORDER BY col` (ASC default)  | NULLs LAST      |
| `ORDER BY col ASC`            | NULLs LAST      |
| `ORDER BY col DESC`           | NULLs FIRST     |
| `ORDER BY col ASC NULLS FIRST`  | NULLs FIRST (override) |
| `ORDER BY col DESC NULLS LAST`  | NULLs LAST (override)  |

## Implementation

### Parser (`internal/parser/ast.go`, `internal/parser/select.go`)

`SortBy.NullsFirst *bool` added:
- `nil` = use default (computed from `Desc`)
- `&true` = explicit `NULLS FIRST`
- `&false` = explicit `NULLS LAST`

`parseSortItem` now checks for `NULLS FIRST` / `NULLS LAST` after
parsing the direction keyword using `acceptIdentKeyword`.

### Planner (`internal/planner/plan.go`, `internal/planner/planner.go`)

`SortKey.NullsFirst bool` added. Helper `sortByNullsFirst(sb parser.SortBy) bool`
computes the effective placement:
```
if sb.NullsFirst != nil { return *sb.NullsFirst }
return sb.Desc  // DESC default: nulls first; ASC default: nulls last
```

All `SortKey` construction sites updated to pass `NullsFirst`.

### Executor (`internal/executor/operators.go`)

`sortOp.lessRows` updated:
```go
if av.IsNull() && !bv.IsNull() {
    return k.NullsFirst  // null comes first when NullsFirst=true
}
if !av.IsNull() && bv.IsNull() {
    return !k.NullsFirst
}
```

### Window operator (`internal/executor/operators_window.go`)

`compareSortDatums` signature extended with `nullsFirst bool`.
NULL placement formula:
```
cmp = 1  when (nullsFirst == desc)   // covers both defaults and
cmp = -1 when (nullsFirst != desc)   // explicit overrides
```

PARTITION BY call site passes `nullsFirst=false` (equality semantics;
direction irrelevant).

### EXPLAIN output (`internal/executor/operators_explain.go`)

Non-default NULL placement emitted in Sort Key lines:
- `NULLS FIRST` when `NullsFirst && !Desc`
- `NULLS LAST` when `!NullsFirst && Desc`

## Impact

| Test    | Before | After  |
|---------|--------|--------|
| select  | 876    | 238    |
| case    | 148    | 93     |
| window  | 3894   | 3269   |

Additionally fixed `TestCompatWindowRankNullPeersAsc` which was asserting
old incorrect behavior (NULLs first for ASC window ORDER BY).
