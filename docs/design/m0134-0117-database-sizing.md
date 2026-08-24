# M0134-0117 — `database.sql`: sizing + `ALTER DATABASE ... RENAME TO` fix

**Status:** PARKED (`failed`). One contained fix landed, taking the case from
24 diff lines / 0 `^-ERROR` / 4 `^+ERROR` to 23 diff lines / 0 `^-ERROR` / 3
`^+ERROR` (0% parity throughout — the residual diff needs two separate,
smaller-but-still-non-trivial missing statements/semantics, neither of which
is safely landable in this loop without its own investigation).

## Oracle case

`postgres/src/test/regress/sql/database.sql` is short (25 lines) and
exercises a narrow slice of `CREATE`/`ALTER`/`DROP DATABASE`: creating a UTF8
database from `template0`, renaming it, moving it to/from a tablespace,
setting its connection limit, a `pg_database.datacl` TOAST-table stress probe
(a huge `array_fill`'d ACL written directly via `UPDATE pg_database`, inside
a transaction that's then rolled back — PG only keeps this around to poke
`PgDatabaseToastTable`), an owner handoff (`ALTER DATABASE ... OWNER TO`
followed by `REASSIGN OWNED BY ... TO ...`), and cleanup (`DROP DATABASE`,
`DROP ROLE`).

Sized live via `scripts/pg-regress-runner.sh --verbose database` against the
PG 18.3 oracle: 24-line diff before any fix (0 `^-ERROR`, 4 `^+ERROR`),
23 lines after the fix below (0 `^-ERROR`, 3 `^+ERROR` — one fewer, no new
false positives).

## Root cause

Diff lines 1-6 of the file (`CREATE DATABASE ... TEMPLATE template0`,
`ALTER DATABASE ... RENAME TO`, `... SET TABLESPACE`, `... RESET TABLESPACE`,
`... CONNECTION_LIMIT 123`) produced **no diff at all** — but that was
misleading. Reading `internal/postmaster/database_ddl.go`'s package doc
comment (`alterDatabaseConfigOp`) revealed that only the `SET`/`RESET
<config>` form of `ALTER DATABASE` is recognised at the wire-protocol
bypass layer (`parseAlterDatabaseConfig`); every other form — including
`RENAME TO`, `SET TABLESPACE`, `RESET TABLESPACE`, and bare
`CONNECTION_LIMIT <n>` (as opposed to the SET-config spelling) — was
explicitly documented as falling through to `compatNoopCommandTag`, a blind
no-op that reports success without touching the catalog at all
(`internal/parser/ddl.go`'s "RENAME TO / OWNER TO / SET SCHEMA / any
unrecognized tail — noop" pattern repeated across `ddl.go` for a dozen other
`ALTER` object types is the same shape).

Because `ALTER DATABASE regression_tbd RENAME TO regression_utf8` was a
no-op, the catalog's database registry still held the name
`regression_tbd`, not `regression_utf8`, for the rest of the script. The
following `SET TABLESPACE` / `RESET TABLESPACE` / `CONNECTION_LIMIT`
statements, all still no-ops themselves, also produced no error regardless
of which name they targeted (they never check existence). The chain only
became externally visible at the file's real catalog-backed statement,
`DROP DATABASE regression_utf8`, which correctly reported "does not exist"
— because, from the catalog's point of view, it never had.

## Landed this loop

**Catalog** (`internal/catalog/catalog.go`): added `RenameDatabase(oldName,
newName string) bool`, mirroring the existing `RenameRole` (used by `ALTER
ROLE ... RENAME TO`) — re-keys the `databases`/`databaseConnLimit`/
`databaseEncoding`/`databaseOwner`/`databaseOid` maps from `oldName` to
`newName`, preserving the oid. Per-oid state (`databaseConfig`, the
`pg_db_role_setting` store) needs no change since the oid is unchanged.

**Postmaster** (`internal/postmaster/database_ddl.go`): added
`databaseRenameFromAlter` (mirrors `role_ddl.go`'s `roleRenameFromAlter`) to
recognise the `ALTER DATABASE <name> RENAME TO <newname>` shape at the
wire-protocol bypass layer, and `(*Server).renameDatabase`, wired into
`tryHandleDatabaseDDL` right after the `parseAlterDatabaseConfig` check (so
the SET/RESET form still takes priority, unchanged) and into
`databaseDDLCommandTag` (so the command tag is `ALTER DATABASE`, not the
generic compat-noop tag). `renameDatabase` mirrors PG's `RenameDatabase`
(`postgres/src/backend/commands/dbcommands.c`): database-exists (`3D000`),
currently-open-database (`0A000` `FeatureNotSupported`, "current database
cannot be renamed" — PG disallows renaming the database a connection is
live on; `liveDBName` is threaded through from `tryHandleDatabaseDDL`'s
existing parameter of the same name), and new-name-already-exists (`42710`)
checks, then calls the new `catalog.RenameDatabase`.

Verified live: the `DROP DATABASE regression_utf8 does not exist` diff line
is gone (24→23 lines, 4→3 `^+ERROR`); `go build ./...`,
`go test ./internal/parser/... ./internal/executor/... ./internal/postmaster/...`,
and `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` all pass.

## Not modelled (out of scope for this loop)

- **Privilege checks.** Like every other database-DDL handler in this file,
  `renameDatabase` is accept-and-ignore on the superuser/`CREATEDB`-and-owner
  gate PG's real `RenameDatabase` runs before touching the catalog.
- **The shared `pg_database` heap row's `datname` column is not re-synced.**
  goopg's own `SELECT * FROM pg_database` is served entirely from the
  registry via `VirtualRows()` (see `sys_pg_database.go`'s package doc, "this
  heap row is the standby's copy"), so this only matters for a real PG
  standby streaming goopg's WAL and reading the row directly — out of scope
  for this fix, which targets the registry the rest of the executor actually
  reads from.

## Remaining gap (why this case is still PARKED)

The residual 23-line diff has two separate causes, neither landed this loop:

1. **`UPDATE pg_database SET datacl = ...` is unconditionally rejected**
   except for the `datconnlimit` column (`internal/executor`'s pg_database
   in-place-update path, `updatePgDatabaseConnLimitOnPage` /
   `execDatabaseACLChange`'s sibling paths) — this specific test block exists
   in PG purely to stress `PgDatabaseToastTable` (a huge `array_fill`'d
   `datacl` forces the row's TOAST path) inside a transaction it then rolls
   back; PG accepts the direct catalog `UPDATE` here because it's running as
   superuser with `allow_system_table_mods`-style trust, which goopg's ACL
   layer does not special-case for `pg_database.datacl` specifically (only
   `GRANT/REVOKE ... ON DATABASE` is wired to `execDatabaseACLChange`, not a
   raw `UPDATE`). Extending direct-`UPDATE`-of-catalog-table support to
   arbitrary `pg_database` columns (not just `datconnlimit`) is its own
   scoped follow-up.
2. **`REASSIGN OWNED BY <role> TO <role>` is entirely unparsed** —
   `internal/executor/cmdtag_table.go` carries the command tag
   (`"REASSIGN OWNED": {false, false}`) but there is no parser arm or
   executor for the statement at all; it currently fails as a syntax error
   at the wire layer. This is a REFACTOR-tier gap on its own: PG's
   `REASSIGN OWNED` (`postgres/src/backend/commands/user.c`
   `ReassignOwnedObjects`) walks every object-type catalog the source role
   could own (tables, sequences, types, functions, schemas, ..., and
   databases) and updates each one's owner column, which needs a real
   pg_shdepend-style enumeration goopg does not have.

Both are independently scoped follow-ups; neither blocks the other.

## Resume point

- For the `datacl` `UPDATE`: extend whichever function currently special-
  cases `datconnlimit` in the `pg_database` in-place-`UPDATE` path
  (`internal/executor/sys_pg_database.go`'s
  `updatePgDatabaseConnLimitOnPage` and its caller in `operators_storage.go`)
  to accept `datacl` too, re-encoding it through the same ACL codec
  `execDatabaseACLChange` (`internal/executor/operators_ddl_database_acl.go`)
  already uses for `GRANT`/`REVOKE`.
- For `REASSIGN OWNED BY`: add a parser arm (mirroring `DROP OWNED BY`'s
  shape, if one already exists, or `role_ddl.go`'s bypass-layer pattern for
  a first cut) and an executor that at minimum reassigns
  `pg_class.relowner`/`pg_type.typowner`/`pg_proc.proowner`/
  `pg_namespace.nspowner`/`pg_database.datdba` for objects owned by the
  source role — PG's real `ReassignOwnedObjects`
  (`postgres/src/backend/commands/user.c`) is the reference implementation
  to mirror the object-type enumeration order from.

Re-run `scripts/pg-regress-runner.sh --verbose database` after either lands.
