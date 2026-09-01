# M0119-0006bo — `verify_heapam()` resolved OIDs against the wrong database

Status: accepted
Milestone: M0119-0006 (pg_amcheck server tier)

## Problem

Following up on `[[0119-0006bn]]`'s "newly discovered, unrelated" deferral —
`CREATE SCHEMA s1; CREATE TABLE s1.t (...)` followed by `pg_amcheck
--schema=s1` reporting "no relations to check in schemas matching \"s1\"" —
this slice re-investigated the repro from scratch on a **clean** server (the
previous loop's observation turned out to have been taken against a stale,
already-corrupted orphaned server, `tmp/bugfix-review`, unrelated to this
tree). On a fresh server:

- `CREATE SCHEMA s1; CREATE TABLE s1.t (a int);` in the `postgres` database
  registers `pg_namespace` and `pg_class.relnamespace` correctly — confirmed
  across a fresh connection and a full server restart.
- The same sequence run inside a **non-default, user-created database**
  (`CREATE DATABASE amcheckdb; ... \c amcheckdb`) also registers
  `pg_namespace`/`pg_class` correctly.
- But `SELECT * FROM verify_heapam(<oid>)` — even for a plain, non-empty,
  `public`-schema table, no schema involved at all — fails with `ERROR:
  42P01: could not open relation: relation does not exist` **whenever the
  query runs against any database other than the cluster's default
  catalog-write database**. The same call against the identical table's oid
  in the `postgres` database succeeds. This is what `pg_amcheck` was actually
  hitting: not a `CREATE SCHEMA`/`pg_namespace` bug at all — that subsystem is
  fine — but `verify_heapam()`'s own relation resolution being scoped to the
  wrong database.

The `.ralph/deferral_ledger.md` row filed 2026-09-02 for "`CREATE SCHEMA`
schema qualification silently dropped" is corrected by this slice: retitled
and resolved with the diagnosis above (see ledger).

## Root cause

`catalog.InMemory.LookupTableByOID(oid uint32, dbOid ...uint32)` /
`LookupTable(name parser.ObjectName, dbOid ...uint32)` are per-database-
namespace lookups — table OIDs are cluster-wide unique (allocated from one
`nextOID` counter) but a table's `catalog.Table` value lives inside
`c.ns(dbOid).tables`, one slice per registered database (the per-DB catalog
work referenced in `CLAUDE.md`). Both take `dbOid` as a **variadic** trailing
argument that defaults to `DefaultDBOid` via `resolveDBOid` when the caller
passes none.

`executor.verifyHeapamResolveTable` (`internal/executor/
operators_verify_heapam.go`), the regclass-argument resolver shared by
`verify_heapam()`, `pg_get_sequence_data()`, and `pg_sequence_parameters()`,
called both lookups with **no `dbOid` argument at all** — every call silently
resolved against `DefaultDBOid` regardless of which database the connection
was actually querying. A table living in any other database was therefore
always "not found", producing the SRF's generic 42P01 "relation does not
exist" error path (`operators_verify_heapam.go`'s `Open`, the `!ok` branch)
— indistinguishable, from the client's perspective, from a genuinely bad oid.

This is the same class of bug the `[[0119-0006bn]]` doc's sibling-paths note
flags: `executor.regclassOIDToName` (`internal/executor/expr.go`) already
threads `connDBOid` through the identical `LookupTableByOID` call for
`::regclass` casts — the two call sites had drifted out of sync.

## Fix

Thread `ctx.CurrentDatabaseOid` (the connection's real, resolved
`pg_database.oid`, the same field `regclassOIDToName` already uses) through
`verifyHeapamResolveTable`:

- `verifyHeapamResolveTable(d Datum, im *catalog.InMemory, dbOid uint32)` —
  added the `dbOid` parameter, passed to both the `KindInt` (`LookupTableByOID`)
  and `KindString` (`LookupTable`) branches.
- All three call sites now pass `ctx.CurrentDatabaseOid`:
  `operators_verify_heapam.go` (`verify_heapam()`),
  `operators_pg_get_sequence_data.go` (`pg_get_sequence_data()`),
  `operators_pg_sequence_parameters.go` (`pg_sequence_parameters()`).
- `verifyHeapamResolveTable` treats a zero `dbOid` as "unresolved, use the
  default": `ctx.CurrentDatabaseOid` is documented (`context.go`) to be zero
  in embedded/test contexts, and zero is never a real dbOid
  (`catalog.DefaultDBOid == 1`) — passing it through unchanged would have
  regressed every unit test that builds a bare `Context{}` without setting
  `CurrentDatabaseOid` (caught by the pre-commit units gate on first attempt:
  `TestVerifyHeapam_*`, `TestPgGetSequenceDataPopulated`,
  `TestPgSequenceParametersBasic` all went from pass to "relation does not
  exist" until this fallback was added).

The fix is a straight database-scoping correction; no change to the
not-found / virtual-relation / TOAST-relid branches below it.

## Verification

- `go build ./...` clean.
- Manual end-to-end against a fresh capped server (`CREATE DATABASE
  amcheckdb`): `SELECT * FROM verify_heapam(<oid>)` for a plain `public`
  table now returns the clean (0-row) result instead of erroring; same for a
  table in a `CREATE SCHEMA s1`-owned schema.
- Real `pg_amcheck --schema=s1 amcheckdb` (after `CREATE EXTENSION amcheck`):
  exit 1 ("no relations to check in schemas matching \"s1\"") → exit 0,
  matching real PG's clean-table result.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full suite.

## Not in scope / deferred

- Real `pg_amcheck amcheckdb` (whole-database, no `--schema` filter) still
  fails: it additionally walks `pg_catalog.pg_class` itself, and
  `verify_heapam()` on `pg_class`'s own oid (1259) fails the same
  `LookupTableByOID` resolution — but not because of this bug. System
  catalogs (`pg_class`, `pg_attribute`, etc.) are, per `[[goopg_pg_class_
  virtual_pg_attribute_heap]]`, built by dedicated virtual-catalog / heap-seed
  logic that appears to register itself only under `DefaultDBOid`'s
  namespace, not replicated into every newly `CREATE DATABASE`d namespace's
  `c.ns(dbOid).tables`. That is a materially different, deeper problem (system
  catalog registration scope, not regclass-argument threading) and is
  ledgered separately rather than folded into this slice.
