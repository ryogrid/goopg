(idle — nothing in flight)

Last loop (#55): M0119-0004 **`GRANT … WITH GRANT OPTION` (`relacl` `*`)
round-trip in pg_dump** (DU-002 slice 332) — LANDED, committed, pushed.

Slice 331 surfaced the table GRANT store into `pg_class.relacl` but dropped the
grant option. PG records the option as a `*` suffix (`aclitemout` →
`grantee2_role=r*/postgres`); pg_dump's `buildACLCommands` (dumputils.c) splits
each grantee's privileges into `privs`/`privswgo` and emits the latter as a
dedicated `GRANT … WITH GRANT OPTION;`. Fix:
- `InMemory.tableACLs` inner value `map[priv]struct{}` → `map[priv]bool` (bool =
  grant option; drop-in for set-membership reads → `truncate-conflict`
  enforcement unaffected).
- New interface method `GrantTablePrivilegeWithGrantOption`; existing 3-arg
  `GrantTablePrivilege` delegates with false. Flag OR-ed in (a later plain GRANT
  never clears an existing option — matches PG `REVOKE GRANT OPTION FOR`).
- `relaclTextLocked` appends `*` after the canonical-order privilege letter.
- `tryRecordTableGrant` (grant_ddl.go) passes `withGrantOption = (WITH-tail ==
  "grant option")`.

Files: internal/catalog/catalog.go (field type + interface method + impl +
relaclTextLocked `*`), internal/catalog/relacl_test.go (new
TestRelaclTextGrantOption), internal/server/grant_ddl.go (WITH-tail detect),
internal/testport/pgdump_connsetup_test.go (slice-332 fixture+assert),
docs/design/0119-0004-grant-option-relacl-pgdump.md (+README 0119-0004ai),
.ralph/fix_plan.md.

Gates: TestRelaclTextGrantOption + slice-332 TestPort_PgDumpConnectionSetup
(byte-identical vs real pg_dump 18.3, 4.7s) PASS; catalog/server/executor +
truncate-conflict isolation PASS; build clean. (pgbench smoke runs in pre-commit
hook.)

NEXT loop — further M0119-0004 pg_dump slices: column-level GRANT
(`pg_attribute.attacl`), sequence GRANT (relkind 'S', `acldefault('s')`), schema
GRANT (`pg_namespace.nspacl`), REVOKE-of-default modelling,
reserved-keyword-named-role quoting (needs a keyword table). Extended-protocol
commit-time deferral stays architecturally entangled (auto-commit-per-statement).
