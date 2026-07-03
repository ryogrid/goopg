# 0119-0004 — `ALTER DATABASE ... SET/RESET` (`pg_db_role_setting.setconfig`) round-trip in pg_dump (M0119-0004-ACLHEAP follow-up)

Status: implemented (2026-07-02)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/bin/pg_dump/pg_dump.c` (`dumpDatabaseConfig`,
`dumpDatabase`); `postgres/src/bin/pg_dump/dumputils.c`
(`makeAlterConfigCommand`, `variable_is_guc_list_quote`); `postgres/src/
include/catalog/pg_db_role_setting.h`

## Problem

`ALTER DATABASE name SET config = value` / `RESET config` / `RESET ALL` had
no grammar in goopg's parser at all (`parseAlter`, `internal/parser/ddl.go`,
requires the literal `TABLE` keyword right after `ALTER` — any other
following keyword, including `DATABASE`, fails to parse outright) and was
absorbed by the wire-protocol layer's generic `compatNoopCommandTag`
fallback, which returns a fake `ALTER DATABASE` `CommandComplete` with zero
effect. `pg_db_role_setting` — the catalog `dumpDatabaseConfig` reads to
reconstruct these lines — was registered as a permanently-empty virtual
table (loop #16's datacl-half slice: "goopg has no `ALTER DATABASE ... SET`
writer yet, so an empty table is correct"). This was the last open resume
point the datacl-half design doc left under M0119-0004-ACLHEAP for the
non-ACL, non-SECURITY-LABEL residual of `pg_database`'s `--create` dump.

Like `datacl`, `dumpDatabaseConfig` is only called from `dumpDatabase`,
which pg_dump only invokes under `-C`/`--create` (`if (dopt.outputCreateDB)
dumpDatabase(fout);`) — so this reuses the `--create` test harness loop #16
built rather than needing a new one.

## Fix

### Wire-protocol bypass (mirrors CREATE/DROP DATABASE, not the real parser)

`internal/server/database_ddl.go` already intercepts `CREATE DATABASE`/`DROP
DATABASE` as a raw-SQL string-prefix classifier at the dispatch layer
(`classifyDatabaseDDL`/`tryHandleDatabaseDDL`) specifically *because*
goopg's parser doesn't recognise the statement — the same shape of gap. This
slice adds a sibling classifier instead of teaching `parseAlter` a new
statement (which would risk the 1300-line ALTER dispatch function for a
narrow, self-contained feature):

- `parseAlterDatabaseConfig(sql)` recognises exactly three forms: `SET name
  {TO|=} value[, value...]`, `SET name TO DEFAULT` (treated as a `RESET`),
  `RESET name`, `RESET ALL`. Any other `ALTER DATABASE` sub-form
  (`CONNECTION LIMIT`, `IS_TEMPLATE`, `RENAME TO`, `OWNER TO`, ...) is
  deliberately left unrecognised (`ok=false`) so it keeps falling through to
  the pre-existing `compatNoopCommandTag` no-op absorption, unchanged.
- `flattenConfigValueList` reproduces `guc.c`'s `flatten_set_variable_args`:
  a string literal is unescaped and stripped of its quotes, a bare token
  (identifier/number) is kept verbatim, multiple comma-separated values
  (the `search_path`-shaped case) are comma-joined with **no** extra
  quoting. The realquoting/display work (`makeAlterConfigCommand`,
  `dumputils.c`) happens **client-side in the real pg_dump binary**, not in
  goopg — goopg only needs to store the same raw text PG's own `SET`
  handler would have flattened into `setconfig`, not reproduce pg_dump's
  quoting rules.
- `tryHandleDatabaseDDL` gained a `liveDBName` parameter
  (`connTx.DBName`, the connection's own database) so
  `applyAlterDatabaseConfig` can apply the same v0-scope restriction
  `execDatabaseACLChange` already applies to `datacl`: naming any database
  other than the connection's own is a silent no-op (goopg v0 has no true
  multi-database storage — there is nothing else to write into).
- `databaseDDLCommandTag` checks `parseAlterDatabaseConfig` first so the
  wire layer's `ALTER DATABASE` tag is written correctly even for the
  now-handled forms.

### Catalog store + virtual-table projection

`catalog.InMemory` gains `dbRoleSettings map[uint32][]string` (an ordered
list of `"name=value"` entries, PG's on-disk `pg_db_role_setting.setconfig`
representation) plus `SetDatabaseConfig`/`ResetDatabaseConfig`/
`ResetAllDatabaseConfig`/`DatabaseConfigEntries`. `SetDatabaseConfig`
replaces an existing same-name entry **in place** (case-insensitive GUC name
match) rather than appending a duplicate, mirroring PG's
`GUC_array_change`.

**Keying subtlety (the one real gotcha this slice hit):** the store is keyed
by `catalog.FirstUserOID` (`16384`) — the *same* SQL-visible placeholder OID
`pg_database.VirtualRows` displays for every non-template database — **not**
`catalog.InMemory.DBOID()` (the real on-disk physical OID `datacl` keys its
heap resync under). `pg_db_role_setting` is a pure virtual table with no
heap to resync, so there's no reason to use the physical OID; but pg_dump's
`dumpDatabaseConfig` issues a genuinely separate query
(`WHERE setdatabase = '<dboid>'::oid`) that cross-references the `oid`
value it already read from a **prior** `pg_database` query — unlike
`datacl`, which is read in the SAME row/query as `pg_database.oid` and so
never needed the two OIDs to agree. Keying by `DBOID()` (5, PG18's
well-known postgres OID) instead of `FirstUserOID` (16384, the display
placeholder) silently produced zero pg_dump output — caught by first writing
the pg_dump round-trip test and observing the `ALTER DATABASE ... SET` lines
were simply missing, then confirming via a direct `SELECT setdatabase,
setrole, setconfig FROM pg_db_role_setting` probe that the row existed under
the wrong key.

`pg_db_role_setting.VirtualRows` (previously `func() [][]string { return nil
}` unconditionally) now projects at most one row: `(FirstUserOID, "0",
optionsArrayLiteral(entries))` when `entries` is non-empty. `setrole` is
always `"0"` — `ALTER ROLE ... SET` / `ALTER ROLE ... IN DATABASE ... SET`
(the role-scoped and role-and-database-scoped forms of the same catalog)
remain unimplemented; this slice only closes the plain
`ALTER DATABASE ... SET` (`setrole = 0`) case `dumpDatabaseConfig`'s first
query reads. `optionsArrayLiteral` (the existing `pg_foreign_server.
srvoptions`/`pg_attribute.attfdwoptions` renderer) already produces the
correct PG `array_out` text[] literal for `"name=value"` elements, so it is
reused as-is — no new array-rendering code was needed.

### WAL / restart persistence

Three new WAL record kinds mirror the CREATE AGGREGATE-family pattern (no
per-object on-disk file namespace, so physical redo is a no-op; a post-replay
driver applies them to the catalog): `RecordKindAlterDatabaseSetConfig` (73,
`dbOid|name|value`), `RecordKindAlterDatabaseResetConfig` (74,
`dbOid|name`), `RecordKindAlterDatabaseResetAllConfig` (75, `dbOid`). New
recovery driver `internal/initdb/database_config_recovery.go`
(`replayDatabaseConfigRecords`) scans the WAL after physical replay and
replays each record via `SetDatabaseConfig`/`ResetDatabaseConfig`/
`ResetAllDatabaseConfig` directly (all three are already idempotent
upsert/remove operations, so no separate `*DuringRecovery` variants were
needed — unlike `RegisterDatabaseDuringRecovery`, which exists because
`CreateDatabase` errors on a duplicate). Wired into `internal/initdb/open.go`
right after `replayDatabaseDDLRecords`; ordering between the two does not
matter since each config record carries its own `dbOid` rather than a name
resolved through the database registry.

## Tests

- `internal/catalog/database_test.go`:
  `TestSetDatabaseConfigUpsertsInPlace`,
  `TestSetDatabaseConfigNameIsCaseInsensitive`,
  `TestResetDatabaseConfigRemovesOnlyNamedEntry`,
  `TestResetAllDatabaseConfigClearsEverything`,
  `TestPgDbRoleSettingVirtualRowsProjectsOverrides` (asserts the exact
  `{work_mem=64MB,"search_path=public,pg_catalog"}` array literal, including
  `quoteArrayElement`'s comma-triggered whole-element double-quoting).
- `internal/server/database_ddl_test.go`: `TestParseAlterDatabaseConfig`
  (SET/SET TO/SET TO DEFAULT/multi-value SET/RESET/RESET ALL/quoted db name,
  plus 6 negative cases confirming unmodelled ALTER DATABASE forms are left
  unrecognised); `TestDatabaseDDLCommandTag` extended with the two new tags.
- `internal/wal/database_config_ddl_test.go`: round-trip + truncated/
  wrong-kind guard tests for all three new record kinds.
- `internal/initdb/database_config_recovery_test.go`: full Init→Open→
  WAL-append→Close→Open cycles for SET, SET+SET+RESET (same-name reset vs.
  sibling-name survival), RESET ALL, plus the missing-WAL-dir no-op guard.
- `internal/testport/pgdump_database_config_test.go`:
  `TestPort_PgDumpDatabaseConfigSet` — `SET work_mem`, `SET search_path TO
  public, pg_catalog` (multi-value), `SET statement_timeout` immediately
  `RESET`, and a re-`SET work_mem` (replace-in-place) against a real
  cluster; asserts pg_dump `--create` emits exactly `ALTER DATABASE postgres
  SET work_mem TO '128MB';` and `ALTER DATABASE postgres SET search_path TO
  'public', 'pg_catalog';`, that the reset `statement_timeout` line is
  absent, and that `work_mem` appears exactly once (confirmed against real
  pg_dump 18.3, not assumed).

## Gates

- `go build ./...` clean.
- `go vet ./internal/catalog/... ./internal/server/... ./internal/wal/... ./internal/initdb/...` clean.
- `go test ./internal/catalog/... ./internal/server/... ./internal/wal/... ./internal/initdb/...` PASS.
- `go test -run TestPort_PgDumpDatabaseConfigSet ./internal/testport/` PASS (matches real pg_dump 18.3 `--create` output).
- `go test -run TestPort_PgDumpDatabaseGrantACL ./internal/testport/` PASS (regression-checked: shares the same `--create` harness and `pg_database` virtual-table columns as the datacl slice).
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS (regression-checked, per the datacl slice's own subdbid-join caution).
- `scripts/tpch-spotcheck.sh` PASS.
- pgbench smoke = pre-commit hook.

## Still open under M0119-0004-ACLHEAP

- **Extended-protocol only reaches the simple-query path.** Like CREATE/DROP
  DATABASE (which this slice's classifier is modelled on), `ALTER DATABASE
  ... SET/RESET` is intercepted in `dispatchSimpleQueryViaExecutor`
  (`internal/server/dispatch.go`) only — `internal/server/
  dispatch_extended.go` has no equivalent hook, so the same statement sent
  over the extended query protocol (a prepared statement / `Parse`+`Bind`+
  `Execute`) still falls through to `compatNoopCommandTag`'s silent no-op.
  This is the same standing extended-protocol gap noted on every prior
  M0119-0004-ACLHEAP slice, not a new one.
- **Multi-database scope.** `ALTER DATABASE <name> SET ...` naming any
  database other than the connection's own live database is a silent no-op
  (same restriction `datacl` already has) — goopg v0 has no true
  multi-database storage.
- **`ALTER ROLE ... SET` / `ALTER ROLE ... IN DATABASE ... SET`** (the
  role-scoped and role-and-database-scoped halves of `pg_db_role_setting`,
  `setrole != 0`) remain entirely unimplemented — `dumpDatabaseConfig`'s
  second query (`setrole = r.oid`) always returns zero rows against goopg.
- **`SET ... FROM CURRENT`** (PG grammar special-case distinct from the
  plain `SET name = value` form — reads the session's *current* value of
  the named GUC rather than a literal) is not recognised by
  `parseAlterDatabaseConfig` and falls through to the pre-existing no-op
  absorption like any other unmodelled ALTER DATABASE form. Needs the
  session's live GUC-value lookup wired through, which the six special
  forms fixed below did not need (they carry their own literal value).

### Follow-up: `TIME ZONE`/`SCHEMA`/`NAMES`/`ROLE`/`SESSION AUTHORIZATION`/`XML OPTION` special-form GUC translation (loop #78)

Closes the `SET TIME ZONE value`/`SET SESSION AUTHORIZATION` bullet above
(minus `SET ... FROM CURRENT`, which remains open — see above). Real PG's
`set_rest` grammar production (`postgres/src/backend/parser/gram.y`
~line 1708) accepts six "special syntaxes" as alternatives to the generic
`name TO|= value` form: `TIME ZONE zone_value`, `SCHEMA Sconst`, `NAMES
opt_encoding`, `ROLE NonReservedWord_or_Sconst`, `SESSION AUTHORIZATION
{NonReservedWord_or_Sconst|DEFAULT}`, `XML OPTION {DOCUMENT|CONTENT}` —
each translating to a plain `VariableSetStmt{kind: VAR_SET_VALUE|
VAR_SET_DEFAULT, name: "timezone"|"search_path"|"client_encoding"|"role"|
"session_authorization"|"xmloption"}`. `SetResetClause` (used by both the
plain `SET` statement and `AlterDatabaseSetStmt`/`AlterRoleSetStmt`)
reduces to the identical `set_rest` production, so all six are valid inside
`ALTER DATABASE ... SET` too, and `dbcommands.c AlterDatabaseSet` ->
`pg_db_role_setting.c AlterSetting` -> `guc_funcs.c
ExtractSetVariableArgs` stores the translated name/value pair exactly like
the generic form — no separate storage path. `parseAlterDatabaseConfig`
only ever matched `configName TO|= value`/`RESET [name|ALL]`, so e.g. `SET
TIME ZONE 'UTC'` failed the `to `/`=` check (its first token, `TIME`, was
treated as `configName`, leaving `ZONE 'UTC'` as the unmatched remainder)
and silently fell through to no-op absorption — a real command a WordPress-
or ORM-style client can plausibly send (`SET SCHEMA`/`SET NAMES` are common
compatibility idioms) was accepted with an `ALTER DATABASE`
`CommandComplete` but never actually applied. New shared helper
`parseSetRestSpecialForm` (`internal/server/database_ddl.go`, used by both
`parseAlterDatabaseConfig` and `parseAlterRoleConfig` — see the sibling
role-config design doc) recognizes the six forms on the raw text following
`SET ` and returns the translated `(configName, configValue, reset)`
before the generic `configName TO|= value` parse runs; `TRANSACTION
SNAPSHOT` is deliberately excluded (`ExtractSetVariableArgs` has no case
for `VAR_SET_MULTI`, so real PG cannot store it via `AlterSetting` either —
it is a transaction-scoped command, not a persistable GUC). Live-verified
end-to-end (throwaway data dir, real `psql`/`pg_dumpall` 18.3): `ALTER
DATABASE postgres SET TIME ZONE 'UTC'`/`SET SCHEMA 'app'`/`SET NAMES
'utf8'` all populate `pg_db_role_setting.setconfig` with the correct
translated GUC name, and `ALTER ROLE ... SET SESSION AUTHORIZATION
'postgres'` round-trips byte-identically through `pg_dumpall
--roles-only` (`ALTER ROLE verifyrole SET session_authorization TO
'postgres';`).

Tests: `TestParseAlterDatabaseConfig`/`TestParseAlterRoleConfig`
(`internal/server/database_ddl_test.go`/`role_config_test.go`), new cases
for all six forms plus one `IN DATABASE` combination.

Gates: `go build ./...` clean; `go test ./internal/server/...` PASS (full
suite, no regression); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
pgbench smoke = pre-commit hook.
