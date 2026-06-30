(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 356** (Partial-REVOKE-keeps-slot in relacl —
TEST-ONLY, no engine change). Complement of slice 355. PG's aclupdate (acl.c)
distinguishes a FULL revoke (privilege count → 0 → aclitem DELETED, later GRANT
re-appends at END) from a PARTIAL revoke (bits removed, entry survives → modified
IN PLACE, array index unchanged). A grantee that keeps ≥1 privilege after REVOKE
stays in its original grant-order slot. Verified vs real PG 18.3
(./postgres/local_install): GRANT SELECT,INSERT→pr_a; GRANT SELECT→pr_b; REVOKE
INSERT FROM pr_a → {postgres=arwdDxtm/postgres,pr_a=r/postgres,pr_b=r/postgres}
(pr_a stays AHEAD of pr_b).

No production code changed: RevokeTablePrivilege (internal/catalog/catalog.go)
calls dropTableACLOrderRole ONLY when the grantee's privilege set empties
(len(privs)==0), so a partial revoke leaves tableACLOrder untouched. Added unit
catalog/relacl_test.go:TestRelaclTextPartialRevokeKeepsSlot (partial-keeps-slot +
contrast guard: full revoke + re-grant → pr_a appends after pr_b). Design
0119-0004-partial-revoke-keeps-slot-relacl.md + README row 0119-0004bf. Gates:
catalog TestRelacl PASS; build clean; state-guard OK; pgbench smoke = pre-commit.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper engine work):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — entangled).
- database GRANT (`datacl`, only under `--create`; harness runs --no-create).
- TYPE/DOMAIN GRANT (`pg_type.typacl`): grant_ddl.go bails on non-table/sequence
  → typacl UNMODELLED; new ACL surface (pg_type virtual — check tractability).
- Extended-protocol commit-time deferral stays architecturally entangled
  ([[goopg_extended_protocol_autocommit]]).
