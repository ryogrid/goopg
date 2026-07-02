# 0119-0004 — CREATE ACCESS METHOD round-trip in pg_dump (DU-002 slice 426)

Status: implemented (2026-07-02)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/backend/commands/amcmds.c` (`CreateAccessMethod`,
`lookup_am_handler_func`); `postgres/src/bin/pg_dump/pg_dump.c`
(`getAccessMethods`, `dumpAccessMethod`)

## Problem

`CREATE ACCESS METHOD name TYPE {INDEX|TABLE} HANDLER handler_name` was a bare
parse error — no `parseCreateAccessMethod*` path existed at all — so any dump
containing a user-defined access method could not even be replayed against
goopg, let alone round-trip through goopg's own pg_dump. `pg_am.VirtualRows`
also only ever emitted the 7 built-in rows (heap/btree/hash/gist/gin/spgist/
brin), so `pg_dump`'s `getAccessMethods()` (which selects every `pg_am` row
with `oid >= FirstNormalObjectId`) always read 0 dumpable rows.

goopg has no pluggable table/index storage engine and never invokes a
user-defined AM's handler function — this slice is **dump-fidelity only**,
the same scope as the existing CREATE OPERATOR/OPERATOR CLASS/CONVERSION
compat-registration slices.

## Fix

1. **Parser** (`internal/parser/ddl.go`, `parseCreateAccessMethodTail`): new
   `CreateAccessMethodStmt{Name, AMType, HandlerName}` AST node
   (`internal/parser/ast.go`). Grammar mirrors `gram.y`'s `CreateAmStmt`:
   `name TYPE_P {INDEX | TABLE} HANDLER handler_name`, where `handler_name` is
   a plain (optionally schema-qualified) function name with no parenthesized
   arg list. `DROP ACCESS METHOD` already parsed generically through the
   ident-DROP-target list (`M0097-0071`); only `CREATE` was missing.
2. **Catalog** (`internal/catalog/catalog.go`): new `AccessMethod{Name, OID,
   AMType, HandlerOID}` + `RegisterAccessMethod`/`DropAccessMethod`/
   `ListAccessMethods`, keyed by `amname` (mirrors the `ForeignDataWrapper`/
   `EventTrigger` compat-registry shape). `pg_am.VirtualRows` appends one row
   per registered user AM after the 7 built-ins; `pg_dump`'s own
   `selectDumpableAccessMethod` (oid > `g_last_builtin_oid`) does the
   built-in filtering client-side, so no oid-range filtering is needed here.
3. **Executor** (`internal/executor/operators_ddl.go`,
   `execCreateAccessMethod`): mirrors `CreateAccessMethod`'s validation order
   — superuser check (`42501`), then handler resolution, then the
   duplicate-name check (`42710`, `RegisterAccessMethod` rejects both a
   built-in-name collision via `catalog.AccessMethodOIDByName` and a
   duplicate user AM). `resolveAccessMethodHandlerFunc` mirrors
   `lookup_am_handler_func`: the handler must resolve to a routine with
   **exactly one argument of type `internal`**, returning the AM-type-matching
   pseudo-type (`index_am_handler` for `TYPE INDEX`, `table_am_handler` for
   `TYPE TABLE`) — a missing/mismatched-arity match is `42883`
   undefined_function, a resolved routine with the wrong return type is
   `42809` wrong_object_type. `execDropCompat`'s existing `"access method"`
   case now also calls `DropAccessMethod` so a drop stops the entry from
   dumping.
4. **New pseudo-type OIDs** (`internal/initdb/pg_proc_view.go`,
   `typeNameToOIDStr`): `index_am_handler` → 325, `table_am_handler` → 269 —
   needed so `CREATE FUNCTION ... RETURNS index_am_handler` (the handler stub
   a test/user must create first, since goopg has no built-in AM handler
   functions to point at) resolves a `prorettype` at all.

## Tests

- `parser`: `TestParseCreateAccessMethod` (both `TYPE INDEX`/`TYPE TABLE`
  forms, schema-qualified handler name), `TestParseCreateAccessMethodErrors`
  (missing `TYPE`/bad am-type/missing `HANDLER`), `TestParseDropAccessMethod`
  (routes through the shared `DropCompatStmt`).
- `executor`: `TestCreateAccessMethodRegistersRow`,
  `TestCreateAccessMethodTableType`,
  `TestCreateAccessMethodUnknownFunctionErrors` (`42883`),
  `TestCreateAccessMethodWrongReturnTypeErrors` (`42809`),
  `TestCreateAccessMethodDuplicateNameErrors` (`42710`, incl. built-in name
  collision), `TestDropAccessMethodRemovesRow`.
- `testport`: slice 426 in `TestPort_PgDumpConnectionSetup` — a real
  `LANGUAGE c` handler stub (never invoked, mirroring the CREATE CONVERSION
  FROM-function precedent) plus `CREATE ACCESS METHOD goopg_am TYPE INDEX
  HANDLER goopg_am_handler` verified byte-identical against real pg_dump
  18.3's `dumpAccessMethod` emit (`CREATE ACCESS METHOD goopg_am TYPE INDEX
  HANDLER public.goopg_am_handler;`), plus a regression guard that the 7
  built-in AMs stay filtered out of the dump.

Gates: `go build`/`go vet` clean; `internal/parser` + `internal/catalog` +
`internal/executor` + `internal/planner` + `internal/initdb` suites PASS;
`TestPort_PgDumpConnectionSetup` PASS (7.5s, byte-identical vs live PG 18.3);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); full pre-commit gate incl.
pgbench TPC-B/simple-update/select-only smoke (0 failed transactions across
5181+6815+398183 transactions) PASS.

## Blast radius

Zero on query execution / DML — `CREATE ACCESS METHOD` only registers a
dump-visible catalog row; no code path ever looks up or invokes a
user-defined AM's handler. `pg_am.VirtualRows` is purely additive (7 built-in
rows unchanged; user rows only appear after an explicit `CREATE ACCESS
METHOD`). No WAL persistence yet — a `CREATE ACCESS METHOD` does not survive
a restart, matching the current scope of several sibling DU-002 compat
registries (e.g. event triggers) before their own WAL-persistence slices
landed.

## Deferred

No WAL/restart persistence for `CREATE`/`DROP ACCESS METHOD` — the registry
is process-local, like `ForeignDataWrapper`/`EventTrigger` were before their
own persistence slices. See `.ralph/deferral_ledger.md` (2026-07-02,
M0119-0004) for the resume point.
