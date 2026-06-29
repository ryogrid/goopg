(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 348** (function GRANT … WITH GRANT OPTION
`pg_proc.proacl` round-trip in pg_dump). The grant-option variant deferred by
slice 345. A function grant recorded with `GrantTablePrivilege` dropped the
WITH-GRANT-OPTION flag, so `GRANT EXECUTE ON FUNCTION f TO r WITH GRANT OPTION`
restored as a plain `GRANT ALL …;`. PG materializes proacl as
`{=X/postgres,postgres=X/postgres,r=X*/postgres}` (grantee EXECUTE carries `*`);
pg_dump emits `GRANT ALL ON FUNCTION … TO r WITH GRANT OPTION;` (verified vs real
PG 18.3). Fix (server-only): `recordFunctionGrant` (grant_ddl.go) gains a
`withGrantOption bool` param (threaded from the function/procedure/routine
branches of `tryRecordTableGrant`) and records the grantee via
`GrantTablePrivilegeWithGrantOption(oid, role, "EXECUTE", withGrantOption)`;
implicit owner/PUBLIC seeds stay plain. Catalog grant-option primitive (table
slice 332) + `renderACLLetters` `*` projection are object-type-agnostic, so no
catalog change. Tests: `TestProcACLGrantWithGrantOption` (catalog) + slice-348
fixture/assert in `TestPort_PgDumpConnectionSetup`. Gates: catalog+server+initdb+
testport connsetup PASS; build clean; pgbench smoke (pre-commit).
Design 0119-0004-function-grant-option-proacl-pgdump.md.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one:
  pg_attribute is HEAP-backed, needs delete-old-rows + syncTableToCatalogHeap,
  which the server GRANT short-circuit cannot reach without executor routing).
- database GRANT (`datacl`, only dumped under `--create`; the harness runs
  pg_dump with `--no-sync` only, so untestable there as-is).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
