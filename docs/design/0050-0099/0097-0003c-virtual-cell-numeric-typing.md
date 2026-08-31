# 0097-0003c — Numeric/boolean typing of virtual catalog-table cells

Status: accepted
Milestone: M0097-0003 (pg_regress coverage — sysviews)
Date: 2026-05-25

## Problem

`pg_backend_memory_contexts` and the other `Virtual: true` system catalog
tables expose their rows as `[][]string` via `catalog.Table.VirtualRows()`.
Both the plan-time materialiser (`planner.buildVirtualValues`) and the
run-time re-materialiser (`executor.rematerialiseVirtualRows`, added in
M0094-0005 to defeat plan-cache staleness) wrapped **every** cell in a
`planner.StringConst`, regardless of the column's declared type.

As a result, columns declared `int8`/`int4`/`bool` were compared and
aggregated **lexicographically over text** rather than by value. The
`sysviews` regress case exposed this directly:

```sql
select type, name, ident, level, total_bytes >= free_bytes
  from pg_backend_memory_contexts where level = 1;
```

For `TopMemoryContext` the synthetic row has `total_bytes = 1048576`,
`free_bytes = 524288`. The intended result is `t` (1048576 ≥ 524288), but
text comparison evaluated `"1048576" >= "524288"` → `'1' < '5'` → **false**,
so goopg emitted `f`. This was 1 of the case's diff lines (sysviews 11 → 9).

## Root cause

Virtual-cell construction was type-blind. The fix belongs at the single point
where the text payload becomes an `Expr`, and — because the planner and
executor build the cells independently — it must be applied identically in
both sibling paths (see [[pattern_sibling_paths_must_agree]]; encode/decode
and fast-path/interpreted twins are the recurring instance of this class).

## Fix

New shared helper `planner.TypedVirtualCell(pos int, value, colType string) Expr`:

- integer family (`int2`/`int4`/`int8`/`integer`/`bigint`/`smallint`):
  `strconv.ParseInt` → `IntegerConst` (executor maps to `KindInt`, numeric
  comparison).
- boolean (`bool`/`boolean`): the standard truthy/falsy spellings →
  `BooleanConst`.
- everything else, or any value that fails to parse for its declared type:
  `StringConst` (prior behavior — strictly a fallback, so no currently-passing
  view can regress unless it relied on a *valid* integer in an integer column
  sorting as text, which is never correct).

`buildVirtualValues` (planner) and `rematerialiseVirtualRows` (executor) both
call it. Display is keyed on the column's wire type, not the Datum kind, so a
typed cell renders identically to the old string cell — the change is
observable only in value comparison/aggregation, where it is now correct.

Scope deliberately excludes `oid`/`numeric`/`float` typing this loop to keep
the blast radius minimal; they remain `StringConst` and can be promoted with
the same helper if a future regress case needs value comparison on them.

## Verification

- `TestTypedVirtualCell` (planner) — unit coverage of every type arm plus the
  non-parsing fallback.
- Regress: `sysviews` 11 → 9 diff lines (the `total_bytes >= free_bytes` line
  flips to `t`). No regression across the int-heavy passing cases —
  `int2`/`int4`/`numerology`/`name`/`char`/`varchar`/`portals_p2`/
  `select_implicit`/`oid`/`reindex_catalog`/`select_having`/`boolean` all
  re-verified pass.

## Remaining sysviews blockers (NOT in this task)

The other two sysviews diffs need separate mechanisms and are out of scope:

1. **`Caller tuples` Bump context row** (4 diff lines): the test opens a
   tuplesort cursor and expects a `Bump`-type context named `Caller tuples`.
   goopg has no PG memory-context system; this needs a synthetic row in
   `pg_backend_memory_contexts.VirtualRows`.
2. **`path` array subscripting** (1 diff line): `c1.path[c2.level] =
   c2.path[c2.level]` needs an `int[]` `path` column plus array-subscript
   expression support — goopg has no array type or subscript operator yet.

## Files

- `internal/planner/planner.go` — `TypedVirtualCell`, `buildVirtualValues`.
- `internal/executor/operators.go` — `rematerialiseVirtualRows`.
- `internal/planner/virtual_test.go` — `TestTypedVirtualCell`.
