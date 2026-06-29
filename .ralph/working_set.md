(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 344** (owner-zero coexisting with a
grantee — `{grantee=…/postgres}` round-trip in pg_dump). The follow-up the
empty-array slices 341/342/343 deferred. After `REVOKE ALL ON TABLE ownerzero_t
FROM postgres` empties relacl to `{}`, a later `GRANT SELECT … TO bob`
re-materializes the array but PG keeps the owner at zero (absent):
`relacl = {bob=r/postgres}`. pg_dump emits BOTH the owner `REVOKE ALL` and the
grantee `GRANT`. Fix (catalog-only, internal/catalog/catalog.go):
`relACLEmptied` re-interpreted as "owner explicitly zero (absent)";
`GrantTablePrivilegeWithGrantOption` clears the flag only for an owner-side
GRANT; `relaclTextLockedFor` suppresses the leading owner entry when the flag is
set. Object-type-agnostic (OID-keyed). Tests:
TestRevokeAllFromOwnerThenGrantGrantee (catalog/relacl_test.go) + slice-344
fixture/assert in TestPort_PgDumpConnectionSetup. Gates: catalog+server suites +
TestPort_PgDumpConnectionSetup PASS; build clean. Committed + pushed.

NEXT M0119-0004 GRANT/ACL slices (the REVOKE-ALL-empty-array + owner-zero family
is now complete; remaining are deeper):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one).
- database GRANT (`datacl`, only dumped under `--create`).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
