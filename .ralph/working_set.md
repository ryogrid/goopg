(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 339** (GRANT … ON SCHEMA then partial
REVOKE — nspacl round-trip in pg_dump). Server-only fix: tryRecordTableRevoke
gained an ON SCHEMA branch → recordSchemaRevoke (mirror of recordSchemaGrant),
reusing catalog RevokeTablePrivilege (slice 338, already correct for schema OIDs).
NO catalog change. Gates: TestRevokeTablePrivilege/Relacl + slice-339
TestPort_PgDumpConnectionSetup (byte-identical vs real pg_dump 18.3) PASS; build
clean; pgbench smoke = pre-commit hook. Committed + pushed.

NEXT M0119-0004 GRANT/ACL slices (pick one next loop):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one).
- database GRANT (`datacl`, only dumped under `--create`).
- REVOKE-of-default (owner-side implicit-privilege revoke — materializes a
  sub-default owner aclitem + a REVOKE line in the dump).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
