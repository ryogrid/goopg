# DU-002 slice 340 — owner-side `REVOKE`-of-default (`relacl`) round-trip in pg_dump

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity)

## Problem

PostgreSQL leaves `pg_class.relacl` NULL while a relation's owner holds its
implicit default privileges. The *first* owner-side `REVOKE` materializes the
relacl as the owner's full default set **minus** the revoked bits:

```sql
CREATE TABLE public.ownrev_t (a integer, b text);   -- relacl NULL
REVOKE TRIGGER ON TABLE public.ownrev_t FROM postgres;
-- pg_class.relacl = {postgres=arwdDxm/postgres}     (full "arwdDxtm" minus 't')
```

`pg_dump`'s `buildACLCommands` (`src/bin/pg_dump/dumputils.c`) diffs that
materialized relacl against `acldefault('r', relowner)` (= `arwdDxtm`) entirely
client-side and re-emits the transform as a `REVOKE ALL` + a re-`GRANT` of the
**surviving** privileges (verified byte-identical to real pg_dump 18.3):

```sql
REVOKE ALL ON TABLE public.ownrev_t FROM postgres;
GRANT SELECT,INSERT,REFERENCES,DELETE,TRUNCATE,MAINTAIN,UPDATE ON TABLE public.ownrev_t TO postgres;
```

Before this slice goopg's REVOKE recorder (`tryRecordTableRevoke`,
`internal/server/grant_ddl.go`, slice 338) only modelled *non-owner* grantees:
the owner is implicit in goopg's ACL store (rendered as the constant
`postgres=arwdDxtm/postgres` leading entry only when some grantee exists,
otherwise relacl is NULL). An `REVOKE … FROM postgres` therefore found no
grantee entry to clear and left relacl NULL → pg_dump emitted nothing, silently
dropping the owner's privilege change on restore.

## Fix

Server-only recording + a small catalog primitive; pg_dump does all the
`REVOKE`/`GRANT` formatting from the projected relacl text, so projecting the
PG-accurate aclitem array is sufficient.

- **catalog** (`internal/catalog/catalog.go`): new interface method +
  `InMemory.MaterializeOwnerACL(relOID, owner, ownerPrivs)`. It records an
  explicit owner aclitem holding exactly `ownerPrivs` (the owner's full default
  set for the object class), but **only when no explicit owner entry exists
  yet** — so a second owner-side REVOKE does not clobber the first. Owner
  privileges are stored without the grant option (PostgreSQL prints the owner's
  self-grant with no `*`).
- The aclitem renderer `relaclTextLockedFor` now special-cases the owner key
  (`aclOwnerRole` = `"postgres"`): when an explicit owner entry is present it
  renders the owner's *actual* remaining privileges (via the new
  `renderACLLetters` helper, extracted from the old inline loop) instead of the
  constant `ownerString`, and skips the owner in the grantee loop. The owner is
  still always emitted first. Absent an explicit owner entry the behaviour is
  unchanged (constant owner default when grantees exist, NULL otherwise).
- **server** (`internal/server/grant_ddl.go`): in `tryRecordTableRevoke`'s
  table/sequence loop, a REVOKE whose grantee is the owner
  (`strings.EqualFold(role, aclOwnerRole)`) first calls
  `MaterializeOwnerACL(tbl.OID, "postgres", allPrivs)` — where `allPrivs` is the
  object class's full owner default (`allTablePrivileges` / `allSequencePrivileges`,
  already selected for the existing grantee path) — then drops the revoked bits
  via the unchanged `RevokeTablePrivilege`. Result: relacl = default − revoked.

## Scope / non-goals

- **Single-privilege owner revoke only.** A `REVOKE ALL … FROM owner` empties
  the owner entry, which `RevokeTablePrivilege` drops → relacl returns to NULL.
  PostgreSQL instead stores an *empty* aclitem array `{}` (distinct from NULL)
  and pg_dump emits a bare `REVOKE ALL … FROM owner;`. Modelling the empty-array
  state is a follow-up.
- Owner-side REVOKE on a **schema** (`recordSchemaRevoke`) and **sequence** is
  not wired in this slice (the schema path is separate; the sequence path shares
  the loop but is untested). The catalog primitive is object-type-agnostic, so
  extending either is a one-line `MaterializeOwnerACL` call.
- Column-level (`pg_attribute.attacl`, heap re-sync) and database (`datacl`,
  only dumped under `--create`) GRANT/REVOKE projection remain open.

## Blast radius

Near zero. `MaterializeOwnerACL` only fires on a REVOKE whose grantee is the
owner — a form that was previously a silent no-op — and is itself a no-op once
an owner entry exists. The renderer change is behaviour-preserving when no
explicit owner entry is present (every prior slice 331–339 fixture). Privilege
*enforcement* (`HasTablePrivilege`) is unaffected: the owner is a superuser in
goopg and is never gated by the relacl store.

## Verification

- `go test ./internal/catalog/` — new `TestMaterializeOwnerACL`
  (REVOKE TRIGGER → `{postgres=arwdDxm/postgres}`; a second owner REVOKE does not
  re-materialize → `{postgres=arwdDx/postgres}`; owner-revoke coexists with a
  later grantee, owner first) + all existing `TestRelaclText*` / `TestRevoke*` /
  `TestNamespaceACLText` PASS.
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` —
  **DU-002 slice 340**: asserts the exact `REVOKE ALL …` line, the exact
  `GRANT SELECT,INSERT,REFERENCES,DELETE,TRUNCATE,MAINTAIN,UPDATE … TO postgres;`
  re-grant (privilege list omits TRIGGER), and that `TRIGGER … TO postgres` does
  NOT reappear. Byte-identical to real pg_dump 18.3 (ground truth captured from
  a fresh PG 18.3 `initdb`).
- `go build ./...` clean; pgbench TPC-B smoke = pre-commit hook.

## Oracle

`postgres/src/bin/pg_dump/dumputils.c` (`buildACLCommands`),
`src/backend/utils/adt/acl.c` (`acldefault`, `aclitemout`/`putid`). Behaviour
compared against `postgres/local_install` PG 18.3.
