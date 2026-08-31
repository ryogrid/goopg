# 0119-0004 — REVOKE-then-re-GRANT grant-order in relacl (DU-002 slice 355)

Status: accepted
Date: 2026-06-30
Milestone: M0119-0004 (pg_dump 002–010 TAP — catalog-view parity battery)
Source slice: DU-002 slice 355 (follow-on to slice 354 grant-order preservation)

## Problem

Slice 354 landed grant-order preservation for `pg_class.relacl` /
`pg_proc.proacl` / `pg_namespace.nspacl`: a brand-new grantee's aclitem is
appended to the end of the array (catalog `tableACLOrder`), matching
PostgreSQL's `aclupdate` (`src/backend/utils/adt/acl.c`). That slice exercised
only **fresh** grants in reverse-alphabetical order.

It left the **teardown + re-append** path — `dropTableACLOrderRole` on a full
revoke, then a re-append on the next GRANT to the same grantee — covered only by
the catalog-internal full-teardown case, never end-to-end through pg_dump.

PostgreSQL's `aclupdate` does NOT preserve a revoked grantee's original slot: a
full REVOKE *deletes* the aclitem, and a later GRANT to that same grantee
*appends* a fresh aclitem at the end of the array. So a re-granted grantee
renders **after** grantees that held their privilege continuously — even if it
was granted first and sorts first.

Verified against real PG 18.3 (`./postgres/local_install`):

```sql
CREATE TABLE rg_t(id int);
GRANT  SELECT ON rg_t TO rg_a;   -- order: a
GRANT  SELECT ON rg_t TO rg_b;   -- order: a, b
REVOKE SELECT ON rg_t FROM rg_a; -- a's aclitem deleted → order: b
GRANT  INSERT ON rg_t TO rg_a;   -- fresh aclitem appended → order: b, a
-- relacl = {postgres=arwdDxtm/postgres,rg_b=r/postgres,rg_a=a/postgres}
```

`pg_dump` fans the aclitem array out in array order, so it emits the `rg_b`
SELECT line **before** the `rg_a` INSERT line — b before a, the reverse of both
alphabetical and original grant order.

## Fix

**Test-only — no engine change.** The slice-354 catalog code already implements
the required semantics:

- `RevokeTablePrivilege` calls `dropTableACLOrderRole(relOID, role)` when the
  grantee's privilege set becomes empty, removing it from `tableACLOrder`.
- `GrantTablePrivilegeWithGrantOption` appends the grantee to `tableACLOrder`
  whenever its per-role privilege map is freshly created — which is exactly the
  case after a full revoke, so the re-granted grantee lands at the end.

The server-side recorders (`tryRecordTableGrant` / `tryRecordTableRevoke` in
`internal/server/grant_ddl.go`) route the GRANT/REVOKE/GRANT sequence through
those catalog primitives unchanged.

This slice adds:

1. `internal/catalog/relacl_test.go` →
   `TestRelaclTextRegrantAfterRevokeMovesToEnd`: a focused unit test asserting
   the relacl text after grant(a)/grant(b)/revoke(a)/regrant(a) is
   `{postgres=arwdDxtm/postgres,rg_b=r/postgres,rg_a=a/postgres}` (b before a),
   guarding the teardown + re-append path directly.
2. `internal/testport/pgdump_connsetup_test.go` → DU-002 **slice 355** fixture
   (`regrant_t` + `rg_role_a`/`rg_role_b`) and assertion: the `rg_role_b` SELECT
   GRANT line must precede the `rg_role_a` INSERT GRANT line in the pg_dump
   output (`strings.Index` position check), byte-identical to real pg_dump 18.3.

## Blast radius

Nil. No production code changed. The new coverage protects a freshly-landed
(slice 354) catalog path that pg_dump fidelity depends on.

## Gates

- `go test ./internal/catalog/ -run TestRelacl` PASS (incl. the new unit).
- `go test ./internal/testport/ -run TestPort_PgDumpConnectionSetup` (DU-002
  slice 355) PASS — byte-identical vs real pg_dump 18.3.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, heap re-sync) / database (`datacl`,
`--create`-only) / TYPE-DOMAIN (`pg_type.typacl`, currently unmodelled) GRANT
projection; function/object WITH-GRANT-OPTION revoke; extended-protocol
commit-time deferral.
