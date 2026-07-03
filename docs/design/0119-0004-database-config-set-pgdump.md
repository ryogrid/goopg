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
  `setrole != 0`) landed in a later, undocumented-here loop (see
  `internal/server/role_ddl.go`'s `parseAlterRoleConfig`/
  `applyAlterRoleConfig`, `internal/server/role_config_test.go`) — this bullet
  was stale. `ALTER ROLE ALL SET/RESET ...` (the `setrole=0`
  cluster-wide-default sub-case) was still unimplemented until the "Follow-up:
  `ALTER ROLE ALL SET/RESET ...` (loop #82)" section below closed it.
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

### Follow-up: `SET ... FROM CURRENT` (loop #79)

Closes the `SET ... FROM CURRENT` bullet above — the residual loop #78
explicitly left open. Real PG's `set_rest_more` grammar production
(`postgres/src/backend/parser/gram.y` ~line 1697) has a `var_name FROM
CURRENT` alternative distinct from the six literal-carrying special forms
loop #78 handled: `VariableSetStmt{kind: VAR_SET_CURRENT, name: var_name}`,
with NO literal value in the parse tree at all. `ExtractSetVariableArgs`
(`postgres/src/backend/utils/misc/guc_funcs.c`) resolves the stored value
at *apply* time via `GetConfigOptionByName(name, NULL, false)` — the
calling session's live/current effective value for that GUC, not anything
present in the SQL text. This is the reason loop #78 scoped it out: the six
special forms are pure syntax translation (no I/O), while `FROM CURRENT`
needs a live-session read that `parseAlterDatabaseConfig`/
`parseAlterRoleConfig` — deliberately kept as pure, session-less parse
functions — have no way to perform.

Landed as a two-layer split mirroring that boundary:
- **Parse layer** (unchanged purity): `parseAlterDatabaseConfig`/
  `parseAlterRoleConfig` (`internal/server/database_ddl.go`/`role_ddl.go`)
  gained a `var_name FROM CURRENT` detection right after they split off
  `configName` via `splitLeadingSQLToken` — a case alongside the existing
  `TO `/`=` branches, not a new case inside `parseSetRestSpecialForm` (that
  helper only matches FIXED keyword prefixes for the six special forms;
  `FROM CURRENT` instead matches an arbitrary `var_name` FOLLOWED by a
  fixed suffix, a different shape). Sets a new `fromCurrent bool` field on
  `alterDatabaseConfigOp`/`alterRoleConfigOp`, leaving `configValue` empty.
- **Apply layer** (new I/O): `applyAlterDatabaseConfig`/
  `applyAlterRoleConfig` take a new `currentGUCResolver func(name string)
  (string, bool)` parameter; when `op.fromCurrent`, they call it to resolve
  `op.configValue` immediately before the `SetDatabaseConfig`/
  `SetRoleConfig` write — an unresolved name (`ok=false`, including a nil
  resolver — no live session) returns PG's exact `unrecognized
  configuration parameter "%s"` text (`guc.c` ~line 1168, `ERRCODE_UNDEFINED_
  OBJECT`/42704 — surfaced via the existing `roleError{code:
  sqlstate.UndefinedObject}` wrapper on the role side; the database side
  still routes through `tryHandleDatabaseDDL`'s pre-existing single
  `sqlstate.SystemError` mapping for ALL its errors — see Deferred below).
  `tryHandleDatabaseDDL`/`tryHandleRoleDDL` threaded the resolver through as
  a new third parameter. `dispatch.go`'s `dispatchSimpleQueryViaExecutor`
  builds ONE resolver closure per dispatch call (`sess.Get(name)` — the
  same `SessionRegistry.Get` SHOW/`ectx.GetSetting` already use) and passes
  it to all three call sites (the CREATE/DROP DATABASE-parse-failure
  intercept, the split-leading-role-DDL recursion, and the plain role-DDL
  intercept); the resolver degrades to always-`ok=false` when `sess` is nil
  (some embedded/test paths), matching an unresolvable name.
  The resolution deliberately happens AFTER the existing "not my own live
  database"/"IN DATABASE otherdb" no-op checks, so an ALTER targeting a
  database/role-scope goopg has no storage for (v0 has no cross-database
  isolation) never has to resolve anything or surface a spurious
  "unrecognized parameter" error.

Live-verified end-to-end (throwaway data dir, real `psql` 18.3, both
goopg and a separately-`initdb`'d real PG 18.3 for comparison):
`SET work_mem = '77MB'; ALTER DATABASE postgres SET work_mem FROM
CURRENT;` and the `ALTER ROLE` equivalent with `search_path` both populate
`pg_db_role_setting.setconfig`/`pg_roles`-joined `setconfig` with the
session's current value; `ALTER DATABASE postgres SET no_such_guc_xyz FROM
CURRENT` errors `unrecognized configuration parameter "no_such_guc_xyz"` on
both engines.

Tests: `TestParseAlterDatabaseConfig`/`TestParseAlterRoleConfig` extended
with `FROM CURRENT` parse cases; new
`TestTryHandleDatabaseDDLAlterDatabaseConfigFromCurrent`
(`database_ddl_test.go`)/`TestTryHandleRoleDDLAlterRoleConfigFromCurrent`
(`role_config_test.go`) cover the apply-layer resolver (success, nil
resolver, unresolved name, and — database side only — the "other database"
no-op never consulting the resolver).

Gates: `go build ./...`/`go vet ./...` clean; `go test ./internal/server/...`
PASS (full suite, no regression); `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); full `scripts/ralph-precommit-test.sh` PASS (incl. pgbench
TPC-B/simple-update/select-only smoke, 0 failed transactions); live
`goopg`/`psql` vs. real PG 18.3 byte-comparison as described above.

Deferred (ledger row appended, `M0119-0004-ACLHEAP`):

1. **GUC unit-display formatting** — real PG's `GetConfigOptionByName`
   calls the variable's `_ShowOption(record, use_units=true)` formatter, so
   `SET work_mem = '77MB'; ... FROM CURRENT` stores the literal
   `work_mem=77MB` (unit-suffixed) in `pg_db_role_setting`. goopg's
   `SessionRegistry.Get` (the mechanism this loop's resolver AND the
   pre-existing `SHOW work_mem` both call) has no such display formatter —
   it returns the canonicalised raw-unit value (`work_mem=78848`, KB
   integer, no suffix). Confirmed via a live A/B: `SHOW work_mem` on goopg
   already printed `78848` (no unit) BEFORE this loop's change — this is a
   pre-existing, broader gap (affects `SHOW`/`SHOW ALL` too, not just `FROM
   CURRENT`), not something this loop introduced or could reasonably fix
   as a bounded follow-up. Resume point: a PG-style optimal-unit
   display formatter for `GUC_UNIT_KB`/`_MB`/`_S`/`_MS`-flagged
   `config.Variable`s, wired into `SessionRegistry.Get`'s effective-value
   path (or a parallel "display" method used by `SHOW`/`ectx.GetSetting`/
   this loop's resolver alike).
2. **Database-DDL error SQLSTATE fidelity** — `tryHandleDatabaseDDL`'s
   caller (`dispatch.go` ~line 112) maps EVERY error from the database-DDL
   intercept (including this loop's new "unrecognized configuration
   parameter") to a single hardcoded `sqlstate.SystemError`, unlike the
   role-DDL side's `roleError`-typed/`roleErrorSQLState`-dispatched errors.
   Pre-existing (shared by `CreateDatabase`/`DropDatabase`'s own error
   paths too), not introduced by this loop, but now newly visible via a
   case (unrecognized GUC name) real PG reports as 42704. Resume point: a
   `databaseDDLError{code, msg}` type mirroring `roleError`, wired into
   `dispatch.go`'s one `s.writeQueryError(w, sqlstate.SystemError, ...)`
   call site the same way `roleErrorSQLState` is.

## Follow-up: database-DDL error SQLSTATE fidelity (loop #80)

Closes deferred item 2 above. Added `databaseDDLError{code sqlstate.Code,
msg string}` (`internal/server/database_ddl.go`) mirroring `roleError`
(`role_ddl.go`) exactly, plus a `databaseDDLErrorSQLState(err) sqlstate.Code`
helper mirroring `roleErrorSQLState` (type-asserts `*databaseDDLError`,
falls back to `sqlstate.SystemError` for an untyped/internal error such as a
WAL-append failure). `dispatch.go`'s one `tryHandleDatabaseDDL` call site
(~line 122) now calls `databaseDDLErrorSQLState(herr)` instead of the
hardcoded `sqlstate.SystemError`.

All four error-construction sites in `database_ddl.go` were converted to the
typed error with the real PG SQLSTATE (cross-checked against
`postgres/src/backend/commands/dbcommands.c`):

- `CREATE DATABASE` on an existing name — `sqlstate.DuplicateDatabase`
  (42P04, `ERRCODE_DUPLICATE_DATABASE`, `createdb()`). Bonus fidelity fix:
  the message text also gained the database name (`database "tpch" already
  exists`, matching `dbcommands.c`'s `errmsg`) — previously it surfaced the
  bare `catalog.ErrDatabaseExists` sentinel text ("database already
  exists", no name), since that raw sentinel was returned as-is.
- `DROP DATABASE` (non-`IF EXISTS`) on a nonexistent name —
  `sqlstate.UndefinedDatabase` (3D000, `ERRCODE_UNDEFINED_DATABASE`,
  `dropdb()`); message text was already PG-correct, only the SQLSTATE
  changed.
- `ALTER DATABASE ... SET name FROM CURRENT` on an unresolved GUC name (nil
  resolver or `resolveCurrent` reporting `ok=false`) —
  `sqlstate.UndefinedObject` (42704), the same code `SET`/loop #78's six
  literal-carrying special forms already use for this message.
- The defensive "missing database name" internal-parse-guard branch (name
  empty despite `classifyDatabaseDDL` matching a kind — not known to be
  reachable from any real SQL shape) got `sqlstate.SyntaxError` for
  consistency; behaviourally inert since no test/live SQL reaches it.

The `errors.Is(err, catalog.ErrDatabaseExists)` / `ErrDatabaseNotFound`
branches are otherwise unchanged — `catalog.InMemory.CreateDatabase`/
`DropDatabase` (`internal/catalog/catalog.go`) return only those two
sentinels or nil, so the surrounding "any other error" fallthrough arms stay
on the generic `sqlstate.SystemError` default (defensive, not known to be
live).

New test `TestDatabaseDDLErrorSQLState` (`database_ddl_test.go`) pins all
three converted user-facing cases (duplicate create, undefined drop,
unresolved `FROM CURRENT` GUC) plus the untyped-error fallback.

Gates: `go build ./...`/`go vet ./...` clean; `go test ./internal/server/...`
PASS (full suite, no regression); `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); full `scripts/ralph-precommit-test.sh` PASS (pgbench smoke,
0 failed transactions).

No new deferrals — this follow-up closes deferred item 2 in full (all
`tryHandleDatabaseDDL`/`applyAlterDatabaseConfig` error sites are now typed).
Deferred item 1 (GUC unit-display formatting) remains open, unrelated scope.

## Follow-up: GUC unit-display formatting (loop #81)

Closes deferred item (1) of the loop #79 row (`FROM CURRENT` storing the
session's raw canonicalised GUC value — e.g. `work_mem=78848` — instead of
PG's unit-suffixed display form `work_mem=77MB`).

**Root cause.** Real PG's `GetConfigOptionByName(name, NULL, false)`
(`guc.c`), the function `ExtractSetVariableArgs`/`set_config_by_name`/
`ShowGUCConfigOption` all resolve a GUC's *value* through, always calls
`ShowGUCOption(record, use_units=true)`, which for a `GUC_UNIT`-flagged
`PGC_INT`/`PGC_REAL` variable converts the stored base-unit value to the
greatest unit that divides it evenly (`convert_int_from_base_unit` /
`convert_real_from_base_unit`) — but only when the value is `> 0`; 0 and
negative "disabled" sentinels print bare, with no unit suffix at all. goopg's
`config.Variable.Value` stores the bare canonical base-unit integer
(`FormatDisplayValue`'s "raw" input) and had no equivalent formatter, so
every display-facing surface — `SHOW`, `SHOW ALL`, `current_setting()`,
`set_config()`, and the `FROM CURRENT` resolver — printed the raw integer
(`work_mem` → `78848` instead of `77MB`), verified as still broken via a
live `SHOW work_mem` A/B against a real, separately-`initdb`'d PostgreSQL
18.3 instance before this loop's fix.

**Fix — additive, not a rewire of `GetSetting`.** `ctx.GetSetting`/
`sess.Get`/`ctx.AllSettings` are also read by internal Go consumers that
parse the *raw* bare integer directly (`Context.deadlockTimeout` parsing
`deadlock_timeout` as milliseconds, `sessionWorkMem`, `track_counts`/
`track_functions` boolean checks, etc.) — reformatting those in place would
have broken every one of them. Instead:

- `config.Variable.FormatDisplayValue(raw string) string`
  (`internal/config/guc.go`) is a new, pure formatter mirroring
  `convert_int_from_base_unit` exactly: two ordered (`memoryDisplayUnits`/
  `timeDisplayUnits`) unit tables keyed by the variable's native `Unit`,
  walked greatest-to-smallest, accepting the first unit whose multiplier
  evenly divides the raw value (falling back to the base unit itself, whose
  multiplier is 1 and therefore always "divides evenly"). Only applies to
  `TypeInt` variables with `Unit != UnitNone`, and only when the parsed
  value is `> 0` (matching upstream's `result > 0` gate) — everything else
  (strings, enums, bools, unitless ints, non-positive values) passes through
  unchanged.
- `config.SessionRegistry.GetDisplay`/`AllDisplay` (`internal/config/
  session.go`) are new parallel accessors — `Get`/`All` plus
  `FormatDisplayValue` — leaving `Get`/`All` themselves untouched.
- `executor.Context` gained two new optional fields, `GetSettingDisplay`/
  `AllSettingsDisplay` (`internal/executor/context.go`), populated by the
  server from `sess.GetDisplay`/`sess.AllDisplay` alongside the pre-existing
  `GetSetting`/`AllSettings` (both `dispatch.go`'s main ectx wiring and
  `dispatch_extended.go`'s extended-protocol wiring). Every genuine
  display-boundary call site now prefers the `*Display` variant, falling
  back to the raw one only when nil (so pre-existing tests/embedded
  `Context`s that never set it keep their old unformatted behaviour):
  `utilitySettingsOp.nextShow`'s both branches (`internal/executor/
  operators_utility_settings.go`, backing SHOW/SHOW ALL through the planner/
  executor path), `current_setting()`/`set_config()`
  (`internal/executor/expr.go`) — the latter matches upstream's
  `set_config_by_name` also returning through `GetConfigOptionByName`. The
  simple-protocol fast paths (`handleShow`/`handleShowAll`,
  `internal/server/query.go`; the `SHOW`/`SHOW ALL` literal-match arms,
  `internal/server/extended.go`) call `sess.GetDisplay`/`AllDisplay`
  directly (always live-wired, no fallback needed). The `FROM CURRENT`
  resolver closure (`resolveCurrentGUC`, `internal/server/dispatch.go`) now
  calls `sess.GetDisplay` instead of `sess.Get`, matching
  `GetConfigOptionByName`'s own `use_units=true`.

**Live-verified** against a running goopg instance (throwaway data dir,
port 5535, real `psql`) and cross-checked against a real, separately-
`initdb`'d PostgreSQL 18.3 instance for every case: `SHOW work_mem` (default
`512MB`; after `SET work_mem='77MB'` → `77MB`), `SELECT
current_setting('work_mem')` → `77MB`, `SELECT set_config('work_mem',
'99MB', false)` → `99MB`, `SHOW checkpoint_timeout` → `5min`, `SHOW
deadlock_timeout` → `1s` (boot value 1000ms), `SHOW statement_timeout`/
`SHOW lock_timeout` (disabled, value 0) → bare `0` (no unit — the `result >
0` gate), `SET deadlock_timeout='250ms'; SHOW deadlock_timeout` → `250ms`,
`SET work_mem='77MB'; ALTER DATABASE postgres SET work_mem FROM CURRENT;
SELECT * FROM pg_db_role_setting` → `setconfig = {work_mem=77MB}` (was
`{work_mem=78848}` before this fix). Confirmed the `search_path` (unitless
string GUC) fast path is unaffected.

Tests: `TestFormatDisplayValue`/`TestSessionRegistryGetDisplay`
(`internal/config/guc_test.go`) pin the exact raw→display mappings observed
against real PG 18.3 above, plus the non-unit/non-int/non-positive
passthrough cases.

Gates: `go build ./...`/`go vet ./...` clean; `go test
./internal/config/... ./internal/server/... ./internal/executor/...` PASS
(no regressions); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); full
`scripts/ralph-precommit-test.sh` PASS (pgbench smoke, 0 failed
transactions).

No new deferrals — this follow-up closes deferred item (1) of the loop #79
row in full. `pg_settings`'s `unit` column
(`internal/catalog/catalog.go`'s `pgSettings.VirtualRows`) remains a
hardcoded minimal 2-row stub (pre-existing, unrelated to this GUC's own
`Value` display — `pg_settings` is out of scope for this loop, tracked
separately if a future loop needs a real per-GUC `pg_settings` row source).

## Follow-up: `ALTER ROLE ALL SET/RESET ...` (loop #82)

Closes the "**`ALTER ROLE ... SET` / `ALTER ROLE ... IN DATABASE ... SET`**"
bullet's cluster-wide-default sub-case under "Still open under
M0119-0004-ACLHEAP" above — the `role_specification = ALL` form of
`ALTER ROLE`/`ALTER USER`, PG's per-cluster default-GUC mechanism (distinct
from a named role's own override).

**Grammar.** `postgres/src/backend/parser/gram.y` ~line 1377 defines
`ALTER ROLE ALL opt_in_database SetResetClause` as a *separate* production
from the `RoleSpec`-carrying form — `n->role = NULL` — not `RoleSpec: ALL`
(that only covers `public` → `ROLESPEC_PUBLIC`, gram.y ~17502). `AlterRoleSet`
(`postgres/src/backend/commands/user.c` ~line 1000) leaves `roleid =
InvalidOid` (0) whenever `stmt->role == NULL`, then calls `AlterSetting
(databaseid, roleid, stmt->setstmt)` — `pg_db_role_setting.c`'s `AlterSetting`
writes `setrole=0` verbatim, no ALL-specific translation. Net: `ALTER ROLE ALL
SET x=y` → `(setdatabase=0, setrole=0)`; `ALTER ROLE ALL IN DATABASE db SET
x=y` → `(setdatabase=<db oid>, setrole=0)` — the *same* `setrole=0` row shape
`ALTER DATABASE ... SET` already produces for its own `(setdatabase=<db oid>,
setrole=0)` case, confirming goopg's existing `SetRoleConfig(roleOid, dbOid,
...)`/`RoleConfigRow`/`pg_db_role_setting.VirtualRows` plumbing (built for
named roles) needed no schema change — only `roleOid=0` needed to be
reachable from the ALL keyword instead of only ever coming from a
`catalog.RoleOID` lookup.

**Fix.** `alterRoleConfigOp` (`internal/server/role_ddl.go`) gained a new
`allRoles bool` field. `parseAlterRoleConfig` detects the bare, UNQUOTED
`ALL` token immediately after `ALTER ROLE`/`ALTER USER` (checked before
`splitLeadingSQLToken` is even called, by inspecting whether the raw text
starts with `"` — a quoted `"ALL"`/`"all"` is a real role identifier, exactly
matching the grammar's ALL-is-a-keyword-not-a-RoleSpec distinction) and sets
`allRoles=true` with `roleName` left empty; every other production
(`IN DATABASE`, the six `set_rest` special forms, `FROM CURRENT`, `RESET`,
`RESET ALL`) is unchanged and composes with the flag automatically, since
they all operate on `rest` after the role-name token is consumed.
`applyAlterRoleConfig` skips the `catalog.RoleOID` lookup entirely when
`op.allRoles` (leaving `roleOid` at its Go zero value, 0 — mirrors
`AlterRoleSet`'s `InvalidOid` default exactly) instead of erroring
`role "all" does not exist`, which is what the pre-existing code did before
this loop (a real, user-visible correctness bug, not a no-op as an earlier
loop's stale comment on `parseAlterRoleConfig` claimed — corrected in the
same edit). No WAL/catalog schema change was needed: `EncodeAlterRoleSetConfig`/
`EncodeAlterRoleResetConfig`/`EncodeAlterRoleResetAllConfig`
(`internal/wal/recovery.go`) and their recovery-replay counterparts
(`internal/initdb/role_config_recovery.go`) already carry `roleOid` as a
plain `uint32` with no reserved-zero special-casing, so restart persistence
worked immediately.

**Live-verified** against a running goopg instance (throwaway data dir, port
5537, real `psql`) and cross-checked against a real, separately-`initdb`'d
PostgreSQL 18.3 instance:
`ALTER ROLE ALL SET work_mem = '64MB'; CREATE ROLE foo LOGIN; ALTER ROLE foo
SET work_mem = '32MB'; ALTER ROLE ALL IN DATABASE postgres SET search_path TO
public; SELECT setdatabase, setrole, setconfig FROM pg_db_role_setting` on
both engines produced the identical three-row shape — `(0, 0,
{work_mem=64MB})` / `(0, <foo's oid>, {work_mem=32MB})` / (`<postgres db
oid>`, `0`, `{search_path=public}`) — differing only in the OID values
themselves (goopg's fixed placeholder OIDs vs real PG's assigned ones, an
existing, unrelated v0 scope restriction). `ALTER ROLE ALL RESET work_mem`
correctly removed only that entry. A quoted `ALTER ROLE "ALL" SET ...` (no
role literally named ALL exists) correctly raised `role "ALL" does not
exist` on both engines, confirming the quoted-identifier-is-not-the-keyword
distinction. Restart persistence confirmed (`goopg stop`/`start` on the same
data dir, `pg_db_role_setting` unchanged).

Tests: `TestParseAlterRoleConfig` extended (6 new `ALL` shapes + 2 quoted-
`"ALL"` negative-classification cases); new
`TestTryHandleRoleDDLAlterRoleAll` (`internal/server/role_config_test.go`) —
cluster-wide SET, `IN DATABASE` SET, RESET, and the quoted-`"ALL"`-errors
case.

Gates: `go build ./...`/`go vet ./...` clean; `go test
./internal/server/...` PASS (no regressions); live psql + real-PG A/B above;
`scripts/tpch-spotcheck.sh` PASS; full `scripts/ralph-precommit-test.sh`
PASS (pgbench smoke).

**Deferred (ledger row appended):** live-verification of this loop's own
`ALTER ROLE foo RESET work_mem` case surfaced a **pre-existing, unrelated**
bug — `ResetRoleConfig`/`ResetDatabaseConfig` (`internal/catalog/catalog.go`)
remove the *matched entry* from a `(roleOid, dbOid)`/`dbOid`'s entry slice
but never delete the map key itself when the slice becomes empty, so
`pg_db_role_setting` keeps emitting a stale row with an empty `setconfig`
(`{}`/blank) after the last override is reset, where real PG deletes the
whole row (confirmed via an A/B: after `ALTER ROLE foo RESET work_mem`
empties foo's only entry, real PG's `pg_db_role_setting` has one fewer row;
goopg's still has the row with a blank `setconfig`). This reproduces
identically for the plain named-role case (predates this loop, not
introduced by the `ALL` fix) and is out of this loop's bounded scope — see
the deferral ledger for the resume point.

## Follow-up: phantom empty-`setconfig` row after last RESET (loop #83)

Closes the loop #82 deferral above. `ResetDatabaseConfig`/`ResetRoleConfig`
(`internal/catalog/catalog.go`) now `delete()` the `dbRoleSettings`/
`roleSettings` map key outright when removing the matched entry leaves the
slice empty, instead of writing the empty-but-non-nil slice back — mirrors
the full-delete semantics `ResetAllDatabaseConfig`/`ResetAllRoleConfig`
already used. Root cause was purely in the two single-entry RESET paths:
`AllRoleConfigRows` (the `pg_db_role_setting.VirtualRows` enumerator for
`setrole != 0` rows) iterates every key present in `c.roleSettings` with no
`len(entries) > 0` guard — unlike the `setrole=0` database-row branch right
above it in the same `VirtualRows` closure, which already checks
`len(entries) > 0` before emitting (`internal/catalog/catalog.go` ~line
9119) — so a lingering empty-slice map entry always surfaced as a row with
`setconfig = {}`, exactly the divergence the loop #82 A/B observed against
real PG.

Tests: `TestResetDatabaseConfigLastEntryDeletesMapKey`,
`TestResetRoleConfigLastEntryDeletesMapKey`
(`internal/catalog/database_test.go`) — assert the map key is gone and
`pg_db_role_setting.VirtualRows`/`AllRoleConfigRows` emit zero matching rows
after the last override is RESET.

Gates: `go build ./...`/`go vet ./...` clean; `go test
./internal/catalog/... ./internal/server/... ./internal/initdb/...` PASS (no
regressions); `scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit
hook.

No new deferral — this closes the loop #82 row cleanly; the fix is a pure
map-hygiene correction with no wire-protocol or WAL-format surface.

## Follow-up: extended-protocol dispatch hook for DATABASE/ROLE DDL (loop #84)

Closes the standing "extended-protocol path has no equivalent hook" residual
named by every M0119-0004-ACLHEAP row since loop #17 (`0119-0004bt` onward).
goopg's parser has no grammar at all for `CREATE`/`DROP`/`ALTER DATABASE` or
`CREATE`/`DROP`/`ALTER ROLE` — both wire protocols must intercept these
statements by string-prefix matching on the raw SQL text after
`parser.Parse` fails. `dispatchSimpleQueryViaExecutor` (`internal/server/
dispatch.go` ~line 122-183, the simple-query protocol psql uses
interactively) has always had this bypass, calling `tryHandleDatabaseDDL`/
`tryHandleRoleDDL`. `executeExtendedQueryViaExecutor` (`internal/server/
dispatch_extended.go`, the Parse/Bind/Execute protocol JDBC, npgsql,
psycopg2's default mode, and most other client libraries use for **every**
statement, not just parameterized ones) never had a counterpart — a parse
failure there went straight to a `42601` syntax error. This was a silent
correctness bug, not a coverage gap: any driver defaulting to the extended
protocol got a `CREATE ROLE`/`ALTER DATABASE ... SET`/etc. rejected outright
where real PG (and goopg's own simple-query path) accepts it.

New `Server.tryHandleDatabaseOrRoleDDLExtended(query, dbName string, sess
*config.SessionRegistry) (*extendedQueryResult, *extendedQueryError, bool)`
(`internal/server/dispatch_extended.go`) mirrors the simple-query bypass:
builds the same `currentGUCResolver` closure over `sess.GetDisplay` (backing
`SET ... FROM CURRENT`), tries `tryHandleDatabaseDDL` then `tryHandleRoleDDL`,
and maps each outcome to an `extendedQueryResult`/`extendedQueryError` with
the same `databaseDDLErrorSQLState`/`roleErrorSQLState` translation and
`databaseDDLCommandTag`/CREATE-ALTER-DROP-ROLE tag logic the simple-query
path uses. Called from `executeExtendedQueryViaExecutor`'s existing
`parser.Parse` error branch, before it falls through to the generic syntax
error. Unlike the simple-query path, no `splitLeadingRoleDDL` multi-statement
recursion is needed — the wire protocol only allows a single SQL command per
Parse message, unlike simple-query's semicolon-separated batches.

Live-verified over the wire (not just via the direct-call unit style prior
loops used): a real Parse/Bind/Execute/Sync sequence against a running
goopg instance for `CREATE ROLE`, `ALTER ROLE ... SET`, `ALTER DATABASE ...
SET`, and `DROP ROLE` all now return the correct `CommandComplete` tag and
apply the catalog mutation, matching the simple-query path byte-for-byte;
`ALTER ROLE nonexistent_role SET ...` over the extended protocol now reports
`42704` (undefined_object), matching `roleErrorSQLState`, instead of a
generic syntax error.

Tests: `TestExtendedProtocolDatabaseAndRoleDDL` (CREATE ROLE → ALTER ROLE
... SET → ALTER DATABASE ... SET → DROP ROLE, each driven over the wire via
Parse/Bind/Execute/Sync, cross-checked against `catalog.InMemory` side
effects) and `TestExtendedProtocolRoleDDLError` (SQLSTATE fidelity for the
nonexistent-role case) — both new, `internal/server/
dispatch_extended_ddl_test.go`.

Gates: `go build ./...`/`go vet ./...` clean; `go test
./internal/server/...` PASS (full package, no regressions); `scripts/
tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke = pre-commit hook.

**Deferred (bounded, out of this loop's scope):**
1. `tryHandleDatabaseDDL`'s `notice` return (e.g. `DROP DATABASE IF EXISTS`
   on a nonexistent name) is dropped on the extended path —
   `extendedQueryResult` has no `Notice` field and `handleExecuteFrame`
   has no NoticeResponse-writing hook, unlike the simple-query path's
   direct `w.WriteNoticeResponse` call. Cosmetic only (the DDL still
   succeeds and returns the correct CommandComplete); adding it means
   threading a new field through `extendedQueryResult` →
   `handleExecuteFrame` → a new `w.WriteNoticeResponse` call, unrelated to
   this loop's actual-failure fix.
2. `roleErrorDetailFields`'s errdetail text (used by e.g. the reserved
   `pg_`-prefix role-name error) has no extended-protocol counterpart
   either — `extendedQueryError`/`extendedMessageError` carry only
   `Code`/`Message`/`Position`, no detail-field slot. Same shape of gap as
   (1); affects only the DETAIL line of one specific error family, not its
   SQLSTATE or message text.
3. `compatNoopCommandTag` (the much broader no-op-DDL absorption for every
   other unrecognised statement form — `CREATE INDEX CONCURRENTLY`, most
   `ALTER TABLE` sub-forms, etc.) still has no extended-protocol
   counterpart. This is a materially larger, pre-existing, separate gap
   (dozens of statement forms, not the two DDL families this loop's ledger
   row named) — out of scope for a single loop.

Resume points: (1)/(2) — add the missing field(s) to `extendedQueryResult`/
`extendedQueryError` and wire the write-through in `handleExecuteFrame`/
`writeExtendedMessageError`. (3) — a dedicated loop scoped to
`compatNoopCommandTag`'s extended-protocol parity, likely its own
M0119-0004 sub-task given the breadth of statement forms it covers.

## Follow-up: extended-protocol Notice/Detail forwarding + a real `DROP DATABASE` dead-stub bug found while testing it (loop #85)

Sets out to close deferred items (1) and (2) of the loop #84 row above: a
`NoticeResponse` and an errdetail (`FieldDetail`) sub-field were both
unreachable on the extended-query-protocol response path.

**Plumbing landed:** `extendedQueryResult` gained `Notice string`;
`extendedQueryError`/`extendedMessageError` both gained `Detail string`
(`internal/server/extended.go`). `writeExtendedMessageError` now appends a
`FieldDetail` `ErrorField` when `em.Detail != ""`. `handleExecuteFrame`
writes a `NoticeResponse` (same four fields the simple-query path's inline
`w.WriteNoticeResponse` call uses: Severity/SeverityNonLocal/SQLState=00000/
Message) right after computing a fresh `portal.Result`, before any
`CommandComplete`/`DataRow` write, whenever `res.Notice != ""`; propagates
`qerr.Detail` into the `extendedMessageError` it builds on the error path.
`tryHandleDatabaseOrRoleDDLExtended` now captures `tryHandleDatabaseDDL`'s
`notice` return (previously discarded with `_`) into the result's `Notice`
field, and a new `roleErrorDetail(err) string` helper (`role_ddl.go`, the
bare-string counterpart of the existing `roleErrorDetailFields` wire-field
wrapper) feeds `roleError.detail` into the `extendedQueryError.Detail` built
for a role-DDL error.

**Real bug found while writing the regression test for (1):** the natural
test — `DROP DATABASE IF EXISTS <nonexistent>` over the extended protocol,
expecting the notice — failed with an EMPTY notice. Root cause: `DROP
DATABASE` has real parser grammar (`parser.parseDropTail`'s `database` arm,
`internal/parser/ddl.go` ~4898, added 2026-06-01 commit `efd725af` — after
M0054-0001's catalog-backed bypass below it, unaware of it), so
`parser.Parse` **succeeds** for every `DROP DATABASE` statement, in both
protocols. That means `tryHandleDatabaseDDL`'s dedicated DROP branch
(`internal/server/database_ddl.go`, the one with the real `notice` return
this follow-up was trying to test) was **entirely unreachable** — both
`dispatchSimpleQueryViaExecutor` and `executeExtendedQueryViaExecutor` only
call it from their `parser.Parse` *failure* branch. A successfully-parsed
`DROP DATABASE` instead routes through the executor's generic
`DropCompatStmt` handling, whose `objType == "database"` arm
(`internal/executor/operators_ddl.go` ~13236, committed the same day as the
parser grammar, `efd725af`) is a **hardcoded pre-catalog-tracking stub**: it
always emits "does not exist[, skipping]" (or the 3D000 error) regardless of
what `catalog.InMemory`'s `databases` registry actually holds — it never
calls `DropDatabase` at all. Net effect: `DROP DATABASE` on a database a
prior `CREATE DATABASE` in the same session actually created **always**
failed with a spurious "does not exist", a real correctness bug (not
specific to the extended protocol — `TestSimpleQueryDropDatabaseActuallyDrops`
below reproduces it over the simple-query protocol too), invisible to the
pre-existing unit tests because they all called `tryHandleDatabaseDDL`
directly rather than driving it through the real wire-dispatch entry point.

**Fix:** a pre-`parser.Parse` check in both dispatch entry points —
`if kind, _ := classifyDatabaseDDL(sql); kind == databaseDDLDrop` — routes
straight to a new shared `Server.handleDatabaseDDLBypass(sql, liveDBName,
resolveCurrent, w) (handled bool, err error)` helper (`database_ddl.go`,
factored out of `dispatchSimpleQueryViaExecutor`'s previously-inlined
notice/error/tag/`ReadyForQuery` sequence) before the parser ever sees the
statement, so the real catalog-backed `tryHandleDatabaseDDL` DROP branch
wins over the dead executor stub. `CREATE`/`ALTER DATABASE` need no such
pre-check — the parser has no grammar for them at all, so `parser.Parse`
already fails and hits the existing post-parse-failure call to the same
helper. `executeExtendedQueryViaExecutor` gained the identical pre-check
(calling the existing `tryHandleDatabaseOrRoleDDLExtended`, not a new
extended-only helper). When `s.cfg.Catalog` is nil or not a
`databaseRegistry` (embedded/test paths), `handleDatabaseDDLBypass` returns
`handled=false` exactly as `tryHandleDatabaseDDL` always has, so both
callers fall through to the pre-existing `parser.Parse` → `DropCompatStmt`
stub path unchanged — preserving the legacy no-op behaviour those paths
relied on. The parser grammar and the executor's `DropCompatStmt` stub are
both left in place (untouched) as that intentional fallback; they are not
now unreachable in the general case, only pre-empted whenever a real
catalog is plumbed.

Tests: `TestExtendedProtocolDatabaseDDLNotice` (extended-protocol `DROP
DATABASE IF EXISTS` on a nonexistent name → `NoticeResponse` then
`CommandComplete "DROP DATABASE"`), `TestExtendedProtocolRoleDDLErrorDetail`
(`CREATE ROLE pg_*` → `42939` + the fixed pg_-prefix `FieldDetail` text),
and `TestSimpleQueryDropDatabaseActuallyDrops` (real `CREATE DATABASE` →
`DROP DATABASE` round-trip over the simple-query protocol, pinning the bug
fix independent of the extended-protocol plumbing) — all new,
`internal/server/dispatch_extended_ddl_test.go`.

Gates: `go build ./...`/`go vet ./...` clean; `go test ./internal/server/...
./internal/catalog/... ./internal/parser/... ./internal/executor/...
./internal/initdb/...` PASS (no regressions); `scripts/tpch-spotcheck.sh`
PASS; full `scripts/ralph-precommit-test.sh` PASS (pgbench smoke).

No new deferrals for items (1)/(2) — both fully closed. Item (3)
(`compatNoopCommandTag`'s extended-protocol parity) remains open, unchanged
from loop #84, still a materially larger separate gap.
