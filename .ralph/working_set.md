(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 355** (REVOKE-then-re-GRANT grant-order —
TEST-ONLY, no engine change). End-to-end coverage for the grant-order teardown +
re-append path landed by slice 354. PG's aclupdate (acl.c) deletes a revoked
grantee's aclitem and a later GRANT to the same grantee appends a FRESH aclitem at
the END, so a re-granted grantee renders AFTER continuously-held grantees even
though granted first / sorting first. Verified vs real PG 18.3
(./postgres/local_install): GRANT SELECT→rg_a; GRANT SELECT→rg_b; REVOKE SELECT
FROM rg_a; GRANT INSERT→rg_a → relacl {postgres=arwdDxtm/postgres,rg_b=r/postgres,
rg_a=a/postgres} (b before a); pg_dump emits rg_b SELECT line before rg_a INSERT
line.

No production code changed: RevokeTablePrivilege already drops the grantee from
catalog.tableACLOrder on full revoke (dropTableACLOrderRole); next GRANT re-appends
(fresh per-role map). Slice 354 covered only fresh reverse-order grants — this
exercises the teardown+re-append. Added unit
catalog/relacl_test.go:TestRelaclTextRegrantAfterRevokeMovesToEnd + connsetup
slice 355 fixture (regrant_t / rg_role_a / rg_role_b) + strings.Index b-before-a
assert. Design 0119-0004-regrant-after-revoke-order-relacl-pgdump.md + README row
0119-0004be. Gates: catalog TestRelacl PASS; connsetup slice 355 PASS; build clean;
state-guard OK; pgbench smoke = pre-commit hook.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper engine work):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — entangled).
- database GRANT (`datacl`, only under `--create`; harness runs --no-create).
- TYPE/DOMAIN GRANT (`pg_type.typacl`): grant_ddl.go bails on non-table/sequence
  → typacl UNMODELLED; new ACL surface (pg_type virtual — check tractability).
- Extended-protocol commit-time deferral stays architecturally entangled
  ([[goopg_extended_protocol_autocommit]]).
