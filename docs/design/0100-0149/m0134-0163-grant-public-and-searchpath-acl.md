# M0134-0163 — `GRANT … TO PUBLIC` / group grants actually confer privileges

Status: **LANDED** (engine-wide fix). `rowsecurity.sql` **PARKED** — the case's
dominant remaining root cause (row-level security enforcement itself) is
REFACTOR-tier.

Task: M0134-0163 (`postgres/src/test/regress/sql/rowsecurity.sql`, CSV status
`not-tried` → `failed`).

## What the case measured

`rowsecurity.sql` was sized live for the first time this loop. At HEAD it
produced **5389 diff lines / 301 `^+ERROR` / 100 `^-ERROR`**, and the single
largest bucket by a wide margin was **165 spurious `permission denied for table
<x>` errors** — more than half of every error the case raised. Those errors were
not about row-level security at all. They came from two independent, engine-wide
defects in goopg's object-ACL path, both of which made a perfectly ordinary
`GRANT` silently confer nothing.

The case's setup is the minimal reproducer for both
(`rowsecurity.sql:33-75`):

```sql
CREATE SCHEMA regress_rls_schema;
SET search_path = regress_rls_schema;
SET SESSION AUTHORIZATION regress_rls_alice;
CREATE TABLE document (...);
GRANT ALL ON document TO public;      -- unqualified name + PUBLIC grantee
```

Every subsequent `SET SESSION AUTHORIZATION regress_rls_bob; SELECT * FROM
document …` — the entire body of the test — was rejected.

## Root cause 1: the GRANT recorder did not resolve `search_path`

`GRANT`/`REVOKE` on a table is not executed by the executor. `query.go`'s
autocommit fast path intercepts it and hands it to the hand-written token
scanners `tryRecordTableGrant` / `tryRecordTableRevoke`
(`internal/postmaster/grant_ddl.go`, design `0118-0039`), which record the
aclitem directly in the catalog ACL store. Because those recorders run *before*
the executor they never hold an `executor.Context`, and they resolved the object
name with a bare `s.cfg.Catalog.LookupTable(on)`.

`InMemory.LookupTable` on an unqualified name tries exactly three keys: the bare
name, `public.<name>`, and `pg_catalog.<name>`
(`internal/catalog/catalog.go:12416`). It has no notion of `search_path` — that
lives one layer up, in the `SearchPathCatalog` wrapper the planner is handed
(`catalog.WithSearchPath`, `catalog.go:24628`).

So a `GRANT` naming an unqualified table in **any schema other than `public`**
resolved to nothing, `continue`d past the grant loop, and returned a successful
`GRANT` command tag having recorded no aclitem whatsoever. Verified directly
against a throwaway server before the fix:

```
SET search_path = sch;  CREATE TABLE doc(a int);
GRANT ALL ON doc TO public;               -- reports "GRANT"
SELECT relacl FROM pg_class WHERE relname='doc';
                    relacl
------------------------------
 {postgres=arwdDxtm/postgres}             -- the PUBLIC aclitem never landed
GRANT SELECT ON sch.doc TO public;        -- schema-qualified: DOES land
```

This is silent and total: it affects every upstream regress case that does `SET
search_path = <schema>` and then grants unqualified, and every application that
keeps its tables outside `public`.

Fix: `grantTableLookup` (`grant_ddl.go`) wraps `s.cfg.Catalog` in the same
`catalog.WithSearchPath` the planner uses, so the recorders inherit the engine's
one canonical fallback order instead of duplicating it. The session's effective
path is `searchPathSchemas(sess)`, already available at the `query.go` call site
and threaded into both recorders. **REVOKE takes the same parameter on purpose**
— if only GRANT resolved the search path the pair would desynchronise and a
REVOKE would silently target a different (or no) relation than the GRANT it was
meant to undo.

## Root cause 2: `HasTablePrivilege` was not `aclmask()`

With the aclitem recorded, the privilege *check* still failed. `HasTablePrivilege`
(`internal/catalog/catalog.go`) was a single map probe: `tableACLs[relOID][role]`.
It matched only an aclitem whose grantee is the querying role itself.

PostgreSQL's `aclmask()` (`postgres/src/backend/utils/adt/acl.c:1389`) matches
grantees in two passes:

1. `aidata->ai_grantee == ACL_ID_PUBLIC || aidata->ai_grantee == roleid` — a
   direct grant, **or a grant to PUBLIC**;
2. otherwise `has_privs_of_role(roleid, aidata->ai_grantee)` — the grant reaches
   the role **indirectly through the INHERIT-marked role memberships it holds**.

goopg implemented only the second half of pass 1. Both missing arms are exercised
by this case: `GRANT ALL ON document TO public` needs the PUBLIC arm, and
`GRANT regress_rls_group1 TO regress_rls_bob` + a grant to `regress_rls_group1`
needs the membership arm. goopg already records a `GRANT … TO PUBLIC` under the
`publicPseudoRole` (`"public"`) key and renders it as the empty grantee in
relacl, so the data was there the whole time — nothing ever read it.

`HasTablePrivilege` is now goopg's `aclmask()`, structured like it:

- pass 1 probes `role` then `publicPseudoRole`;
- pass 2 collects the remaining grantees that hold `priv` (aclmask's
  `aidata->ai_privs & remaining` pre-filter, which exists precisely to skip the
  expensive membership test) and adjudicates each with `HasPrivsOfRole`.

`HasPrivsOfRole` is M0134-0162's `rolinherit`-aware walk, so **NOINHERIT is
honoured for free**: a NOINHERIT member of a granted group is correctly denied
(PG makes such a role reach the privilege only through an explicit `SET ROLE`).
Verified live.

Two implementation notes worth carrying:

- **Locking.** `HasPrivsOfRole` and `RoleOID` take `c.mu` themselves, so pass 2's
  candidates are collected under the read lock and adjudicated after it is
  dropped. Nesting the `RLock` would deadlock against any waiting writer.
- **The owner sentinel is not a real role.** goopg stores the owner's aclitem
  under `aclOwnerRole = "postgres"` regardless of the object's actual owner
  (`grant_ddl.go`). Pass 2 resolves that key to OID 10, so `HasPrivsOfRole(x, 10)`
  is true only for OID 10 itself or a superuser — which is the correct answer,
  and leaves `TestTryRecordTableGrantOwnerSentinel`'s expectation intact. It does
  mean a *member of the real owner role* cannot yet inherit the owner's entry;
  see the deferral ledger.

## Effect

`rowsecurity.sql`: `^+ERROR` **301 → 137**, of which `permission denied for
table` went **165 → 11**. 154 statements that used to abort now execute.

The raw diff-line count went **5389 → 6047** and that is the expected direction,
not a regression: a statement that aborted contributed one `+ERROR` line in place
of its whole expected result set, and now contributes a full — but unfiltered —
result set instead. Line count is the wrong metric for this particular change;
`^+ERROR` is the honest one.

Zero regressions: an 8-case A/B against a HEAD worktree (`privileges`,
`sequence`, `roleattributes`, `create_role`, `security_label`, `init_privs`,
`dependency`, `password`) is byte-identical after the runner's tmpfile headers —
2 PASS and 6 FAIL identically on both sides. Note that `privileges.sql` did
**not** move: it operates in `public`, where the bare `LookupTable` already
resolved, and its own divergence is dominated by unrelated gaps.

## Why `rowsecurity.sql` is PARKED

The remaining divergence is dominated by RLS itself. `CREATE POLICY` stores
`pg_policy` rows correctly, but **no policy is ever applied at scan time** —
`SELECT * FROM document` as `regress_rls_bob` now returns all 10 rows where PG
returns the 5 the policy admits, and `f_leak` fires on every row (the case's
whole point is that it must NOT). PG injects policy quals during rewrite
(`postgres/src/backend/rewrite/rowsecurity.c` `get_row_security_policies`,
called from `fireRIRrules`), wrapping the relation in a security-barrier
subquery so leakproof-ness ordering is preserved against functions like
`f_leak`. goopg has no rewrite-time RLS stage at all, and adding one is a
planner/rewriter refactor — squarely REFACTOR-tier, and out of scope for a
contained single-loop fix.

The smaller remaining buckets, filed as follow-ups: 51 `relation does not exist`
(chiefly the missing `pg_policies` system view), 40 `syntax error` (`TABLESAMPLE`
in this case's sampled variants, among others), a missing `row_security_active()`
builtin, and `CREATE POLICY … AS <bogus>` being accepted instead of raising
`unrecognized row security option`.

## Files

| file | change |
|---|---|
| `internal/catalog/catalog.go` | `HasTablePrivilege` becomes `aclmask()`: PUBLIC arm + role-membership pass |
| `internal/postmaster/grant_ddl.go` | `grantTableLookup`; `tryRecordTableGrant`/`tryRecordTableRevoke` take `searchPath` |
| `internal/postmaster/query.go` | pass `searchPathSchemas(sess)` to both recorders |
| `internal/catalog/relacl_test.go` | `TestHasTablePrivilegeAclmaskGranteeMatching` |
| `internal/postmaster/grant_ddl_test.go` | `TestTryRecordTableGrantResolvesSearchPath` |

## Upstream references

- `postgres/src/backend/utils/adt/acl.c:1389` — `aclmask()`, both grantee passes.
- `postgres/src/backend/utils/adt/acl.c` — `has_privs_of_role()`.
- `postgres/src/backend/rewrite/rowsecurity.c` — `get_row_security_policies()`.
- `postgres/src/test/regress/sql/rowsecurity.sql:33-75` — the setup that
  reproduces both defects.
