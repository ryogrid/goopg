(idle — nothing in flight)

Last loop (#57): M0119-0004 **`GRANT … TO PUBLIC` (`relacl` empty grantee)
round-trip in pg_dump** (DU-002 slice 334) — LANDED, committed, pushed.

PG stores a grant to the PUBLIC pseudo-role with an EMPTY grantee in
pg_class.relacl (`=r/postgres`); pg_dump's buildACLCommands renders an empty
grantee (`grantee->len == 0`) as the keyword PUBLIC →
`GRANT SELECT ON TABLE public.grant_pub TO PUBLIC;`. Slices 331–333 rendered
every grantee under its stored name, so a grant to PUBLIC (recorded under the
lower-cased reserved name `public`) would have mis-rendered as `public=r/postgres`
→ `TO public`. Fix was rendering-only:
- catalog.go: new `publicPseudoRole = "public"`; `relaclTextLockedFor` maps that
  role key to the empty grantee `""`.
- NO grant_ddl.go change (`GRANT … TO PUBLIC` already records under `public`).
- Shared OID-keyed ACL store → HasTablePrivilege/truncate-conflict untouched;
  grant-option + sequence grants to PUBLIC round-trip via the shared path.

Files: internal/catalog/catalog.go, internal/catalog/relacl_test.go (new
TestRelaclTextPublic), internal/testport/pgdump_connsetup_test.go (slice-334
fixture+assert), docs/design/0119-0004-grant-public-relacl-pgdump.md (+README
0119-0004ak), .ralph/fix_plan.md.

Gates: TestRelaclTextPublic + slice-334 TestPort_PgDumpConnectionSetup
(byte-identical vs real pg_dump 18.3, 4.9s) PASS; catalog/server +
truncate-conflict isolation PASS; build clean. (pgbench smoke = pre-commit hook.)

NEXT loop — further M0119-0004 pg_dump slices: column-level GRANT
(`pg_attribute.attacl` — needs heap re-sync, see [[pg_attribute_alter_needs_heap_resync]]),
schema GRANT (`pg_namespace.nspacl` — new store + dumpNamespace path), database
GRANT (`pg_database.datacl`), REVOKE-of-default modelling, reserved-keyword-named-role
quoting. Extended-protocol commit-time deferral stays architecturally entangled
(auto-commit-per-statement).
