# M0122-0015e — foreign-data wrappers are per-database

Status: landed 2026-10-12.
Parent: M0122-0015. Found by the `005_pg_dump_filterfile.pl` port.

## Problem

PostgreSQL's pg\_foreign\_data\_wrapper is per-database. goopg's FDW registry
(`InMemory.fdws`) was keyed by bare name, which caused three problems:

- **Visible everywhere.** An FDW created in postgres was listed in every
  database's pg\_foreign\_data\_wrapper, and therefore emitted by every
  database's pg\_dump (`CREATE FOREIGN DATA WRAPPER dummy;`).
- **Collisions.** A same-named FDW in a second database collided with it.
- **Cross-database DROP.** A DROP in one database removed the FDW for all.

Foreign servers had already been scoped by database (design 0122-0018
follow-up 36). FDWs were not.

## Design

- **Registry.** `ForeignDataWrapper` gains `DBOid`, and the registry is keyed
  by `fdwKey(dbOid, name)` (foreignServerKey's `"<dbOid>:<name>"`). Register,
  Drop, Lookup, OID and List take the trailing variadic `dbOid` (namespace
  oid, DefaultDBOid when omitted). Recovery registers with the persisted
  `DBOid`, or DefaultDBOid when none is stored.
- **pg_foreign_data_wrapper rows.**
  `PGForeignDataWrapperRowsForDBOid(dbOid)` builds them, wired through
  M0122-0015d's generic `Table.VirtualRowsForDB` hook. No executor arm or
  context field is needed.
- **srvfdw.** pg\_foreign\_server's `srvfdw` resolves the wrapper in the
  server's own database (`ForeignServer.DBOid`), as do the server heap row,
  the planner's FDW handler lookup and the CREATE SERVER heap sync.
- **CREATE SERVER validates the wrapper.** PG's `CreateForeignServer`
  resolves it with `GetForeignDataWrapperByName`, which raises 42704 when it
  is missing. goopg registered a server against any name. The lookup is in
  the connection's database.
  - Executor tests had relied on that laxity (`CREATE SERVER … FOREIGN DATA
    WRAPPER goopg_fdw` with no wrapper). They now create the wrapper first,
    as a superuser, via `mustCreateFDW`.

## Verification

- Probe on PG 18.3 vs goopg, byte-identical:
  - an FDW created in postgres is invisible in a new database;
  - CREATE SERVER there on it is 42704;
  - a same-named FDW in the second database works, with `srvfdw` naming the
    database's own wrapper;
  - the second database's dump carries only its own wrapper;
  - DROP in postgres leaves the other database's wrapper.
- Test: `TestForeignDataWrappersArePerDatabase`.
- The executor FDW / server / user-mapping tests pass.
- The pg\_dump testport family passes.
- Regress A/B:
  - `foreign_data` now matches PG's `CREATE SERVER s1 … -- ERROR`, but its
    diff grows by 8 lines. goopg lacks the built-in
    `postgresql_fdw_validator`, so the `postgresql` wrapper is never created,
    and the servers the script builds on it now fail as PG would against a
    missing wrapper (filed as M0122-0015g).
  - `foreign_key` and `privileges` show only row-order and OID noise.
- Gates: units, tpch-spotcheck, acceptance arm, sf025 PASS=99, and the
  TPC-DS fire set (empty, PASS).

## Residuals (ledgered)

- **Heaps are single-database.** The foreign-catalog heaps are written to
  the default database's heap (`sysForeignRel`), and FDWs, servers and user
  mappings reload there. After a restart, a non-default database's wrapper
  and servers come back in the default database.
- **Postmaster GRANT / REVOKE path.** `GRANT … ON FOREIGN DATA WRAPPER`
  through `grant_ddl` resolves the wrapper in the default database only.
- **No USAGE check.** PG requires USAGE on the wrapper for a non-superuser
  CREATE SERVER; goopg does not check it.
