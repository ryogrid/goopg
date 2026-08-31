# M0134-0183 — `typed_table.sql`: typed-table ALTER TABLE restrictions landed, PARKED

Status: **PARKED** (case sized live for the first time; one real, verified
fix landed — the five typed-table `ALTER TABLE` restrictions PG enforces
were entirely unchecked — plus a dominant REFACTOR-tier `DROP TYPE`
dependency-tracking gap and three smaller buckets ledgered for later work).

## What the file tests

`postgres/src/test/regress/sql/typed_table.sql` (80 lines) exercises
`CREATE TABLE ... OF <composite_type>` ("typed tables"): creation,
`\d` display of the `OF type` annotation, column-option syntax
(`WITH OPTIONS PRIMARY KEY`/`DEFAULT`/`NOT NULL`), the five `ALTER TABLE`
operations PG specifically disallows on a typed table, `DROP TYPE`
dependency enforcement against the tables/functions built on it, implicit
casting of a typed table's row value to its declared type when passed to a
function expecting that type, and calling a `LANGUAGE SQL` function that
`RETURNS SETOF <composite_type>`.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v typed_table`: **0/1 PASS, 150 diff lines**
(first live run — CSV was `not-tried`) → **135 diff lines** after this
loop's fix.

### Root-cause bucketing (confirmed via a live throwaway server)

1. **Five typed-table `ALTER TABLE` restrictions were entirely
   unimplemented — FIXED this loop.** `ALTER TABLE persons ADD COLUMN
   comment text` / `DROP COLUMN name` / `RENAME COLUMN id TO num` / `ALTER
   COLUMN name TYPE varchar` / `INHERIT stuff` all silently succeeded (or,
   for the last two, failed with the WRONG error) where PG unconditionally
   refuses all five on any table created `OF` a composite type. This is the
   single largest cause of the file's diff once traced downstream: the
   silently-successful `DROP COLUMN name` meant the immediately-following
   `ALTER COLUMN name TYPE varchar` in the same block hit "column does not
   exist" instead of PG's typed-table-specific message — a second symptom
   of the SAME root cause, not a separate bug.
2. **`DROP TYPE` has no dependency tracking — REFACTOR-tier, dominant
   remaining bucket.** `DROP TYPE person_type RESTRICT` should fail
   (listing the table/function dependents) and `DROP TYPE person_type
   CASCADE` should succeed and drop them; goopg's RESTRICT silently
   succeeds with no check, so by the time the CASCADE statement runs the
   type is already gone (`does not exist`) and the tables it should have
   cascade-dropped (`persons`, `persons2`, `persons3`) are still there,
   un-dropped. Every downstream statement in the file's "implicit casting"
   block that re-creates `persons`/`persons2` then fails with "relation
   already exists" — a single root cause propagating through roughly half
   the remaining diff. Two further symptoms trace to the same root: `CREATE
   TABLE persons5 OF stuff` (a table's own implicit row type) and `CREATE
   TABLE of_tt_enum_type OF tt_enum_type` (an enum type) both report the
   generic "type does not exist" instead of PG's specific "type X is the
   row type of another table" / "type X is not a composite type" —
   PG's checks only run because the type-resolution machinery normally
   reaches that far; whether those two checks are *also* independently
   missing was not isolated from the desync above this loop.
3. **`SELECT * FROM get_all_persons()` doesn't expand the composite return
   type.** A `LANGUAGE SQL` function declared `RETURNS SETOF person_type`
   called via `SELECT * FROM func()` should star-expand into the
   composite's fields (`id`, `name`) as separate output columns; goopg
   instead emits one column named after the function.
4. **Three smaller, independently-verified gaps**, none attempted this
   loop: a text-typed column's `''` default displays without PG's
   `::text` cast decoration in `\d`; specifying `WITH OPTIONS` for the same
   column twice in `CREATE TABLE OF (...)` is accepted instead of raising
   "column ... specified more than once"; and `$1.name` (composite-field
   dot-access on a positional SQL-function parameter) is a parser syntax
   error.

### What this means for scoping

Unlike the pure-catalog-consistency M0134 cases, this file's dominant
remaining gap (bucket 2) is genuine CASCADE/RESTRICT dependency tracking
for `DROP TYPE` — the same class of "no object-dependency graph" gap
already ledgered for other DDL DROP paths in this project, not something
`typed_table.sql` introduces on its own. Bucket 1 (the ALTER TABLE
restrictions) was fully self-contained — five independent guard checks, one
`tbl.OfTypeOID != 0` predicate each, no shared machinery with any other
bucket — and was shipped.

## What landed

Five new guard checks, one per restricted `ALTER TABLE` subcommand, each
gated on `tbl.OfTypeOID != 0` and placed at the exact point in goopg's
(conflated prep+exec) handler that matches where PG's separate prep-pass
check (`ATPrepAddColumn`/`ATPrepDropColumn`/`renameatt_check`/
`ATPrepAlterColumnType`/`ATPrepAddInherit`) runs relative to that handler's
other checks — all in `internal/executor/operators_ddl.go`:

| operation | function | PG citation | error | Pos |
|---|---|---|---|---|
| ADD COLUMN | `execAlterTableAddColumn` | `tablecmds.c:7200-7203` | `cannot add column to typed table` | 0 |
| DROP COLUMN | `execAlterDropColumn` | `tablecmds.c:9260-9263` | `cannot drop column from typed table` | 0 |
| RENAME COLUMN | `execAlterTable` (`AlterTableRenameColumn` case) | `tablecmds.c:3798-3802` | `cannot rename column of typed table` | 0 |
| ALTER COLUMN ... TYPE | `execAlterColumnType` | `tablecmds.c:14395-14400` | `cannot alter column type of typed table` | `act.Pos()` |
| INHERIT | `execAlterTable` (`AlterTableInherit` case) | `tablecmds.c:17237-17241` | `cannot change inheritance of typed table` | 0 |

All five are placed as the FIRST check in their handler (before the
partition-child check, system-column-collision check, column-existence
lookup, or parent-table lookup that each handler otherwise does first) —
matching PG's two-pass prep/exec split, where the prep pass's reloftype
check always runs before anything in the exec pass. `ALTER COLUMN ... TYPE`
is the one exception carrying a nonzero `Pos` (PG's `parser_errposition`
call there, unlike its four siblings).

New test: `internal/executor/alter_table_typed_table_restrictions_test.go`
— `TestAlterTableTypedTableRestrictions`, pinning all five exact
SQLSTATE/message pairs on a typed table plus a regression guard confirming
the same five operations remain unaffected on a plain (never-typed) table.

## Resume points

- **Bucket 2 (`DROP TYPE` dependency tracking)** — own scope, REFACTOR-tier.
  Resume at `internal/executor/operators_ddl.go`'s `DROP TYPE` handler
  (grep `execDropType`/`DropTypeStmt`): add a dependency scan over
  `catalog.InMemory` tables (`OfTypeOID == typeOID`) and functions
  (arg/return type == typeOID) before committing a RESTRICT drop, and
  implement CASCADE to actually walk and drop each dependent (reusing the
  existing DROP TABLE / DROP FUNCTION paths). Re-verify buckets "persons5
  OF stuff" / "of_tt_enum_type OF tt_enum_type" once the desync is fixed —
  they may already be correct once earlier statements stop corrupting
  catalog state.
- **Bucket 3 (composite SRF star-expansion)** — resume wherever FROM-list
  star-expansion resolves a function-call RTE (`internal/optimizer` /
  `internal/parser/analyzer`); needs to look up the function's declared
  composite return type via `pg_proc.prorettype` and expand its
  `pg_attribute` rows as separate output columns instead of treating the
  call as one opaque value.
- **Bucket 4 items** — each independently contained; see the three
  `.ralph/deferral_ledger.md` rows filed 2026-09-01 for exact resume
  points (default-value `\d` cast decoration; duplicate `WITH OPTIONS`
  detection in `CREATE TABLE OF (...)`; `$N.field` parser grammar).
- Re-arm this case (re-run `scripts/pg-regress-runner.sh -v typed_table`)
  once bucket 2 lands — it should move the diff line count the most.

## Gates run

- `scripts/pg-regress-runner.sh -v typed_table` (before/after; 150 → 135
  diff lines, all five typed-table restriction error lines now
  byte-identical to PG).
- `go build ./...` clean.
- `go test ./internal/executor/...` — full package including the new
  `TestAlterTableTypedTableRestrictions` and the pre-existing
  `TestAlterTableOfNotOfRegressMatrix`/`TestAlterTableOfReassignAndNotOf` —
  all PASS.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — full
  units suite PASS.
- `make check-testport-inventory` PASS.
- `make regen-testport` — clean regen (CSV status flip + derived docs).
- `make ralph-state-guard` PASS.
