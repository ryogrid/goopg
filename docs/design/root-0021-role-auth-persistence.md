# root-0021 — Role/auth persistence (pg_authid heap base + WAL tail)

## Problem

goopg's roles were in-memory only: `CREATE ROLE`/`CREATE USER` registered the
name in two registries (`Server.roles`, `catalog.InMemory.roles`) and
**discarded the `PASSWORD`**; there was no `ALTER ROLE` handler at all; and
nothing survived a restart. The pg_authid heap (`global/1260`) was written
once by initdb and never read back — the only persistent credential source
was a hand-maintained `pg_auth` file. The server also ignored the data
directory's `pg_hba.conf` unless `--hba` was passed, so operator auth edits
silently never took effect.

## Upstream model

PostgreSQL's durable store for roles is the **pg_authid shared catalog heap**;
the WAL only carries the crash-recovery tail for those heap pages
(`postgres/src/backend/commands/user.c` CreateRole/AlterRole write the heap;
recovery replays onto it). `PASSWORD 'x'` is shadowed to a SCRAM-SHA-256
verifier by `encrypt_password` (`postgres/src/backend/libpq/crypt.c`) under
the default `password_encryption`; pre-computed `SCRAM-SHA-256$…`/`md5…`
secrets are stored verbatim. `pg_hba.conf` is always read from the data
directory, first-match-wins.

## Design — the same base+tail split

**Base store: the pg_authid heap file** (`executor.SyncPgAuthidFile`,
`internal/executor/pg_authid_sync.go`). Every role DDL atomically rebuilds
`global/1260` (temp file + rename + fsync) from the catalog role registry:
the bootstrap superuser row (OID 10, `--pwfile` verifier preserved from the
existing file), one bootstrap-shaped row per registered user role
(attributes + verifier from the `RoleAttrs` sidecar), and the 16 predefined
`pg_*` rows — the same `Form_pg_authid` byte shapes initdb writes (pinned by
`pg_authid_heap_row_test.go`), so a PG18 standby keeps reading it. No
buffer-pool interaction: nothing reads `global/1260` through the pool at
runtime (`pg_roles` is a virtual table), so the file-level swap is coherent.

**Tail: logical WAL records** `RecordKindRoleState(67)` /
`RecordKindDropRole(68)` (`internal/wal/recovery.go`), appended by every role
DDL before the heap rewrite — covering a crash between the WAL append and the
file rename. Payload carries name, OID (stable across restarts), LOGIN/
SUPERUSER flags, credential type and verifier.

**Startup** (`internal/initdb/open.go`): `LoadRolesFromAuthidHeap`
(`pg_authid_load.go`) decodes the heap (header-driven,
`DecodeRowIntoMctxPGTuple`) and re-registers user roles with their persisted
OIDs + attributes (`RegisterRoleWithOID`/`SetRoleAttrs`; the bootstrap
verifier lands in the sidecar under `postgres`); then `replayRoleDDLRecords`
(`role_ddl_recovery.go`) applies the WAL tail last-record-wins. Because the
heap is rewritten on every DDL, the base alone survives checkpoint-driven WAL
segment pruning — the known limitation of pure logical-record persistence.

## Two role-DDL paths (sibling-path hazard)

- **CREATE/ALTER ROLE** cannot be parsed, so they hit the server-side
  intercept (`internal/server/role_ddl.go`, `tryHandleRoleDDL`): parses
  `PASSWORD` (from the ORIGINAL case-preserved SQL; `normalizeCompatSQL`
  lower-cases), `LOGIN/NOLOGIN`, `SUPERUSER/NOSUPERUSER`; CREATE USER
  defaults LOGIN like PG; plaintext shadows via `auth.NewSCRAMSecret`;
  updates registries + `MapUserStore` + WAL + heap.
- **DROP ROLE parses** as a generic DropStmt and lands in the executor's
  `execDropCompat` role arm (`operators_ddl.go`), which now appends the drop
  record, rewrites the heap (`ctx.DataDir`), and notifies the server through
  the new `Context.OnRoleDropped` hook (set in dispatch) so `Server.roles`
  and the UserStore stay in sync. This asymmetry is why `SyncPgAuthidFile`
  lives in `executor` (initdb→executor import direction).

## Auth wiring

`cmd/goopg/main.go` now always builds a mutable `auth.MapUserStore`: seeded
from the restored catalog roles (including the bootstrap `--pwfile`
verifier — password auth for `postgres` now survives restart with no
hand-written file), then overlaid with the optional `pg_auth` file (operator
override wins). The existing SCRAM/md5/cleartext exchange
(`internal/auth/exchange.go`) needed zero changes. `Server.New` seeds its
connection-time role set from the catalog. `goopg start` resolves HBA
PG-style: `--hba` flag > `<datadir>/pg_hba.conf` > built-in loopback trust.
The shipped default stays trust, so existing setups (including the `wp/`
WordPress instance) are unaffected.

## Tests

- `internal/wal/role_ddl_test.go` — record round trips + truncation guards.
- `internal/initdb/role_ddl_recovery_test.go` — heap sync/load round trip
  across real `Open` cycles; bootstrap `--pwfile` verifier restore; WAL-tail
  replay incl. drop-wins ordering.
- `internal/testport/role_auth_durability_test.go` —
  `TestPort_CreateRoleSurvivesRestart`: CREATE ROLE … PASSWORD survives a
  clean stop→start with a **real SCRAM-SHA-256 handshake** (per-role
  scram rule first in pg_hba.conf, first-match-wins); NOLOGIN attribute
  persists; DROP ROLE stays dropped; ALTER ROLE PASSWORD rotation is durable
  (old password rejected, new accepted post-restart).

## Known bounds

- `SET`/`RESET` forms keep the legacy compat no-op path (hasAttrs=false in
  `roleNameFromAlter`). `RENAME TO` is now handled — see the follow-up below.
- Membership (`GRANT role TO role`), `CREATEDB`/`REPLICATION`/`VALID UNTIL`
  attributes remain accept-and-ignore, as before.
- The `pg_roles` virtual view reports real `rolsuper`/`rolcanlogin` for roles
  with recorded attributes; roles registered through other paths keep the
  historical `f`/`t` defaults.

## Follow-up: `ALTER ROLE/USER … RENAME TO` restart persistence

Closes the "Known bounds" RENAME TO gap above. `renameRole`
(`internal/server/role_ddl.go`) intercepts `ALTER ROLE/USER <name> RENAME TO
<newname>` ahead of the attribute-form parse (`roleRenameFromAlter`), mirrors
PostgreSQL's `RenameRole` (`postgres/src/backend/commands/user.c`) checks —
role-exists (42704), reserved `pg_`-prefix on the new name (42939),
new-name-already-exists/`postgres` (42710) — then re-keys three places
together: the catalog role registry (`catalog.InMemory.RenameRole`, new
method beside `RegisterRole`/`UnregisterRole`, preserves the OID so
`pg_policy.polroles`/ownership references stay valid), `Server.roles`, and
the live `auth.MapUserStore` credential. A new `RecordKindAlterRoleRename`
(kind 72, `internal/wal/recovery.go`) is the WAL tail entry, replayed by
`internal/initdb/role_ddl_recovery.go` after physical redo (a no-op, same as
`RecordKindRoleState`/`RecordKindDropRole` — role DDL never touches the
pg_authid heap directly at runtime, only the periodic full-registry
`SyncPgAuthidFile` rewrite does).

Renaming the bootstrap superuser (`postgres`) is rejected
(`FeatureNotSupported`, not persistence-related) — its name is hardcoded in
several places (`RoleOID`, initdb's pg_authid seeding), a structural change
out of scope here.

Not modelled (unchanged deferral): PG's "session/current user cannot be
renamed" guard needs per-connection session-role context this SQL-string-level
handler doesn't have, and the superuser-may-only-rename-superuser privilege
check is accept-and-ignore like every other role-DDL privilege check in this
handler.

Tests: `internal/server/role_ddl_rename_test.go` (parsing +
`tryHandleRoleDDL` success/error paths, `catalog.InMemory.RenameRole` OID
preservation) + case (e) added to `TestPort_CreateRoleSurvivesRestart`
(`internal/testport/role_auth_durability_test.go`: rename survives a real
cluster restart, old name gone, attributes carried to the new name).
