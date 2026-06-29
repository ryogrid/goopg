(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 340** (owner-side REVOKE-of-default —
relacl round-trip in pg_dump). PostgreSQL leaves relacl NULL while the owner
holds its implicit default privs; the first owner-side REVOKE materializes
relacl = owner default − revoked. `REVOKE TRIGGER ON TABLE ownrev_t FROM
postgres` → `{postgres=arwdDxm/postgres}`; pg_dump re-emits `REVOKE ALL … FROM
postgres;` + `GRANT <surviving> … TO postgres;`. Fix: catalog new
`MaterializeOwnerACL` (records owner aclitem once) + renderer special-cases the
owner key via extracted `renderACLLetters`; server `tryRecordTableRevoke` calls
it when grantee==owner. Gates: TestMaterializeOwnerACL (new) +
TestPort_PgDumpConnectionSetup slice-340 (byte-identical vs pg_dump 18.3) PASS;
build clean. Committed + pushed.

NEXT M0119-0004 GRANT/ACL slices (pick one next loop):
- owner revoke-ALL → PG stores empty `{}` array (distinct from NULL); goopg
  currently drops to NULL. Needs an "empty owner entry" sentinel in the renderer.
- schema/sequence owner-revoke (MaterializeOwnerACL is type-agnostic; wire the
  recordSchemaRevoke path + the sequence branch).
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one).
- database GRANT (`datacl`, only dumped under `--create`).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
