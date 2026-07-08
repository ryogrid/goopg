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
- Membership (`GRANT role TO role`) remains accept-and-ignore, as before.
  `CREATEDB`/`CREATEROLE`/`REPLICATION`/`BYPASSRLS`/`CONNECTION LIMIT`/`VALID
  UNTIL` are now modelled — see the follow-up below.
- The `pg_roles` virtual view reports real `rolsuper`/`rolcanlogin` for roles
  with recorded attributes; roles registered through other paths keep the
  historical `f`/`t` defaults. `pg_roles` itself is NOT extended with the new
  attribute columns (it only ever exposed 4 of PG's ~15 `pg_roles` columns,
  a pre-existing intentionally-minimal shape for HammerDB probing — out of
  scope here); `pg_authid`, which `pg_dump`/`pg_dumpall` actually query for
  these attributes, is fully extended (see below).

## Follow-up: `CREATEDB`/`CREATEROLE`/`REPLICATION`/`BYPASSRLS`/`CONNECTION
LIMIT`/`VALID UNTIL` now modelled (DU-002 slice 439 follow-up)

Closes the "Known bounds" accept-and-ignore gap above (deferral-ledger item 1
of the loop-#107 triage shortlist). `catalog.RoleAttrs` gained `CreateDB`,
`CreateRole`, `Replication`, `BypassRLS` (bool), `ConnLimit` (int32, PG
default `-1` — the Go zero value `0` is a DIFFERENT, valid PG setting, so
every "fresh attrs" construction site in `role_ddl.go` sets `ConnLimit: -1`
explicitly, mirroring how `CanLogin`'s default already varies by call site),
and `ValidUntil` (string, the raw `VALID UNTIL '<literal>'` text — goopg
stores it verbatim and never evaluates it; no password-expiry enforcement).

`applyRoleAttrOptions` (`internal/server/role_ddl.go`) recognises
`[NO]CREATEDB`/`[NO]CREATEROLE`/`[NO]REPLICATION`/`[NO]BYPASSRLS` (same
boolean-toggle shape as the pre-existing `LOGIN`/`SUPERUSER` handling),
`CONNECTION LIMIT <n>` (new `extractRoleConnLimit`, allows negative — PG's
own `-1` "no limit" sentinel), and `VALID UNTIL '<literal>'` / `VALID UNTIL
NULL` (new `extractRoleValidUntil`, mirrors `extractRolePassword`'s
case-preserving literal extraction from the ORIGINAL sql, since
`normalizeCompatSQL` lower-cases the whole statement).

Persistence follows the same heap-base/WAL-tail split as every other
attribute in this design, with one exception:
- `wal.RoleStatePayload` gained 4 new flag bits (`CreateDB`/`CreateRole`/
  `Replication`/`BypassRLS`), a 4-byte `ConnLimit` field, and a `str16`
  `ValidUntil` field — the WAL crash-tail (`persistRoleState` →
  `EncodeRoleState`/`DecodeRoleState`, replayed by
  `internal/initdb/role_ddl_recovery.go`) carries ALL six new fields.
- `executor.SyncPgAuthidFile`/`ReadPgAuthidRows`/`initdb.LoadRolesFromAuthidHeap`
  (the durable base, `global/1260`) carry the four bools + `ConnLimit`
  through `buildAuthidUserRow`'s expanded signature. `ValidUntil` originally
  did **NOT** round-trip through the heap column (it stayed unconditionally
  `NULL`, previously it was, oddly, Unix epoch `1970-01-01` for every
  non-predefined row — an unrelated latent bug fixed in the same pass since
  it's the exact field this change touches) — **closed by the "Follow-up:
  `VALID UNTIL` heap round-trip" section below.**

`catalog.go`'s `pg_authid` `VirtualRows` (the live SQL-visible table
`pg_dump`/`pg_dumpall` actually query — not the heap file, see the file's own
header comment) now renders `rolcreaterole`/`rolcreatedb`/`rolreplication`/
`rolbypassrls`/`rolconnlimit`/`rolvaliduntil` from the `RoleAttrs` sidecar
instead of the old hardcoded `'f'`/`'f'`/`'f'`/`'f'`/`'-1'`/`NULL` literals;
the 16 predefined `pg_*` roles keep reporting PG's own predefined-role
defaults (`ConnLimit: -1` passed explicitly, not the zero-value struct, for
the same reason as above).

Tests: `internal/server/role_ddl_attrs_test.go` (parse → `RoleAttrs` sidecar
→ `pg_authid` `VirtualRows`, incl. `ALTER ROLE` negation/override and
unspecified-attribute-survives-ALTER semantics); `internal/wal/role_ddl_test.go`
(round-trip incl. the new fields); `internal/initdb/role_ddl_recovery_test.go`
(`TestPgAuthidSyncLoadRoundTrip` extended to assert the four bools + ConnLimit
survive a real heap-file round trip across `Open` cycles).

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

## Follow-up: `VALID UNTIL` heap round-trip (DU-002 slice 439 triage item 1
follow-up)

Closes the residual named above — `rolvaliduntil` was unconditionally
written as `NULL` to the pg_authid heap file, so a `VALID UNTIL` value set
by `ALTER ROLE` reverted to no-expiration after a clean restart once the WAL
segment carrying its crash-tail record was pruned by a checkpoint.

The blocker the original row cited — no PG-timestamp-literal parser exists —
turned out to be already solved elsewhere: `parseCopyTimestamp`
(`internal/executor/copy_text.go`) already parses every layout PG's own
`timestamptz_in` commonly produces (used today for `COPY` text input and
generic `timestamptz`-column literal coercion, `codec.go`'s `encodeValuePG`
switch). `buildAuthidUserRow` (`internal/executor/pg_authid_sync.go`) now
parses `validUntil` with it and, on success, writes a real `timestamptz`
column value (`NewTimeDatum`) instead of `NullDatum`; `ReadPgAuthidRows`
decodes it back via a new `formatValidUntilText` helper that renders the
same `"YYYY-MM-DD HH:MM:SS[.ffffff]+00"` text shape
`extractRoleValidUntil` (`internal/server/role_ddl.go`) captures from the
original `VALID UNTIL '...'` literal (goopg stores the column as UTC
internally, so the zone suffix is always `+00`).

**Deliberately still deferred, narrower than the original gap:** PG's
`infinity`/`-infinity` timestamptz sentinels (and any other literal
`parseCopyTimestamp` can't parse) still fall back to `NULL` in the heap
file — goopg's `timestamptz` type has no infinity representation anywhere
in the engine yet (encode/decode, comparison, arithmetic), so teaching just
this one column about it would be an inconsistent, narrow carve-out. A
`VALID UNTIL 'infinity'` role still round-trips correctly through the WAL
crash-tail + live `RoleAttrs` sidecar (unchanged), only the heap-file base
loses it across a checkpoint-pruned clean restart — see the deferral
ledger for the resume point (full engine-wide timestamptz infinity support).

Tests: `internal/initdb/role_ddl_recovery_test.go`'s
`TestPgAuthidSyncLoadRoundTrip` extended to set and assert a `ValidUntil`
value survives a real heap-file round trip across `Open` cycles (previously
explicitly NOT asserted, per the stale comment this loop removed).

## Follow-up: `scram_iterations` GUC wired into password hashing (M0122-0008)

`scram_iterations` (`internal/config/defaults.go`, `ScopeSession`,
`ContextUserset`) was registered as a GUC but never actually read anywhere
— `CREATE`/`ALTER ROLE ... PASSWORD 'plain'` always hashed through
`auth.NewSCRAMSecret`, which hardcoded `scramDefaultIterations` (4096, PG's
`SCRAM_SHA_256_DEFAULT_ITERATIONS`). Upstream's `encrypt_password`
(`postgres/src/backend/commands/user.c`) and `scram_build_secret`
(`postgres/src/common/scram-common.c`) read the live GUC instead, so `SET
scram_iterations = N; ALTER ROLE ... PASSWORD ...` changes the PBKDF2 cost
of the newly-derived verifier — a real, user-visible security knob that
goopg silently ignored.

Fixed: `auth.NewSCRAMSecretWithIterations(password, iterations)`
(`internal/auth/scram.go`) is a new sibling of `NewSCRAMSecret` taking an
explicit count (non-positive falls back to the same default, so existing
callers — `initdb`'s bootstrap superuser, the mock-auth channel-binding
path, tests — are unaffected by construction, not by convention).
`applyRoleAttrOptions` (`internal/server/role_ddl.go`) now takes the same
`currentGUCResolver` its two callers (`tryHandleRoleDDL`'s CREATE/ALTER ROLE
attribute-clause branches) already had in scope for the unrelated `SET ...
FROM CURRENT` path, and a new `resolveScramIterations` helper reads
`scram_iterations` off it (falls back to 0 → `NewSCRAMSecretWithIterations`'s
own default whenever no session/GUC is available, e.g. `initdb`'s bootstrap
call which doesn't go through this path at all). No change needed on the
authentication/verification side: `parse_scram_secret`'s Go port
(`ParseSCRAMSecret`) already reads the iteration count back out of the
stored `SCRAM-SHA-256$<iterations>:...` verifier and `scramState`'s
server-first-message (`scram.go:326`) already renders `s.secret.Iterations`
from the parsed value, not a constant — the read side was already correct,
only the write side was pinned to the default.

Not modelled (unchanged from before this fix): `initdb`'s bootstrap
superuser secret always uses the compiled-in default regardless of any
`postgresql.conf` `scram_iterations` setting present at `initdb` time —
matches upstream's own bootstrap behavior (the value used to hash the
bootstrap password is fixed at initdb time, before GUC processing of a
user-supplied config file would apply), so no gap.

Tests: `internal/server/role_ddl_scram_iterations_test.go`
(`TestCreateAlterRolePasswordHonorsScramIterationsGUC`) — CREATE ROLE with a
nil resolver derives 4096 iterations; CREATE ROLE and ALTER ROLE ...
PASSWORD with a live resolver reporting `scram_iterations = "1024"`/`"42"`
derive exactly that count (parsed back out of the stored verifier via
`auth.ParseSCRAMSecret`). Confirmed non-vacuous via `git stash` on
`scram.go`/`role_ddl.go` (fails, both roles report 4096 regardless of the
live GUC).
