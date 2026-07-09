# DROP DATABASE guard checks (M0122-0007)

Status: accepted (partial)
Date: 2026-07-09
Supersedes: none

## Problem

`tryHandleDatabaseDDL`'s `databaseDDLDrop` branch (`internal/server/database_ddl.go`,
M0054-0001) was a bare `cat.DropDatabase(name)` map-delete with no guard checks at
all — real PG's `dropdb()` (`postgres/src/backend/commands/dbcommands.c` ~line 1690)
runs several rejection checks before ever touching `pg_database`:

1. `db_istemplate` → `cannot drop a template database` (42809, `ERRCODE_WRONG_OBJECT_TYPE`).
2. `db_id == MyDatabaseId` → `cannot drop the currently open database` (55006,
   `ERRCODE_OBJECT_IN_USE`).
3. `CountOtherDBBackends()` → `database "%s" is being accessed by other users`
   (55006), optionally preceded by `TerminateOtherDBBackends()` under `WITH (FORCE)`.

goopg had none of these — `DROP DATABASE postgres` while connected *to* `postgres`,
or while another session was actively using the target database, silently
succeeded. This is one of the long-repeated "CREATE/DROP DATABASE full DDL" items
under the M0122-0007 fix_plan bucket; full per-database physical storage
isolation (goopg still routes every relation through a single
`catalog.DefaultDBOid`, see the package doc comment in `database_ddl.go`) remains
a separate, much larger architectural item — this slice only adds the three
guard checks above, which need no physical-storage work at all.

## Fix

`databaseDDLDrop` now runs, in PG's own order:

1. `cat.HasDatabase(name)` existence pre-check (moved ahead of the mutating call
   so the guards below can run before anything changes) — preserves the existing
   `IF EXISTS`/`UndefinedDatabase` behavior unchanged.
2. Template rejection: `name == "template0" || name == "template1"`. goopg has no
   `ALTER DATABASE ... IS_TEMPLATE`, so these two names are permanently templates
   — the same hardcoded rule `pg_database`'s own `datistemplate` rendering already
   uses (`catalog.go`'s `pgDatabase.VirtualRows`).
3. Self-drop rejection: `name == liveDBName` (the calling connection's own
   database, already threaded into `tryHandleDatabaseDDL` for the ALTER DATABASE
   SET branch). Checked *before* the busy check below — once it passes,
   `name != liveDBName` is guaranteed, so any backend `CountByDatName(name)` finds
   next is necessarily a connection other than this one. No separate
   self-exclusion bookkeeping was needed (unlike `postinit.c`'s
   `CountOtherDBBackends`, which explicitly skips `MyProcNumber`).
4. Busy check: `s.cfg.Activity.CountByDatName(name) > 0` (nil-safe — some
   embedded/test paths run with no activity registry plumbed, matching the
   pre-existing nil-check pattern for `s.cfg.WAL`). `CountByDatName` already
   existed (`internal/activity/registry.go`, built for M0119-0006's
   `datconnlimit` connect-time check) and is self-inclusive by design — safe here
   precisely because of point 3 above.

`WITH (FORCE)` (`TerminateOtherDBBackends`) is not implemented — goopg has no
signal-driven connection-termination mechanism yet — so `DROP DATABASE ... WITH
(FORCE)` on a busy database still errors the same as the plain form (no
`FORCE`-clause parsing exists either; the clause would currently fall through
`classifyDatabaseDDL`'s prefix match unchanged since it comes after the name).

## Tests

`TestTryHandleDatabaseDDLDropGuards` (`internal/server/database_ddl_test.go`):
template rejection for both `template0`/`template1`, self-drop rejection, busy
rejection via a fake `activity.Backend` registered on the target database name,
and confirmation the drop succeeds once that backend unregisters. Confirmed
non-vacuous via `git stash` on `database_ddl.go` alone (fails: `template0`
drops with no error pre-fix).

## Gates

- `go build ./...` / `go vet ./internal/server/... ./internal/activity/...` clean.
- `go test ./internal/server/... ./internal/activity/...` PASS (full packages,
  no regression).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed
  transactions, all 3 workloads).

## Ownership check (2026-07-09 follow-up, this loop)

Closes the "ownership/permission check before DROP" residual named above.
Real `dropdb()` runs `object_ownercheck(DatabaseRelationId, db_id,
GetUserId())` **immediately after the existence lookup and before every
other guard** (`dbcommands.c` ~line 1720) — failing it raises 42501
`ERRCODE_INSUFFICIENT_PRIVILEGE`, `aclchk.c`'s `"must be owner of database
%s"` (`ACLCHECK_NOT_OWNER`/`OBJECT_DATABASE` case). goopg's database
registry (`catalog.InMemory.databases`, a bare `map[string]bool`) tracked no
owner at all, so this check was structurally impossible before now.

- `catalog.InMemory` gained `databaseOwner map[string]uint32` (parallel to
  the pre-existing `databaseConnLimit` map). `CreateDatabase`/
  `RegisterDatabaseDuringRecovery` both now take an `owner uint32` and record
  it; `DropDatabase`/`UnregisterDatabaseDuringRecovery` clear it. A new
  `DatabaseOwner(name) uint32` getter defaults to `BootstrapSuperuserOID` (10)
  for names with no recorded owner — the bootstrap `postgres`/`template0`/
  `template1` rows, which predate any `CreateDatabase` call and previously
  rendered a hardcoded `"10"` in `pg_database.datdba` anyway (now genuinely
  computed via this getter, same `catalog.go` `pgDatabase.VirtualRows`
  builder).
- `wal.EncodeCreateDatabase`/`DecodeCreateDatabase` gained the owner OID as a
  trailing 4-byte field (format: `kind(1) | nameLen(2) | name | owner(4)`).
  `DecodeCreateDatabase` defaults to `BootstrapSuperuserOID` when the
  trailing 4 bytes are absent, so a WAL stream written before this change
  still replays correctly.
- `tryHandleDatabaseDDL` gained an `actingRole string` parameter — the same
  `connTx.NonSuperuserRole`-or-`""`-for-bootstrap-superuser convention
  `tryRecordTableGrant` already uses (`grant_ddl.go`). Two new `*Server`
  helpers, `resolveActingRoleOID`/`actingRoleIsSuperuser`, resolve it to an
  OID (via `catalog.InMemory.RoleOID`) and check `IsSuperuser`. CREATE
  DATABASE now records the resolved OID as the new database's owner; DROP
  DATABASE rejects with 42501 when `actingRole` neither owns the target nor
  is a superuser — inserted as the very first guard after the existence
  check, ahead of the template/self-drop/busy checks added earlier, matching
  `dropdb()`'s real ordering.
- Both call sites (`dispatch.go`'s simple-query path, `dispatch_extended.go`'s
  extended-query path) now thread `connTx.NonSuperuserRole` through as
  `actingRole`.

**Known limitation carried over, not introduced by this change:** goopg's
RBAC model has no notion of "the login role that never issued `SET ROLE`/`SET
SESSION AUTHORIZATION`" — `connTx.NonSuperuserRole` only diverges from `""`
after one of those statements, so a plain non-superuser login connection is
still treated as the bootstrap superuser for this check, identical to every
other `actingRole`-gated check in this codebase (`tryRecordTableGrant`
included). Not a new gap; not fixed here (would need session-startup role
tracking, out of scope for a DROP DATABASE guard).

Tests: `TestTryHandleDatabaseDDLDropRequiresOwnership`
(`internal/server/database_ddl_test.go`) — non-owner rejection (42501 +
exact message), superuser bypass despite not owning, and owner-drops-own-db
success; `TestDecodeCreateDatabaseDefaultsOwnerForPreM01220007Payload`
(`internal/wal/database_ddl_test.go`) pins the backward-compat decode path.
Gates: `go build ./...`/`go vet ./internal/catalog/... ./internal/wal/...
./internal/server/... ./internal/initdb/...` clean; `go test
./internal/catalog/... ./internal/wal/... ./internal/server/...
./internal/initdb/...` PASS (full packages, no regressions).

## Still open (M0122-0007 bucket)

- Full per-database physical storage isolation (template copy on CREATE, real
  directory removal on DROP) — the architectural item every prior loop in this
  bucket has correctly deferred whole; see the deferral ledger row appended
  alongside this doc.
- `WITH (FORCE)` connection termination (needs a signal/cancel-backend
  mechanism goopg doesn't have yet).
- `REINDEX ... CONCURRENTLY` physical rebuild (separate, pre-existing item,
  unaffected by this change).
