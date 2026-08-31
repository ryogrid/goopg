# GRANT … then partial REVOKE (`relacl`) round-trip in pg_dump (DU-002 slice 338)

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 TAP, DU-002 catalog-view parity umbrella)
Date: 2026-06-30

## Problem

A table whose privileges were granted and then partially revoked must round-trip
through `pg_dump` so the dump re-emits only the privileges that *survive* the
REVOKE:

```sql
CREATE ROLE revoke_role;
CREATE TABLE public.revoke_t (a integer);
GRANT SELECT, INSERT ON TABLE public.revoke_t TO revoke_role;
REVOKE INSERT ON TABLE public.revoke_t FROM revoke_role;
```

PostgreSQL clears the named bits from the grantee's aclitem mask. After the
REVOKE, `pg_class.relacl` is `{postgres=arwdDxtm/postgres,revoke_role=r/postgres}`
(the lone surviving `SELECT`), and pg_dump's `buildACLCommands` (`dumputils.c`)
diffs that against `acldefault('r', relowner)` client-side and emits exactly:

```sql
GRANT SELECT ON TABLE public.revoke_t TO revoke_role;
```

— **not** the revoked `INSERT`.

goopg treated REVOKE as a pure no-op (documented in `grant_ddl.go`: "REVOKE is
likewise left as a no-op"). The prior GRANT recorder (slices 331–337) had
materialized `revoke_role=ar/postgres` in `relacl`, and since REVOKE never
removed the `INSERT` bit, the dump over-emitted `GRANT INSERT, SELECT ON TABLE
public.revoke_t TO revoke_role;` — a privilege the live database no longer
grants. Restoring such a dump re-introduces the revoked privilege: a silent ACL
drift on dump/restore.

## Fix (dump-fidelity + ACL-store correctness)

The GRANT store (`InMemory.tableACLs`, `map[relOID]map[role]map[priv]bool`,
keyed by lower-cased role) already models privileges as a per-(role,priv) flag.
REVOKE is the natural inverse: remove the bit.

### `internal/catalog/catalog.go`

New interface method + `InMemory` implementation `RevokeTablePrivilege(relOID
uint32, role, priv string)`:

- lower-cases `role` and upper-cases `priv` (symmetric with the grant recorder
  so case-insensitive lookups still resolve);
- deletes `priv` from the role's privilege set;
- if the role's set becomes empty, drops the role entry entirely (so
  `relaclTextLockedFor` no longer emits that grantee's aclitem);
- if the relation has no grantees left, drops the whole `tableACLs[relOID]`
  entry (so `relacl` returns to NULL — matching `acldefault`, pg_dump emits
  nothing);
- a revoke of a privilege the role never held is a no-op (absent map entries).

The grantee's display-case override (`roleACLDisplay`, slice 337) is
intentionally **retained** on revoke: it is keyed by lower-cased role and
consulted only when that role still appears in some relacl, so a stale entry is
harmless and a later re-GRANT reuses the correct spelling.

### `internal/server/grant_ddl.go`

New `tryRecordTableRevoke(stmt)` mirrors `tryRecordTableGrant`: it parses
`REVOKE <privs> ON [TABLE|SEQUENCE] <objs> FROM <roles> [CASCADE|RESTRICT]`,
expands `ALL [PRIVILEGES]` via the shared `parseGrantPrivileges`, and calls
`RevokeTablePrivilege` per (object, role, priv). It is best-effort and bails
(leaving the statement a successful no-op) on any form it does not model:

- column-level (`REVOKE … (col) …` — parenthesised privilege list);
- `REVOKE GRANT OPTION FOR …` (clears only the option flag, not the privilege —
  separate from this slice);
- non-table object classes (`ON SCHEMA/DATABASE/FUNCTION/…` via the shared
  `nonTableGrantObjects` set).

A trailing `CASCADE`/`RESTRICT` drop-behaviour keyword is stripped from the role
list before splitting.

### `internal/server/query.go`

A single-statement autocommit `REVOKE` is intercepted symmetrically with the
GRANT branch: `tryRecordTableRevoke(matchable)` then `CommandComplete("REVOKE")`
+ `ReadyForQuery(Idle)`. Inside an explicit transaction it falls through to the
executor's existing no-op path (unchanged), exactly like GRANT.

## Why this is safe (blast radius)

- For any statement that is **not** a recognised table/sequence REVOKE,
  `tryRecordTableRevoke` returns without touching the store, and the response is
  identical to the prior no-op (`CommandComplete("REVOKE")`).
- `RevokeTablePrivilege` only ever *removes* bits already present; it cannot
  create or alter a grant, so `HasTablePrivilege`/`truncate-conflict`
  enforcement can only become *more* correct (a revoked privilege now reads
  false), never falsely grant.
- A GRANT with no following REVOKE renders byte-identically to slices 331–337.

## Gates

- `TestRevokeTablePrivilege` (catalog): `GRANT SELECT, INSERT` → `ar`; `REVOKE
  INSERT` → lone `r` and `HasTablePrivilege(INSERT)` false / `(SELECT)` true;
  no-op revoke of an un-granted `DELETE`; `REVOKE SELECT` (last priv) →
  grantee entry dropped, relacl back to NULL; case-insensitive revoke
  (`GRANT MixedCase` then `REVOKE MIXEDCASE`) clears it.
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 338**: the fixture above
  produces `GRANT SELECT ON TABLE public.revoke_t TO revoke_role;` and **no**
  `INSERT … TO revoke_role` line — byte-identical vs real pg_dump 18.3.
- `TestRelaclText`/`…GrantOption`/`…Sequence`/`…Public`/`…QuotedGrantee`/
  `…MixedCaseGrantee`/`TestACLQuoteName`/`TestNamespaceACLText` +
  catalog/server + `truncate-conflict` isolation suites PASS.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

- column-level GRANT/REVOKE (`pg_attribute.attacl`, needs heap re-sync from an
  executor context the server-side recorder lacks);
- database GRANT (`datacl`, only dumped under `--create`, not in this fixture);
- REVOKE-of-default modelling (revoking the *owner's* implicit privileges, which
  materializes a sub-default owner aclitem and a `REVOKE` line in the dump —
  distinct from this grantee-side partial revoke);
- extended-protocol commit-time deferral (auto-commit-per-statement,
  architecturally entangled).
