# M0146-0156 — routines in a CREATE DATABASE'd database are callable and durable

Status: landed 2026-10-12.
Parent: none (S2, placed into banner item 2a by the owner on 2026-10-12).
Found by the M0122-0015d probe.

## Problem

In a database made by `CREATE DATABASE`, `CREATE FUNCTION foo1(int) ...`
succeeded and that database's `pg_proc` listed the function. But:

- **Calls failed.** `SELECT foo1(5)` raised `function foo1 does not exist`,
  and `CALL` of a procedure raised `procedure ... does not exist`.
- **Restarts lost them.** After a clean restart the function was gone from
  every database's `pg_proc`.
- **Aggregates (measured while fixing).** User aggregates were callable but
  equally lost on restart.

PostgreSQL 18.3 behaves differently: `pg_proc` is per-database, so a
routine is callable in its own database, invisible in the others, and
durable.

## Cause

The registry was already per-database. `catalog.Routines` keys every routine
by `DBOid`, CREATE FUNCTION stamps the connection's database, and since
M0122-0015d the `pg_proc` view filters by it. The defects were in the callers.

1. **Resolution addressed the default database.** The lookup methods default
   to `DefaultDBOid` when the caller passes no database, and none of them
   did. That covers about 40 call sites: the planner's function resolution,
   the analyzer, executor routine dispatch, CALL, triggers, regproc input,
   and the ALTER / DROP / COMMENT paths.
2. **The reload read one heap.** `syncRoutineToCatalogHeap` writes a
   routine's row to its own database's `pg_proc` heap (`pgProcRel`), but
   `reloadUserRoutinesFromHeap` scanned only the default database's heap.
   It also could not have stamped a database, because `CreateDuringRecovery`
   forced `DefaultDBOid`. `reloadUserAggregatesFromHeap`, the heap's other
   consumer, had the same single-heap scan.

## Design

- **A database-bound handle over one store.** `Routines` is split into a
  shared `routineStore` (maps, OID counter, heap TIDs) and a handle carrying
  a database.
  - `rs.InDB(db)` returns a handle over the same store, with the database
    normalised by `NamespaceDBOid`.
  - Every lookup resolves its database through `dbFor`: an explicit `dbOid`
    first, then the handle's database, then `DefaultDBOid`.
  - `Create` stamps the handle's database when the routine has none.
  - OID-keyed calls and `List` are unchanged, since routine OIDs are
    cluster-unique.
- **Two choke points hand out bound handles.**
  - `SearchPathCatalog.Routines()` binds the connection's database. This
    covers the planner, the analyzer and everything else holding the planner
    catalog.
  - The executor's `routineRegistry(ctx)` binds
    `ctx.CurrentDatabaseOid`. All 35 executor uses of `ctx.Catalog.Routines()`
    now go through it.
- **Per-database reload.**
  - `reloadUserRoutinesFromHeap` and `reloadUserAggregatesFromHeap` scan the
    default database's `pg_proc` heap and then every other database's. The
    skip set is the same as `ReloadUserDatabaseSchemasFromHeap`.
  - Routines are stamped with `NamespaceDBOid(heapDBOid)`.
  - `CreateDuringRecovery` keeps a stamped database and defaults only an
    unstamped one.
- **DROP DATABASE** calls `Routines.DropAllInDB`, so the dropped database's
  routines leave the store. Otherwise they would stay reachable through
  `LookupByOID` and `List`.

## Verification

- **Probe against PG 18.3.** The probe covers:
  - the same-named function in two databases;
  - an overload, CREATE OR REPLACE, RENAME and DROP in one database, with the
    other database untouched;
  - a trigger function, a set-returning function, a column default and a
    view over a function;
  - `regproc` input;
  - DROP and re-CREATE DATABASE.

  It matches PG, except for the pre-existing message differences listed
  under Residuals.
- **Restarts.** Calls and `pg_proc` survive two restarts, and an aggregate
  survives too.
- **Tests.**
  - `TestRoutinesInDBHandle` (catalog): handle scoping, postgres-OID
    normalisation, explicit `dbOid`, OID lookup, the stamp
    `CreateDuringRecovery` keeps, and `DropAllInDB`.
  - `TestPort_RoutinesArePerDatabase`: two databases, a function, a plpgsql
    function, a procedure and an aggregate, isolation from `postgres`, DROP
    DATABASE, and two restarts. It fails on HEAD in every arm.
- **Regress A/B** over 19 function, aggregate and privilege cases: identical,
  except `plpgsql` and `polymorphism`, which flip in both directions between
  runs of the same binary (known run-context noise).
- **Gates.**
  - Units; catalog / executor / initdb / optimizer / postmaster packages.
  - The pg\_dump and per-database testport families.
  - tpch-spotcheck, the acceptance arm and sf025 PASS=99, each run twice:
    before and after the aggregate reload was added.

## Residuals

- **Filed as S2 M0146-0160: per-database reloads of every other catalog
  kind.** About 25 reloads still scan only the default database's heap:
  sequences, domains, enums, casts, operators, collations, conversions,
  opfamilies / opclasses, transforms, event triggers, publications, FDW
  objects, text-search objects and access methods.
  - Measured after one restart in a CREATE DATABASE'd database: the sequence
    is gone (`relation "sq" does not exist`), a domain's CHECK no longer
    applies (`(-1)::posint` succeeds), and a serial column's default is gone
    (`INSERT` fails with a not-null violation).
- **Ledgered:**
  - The user-aggregate registry is not per-database: an aggregate created in
    one database is visible in all of them.
  - GRANT / REVOKE ON FUNCTION (postmaster `lookupFunctionOID`) still
    resolves in the default database, because no connection database is
    plumbed into that path. The same applies to the other grant paths
    0119-0006 ledgered.
  - Routines created in `template1` are not copied into new databases.
    PostgreSQL copies the template's `pg_proc`.
  - Pre-existing message differences, the same in the default database:
    - `function same does not exist` with no argument list and no HINT;
    - a `regproc` caret offset;
    - `pronamespace = 'public'::regnamespace` matching no rows.
