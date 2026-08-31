# M0097-0076 — DELETE … USING clause

Date: 2026-05-29
Status: Landed
Scope: `internal/parser/{ast,dml}.go`, `internal/analyzer/analyzer.go`,
       `internal/planner/{plan,planner}.go`,
       `internal/executor/operators_storage.go`
Related: M0097-0075 (UPDATE FROM RETURNING projection — same regress
         test, sibling DML branch), M0097-0065 (UPDATE FROM landing)

## Problem

`returning` regress test diff (line 104) failed on the next case after
the UPDATE FROM RETURNING projection fix:

```sql
DELETE FROM foo USING int4_tbl WHERE foo.f1 + 123455 = int4_tbl.f1
  RETURNING foo.*, int4_tbl.f1 as "i.f1";
```

`parseDelete` had no USING branch, so the statement silently no-op'd
with leftover tokens.

## Fix

Mirrors the existing UPDATE … FROM path (planUpdate / updateWithFrom /
appendUpdateRetRowWithFrom), one DML branch at a time:

1. **Parser** (`internal/parser/ast.go`, `internal/parser/dml.go`):
   `DeleteStmt` gains a `Using []RangeVar` field. `parseDelete`
   consumes an optional `USING` keyword after the target relation and
   parses a comma-separated list of `RangeVar`s before the WHERE
   clause. The grammar position matches PG (USING precedes WHERE).

2. **Analyzer** (`internal/analyzer/analyzer.go::analyzeDelete`):
   each USING table is appended to the scope's `rels` list with its
   alias (or the table name when none is given). This is what makes
   `WHERE … = a.x` and `RETURNING … a.y` type-check; without it,
   `resolveColumnRefType` raises 42P01 *missing FROM-clause entry for
   table "a"* before the planner runs. `analyzeDelete` now also
   analyzes `s.Returning` (mirroring `analyzeUpdate`).

3. **Planner** (`internal/planner/plan.go`, `internal/planner/planner.go`):
   `Delete` gains `UsingTables`, `UsingScans`, `UsingSchema`, and
   `UsingPred` fields. `planDelete` adds an early branch when
   `len(s.Using) > 0` that builds a combined resolve context:
   `rangeBinding`s for target + every USING table with monotonically
   increasing `sourceIdx` (1, 2, …) and `offset` advancing by each
   table's column count. `Schema` is the concatenation of all
   table-with-source schemas. `WHERE` resolves to `UsingPred`;
   `RETURNING` resolves against the same context (so projecting
   USING-table columns works the same way as UPDATE FROM RETURNING).

4. **Executor** (`internal/executor/operators_storage.go`):
   `deleteOp.Next` dispatches to a new `deleteWithUsing` when
   `len(o.plan.UsingTables) > 0`. The method mirrors `updateWithFrom`:
   collect all rows of each USING table into memory, scan the target
   table with no predicate, and for each target row do a recursive
   nested-loop cross-product against the cached USING rows. Each
   combination is evaluated against `UsingPred`; matching combinations
   become victims. Each target slot is recorded **at most once**
   (`seen` set keyed by `(block, slot)`), matching PG's semantics
   that a single qualifying row is enough to delete the tuple even
   when multiple cross-product rows match.

   For RETURNING, the planner indices place USING-table columns
   after the target columns, so `appendDeleteRetRowWithUsing(oldRow,
   usingPortion)` builds `evalRow = [oldRow..., usingPortion...]`
   before evaluating each RETURNING expression. The plain
   (non-USING) `Next` path keeps calling `appendDeleteRetRow`, which
   forwards `usingPortion == nil` for backwards compatibility.

   The USING-victim apply loop fires BEFORE-DELETE triggers and FK
   constraints (same as plain DELETE) and skips the EvalPlanQual
   retry chain — concurrent xmax conflicts simply skip the victim
   rather than chasing the update chain. The simplification is
   acceptable for v0: the regress test that motivates this branch
   does not run concurrently, and the broader EPQ retry semantics
   for DELETE FROM USING is M0097-0020 follow-up scope.

## Verification

- `go build ./...` clean, `go vet ./...` clean.
- `go test ./internal/parser/` — `TestParseDeleteUsing` confirms the
  `USING t1, t2 alias WHERE … RETURNING …` syntax round-trips.
- `go test ./internal/planner/` — `TestPlanDeleteUsing` confirms
  the planner populates `Delete.UsingTables`/`UsingPred` and resolves
  RETURNING references to USING-table columns.
- `go test ./internal/analyzer/` — full package passes.
- `go test ./internal/executor/` — full package passes modulo the
  pre-existing `TestToastByteaRoundTrip` flake (also flaky on
  baseline `e1185591`, unrelated to this change).

## Remaining `returning` blockers

This change clears the second item on the M0097-0075 remaining-blocker
list. The next in-order divergences are:

- ALTER TABLE … ADD COLUMN … DEFAULT backfill into existing rows.
- INHERITS row propagation through UPDATE FROM / DELETE USING.
- RETURNING OLD / RETURNING NEW alias references.

Each is sized for a single follow-up loop.
