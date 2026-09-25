# M0122\-0008: reload pg\_database\.datacl at startup

Status: **landed 2026\-09\-22** \(`feff73f01`\). Parent: M0122\-0008.

## Problem

`GRANT` already rewrites the shared `global/1262` pg\_database tuple with a
PG\-native `aclitem[]` value. After restart, however, the virtual
pg\_database reader consults the empty in\-memory ACL registry, so every
`datacl` becomes SQL NULL.

The task note called the array decoder missing. Investigation shows the codec
already has the required binary reader in `internal/executor/codec_aclitem.go`,
but it is private and no catalog reload pass consumes it. The missing mechanism
is therefore projection from the durable array into the existing ACL registry.

## Design

Expose a small executor codec API which decodes each `aclitem[]` element to its
grantee, grantor, and canonical privilege letters. Keep role\-OID lookup at the
caller boundary; the codec remains catalog\-independent.

Add a `global/1262` heap scan after `reloadDatabasesFromHeap`. For every live
row whose nullable `datacl` is non\-NULL, resolve OIDs through
`RoleNameForOID`, replay each database privilege into the existing
`tableACLs`/grantor/order stores, and preserve grant\-option bits. A non\-NULL
zero\-dimension array must remain `{}` rather than collapse to NULL, so the
reload materializes then removes the owner defaults through the existing public
catalog operations.

The scan is intentionally limited to `pg_database.datacl`. It does not change
the shared ACL renderer's owner\-before\-world order, which is separately
filed, and it does not broaden restart rehydration to other ACL\-bearing
catalogs.

## Verification

Unit coverage will pin decoder item fields and a cold\-start integration test
will create a second database, grant both ordinary and grant\-option database
privileges, restart, and verify the two rendered `datacl` values. The test
also keeps a NULL\-ACL database as the paired control.

## What landed, and what the fixture found

The implementation is the design above: `Open` scans `global/1262` after the
role reload \(the order is load\-bearing — aclitem stores role OIDs while the
catalog ACL store is keyed by role name\), decodes each non\-NULL `datacl`
through the newly exported `DecodeACLItemArray`, and replays the entries into
the existing ACL stores with their grant\-option bits.

The integration test's expected string is a **PostgreSQL 18.3 capture**, not a
derivation: `GRANT ALL ON DATABASE d3 TO PUBLIC` yields
`{=CTc/postgres,postgres=CTc/postgres}` there, and goopg now returns that both
before and after a restart. Because PUBLIC leads a database's array
\(M0122\-0008b\), the test also pins that the reload preserves array ORDER, not
merely the privilege set. Non\-vacuity checked: with the reload call
neutralised the post\-restart read is `""`.

Two divergences surfaced while building the fixture. Neither is fixed here and
both are ledgered:

1. **`GRANT … TO PUBLIC WITH GRANT OPTION` is rejected by PostgreSQL**
   \("grant options can only be granted to roles",
   `postgres/src/backend/catalog/aclchk.c`\) and **accepted by goopg**. The
   test's first draft used exactly that statement and therefore pinned a state
   PostgreSQL cannot produce; it now grants to PUBLIC without the option, and
   the grant\-option bit is covered by
   `TestDecodeACLItemArrayPreservesGrantOption` in `internal/executor`.
2. **`template1.datacl`**: goopg's initdb leaves it NULL; PostgreSQL seeds
   `{=c/postgres,postgres=CTc/postgres}` \(PUBLIC keeps CONNECT, TEMP is
   revoked\). The test uses goopg's NULL as its paired control, with the
   divergence named in a comment so the control is not mistaken for parity.

A third limitation is environmental rather than a divergence: `CREATE ROLE` is
handled in the postmaster, not the grammar, so a named\-role grantee is out of
reach from an `internal/initdb` test.

## Gates

initdb + executor + catalog unit tests; `tpch-spotcheck` PASS \(Q12=2,
Q13=33\); `tpcds-sf025` sweep `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0` with `PLAN-SHAPE same=99 changed=0`; `tpch-acceptance-arm` PASS 24
MATCH; the full `TestPort_RegressSuite` with an unchanged failing set
\(`partition_aggregate` only\); pgbench smoke.
