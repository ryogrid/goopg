(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 352** (multi-grantee table, `pg_class.relacl`,
round-trip in pg_dump). Test-only — NO engine change. Two distinct grantees on one
table each emit their own GRANT line. `GRANT SELECT … TO mg_role_a` then `GRANT INSERT
… TO mg_role_b` → relacl `{postgres=arwdDxtm/postgres,mg_role_a=r/postgres,mg_role_b=a/
postgres}`; pg_dump's buildACLCommands fans out one `GRANT <privs> ON TABLE … TO
<grantee>;` per non-owner aclitem → BOTH the SELECT (mg_role_a) and INSERT (mg_role_b)
lines, no merge (verified vs real PG 18.3 — relacl + ACL lines captured). No production
code: each GRANT records under OID-keyed `tableACLs`; `relaclTextLockedFor` sorts
grantees via sort.Strings (here matches PG grant order). Catalog multi-grantee sort
already unit-covered by `TestRelaclText` two-grantee case; slice adds end-to-end fixture
+ per-grantee asserts to `TestPort_PgDumpConnectionSetup`. Design
0119-0004-multi-grantee-table-relacl-pgdump.md. Gates: connsetup slice 352 PASS;
catalog suite PASS; build clean; pgbench smoke (pre-commit).

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — entangled).
- database GRANT (`datacl`, only under `--create`; harness runs --no-sync only).
- TYPE/DOMAIN GRANT (`pg_type.typacl`, always NULL today; new ACL surface).
- multi-grantee differing-priv merge edge (same priv set across grantees still
  emits separate lines — could add a same-priv-set fixture variant).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
