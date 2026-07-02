# 0119-0004 — `ALTER ROLE ... [IN DATABASE ...] SET/RESET` (`pg_db_role_setting.setconfig`, `setrole != 0`) round-trip in pg_dump (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-02)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/bin/pg_dump/pg_dump.c` (`dumpDatabaseConfig`'s second
query); `postgres/src/bin/pg_dump/dumputils.c` (`makeAlterConfigCommand`,
`variable_is_guc_list_quote`); `postgres/src/bin/pg_dump/pg_dumpall.c`
(`dumpUserConfig`, `dumpRoleGUCPrivs` — out of scope, see below);
`postgres/src/include/catalog/pg_db_role_setting.h`

## Problem

`0119-0004-database-config-set-pgdump.md` (loop #17) closed the `setrole =
0` half of `pg_db_role_setting` (`ALTER DATABASE ... SET/RESET`) but left
its own "still open" section noting the `setrole != 0` half — `ALTER ROLE
... SET` (cluster-wide, `setdatabase = 0`) and `ALTER ROLE ... IN DATABASE
... SET` (`setdatabase = <dboid>`) — entirely unimplemented:
`dumpDatabaseConfig`'s second query (`SELECT rolname, unnest(setconfig)
FROM pg_db_role_setting s, pg_roles r WHERE setrole = r.oid AND setdatabase
= '<dboid>'::oid`) always returned zero rows against goopg, and
`roleNameFromAlter` (`internal/server/role_ddl.go`) explicitly excluded
`set `/`reset `/`in database ` prefixes from its attribute-form match so the
statement fell through to the pre-existing `compatNoopCommandTag` no-op —
the same "not implemented yet" gate the ALTER DATABASE slice had before
loop #17.

**Scope boundary vs. pg_dumpall:** PG splits `pg_db_role_setting` dumping
across two client binaries. `pg_dump.c`'s `dumpDatabaseConfig` (used by
`pg_dump --create`) covers `setdatabase != 0` — both the database-only
(`setrole = 0`) and role-and-database (`setrole = r.oid`) rows for the ONE
database being dumped. `pg_dumpall.c`'s `dumpUserConfig`/`dumpRoleGUCPrivs`
(used by `pg_dumpall`, a separate binary this milestone's TAP suite does not
cover beyond CLI-arg-error cases — see `internal/testport/
pgdump_port_test.go`) cover `setdatabase = 0` — the plain cluster-wide
`ALTER ROLE ... SET` form with no `IN DATABASE` clause. Since M0119-0004 is
specifically the `pg_dump` TAP battery, this slice's **pg_dump-reachable,
test-covered surface is the `IN DATABASE` form only** — but the engine-level
SET/RESET/RESET ALL plumbing built here is generic over both `dbOid == 0`
(cluster-wide) and `dbOid == FirstUserOID` (`IN DATABASE`), since PG models
both as the same catalog row shape (`setrole != 0`, `setdatabase` either 0
or a real oid). A future `pg_dumpall` milestone can reuse
`RoleConfigEntries(roleOid, 0)` unchanged.

## Fix

### Wire-protocol bypass (mirrors `parseAlterDatabaseConfig`, not the real parser)

`internal/server/role_ddl.go` already intercepts `CREATE`/`ALTER`/`DROP
ROLE/USER` as a raw-SQL string-prefix classifier (`tryHandleRoleDDL`) for
the same reason `database_ddl.go` does — goopg's parser has no grammar for
these statements. This slice adds a sibling parser function rather than
extending the real parser:

- `parseAlterRoleConfig(sql)` recognises `ALTER ROLE|USER <name> [IN
  DATABASE <dbname>] SET <config> {TO|=} <value>[, <value>...]`, `SET
  <config> TO DEFAULT` (treated as `RESET`), `RESET <config>`, `RESET ALL`.
  Reuses `splitLeadingSQLToken`/`flattenConfigValueList`/
  `splitTopLevelSQLCommas`/`unquoteSQLStringLiteral` from
  `database_ddl.go` (same package) rather than duplicating the tokenizer.
  Any other `ALTER ROLE` form (attribute list, `RENAME TO`) is left
  unrecognised (`ok=false`) so `tryHandleRoleDDL`'s pre-existing branches
  still handle them — `parseAlterRoleConfig` is tried FIRST in the `alter
  role`/`alter user` case, before `roleRenameFromAlter` and the attribute
  path, mirroring the ordering `roleNameFromAlter`'s own exclusion list
  already implied.
- `tryHandleRoleDDL` gained a `dbName` parameter (`connTx.DBName`) — both
  call sites in `dispatch.go` (the single-statement path and the
  leading-role-DDL-peel path for a multi-statement batch) now pass
  `connTx.DBName` through, mirroring `tryHandleDatabaseDDL`'s existing
  `liveDBName` parameter. `applyAlterRoleConfig` applies the identical
  v0-scope restriction: naming an `IN DATABASE` other than the connection's
  own is a silent no-op (goopg v0 has no storage for any other database).
- Role resolution: `applyAlterRoleConfig` calls `s.cfg.Catalog.RoleOID(name)`
  (already on the `catalog.Catalog` interface, used elsewhere for policy
  role resolution) and returns `roleDoesNotExistErr` (42704) for an unknown
  role — matching the attribute-form branch's existing check. "ALTER ROLE
  ALL SET ..." (PG's `role_specification = ALL` cluster-wide-default form,
  `setrole = 0` — semantically distinct from an actual role's oid) is not
  special-cased: `RoleOID("all")` simply fails to resolve (no role is named
  "all" in ordinary use) and the statement falls through to the pre-existing
  no-op path, same as any other unrecognised form.
- `ALTER ROLE`'s `CommandComplete` tag is already correct with no changes:
  `dispatch.go`'s generic `strings.HasPrefix(norm, "alter ")` branch emits
  `"ALTER ROLE"` for any statement `tryHandleRoleDDL` handles, matching PG's
  own tag for both `ALTER ROLE` and `ALTER USER` syntax.

### Catalog store + virtual-table projection

`catalog.InMemory` gains `roleSettings map[roleSettingKey][]string` where
`roleSettingKey{RoleOID, DBOid uint32}` — the `setrole != 0` complement of
the existing `dbRoleSettings map[uint32][]string` (`setrole = 0`) added by
the ALTER DATABASE slice. `DBOid` is `0` for a cluster-wide override or
`FirstUserOID` (16384, the same SQL-visible placeholder oid `pg_database`
displays) for the `IN DATABASE` form — same keying rationale as
`dbRoleSettings`: `pg_db_role_setting` is a pure virtual table with no heap
to resync, and pg_dump's `dumpDatabaseConfig` cross-references `setdatabase`
against the oid it already read from a prior `pg_database` query, so the two
must agree. New methods `SetRoleConfig`/`ResetRoleConfig`/
`ResetAllRoleConfig`/`RoleConfigEntries` mirror the `*DatabaseConfig`
quartet's exact semantics (in-place case-insensitive-name replace-or-append
for `SET`, single-entry removal for `RESET`, whole-slice clear for `RESET
ALL`). A new `AllRoleConfigRows()` enumerator returns every recorded
`(RoleOID, DBOid) -> entries` row sorted for deterministic virtual-row
output (no such enumerator was needed for `dbRoleSettings`, which only ever
has the single `FirstUserOID` key in v0's single-live-database scope, but
`roleSettings` can have arbitrarily many role/scope combinations).

`pg_db_role_setting.VirtualRows` (previously exactly one conditional row for
the `setrole = 0` case) now appends one row per `AllRoleConfigRows()` entry
after that row, so both catalog halves project through the same table.

### WAL / restart persistence

Three new WAL record kinds mirror the ALTER DATABASE slice's pattern (no
per-object on-disk file namespace, so physical redo is a no-op; a
post-replay driver applies them to the catalog): `RecordKindAlterRoleSetConfig`
(76, `roleOid|dbOid|name|value`), `RecordKindAlterRoleResetConfig` (77,
`roleOid|dbOid|name`), `RecordKindAlterRoleResetAllConfig` (78,
`roleOid|dbOid`). New recovery driver `internal/initdb/
role_config_recovery.go` (`replayRoleConfigRecords`) scans the WAL after
physical replay and re-applies each record via `SetRoleConfig`/
`ResetRoleConfig`/`ResetAllRoleConfig` (all idempotent, like their
database-config counterparts — no separate `*DuringRecovery` variants
needed). Wired into `internal/initdb/open.go` right after
`replayDatabaseConfigRecords`; ordering relative to role DDL replay does not
matter because each record keys off the role's OID (stable across a
rename — `RenameRole` preserves it), not its name.

## Tests

- `internal/catalog/database_test.go`: `TestSetRoleConfigUpsertsInPlace`,
  `TestRoleConfigScopedByDatabase` (cluster-wide vs. IN-DATABASE scopes are
  independent rows for the same role), `TestResetRoleConfigRemovesOnlyNamedEntry`,
  `TestPgDbRoleSettingVirtualRowsProjectsRoleOverrides` (asserts row order
  and exact array-literal rendering across all three row shapes: the
  pre-existing `setrole=0` database row, a role's cluster-wide row, and a
  role's IN-DATABASE row).
- `internal/server/role_config_test.go`: `TestParseAlterRoleConfig`
  (SET/SET TO/SET TO DEFAULT/multi-value SET/RESET/RESET ALL, each with and
  without `IN DATABASE`, plus quoted role/db names, plus 6 negative cases for
  forms handled elsewhere in `tryHandleRoleDDL` or unrelated statements);
  `TestTryHandleRoleDDLAlterRoleConfig` (full `Server`-level exercise:
  cluster-wide SET, IN-DATABASE SET scoped independently, an other-database
  IN-DATABASE statement is a no-op, RESET removes only the named entry,
  RESET ALL clears a scope, an unknown role raises 42704).
- `internal/wal/role_config_ddl_test.go`: round-trip + truncated/wrong-kind
  guard tests for all three new record kinds.
- `internal/initdb/role_config_recovery_test.go`: full Init→Open→
  WAL-append→Close→Open cycles for SET, SET+SET+RESET (verifying the
  cluster-wide and IN-DATABASE scopes replay independently), RESET ALL, plus
  the missing-WAL-dir no-op guard.
- `internal/testport/pgdump_role_config_test.go`: `TestPort_PgDumpRoleConfigSet`
  — creates a role, `SET work_mem`/`SET search_path` (multi-value)/`SET
  statement_timeout` immediately `RESET`/re-`SET work_mem` (replace-in-place)
  all `IN DATABASE postgres`, plus one cluster-wide (no `IN DATABASE`) `SET
  lock_timeout` — against a real cluster; asserts pg_dump `--create` emits
  exactly `ALTER ROLE cfgrole IN DATABASE postgres SET work_mem TO
  '128MB';` and the `search_path` line, that the reset `statement_timeout`
  line and the cluster-wide `lock_timeout` override are both absent from the
  `pg_dump` (non-`pg_dumpall`) output, and that `work_mem` appears exactly
  once (confirmed against real pg_dump 18.3, not assumed).

## Gates

- `go build ./...` clean.
- `go vet ./internal/catalog/... ./internal/server/... ./internal/wal/... ./internal/initdb/...` clean.
- `go test ./internal/catalog/... ./internal/server/... ./internal/wal/... ./internal/initdb/...` PASS.
- `go test -run TestPort_PgDumpRoleConfigSet ./internal/testport/` PASS (matches real pg_dump 18.3 `--create` output).
- `go test -run 'TestPort_PgDumpDatabaseConfigSet|TestPort_PgDumpConnectionSetup|TestPort_PgDumpDatabaseGrantACL' ./internal/testport/` PASS (regression-checked: shares the `pg_db_role_setting` virtual table and the `--create` harness with this slice).
- pgbench smoke = pre-commit hook.

## Still open under M0119-0004-ACLHEAP

- **Extended-protocol only reaches the simple-query path.** Same standing
  gap as every prior M0119-0004-ACLHEAP slice — `ALTER ROLE ... SET/RESET`
  is intercepted in `dispatchSimpleQueryViaExecutor` only.
- **Multi-database scope.** `IN DATABASE <name>` naming any database other
  than the connection's own live database is a silent no-op (same
  restriction `datacl`/`ALTER DATABASE ... SET` already have).
- **`pg_dumpall`'s cluster-wide dump (`dumpUserConfig`/`dumpRoleGUCPrivs`,
  `setdatabase = 0`, no `IN DATABASE` clause) is untested** — this
  milestone's TAP battery is pg_dump-only; `internal/testport/
  pgdump_port_test.go` covers only `pg_dumpall`'s CLI-arg-error surface, not
  an actual cluster-wide dump/restore. The engine-side storage
  (`RoleConfigEntries(roleOid, 0)`) already supports this shape (exercised
  by the unit/WAL/recovery tests above), so closing this is purely a
  pg_dumpall TAP-porting task, not a new engine capability.
- **`SET TIME ZONE value`, `SET SESSION AUTHORIZATION`, and `SET ... FROM
  CURRENT`** are not recognised by `parseAlterRoleConfig` and fall through
  to the pre-existing no-op absorption, same as `parseAlterDatabaseConfig`.
- **"ALTER ROLE ALL SET ..."** (PG's `role_specification = ALL` form) is not
  supported — see the Fix section's role-resolution note.
