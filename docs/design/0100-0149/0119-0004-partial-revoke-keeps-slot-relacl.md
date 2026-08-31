# 0119-0004 — Partial REVOKE keeps grant-order slot in relacl (DU-002 slice 356)

Status: accepted
Date: 2026-06-30
Milestone: M0119-0004 (pg_dump 002–010 TAP — catalog-view parity battery)
Source slice: DU-002 slice 356 (complement of slice 355)

## Problem

Slice 355 pinned the **full-revoke-then-re-GRANT** path: a fully revoked grantee
loses its array slot, and a later GRANT appends a fresh aclitem at the end of
`pg_class.relacl`, so the re-granted grantee renders *after* continuously-held
grantees.

The complementary case — a **partial** REVOKE where the grantee retains at least
one privilege — was never pinned. PostgreSQL's `aclupdate`
(`src/backend/utils/adt/acl.c`) treats the two cases differently:

- **Full revoke** (`ACL_NUM` of the modified aclitem drops to zero): the aclitem
  is *deleted* from the array; a later GRANT appends a fresh one at the end.
- **Partial revoke** (bits removed but the aclitem survives): `aclupdate`
  modifies the existing aclitem *in place* — its array index is unchanged, so
  the grantee keeps its original position relative to the others.

Without an explicit guard, a future refactor of the revoke path could wrongly
drop-and-re-append a partially-revoked grantee, silently reordering relacl and
breaking pg_dump byte-fidelity.

Verified against real PG 18.3 (`./postgres/local_install`):

```sql
CREATE TABLE pr_t(id int);
GRANT  SELECT, INSERT ON pr_t TO pr_a;  -- order: a (privs ar)
GRANT  SELECT ON pr_t TO pr_b;          -- order: a, b
REVOKE INSERT ON pr_t FROM pr_a;        -- a survives (keeps SELECT) → in place
-- relacl = {postgres=arwdDxtm/postgres,pr_a=r/postgres,pr_b=r/postgres}
--          pr_a stays AHEAD of pr_b (NOT moved to the end)
```

## Fix

**Test-only — no engine change.** The slice-354/355 catalog code already
implements the required semantics: `RevokeTablePrivilege`
(`internal/catalog/catalog.go`) calls `dropTableACLOrderRole(relOID, role)`
**only** when the grantee's privilege set becomes empty (`len(privs) == 0`). A
partial revoke leaves the per-role map non-empty, so `tableACLOrder` is
untouched and the grantee keeps its slot — exactly mirroring `aclupdate`'s
in-place modification.

This slice adds `internal/catalog/relacl_test.go` →
`TestRelaclTextPartialRevokeKeepsSlot`:

1. grant(pr_a: SELECT,INSERT) / grant(pr_b: SELECT) / revoke(pr_a: INSERT) → the
   relacl text is `{postgres=arwdDxtm/postgres,pr_a=r/postgres,pr_b=r/postgres}`
   (pr_a stays ahead of pr_b), byte-identical to real PG 18.3.
2. A contrast guard in the same test: a *full* revoke of pr_a followed by a
   re-GRANT appends pr_a at the end
   (`{postgres=…,pr_b=r/postgres,pr_a=d/postgres}`), re-confirming the slice-355
   semantics alongside the partial-revoke case so the two paths are pinned
   together.

End-to-end relacl ordering through pg_dump is already covered by the slice
354/355 connsetup fixtures; this slice pins the partial-vs-full revoke contrast
at the deterministic catalog-unit level.

## Blast radius

Nil. No production code changed. The new coverage protects the slice-354
`tableACLOrder` machinery that pg_dump relacl fidelity depends on.

## Gates

- `go test ./internal/catalog/ -run TestRelacl` PASS (incl. the new unit).
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, heap re-sync) / database (`datacl`,
`--create`-only) / TYPE-DOMAIN (`pg_type.typacl`, currently unmodelled) GRANT
projection; extended-protocol commit-time deferral.
