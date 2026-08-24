# M0134-0116 — `create_type.sql`: sizing + bare-shell duplicate-CREATE fix

**Status:** PARKED (`failed`). One contained fix landed, taking the case from
417 diff lines / 17 `^-ERROR` / 8 `^+ERROR` to 405 diff lines / 15 `^-ERROR` /
8 `^+ERROR` (0% parity throughout — the file needs a REFACTOR-tier subsystem
that does not exist yet).

## Oracle case

`postgres/src/test/regress/sql/create_type.sql` exercises PG's full
`CREATE TYPE` surface: "old style" base types with `LANGUAGE C` I/O functions
(`widget`, `city_budget`, and the `pt_in_widget` cross-type operator, all
backed by `regress.so`), shell-type create/destroy/duplicate-detection,
"new style" base types built from `LANGUAGE internal` I/O functions reusing
existing types' support routines (`int42`, `text_w_default`, `base_type`,
`myvarchar`), standalone composite types (`default_test_row`), `COMMENT ON
TYPE`/`COMMENT ON COLUMN`, dependency-tracking `DROP` failures/cascades, and
`ALTER TYPE ... SET (...)` option updates (storage, send/receive/typmod/
analyze/subscript) on a shell-then-filled-in base type.

Sized live via `scripts/pg-regress-runner.sh --verbose create_type` against
the PG 18.3 oracle: 417-line diff before any fix (17 `^-ERROR`, 8 `^+ERROR`),
405 lines after the fix below (15 `^-ERROR`, 8 `^+ERROR` — unchanged, no new
false positives).

## Root cause

Two independent, non-overlapping gaps dominate this file:

1. **No `LANGUAGE C` dynamic-extension loader** (the standing M0134-0106 gap
   named in `.ralph/working_set.md`'s recommendations list). `widget`,
   `city_budget`, and `pt_in_widget` need `regress.so`'s actual C function
   bodies (`widget_in`/`widget_out` parse/format a 3-tuple, `int44in`/
   `int44out` parse/format a variable-length int4 array, `pt_in_widget` does
   real point-in-circle geometry) — goopg has no mechanism to load or call
   into a `.so` at all, so every later use of these types (typmod-bearing
   `CREATE TEMP TABLE mytab (foo widget(42,13))`, widget literal INSERT/
   SELECT, `pg_input_is_valid` round-trips, the `<%` operator, the `city`
   table's `city_budget` column) either silently no-ops or diverges.
2. **`CREATE TYPE`'s "base type" and bare-shell forms are parser/executor
   stubs with almost no real semantics.** `internal/parser/ddl.go`'s
   `parseCreateType` (the arm entered once `AS ENUM`/`AS RANGE`/`AS (fields)`
   have all been ruled out) just skips every token up to the terminating
   `;`/EOF — it never parses the option list (`input =`, `output =`,
   `internallength =`, `default =`, `alignment =`, …) at all. The executor's
   `execCreateType` (`internal/executor/operators_ddl.go` ~24296-24420)
   correspondingly just registers a bare name via
   `cat.RegisterCompositeType` with **no duplicate check, no I/O-function
   validation, no default-value application, no dependency tracking, and no
   distinction between the bare shell spelling (`CREATE TYPE name;`) and the
   full base-type spelling (`CREATE TYPE name (input = ..., ...)`)** — both
   forms parse identically since the option list is discarded either way.
   This is why the bulk of the file's non-C-loader lines diverge too: PG's
   `default = 42/'zippo'` never populates `default_test`'s `DEFAULT VALUES`
   row, `COMMENT ON TYPE`/`COMMENT ON COLUMN` on the standalone composite
   type fail because nothing wires the type to its implicit relation for
   `\d`-adjacent lookups, `DROP TYPE ... ; -- error` / `DROP FUNCTION ...;
   -- error` don't detect the I/O-function-to-type dependency PG's
   `recordDependencyOnExpr`/pg_depend machinery would, and `ALTER TYPE
   myvarchar SET (storage = plain)` — a real *disallowed* transition PG
   rejects at the semantic level — silently no-ops in goopg instead of
   erroring.

Both gaps are REFACTOR-tier (a real extension-loading subsystem; a real
base-type-definition executor with I/O-function signature validation,
pg_depend wiring, and ALTER TYPE option semantics) and out of scope for a
single loop. **But** one narrow slice of gap 2 was independently
fixable without touching either subsystem: the file's very first
shell-type-duplication check,

```sql
CREATE TYPE shell;
CREATE TYPE shell;   -- fail, type already present
```

and the parallel already-fully-defined case a few lines later,

```sql
CREATE TYPE text_w_default;  -- should fail (already fully defined earlier)
```

both expect `ERROR: type "…" already exists` (`42710`,
`ERRCODE_DUPLICATE_OBJECT`) — PG's `DefineType` (`postgres/src/backend/
commands/typecmds.c` ~236-266) always rejects a **bare, parameterless**
`CREATE TYPE name` if a type of that name already exists in *any* form
(undefined shell or fully defined). goopg's `RegisterCompositeType`
(`internal/catalog/catalog.go` ~22305) was — and, for the base-type-with-
options spelling, still is — unconditionally idempotent, so both bare
re-declarations silently succeeded as no-ops.

## Landed this loop

**Parser** (`internal/parser/ddl.go`'s `parseCreateType`,
`internal/parser/ast.go`'s `CreateTypeStmt`): the stub-consuming arm (taken
once `AS` has been ruled out) now records a new `HasOptions bool` field —
`true` iff a `(` immediately follows the type name — before sweeping the
remaining tokens to the terminating `;`/EOF exactly as before. This is the
one piece of information PG's grammar distinguishes (`parameters == NIL` vs
not, in `DefineType`) that goopg's stub previously discarded entirely.

**Executor** (`internal/executor/operators_ddl.go`'s `execCreateType`, the
`else` branch of the composite/base-type case): when `!s.HasOptions` (the
bare shell spelling), looks up the type by name first — if it already exists
in any form, raises `42710 type "%s" already exists` instead of proceeding
to `RegisterCompositeType`. The base-type-with-options spelling
(`HasOptions == true`) is deliberately left alone — see "Regression avoided"
below.

Verified live: both `^-ERROR` lines for `shell`/`text_w_default` are gone;
`go test ./internal/parser/... ./internal/executor/... ./internal/postmaster/...`
and `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` both pass.

## Regression avoided: no mirror-image "requires a pre-existing shell" check

PG's `DefineType` also rejects the base-type-with-options spelling
(`CREATE TYPE name (input = ..., ...)`) with a *different* error —
`42710 type "%s" does not exist` plus a shell-first hint — when **no**
shell of that name exists yet (`typecmds.c` ~270-278: "there is no other way
the I/O functions could have been created"). A first version of this fix
added that mirror-image check too, which *did* eliminate the file's
`bogus_type does not exist` `^-ERROR` line — but it also introduced two new
`^+ERROR` false positives: `widget` and `city_budget` are created via
`CREATE TYPE widget (internallength = ..., input = widget_in, ...)` with
**no preceding bare `CREATE TYPE widget;`** in the file at all. In real PG
those names get an implicit shell from `CREATE FUNCTION widget_in(cstring)
RETURNS widget ...` (a return/argument type that doesn't exist yet triggers
`TypeShellMake` as a parser-time side effect, per `parse_type.c`'s lookup
failure path); goopg has no such auto-shell mechanism at all. Enforcing the
"pre-existing shell required" half of the check therefore broke `widget`/
`city_budget`/`base_type`-style types outright, which cascaded into many
more downstream diff lines than the one `bogus_type` line it fixed — a net
regression (417→422 diff lines instead of 417→405). That half of the check
was reverted; only the always-safe "bare CREATE TYPE never re-creates an
existing name" half remains. See the code comment at
`internal/executor/operators_ddl.go`'s `execCreateType` else-branch for the
in-line version of this note.

## Remaining gap (why this case is still PARKED)

Everything else in the 405-line residual diff falls into the two REFACTOR-
tier buckets above:

- **No `LANGUAGE C` loader** (M0134-0106): blocks `widget`/`city_budget`/
  `pt_in_widget` and everything downstream of them (typmod-bearing user
  types, the `<%` operator, `pg_input_is_valid` on a C-backed type, the
  `city` table).
- **No real base-type-definition executor**: `bogus_type`'s I/O-function-
  signature validation (`type input function must be specified` /
  `type input function array_in must return type bogus_type`), the shell-
  first-requirement error for `bogus_type` after its `DROP TYPE`, type-level
  `default =` application to `DEFAULT VALUES`, `COMMENT ON
  TYPE`/`COMMENT ON COLUMN` on `default_test_row`, pg_depend-based `DROP
  FUNCTION`/`DROP TYPE` dependency-cycle detection for `base_type` and
  `myvarchar`, and `ALTER TYPE ... SET (...)`'s semantic option-transition
  checks (rejecting `storage = plain`, applying `send`/`receive`/
  `typmod_in`/`typmod_out`/`analyze`/`subscript`, and propagating those
  changes to the array/domain-over-the-type variants queried via `pg_type`).

## Resume point

Re-run `scripts/pg-regress-runner.sh --verbose create_type` after either
subsystem lands. A real base-type-definition executor would want to:
replace the parser's token-skip with genuine option-list parsing (key/value
pairs, mirroring the `AS RANGE (...)` arm just above it in `ddl.go`); track
shell vs. defined state per type (a `Defined bool` on `catalog.CompositeType`
mirroring PG's `typisdefined`) so the reverted "requires pre-existing shell"
check in this design doc's "Regression avoided" section can be reintroduced
*correctly* — gated on `Defined`, not bare existence — once goopg also grows
the `CREATE FUNCTION`-triggered auto-shell side effect that makes `widget`/
`city_budget` well-formed without it; and wire I/O-function resolution
through `catalog.ResolveFunction` to validate signatures and record
pg_depend rows. The `HasOptions` field added this loop
(`internal/parser/ast.go`'s `CreateTypeStmt`) is exactly the signal that
work will need to distinguish the two `CREATE TYPE` spellings.
