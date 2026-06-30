(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 354** (ACL grantee GRANT-ORDER preservation —
ENGINE change in internal/catalog). Fixed the KNOWN DIVERGENCE flagged in loop #78's
working_set: goopg rendered relacl/proacl/nspacl grantees via `sort.Strings`
(alphabetical), but PG's `aclupdate` (acl.c) APPENDS a new grantee's aclitem to the end
→ array preserves GRANT ORDER. They coincide only for alphabetical grant sequences
(every prior fixture, which masked the bug). Verified vs real PG 18.3
(./postgres/local_install): `GRANT … TO z_role` then `… TO a_role` →
`{postgres=arwdDxtm/postgres,z_role=r/postgres,a_role=r/postgres}` (z before a).

Fix (catalog.go): new `tableACLOrder map[uint32][]string` tracks per-relation first-grant
order of non-owner grantees. GrantTablePrivilegeWithGrantOption appends on first
appearance; RevokeTablePrivilege drops via `dropTableACLOrderRole` on full revoke; all 4
`delete(tableACLs,oid)` teardown sites mirror into tableACLOrder;
`relaclTextLockedFor` iterates the order list (skip owner + stale, sorted-append backstop
so a grant is never silently dropped) instead of sort.Strings. ONE store + ONE render
core ⇒ covers relacl/proacl/nspacl uniformly (working_set #78 overestimated scope as
4 stores). Owner-first rendering unchanged (PG's PUBLIC-before-owner function default
differs but is pg_dump-invisible).

Tests: FOUR relacl_test.go units CORRECTED — they had encoded the OLD alphabetical order;
real-PG verification proved grant-order correct (grantee_role before another_role; PUBLIC
before named_role; grantee_fn LAST). New DU-002 slice 354 in connsetup (reverse-order
grant; z-before-a GRANT lines asserted via strings.Index). slices 352/353 comments
de-staled. Design 0119-0004-acl-grant-order-relacl.md + README index row 0119-0004bd.
Gates: catalog+executor+initdb suites PASS; connsetup slice 354 PASS; build clean;
gofmt diffs are pre-existing version-mismatch noise (none touch my edits); pgbench smoke
= pre-commit hook.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper — all genuine engine work):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — entangled).
- database GRANT (`datacl`, only under `--create`; harness runs --no-sync only).
- TYPE/DOMAIN GRANT (`pg_type.typacl`): grant_ddl.go bails on non-table/sequence, so
  typacl is UNMODELLED — new ACL surface.
- Extended-protocol commit-time deferral stays architecturally entangled
  ([[goopg_extended_protocol_autocommit]]).
