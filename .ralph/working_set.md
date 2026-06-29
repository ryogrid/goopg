Loop #62 — M0119-0004 **`GRANT … then partial REVOKE` relacl round-trip in
pg_dump** (DU-002 slice 338) — LANDED, pending final isolation gate + commit.

REVOKE was a documented pure no-op, so after `GRANT SELECT, INSERT … TO r` then
`REVOKE INSERT … FROM r`, the GRANT recorder's `r=ar/postgres` survived and
pg_dump over-emitted the revoked INSERT (silent ACL drift on restore). Real PG
clears the bit → relacl `r=r/postgres` → pg_dump emits only `GRANT SELECT`.

Fix:
- catalog.go: new interface method + `InMemory.RevokeTablePrivilege(relOID,
  role, priv)` — lower/upper-case symmetric with grant; delete the bit; drop the
  grantee entry when its set empties, drop the whole relOID entry when no
  grantees remain (relacl→NULL); no-op for never-held priv; retains slice-337
  `roleACLDisplay`.
- server/grant_ddl.go: new `tryRecordTableRevoke` mirrors tryRecordTableGrant
  (parses `REVOKE <privs> ON [TABLE|SEQUENCE] <objs> FROM <roles>
  [CASCADE|RESTRICT]`; shares parseGrantPrivileges/splitGrantList/
  nonTableGrantObjects; bails on column-level/`GRANT OPTION FOR`/non-table).
- server/query.go: intercepts single-statement autocommit REVOKE symmetric with
  GRANT (record → CommandComplete("REVOKE")); explicit-txn REVOKE unchanged.

Files: internal/catalog/catalog.go, internal/catalog/relacl_test.go (new
TestRevokeTablePrivilege), internal/server/grant_ddl.go, internal/server/query.go,
internal/testport/pgdump_connsetup_test.go (slice-338 fixture revoke_t + assert),
docs/design/0119-0004-revoke-relacl-pgdump.md (+README 0119-0004ao),
.ralph/fix_plan.md.

Gates: catalog units + slice-338 TestPort_PgDumpConnectionSetup (byte-identical
vs real pg_dump 18.3, 5.1s) PASS; catalog+server pkgs PASS; full build clean;
isolation port suite (truncate-conflict ACL) running at write time. (pgbench
smoke = pre-commit hook.)

NEXT loop — remaining M0119-0004 GRANT slices: REVOKE-of-default (owner-side
implicit-privilege revoke — materializes a sub-default owner aclitem + a REVOKE
line in the dump; distinct from this grantee-side partial revoke). Then
column-level (`pg_attribute.attacl`, heap re-sync, the entangled one) /
database (`datacl`, needs `--create`). Extended-protocol commit-time deferral
stays architecturally entangled.
