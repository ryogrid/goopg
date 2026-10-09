# M0146-0039a — CREATE TEMP TABLE … AS creates a temporary relation

Status: done (2026-10-03). Parent: M0146-0039 (banner item 2a, S2).

## The defect

`CREATE TEMP TABLE tt AS SELECT 1 x` created a permanent relation:
`pg_class.relpersistence` was `p` in the user's schema, and the table
survived `goopg stop` + start, where every session could see it. A later
`CREATE TEMP TABLE tt AS …` then failed. Plain `CREATE TEMP TABLE tt (x int)`
was already correct, because M0146-0039 fixed the CREATE TABLE path only.
`CREATE UNLOGGED TABLE … AS` had the same gap and came out `p`.

## The PG behaviour

`create_ctas_internal` (createas.c) builds a `CreateStmt` from the INTO
clause, including `into->rel->relpersistence`, and hands it to
`DefineRelation`. So a CTAS relation is temporary or unlogged exactly as
the CREATE TABLE form would be. A temp relation lives in the backend's
`pg_temp_N` namespace and goes away with that backend.

## What landed

`execCreateTableAs` (internal/executor/operators_ddl.go) now stamps the
statement's persistence on the new table, the same way `execCreateTable`
does and before the catalog sync persists the row:

- `Unlogged`;
- `Temp`, `TempOwner = sessionTempOwner(ctx)`, and the session's pg_temp
  namespace (`EnsureTempNamespace`).

The temp-schema guards and the shadowing of a permanent table of the same
name already ran in `execCreateTable` before it dispatches to CTAS, and the
`pg_temp.`-qualified form is already promoted to TEMP there.

Two more changes:

- A temp CTAS no longer records the search_path's writable schema as its
  `Schema`, matching `execCreateTable`'s pre-resolve, which skips temp
  tables.
- The ON COMMIT registration now reads `tbl.Temp` rather than working
  around its absence.

## Verification

- Live probe on a throwaway cluster against PG 18.3, with identical output:
  - `CREATE TEMP TABLE … AS`, `CREATE TABLE pg_temp.x AS` and
    `… ON COMMIT DROP AS` give `relpersistence t` in a `pg_temp` namespace;
  - `CREATE UNLOGGED TABLE … AS` gives `u`;
  - a second session does not see the temp tables.
- After stop/start the temp CTAS table is gone and `CREATE TEMP TABLE rr AS
  …` succeeds again.
- `TestCTASHonoursTempAndUnlogged` pins the four persistence forms.
- Regress A/B against HEAD: temp, select_into, create_table,
  create_table_like, matview and create_view are byte-identical.
- Gates: units, spotcheck, SF0.25 sweep, TPC-H arm.

## Residuals (ledgered)

- `SELECT … INTO TEMP|TEMPORARY|UNLOGGED t` is a syntax error in goopg,
  which parses only `INTO name`. Filed as M0146-0039b.
- `relpersistence u`, whether from CTAS or plain CREATE, reloads as `p`
  after a restart. This is the known codec/reload gap recorded with
  M0122-0007.
