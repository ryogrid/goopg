# Object-privilege ACL grantor tracking (`pg_class.relacl` and siblings)

Status: accepted
Milestone: M0119-0004 (pg_dump fidelity, ACLHEAP follow-up)

## Problem

Every `aclitem` PostgreSQL stores (`grantee=privs/grantor`) carries a
**grantor** — the role that actually executed the `GRANT` — as its trailing
component. goopg's shared OID-keyed ACL store (`tableACLs`, backing
`pg_class.relacl` and, via the same `relaclTextLockedFor` renderer,
`pg_namespace.nspacl`/`pg_proc.proacl`/`pg_type.typacl`/`pg_database.datacl`/
`pg_parameter_acl`/`pg_foreign_server.srvacl`/`pg_foreign_data_wrapper.fdwacl`)
had no grantor dimension at all: every rendered aclitem hardcoded the trailing
component to the literal string `"postgres"` (the object owner), regardless of
who actually ran the `GRANT`.

This is a real, reachable divergence from PostgreSQL, not merely a cosmetic
one. Verified against a scratch PostgreSQL 18.3 instance:

```sql
CREATE ROLE bob; CREATE ROLE charlie;
CREATE TABLE t (id int);
GRANT SELECT ON t TO bob WITH GRANT OPTION;
SET SESSION AUTHORIZATION bob;
GRANT SELECT ON t TO charlie;
RESET SESSION AUTHORIZATION;
SELECT relacl FROM pg_class WHERE relname = 't';
--  {postgres=arwdDxtm/postgres,bob=r*/postgres,charlie=r/bob}
```

`charlie`'s aclitem grantor is `bob`, not the table owner. `pg_dump`'s
`buildACLCommands` (`postgres/src/bin/pg_dump/dumputils.c:261-333`) parses this
grantor out of the aclitem string **client-side** and, whenever it differs from
the object owner, wraps that grant in:

```sql
SET SESSION AUTHORIZATION bob;
GRANT SELECT ON TABLE public.t TO charlie;
RESET SESSION AUTHORIZATION;
```

Since goopg always rendered `/postgres`, this wrap was structurally
unreachable — a dump-fidelity gap for any table whose ACL was built up through
a `WITH GRANT OPTION` delegation chain (a real, if not everyday, PostgreSQL
usage pattern; catalogued as a probe candidate in `.ralph/deferral_ledger.md`'s
DU-002 slice 436 row).

A second, narrower question this loop resolved empirically: does the SQL
standard `GRANTED BY role_specification` clause on an object-privilege
`GRANT`/`REVOKE` (as opposed to role-membership `GRANT role TO member GRANTED
BY grantor`, a separate, already-implemented mechanism) let a caller name an
*arbitrary* grantor? No — `postgres/src/backend/catalog/aclchk.c:394-412`
(`ExecuteGrantStmt`) rejects any named grantor other than `GetUserId()` with
`ERRCODE_FEATURE_NOT_SUPPORTED` ("grantor must be current user"); the clause
exists purely for SQL-standard-compatibility syntax acceptance. Confirmed live:
`GRANT SELECT ON TABLE t TO role1 GRANTED BY role2;` parses but raises that
exact error unless `role2` is the caller's own current role. goopg's own
existing `GRANTED BY` acceptance tests for `TYPE`/`DATABASE`/`PARAMETER` ACLs
and role-membership grants only ever exercised `GRANTED BY postgres` while
connected as the bootstrap superuser — silently correct-by-coincidence with
this restriction, but the restriction itself was never enforced.

## Fix

### Grantor storage (`internal/catalog/catalog.go`)

New `tableACLGrantor map[uint32]map[string]string` field on `InMemory`, keyed
identically to `tableACLs` (relOID → lower-cased grantee role name → grantor
role name). A new `GrantTablePrivilegeAs(relOID, role, priv string,
withGrantOption bool, grantor string)` method is the grantor-aware sibling of
the pre-existing `GrantTablePrivilegeWithGrantOption`, which now becomes a
thin wrapper (`GrantTablePrivilegeAs(..., aclOwnerRole)`) — every existing
caller that never had a grantor concept (TYPE/DATABASE/PARAMETER ACL,
implicit-default seeding) is unaffected byte-for-byte. Every `GRANT` to a
grantee re-stamps that grantee's grantor (mirroring PostgreSQL's `aclupdate`,
which updates an existing aclitem's grantor field to whoever issued the latest
grant); `RevokeTablePrivilege`/`DropTableACL` clean up the grantor entry
alongside the privilege entry so a later re-grant never resurrects a stale
grantor. Mixed-case grantor names reuse the pre-existing `roleACLDisplay`
case-preservation map (DU-002 slice 337) and `aclQuoteName` quoting (slice
336) — the same treatment grantee names already receive.

`relaclTextLockedFor` (the shared renderer behind `pg_class.relacl` and every
sibling ACL text projection listed above) now looks up the real grantor per
grantee instead of hardcoding `/postgres`, falling back to the owner when no
explicit grantor was ever recorded (the common, unchanged case). This one
change point covers every object kind sharing the renderer.

Column-level ACLs (`pg_attribute.attacl`, `AttrACLText`) are a structurally
separate store (`attrACLs`, no owner/PUBLIC default entry) and are **not**
covered by this fix — still hardcoded to the owner. Recorded as a deferred
sibling gap (ledger row), not fixed here, to keep this slice bounded to the
`tableACLs`-backed object kinds.

### Grantor stamping (`internal/server/grant_ddl.go`, `query.go`)

`tryRecordTableGrant` (the best-effort string-level recorder backing `TABLE`/
`SEQUENCE`/`SCHEMA`/`FUNCTION`/`PROCEDURE`/`ROUTINE`/`FOREIGN SERVER`/`FOREIGN
DATA WRAPPER` grants — every object kind it handles shares the OID-keyed
store) now takes an `actingRole string` parameter: the session's current
effective role, i.e. `connTx.NonSuperuserRole` (empty = still the bootstrap
superuser) as tracked by the pre-existing `SET ROLE`/`SET SESSION
AUTHORIZATION` machinery. It threads that role through to
`GrantTablePrivilegeAs` (via `recordSchemaGrant`/`recordFunctionGrant`/
`recordForeignServerGrant`/`recordForeignDataWrapperGrant`, each gaining the
same parameter) as the grantor for every grantee it records — the implicit
`PUBLIC`/owner default-ACL seeding calls are deliberately left on the old
owner-defaulting path, since they represent PostgreSQL's built-in `acldefault`
baseline, not an explicit grant.

The previously-discarded `GRANTED BY <role>` clause is now validated: if the
named role does not match the acting role (case-insensitively), the function
returns a new sentinel `errGrantorMustBeCurrentUser`, which `query.go`'s
`GRANT` dispatch branch turns into a real `0A000`
(`feature_not_supported`)/`"grantor must be current user"` error via the
existing `writeQueryError` helper — matching real PostgreSQL exactly instead
of silently ignoring the clause.

### Verification

Live-verified against a running goopg instance with the real (unmodified)
PostgreSQL 18.3 `psql`/`pg_dump` binaries — the identical `SET SESSION
AUTHORIZATION bob; GRANT ...; SET SESSION AUTHORIZATION charlie` sequence from
the Problem section reproduces the identical `relacl` string on goopg, and
`pg_dump` (unmodified, connected over the wire protocol) emits:

```sql
GRANT SELECT ON TABLE public.grantor_t TO bob WITH GRANT OPTION;
SET SESSION AUTHORIZATION bob;
GRANT SELECT ON TABLE public.grantor_t TO charlie;
RESET SESSION AUTHORIZATION;
```

byte-identical to real PostgreSQL's own output for the same DDL — no
goopg-side dump-rendering code was needed at all, since `pg_dump` is the real
client binary and `buildACLCommands` already does the `SET SESSION
AUTHORIZATION` wrapping client-side; getting the raw `relacl` string right was
the entire fix. The `GRANTED BY <mismatched role>` rejection was also
confirmed live: `GRANT SELECT ON grantor_t TO charlie GRANTED BY bob;` (while
connected as `postgres`) raises `ERROR:  grantor must be current user`,
matching real PG's wording exactly.

Tests: `internal/catalog/relacl_test.go`'s `TestRelaclTextGrantor` (storage +
rendering + revoke cleanup + mixed-case grantor); `internal/server/
grant_ddl_test.go`'s `TestTryRecordTableGrantStampsActingRoleAsGrantor`,
`TestTryRecordTableGrantGrantedByCurrentUserIsNoop`,
`TestTryRecordTableGrantGrantedByOtherRoleErrors`.

Gates: `go build ./...` clean; `go test ./internal/catalog/...
./internal/server/... ./internal/parser/... ./internal/executor/...` PASS (no
regressions); `scripts/tpch-spotcheck.sh` PASS; live psql/pg_dump A/B against
real PostgreSQL 18.3.

## Deferred (ledger row appended)

- Column-level ACL (`pg_attribute.attacl`) grantor tracking — the `attrACLs`
  store still hardcodes the owner, unchanged by this fix.
- `TYPE`/`DOMAIN`/`DATABASE`/`PARAMETER` ACL grants, which route through the
  executor (`execTypeACLChange`/`execDatabaseACLChange`/
  `execParameterACLChange`) rather than `grant_ddl.go`'s string-level recorder,
  still stamp the default owner grantor — not wired to the acting role in this
  loop.
- No PostgreSQL grant-option delegation-chain resolution
  (`select_best_grantor`, `postgres/src/backend/utils/adt/acl.c`): the
  recorded grantor is simply whichever role executed the `GRANT` statement,
  not necessarily the specific upstream role whose grant option was actually
  exercised in a multi-hop chain. Sufficient for the common single-hop
  delegation case this fix targets.
