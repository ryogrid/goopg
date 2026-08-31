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

## `WITH (FORCE)` connection termination (2026-07-09 follow-up 2, this loop)

The previous follow-up's "still open" list was wrong about the blocker: goopg
already has a connection-termination mechanism — `backendCancelRegistry`
(`cancel.go`), the process-wide registry behind a peer
`pg_terminate_backend(pid)` call (`ctx.TerminateBackend`, wired in
`dispatch.go`/`dispatch_extended.go`). `DROP DATABASE ... WITH (FORCE)` just
needed to drive that same registry itself instead of only being reachable
from the `pg_terminate_backend` SQL function.

Mirrors `dbcommands.c dropdb()`'s real shape: `if (force)
TerminateOtherDBBackends(db_id)` runs immediately before the
`CountOtherDBBackends` busy check (`procarray.c`). New `dropDatabaseHasForce`
(regex on the trailing `[ [ WITH ] ( FORCE ) ]` clause — `classifyDatabaseDDL`
already strips/ignores every other trailing option, so this only needs to
special-case FORCE) gates two new `*Server` methods called from
`tryHandleDatabaseDDL`'s `databaseDDLDrop` branch, both in
`internal/server/database_ddl.go`:

- `terminateOtherDBBackends(name)` — walks `activity.Registry.Snapshot()`
  for every backend whose `DatName == name` and fires
  `cancelReg.terminateByPID` on each PID, exactly the path
  `pg_terminate_backend(pid)` uses for a peer backend.
- `waitForDatabaseBackendsToDrain(name)` — polls `CountByDatName(name)` for up
  to 5s (50 × 100ms), mirroring `CountOtherDBBackends`'s own retry loop: a
  terminated backend's connection goroutine needs a moment to actually tear
  down and unregister from the activity registry before the immediately
  following busy check runs.

Both are called ONLY when `WITH (FORCE)` is present — a plain `DROP DATABASE`
keeps its pre-existing single immediate `CountByDatName` check unchanged (no
wait), since upstream's *unconditional* 5s retry-wait inside
`CountOtherDBBackends` (which applies even without FORCE, e.g. to give an
already-disconnecting backend time to leave) is a separate, not-yet-ported
behaviour change, deferred below.

**Simplification carried over from `pg_terminate_backend`:** no per-backend
permission check (upstream's `TerminateOtherDBBackends` requires
`has_privs_of_role`/superuser-vs-superuser checks per target PID) — goopg's
existing `pg_terminate_backend` SQL function already skips this
(`expr.go`), and the DROP DATABASE ownership/superuser guard already gates
the whole statement, so this mirrors the codebase's existing scope level
rather than introducing a new gap.

Live-verified against a real `cmd/goopg` binary (throwaway data dir, port
5601): `CREATE DATABASE forcetest`, a background `psql -d forcetest -c
"SELECT pg_sleep(60)"` held it busy. `DROP DATABASE forcetest` (no FORCE)
correctly errored `"is being accessed by other users"`. `DROP DATABASE
forcetest WITH (FORCE)` succeeded in ~100ms; the busy session's client saw
`FATAL: terminating connection due to administrator command` (the same
message a real `pg_terminate_backend` termination produces); `pg_database`
no longer listed `forcetest` afterward. Tests:
`TestTryHandleDatabaseDDLDropForceTerminatesOtherBackends`,
`TestDropDatabaseHasForce` (`internal/server/database_ddl_test.go`). Gates:
`go build ./...` clean; `go vet ./internal/server/...` clean; `go test
./internal/server/...` PASS (full package, no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
workloads).

## Physical-storage-isolation slice 1: real, distinct `pg_database.oid` (2026-07-09 follow-up 3, this loop)

Every prior loop in this bucket correctly deferred "full per-database physical
storage isolation" whole, as an architectural item too large for one loop: it
needs (a) a real distinct OID per database, (b) a `base/<dbOid>` directory
populated by copying the template database's files on CREATE, (c) removing
that directory tree on DROP, and (d) routing every relation lookup through the
connection's actual database instead of the single hardcoded
`catalog.DefaultDBOid`. (d) in particular is a cluster-wide catalog
refactor (goopg's `catalog.InMemory` is one shared table/index namespace for
the whole process, not scoped per database) that cannot land in a single loop.

This loop lands (a) only — the prerequisite every later slice depends on —
without touching (b)/(c)/(d). Before this slice, `CreateDatabase` never
allocated an OID at all: `pg_database.VirtualRows` rendered the SAME hardcoded
`"16384"` placeholder for every non-template database, so two databases
created via `CREATE DATABASE foo`/`CREATE DATABASE bar` were indistinguishable
by `pg_database.oid` — a real correctness bug independent of physical storage
(`pg_database.oid` is a primary key in upstream PG), and a hard blocker for
slice 2 (no directory name to allocate without a real, unique OID).

**What changed:**

- `catalog.InMemory.CreateDatabase(name, owner)` now allocates a real OID from
  the same cluster-wide `nextOID` counter every other catalog object uses
  (mirrors real PG's single shared OID space) and returns it —
  `(uint32, error)` instead of just `error`. Stored in a new `databaseOid
  map[string]uint32` field.
- New `DatabaseOid(name) uint32` getter returns the stored oid, or 0 ("no
  override" sentinel) for a name never registered through `CreateDatabase` —
  i.e. the three bootstrap rows (`postgres`/`template0`/`template1`), which
  predate any `CreateDatabase` call.
- `pgDatabase.VirtualRows`'s oid-selection switch gained a `default` arm: any
  `datname` that isn't `template1`/`template0` now renders
  `DatabaseOid(n)` when non-zero, instead of unconditionally falling back to
  the old `"16384"` placeholder.
- **Deliberately unchanged: the live `"postgres"` row's displayed oid.**
  `"postgres"` is seeded at bootstrap, never through `CreateDatabase`, so
  `DatabaseOid("postgres")` is 0 and the placeholder stands — confirmed
  necessary by the pre-existing comment right above this switch: `CREATE
  SUBSCRIPTION`'s `subdbid` and the `datacl` heap resync both key off the
  `"16384"` placeholder for the one live/connected database, and a past loop
  already found that changing it broke the pg_dump subscription round-trip.
  Since v0 can only ever actually connect to `"postgres"` (no per-database
  routing yet — that's slice 4/(d) above), only a name nobody can connect to
  is affected by this slice, which is why it's safe to land in isolation.
- `wal.EncodeCreateDatabase`/`DecodeCreateDatabase` gained a fourth trailing
  4-byte `oid` field (same backward-compat pattern the M0122-0007 `owner`
  suffix already used): a pre-slice-1 WAL payload with no oid suffix decodes
  oid=0, matching "no override" exactly.
- `RegisterDatabaseDuringRecovery(name, owner, oid)` (the WAL-replay driver's
  entry point, `internal/initdb/database_ddl_recovery.go`) now also advances
  `nextOID` past a recovered non-zero oid — mirrors `advanceNextOIDLocked`'s
  pattern for every other recovered OID, so a later `CREATE TABLE`/`CREATE
  DATABASE` in the same process can never reallocate an oid a crash-recovered
  database already owns.
- The one existing rollback path that re-creates a database entry after a WAL
  append failure (`databaseDDLDrop`'s "re-create on DROP's own WAL-append
  failure" branch) keeps calling `CreateDatabase` (allocating a fresh oid)
  rather than restoring the exact dropped oid — an accepted simplification
  for this exceedingly rare double-failure edge case, since no `DropDatabase`
  WAL record was ever durably written in that path either.

Tests: `TestCreateDatabaseAllocatesDistinctDisplayedOid`,
`TestRegisterDatabaseDuringRecoveryAdvancesNextOID`
(`internal/catalog/database_test.go`);
`TestDecodeCreateDatabaseDefaultsOidForPreSlice1Payload`
(`internal/wal/database_ddl_test.go`, alongside the updated pre-existing
owner-defaulting test). Gates: `go build ./...`/`go vet ./...` clean; `go test
-count=1 ./internal/catalog/... ./internal/wal/... ./internal/server/...
./internal/initdb/...` PASS (full packages, no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
workloads). Also spot-checked the exact regression the "postgres" comment
warns about by re-running `TestPort_PgDumpDatabaseConfigSet`,
`TestPort_PgDumpRoleConfigSet`, `TestPort_PgDumpallGlobalsOnly` (all PASS) and
`TestPort_Subscription001RepChanges` (FAILs — confirmed via `git stash` to
fail identically at HEAD before this change, an unrelated pre-existing
failure, not a regression introduced here).

## Physical-storage-isolation slice 2: `base/<dbOid>` directory on CREATE DATABASE (2026-07-09 follow-up 4)

Lands the directory-allocation half of slice 2 named above. **The
template-copy half is explicitly NOT implemented and is re-scoped below** —
see "What this does NOT do" for why.

**What changed:**

- `internal/initdb.createPerDatabaseScaffolding` (Init-time-only, unexported)
  is now `CreatePerDatabaseScaffolding` (exported): `os.MkdirAll(base/<oid>)`
  + write `base/<oid>/PG_VERSION`. Both operations are naturally idempotent
  (`MkdirAll` on an existing dir, `WriteFile` truncate-and-rewrite), so the
  function is safe to call repeatedly for the same oid.
- `Server.createDatabasePhysicalDirectory(oid)` (new,
  `internal/server/database_ddl.go`) calls it using `s.cfg.Pool.Manager().
  DataDir()` — a nil `Pool` or empty `DataDir` (embedded/test contexts) is a
  silent no-op, matching how `execCreateTablespace`/
  `relocateRelationPhysicalFile` skip cluster-filesystem effects in the same
  contexts.
- `tryHandleDatabaseDDL`'s `databaseDDLCreate` branch calls it **BEFORE** the
  WAL append (not after) — mirrors `relocateRelationPhysicalFile`'s
  crash-safety ordering: the physical artifact must exist before the
  operation that makes it durable/authoritative runs, not after. A directory-
  creation failure rolls back the catalog's oid allocation exactly like a
  WAL-append failure already did (`cat.DropDatabase(name)`); a WAL-append
  failure now ALSO removes the just-created directory via the new
  `Server.removeDatabasePhysicalDirectory(oid)` (best-effort — like
  `relocateRelationPhysicalFileCleanupOld`, a failure here only leaves a
  harmless orphaned empty directory).
- **Restart durability**: `replayDatabaseDDLRecords`
  (`internal/initdb/database_ddl_recovery.go`) gained a `dataDir string`
  parameter; for every replayed `RecordKindCreateDatabase` it also calls
  `CreatePerDatabaseScaffolding(dataDir, oid)`. This matters even though the
  live create-path already created the directory before its WAL record went
  durable, because the directory itself has no independent WAL protection —
  a crash could in principle lose the `mkdir` while the WAL record survives
  (analogous to why upstream's `CreateDirAndVersionFile` tolerates `EEXIST`
  during redo). Idempotent recreation on every replay closes that gap for
  free. DROP DATABASE replay did **not** remove the directory at the time
  this section was written — closed by slice 3, below.

**What this does NOT do (re-scoping slice 2's original template-copy plan):**

The original slice-2 plan (see the "Still open" note this section replaces)
assumed a non-`template0` `CREATE DATABASE ... TEMPLATE x` could "walk the
template database's relations copying each main-fork file". That assumption
doesn't hold: goopg still has **one shared table/index namespace for the
whole process** (`catalog.InMemory.tables`/`indexes` have no per-database
key), so there is no way to enumerate "template1's relations" as distinct
from "postgres's relations" — every relation ever created lives in the same
map, keyed only by name, regardless of which database a connection was
attached to when it ran `CREATE TABLE`. Attempting a template copy today
would either copy nothing (vacuous, since `template1` is never populated
through any real path) or copy the ONE shared namespace's relations into an
oid nothing will ever route I/O through (since `catalog.DefaultDBOid` is
still hardcoded everywhere) — neither is a meaningful step forward. The
template-copy mechanism (upstream's `CreateDatabaseUsingFileCopy`/`copydir`,
per `postgres/src/backend/commands/dbcommands.c`) genuinely needs slice 4's
per-database catalog namespace to land first; it is not an independent
sibling slice the way the ledger's original framing assumed. `TEMPLATE`
options on `CREATE DATABASE` remain silently ignored, unchanged from before
this loop.

Tests: `TestTryHandleDatabaseDDLCreateCreatesPhysicalDirectory` (confirmed
non-vacuous via `git stash` on `database_ddl.go`/`initdb.go`/
`database_ddl_recovery.go`/`open.go` together — fails "PG_VERSION missing"
pre-fix), `TestTryHandleDatabaseDDLCreateNoPoolIsNoop`
(`internal/server/database_ddl_test.go`);
`TestDatabaseDDLRecoveryRecreatesMissingDatabaseDirectory` (new — deletes the
directory between two `Open` calls and confirms replay recreates it), plus an
assertion added to the pre-existing `TestDatabaseDDLRecoveryReplaysCreate`
(`internal/initdb/database_ddl_recovery_test.go`). Live-verified against a
real `cmd/goopg` binary (port 65498): `CREATE DATABASE slicetest` produced
`base/16403/PG_VERSION` matching the connection's `pg_database.oid`; deleting
that directory and restarting the server recreated it via WAL replay before
any client reconnected. Gates: `go build ./...` clean; `go vet
./internal/initdb/... ./internal/server/... ./internal/catalog/...` clean;
`go test ./internal/initdb/... ./internal/server/... ./internal/catalog/...
./internal/wal/...` PASS (full packages, no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
workloads).

## Physical-storage-isolation slice 3: `base/<dbOid>` directory removal on DROP DATABASE (2026-07-09 follow-up 5)

Closes the gap slice 2 left open: `base/<dbOid>` was created on `CREATE
DATABASE` but never removed on `DROP DATABASE`, so every dropped database
left a permanently orphaned empty directory.

**What changed:**

- `internal/initdb.RemovePerDatabaseScaffolding(dataDir, dbOID)` (new,
  `internal/initdb/initdb.go`) — the symmetric counterpart to
  `CreatePerDatabaseScaffolding`: `os.RemoveAll(base/<oid>)`. Idempotent
  (`RemoveAll` on an already-missing directory is a no-op), so it is safe to
  call from both the live drop path and WAL replay, including replaying the
  same record twice.
- `Server.removeDatabasePhysicalDirectory` (`internal/server/database_ddl.go`)
  — previously only used to roll back a `CREATE DATABASE` whose WAL append
  failed — now also calls the new `initdb` function (was inlining its own
  `os.RemoveAll(filepath.Join(...))`, now delegates instead of duplicating the
  path construction) and gained a second caller: the end of
  `tryHandleDatabaseDDL`'s `databaseDDLDrop` branch, once the drop itself is
  durable (after the `WAL.Append` succeeds, or immediately if no WAL is
  configured). The database's oid is captured via the existing
  `catalog.InMemory.DatabaseOid(name)` (added to the `databaseRegistry`
  interface) **before** `cat.DropDatabase(name)` runs, since `DropDatabase`
  deletes the name→oid mapping along with the rest of the catalog entry.
  Ordering mirrors slice 2's create-side crash-safety discipline in reverse:
  slice 2 creates the directory *before* the operation that commits to it
  (WAL append); slice 3 removes the directory only *after* the operation
  that commits to the drop, so a WAL-append failure (which re-creates the
  catalog entry with a fresh oid) never removes a directory a still-live
  database might resolve to.
- **Restart durability**: `replayDatabaseDDLRecords`
  (`internal/initdb/database_ddl_recovery.go`) now removes `base/<oid>` for
  every replayed `RecordKindDropDatabase`, mirroring the create-side
  recreation added in slice 2. The `DropDatabase` WAL record only carries the
  database name (no oid), so the oid is read from the registry via the new
  `databaseRegistryRecovery.DatabaseOid` method immediately before
  `UnregisterDatabaseDuringRecovery` erases it — correct even when a CREATE
  and its matching DROP replay within the same pass (the oid is still live
  in the registry at the point the DROP record is processed).

Tests: `TestTryHandleDatabaseDDLDropRemovesPhysicalDirectory`
(`internal/server/database_ddl_test.go`, confirmed non-vacuous via `git
stash` on `database_ddl.go` alone — fails "still present after DROP DATABASE"
pre-fix); `TestDatabaseDDLRecoveryReplaysDropAfterCreate` extended with a
`base/16402` absence assertion (`internal/initdb/database_ddl_recovery_test.go`,
confirmed non-vacuous via `git stash` on `database_ddl_recovery.go`/
`initdb.go` together). Live-verified against a real `cmd/goopg` binary (port
65499): `CREATE DATABASE slice3test` produced `base/16403/`;
`DROP DATABASE slice3test` removed it immediately; a second
create/drop/restart cycle confirmed the directory stays gone across a real
server restart (WAL replay does not resurrect a dropped database's
directory). Gates: `go build ./...` clean; `go vet ./internal/initdb/...
./internal/server/... ./internal/catalog/...` clean; `go test
./internal/initdb/... ./internal/server/... ./internal/catalog/...
./internal/wal/...` PASS (full packages, no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
workloads).

## Still open (M0122-0007 bucket)

- Physical-storage-isolation slice 4: routing every relation lookup through
  the connection's actual database (the full cluster-wide catalog-scoping
  refactor, `catalog.InMemory` gaining a per-database table/index namespace)
  — the prerequisite the template-copy mechanism above actually needs. See
  the deferral ledger row appended alongside this doc for the concrete
  resume point.
- ~~`REINDEX ... CONCURRENTLY` physical rebuild~~ — landed 2026-07-09 (see
  `0122-0007-reindex-physical-rebuild.md`'s "shadow-file build-then-swap"
  follow-up), separate from this doc's own work. Its own residual gap (no
  second validation scan, so a write racing the shadow build's heap scan may
  not appear in the rebuilt index) is tracked there, not here.
