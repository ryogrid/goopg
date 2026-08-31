# 0119-0004 — Owner-side function REVOKE (`pg_proc.proacl`) round-trip in pg_dump (DU-002 slice 347)

Status: accepted
Milestone: M0119-0004 (pg_dump TAP port DU-002, slice 347)
Date: 2026-06-30

## Problem

Slice 346 made the *PUBLIC-side* function REVOKE
(`REVOKE EXECUTE ON FUNCTION f FROM PUBLIC`) round-trip through pg_dump. Its
counterpart, the **owner-side** function REVOKE
(`REVOKE EXECUTE ON FUNCTION f FROM postgres`), did not — and produced the wrong
`proacl`.

A function's `acldefault('f', owner)` grants `EXECUTE` to **both** the owner and
`PUBLIC`:

```
{=X/postgres,postgres=X/postgres}
```

PostgreSQL materializes that full array on the first GRANT/REVOKE and then
mutates it. So the two single-role revokes diverge:

| statement | PG 18.3 proacl | pg_dump emits |
|---|---|---|
| `REVOKE EXECUTE … FROM PUBLIC`  | `{postgres=X/postgres}` | `REVOKE ALL ON FUNCTION … FROM PUBLIC;`   (slice 346) |
| `REVOKE EXECUTE … FROM postgres`| `{=X/postgres}`         | `REVOKE ALL ON FUNCTION … FROM postgres;` (this slice) |
| both                            | `{}`                    | both lines |

(Verified against `./postgres/local_install` PG 18.3.)

Unlike a table or sequence — whose `acldefault` grants **only** the owner, so
`REVOKE ALL FROM <owner>` empties the array to `{}` (slices 341/343) — a
function's owner revoke leaves PUBLIC's implicit `EXECUTE` behind. goopg got this
wrong two ways:

1. **`recordFunctionRevoke` (slice 346) never materialized PUBLIC's implicit
   default.** It seeded only the owner's `EXECUTE`, so revoking the owner emptied
   `byRole` and (via `relACLEmptied`) rendered `{}` — pg_dump then wrongly emitted
   a `… FROM PUBLIC;` line too, stripping PUBLIC's surviving default on restore.

2. **`relaclTextLockedFor` re-added the absent owner.** Even if PUBLIC had been
   seeded, the renderer inserts the leading `postgres=<default>/postgres` entry
   whenever the owner key is absent from a non-empty array and `relACLEmptied` is
   unset — so `{=X/postgres}` would have rendered as
   `{postgres=X/postgres,=X/postgres}` (owner resurrected), and pg_dump would have
   dropped the owner `REVOKE`.

The pre-existing `relACLEmptied` flag is too narrow: it fires only when the owner
revoke *also empties the whole array*. An object whose `acldefault` grants a
**non-owner** implicit privilege (a function's PUBLIC `EXECUTE`) leaves a
surviving aclitem after the owner is revoked, which `relACLEmptied` does not
cover.

## Fix

Server-only recorder change plus a new catalog flag, both bounded to functions /
the owner-revoke-with-survivor case.

### Catalog: `relACLOwnerRevoked` (the broader owner-revoked signal)

New OID-keyed flag `relACLOwnerRevoked map[uint32]bool` recording that the
owner's implicit default aclitem was explicitly revoked, **regardless of whether
other grantees survive**. `relACLEmptied` is the special case of it where the
owner revoke also empties the array.

- **`RevokeTablePrivilege`** sets `relACLOwnerRevoked[relOID]` whenever the owner
  (`aclOwnerRole`) entry is fully removed (its privilege set hits empty),
  *before* the existing `relACLEmptied` (whole-array-empty) branch.
- **`relaclTextLockedFor`** suppresses the leading owner entry when
  `relACLEmptied || relACLOwnerRevoked`, and the empty-`byRole` early-return
  renders `{}` for either flag (covers revoking every grantee in one statement,
  where the *last* removed entry is a non-owner so `relACLEmptied` stays unset).
- **`MaterializeOwnerACL`** early-returns while either flag is set (never
  resurrect a revoked owner).
- **`GrantTablePrivilege`** clears *both* flags only on an owner-side GRANT
  (a grantee GRANT leaves the owner zeroed — the slice-344 invariant).
- **`DropTableACL`** clears the new flag alongside `relACLEmptied`.

Because the flag is set only when the owner entry is *fully* revoked, it adds
nothing new for tables/sequences/schemas (whose owner revoke already empties the
array and sets `relACLEmptied`). It additionally fixes a previously-latent table
bug — `REVOKE ALL ON TABLE FROM postgres` while a grantee survives now correctly
suppresses the owner (was re-adding the default owner). The flag is OID-keyed and
object-type-agnostic, so the same correction holds for `ProcACLText`,
`NamespaceACLText`, and the sequence renderer.

### Server: materialize the full function default on the first mutation

`recordFunctionRevoke` (`internal/server/grant_ddl.go`) now seeds **both** the
owner and PUBLIC implicit `EXECUTE` entries — but only while `proacl` is still
NULL (no prior mutation), detected via the newly interface-exposed
`Catalog.ProcACLText(oid) == ""`:

```go
if s.cfg.Catalog.ProcACLText(oid) == "" {
    s.cfg.Catalog.MaterializeOwnerACL(oid, aclOwnerRole, allFunctionPrivileges)
    s.cfg.Catalog.GrantTablePrivilege(oid, "PUBLIC", "EXECUTE")
}
// then RevokeTablePrivilege per (role, priv)
```

The NULL guard is essential: re-seeding PUBLIC on a *second* REVOKE would
resurrect an already-revoked grantee, so `REVOKE … FROM PUBLIC` then
`… FROM postgres` would reach `{=X/postgres}` instead of `{}`. PostgreSQL
expands `acldefault` exactly once, on the first GRANT/REVOKE; the NULL check
mirrors that. `ProcACLText` is added to the `catalog.Catalog` interface as a pure
read.

This keeps slice 346 (`… FROM PUBLIC` → `{postgres=X/postgres}`) byte-identical
(seed both, revoke the absent-then-present PUBLIC → owner-only) and adds:

- owner-side → `{=X/postgres}` (PUBLIC survives, owner suppressed by the flag);
- both roles in one statement → `{}` (empty array via `relACLOwnerRevoked`).

## Scope / non-goals

- Pinned cases: `REVOKE EXECUTE ON FUNCTION … FROM postgres` (owner) and the
  full `… FROM PUBLIC` + `… FROM postgres` empty-array path, on a never-granted
  routine, single-statement autocommit.
- `WITH GRANT OPTION` on functions, column-level (`pg_attribute.attacl`, heap
  re-sync) and database (`datacl`, `--create`-only) ACL projection remain open.
- Extended-protocol commit-time deferral stays architecturally entangled.
- Dump-fidelity only — goopg does not enforce function EXECUTE privileges.

## Blast radius

Near-zero. The recorder only ever removes privilege bits already present, and
re-seeds defaults exclusively when `proacl` is NULL. `relACLOwnerRevoked` fires
only on a full owner revoke; every prior table/sequence/schema slice either never
sets it or sets it together with `relACLEmptied` (rendered identically). The
table latent-bug fix (owner-revoke-with-surviving-grantee) makes goopg *more*
PG-faithful.

## Tests / gates

- `internal/catalog/relacl_test.go` new `TestProcACLRevokeFromOwner`
  (owner-only → `{=X/postgres}`; owner+PUBLIC → `{}`; owner re-GRANT restores
  `{postgres=X/postgres,=X/postgres}`); existing `TestProcACLRevokeFromPublic`
  / `TestProcACLText` / `TestMaterializeOwnerACL` / `TestNamespaceACLText` /
  table+sequence empty-array tests PASS.
- `internal/testport` `TestPort_PgDumpConnectionSetup` **DU-002 slice 347**
  asserts the exact `REVOKE ALL ON FUNCTION public.ownrevfn(integer) FROM
  postgres;` line appears **and** no spurious `… FROM PUBLIC;` line — byte-identical
  vs real pg_dump 18.3 (the test drives the real pg_dump binary against goopg).
- `internal/server` + `internal/initdb` suites PASS; `go build ./...` clean;
  pgbench smoke = pre-commit hook.
