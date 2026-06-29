(idle — nothing in flight)

Last loop (#58): M0119-0004 **`GRANT … ON SCHEMA` (`nspacl`) round-trip in
pg_dump** (DU-002 slice 335) — LANDED, committed, pushed (commit pending below).

pg_dump's getNamespaces reads pg_namespace.nspacl, diffs vs acldefault('n',10) =
`{postgres=UC/postgres}`, dumpACL (objtype SCHEMA) emits
`GRANT USAGE ON SCHEMA grant_sch TO schema_role;`. goopg lost it two ways:
grant_ddl bailed on `schema` (no-op) + pg_namespace hard-coded nspacl=NULL.
Fix (rendering+record only): schema OIDs share nextOID with relations (no
collision) → reuse OID-keyed ACL store + object-agnostic relaclTextLockedFor.
- catalog.go: schemaACLPrivOrder (USAGE 'U' < CREATE 'C'), ownerSchemaACLString
  "UC", NamespaceACLText(oid); pg_namespace VirtualRows now projects it.
- server/grant_ddl.go: allSchemaPrivileges {USAGE,CREATE}; tryRecordTableGrant
  strips leading SCHEMA keyword → recordSchemaGrant (resolves SchemaOID).
- Shared core → grant-option + TO PUBLIC round-trip; HasTablePrivilege/
  truncate-conflict untouched; system schemas stay NULL → zero blast radius.

Files: internal/catalog/catalog.go, internal/catalog/relacl_test.go (new
TestNamespaceACLText), internal/server/grant_ddl.go,
internal/testport/pgdump_connsetup_test.go (slice-335 fixture+assert),
docs/design/0119-0004-schema-grant-nspacl-pgdump.md (+README 0119-0004al),
.ralph/fix_plan.md.

Gates: TestNamespaceACLText + slice-335 TestPort_PgDumpConnectionSetup
(byte-identical vs real pg_dump 18.3, 4.8s) PASS; catalog/server +
truncate-conflict isolation PASS; build clean. (pgbench smoke = pre-commit hook.)

NEXT loop — further M0119-0004 pg_dump GRANT slices: column-level GRANT
(`pg_attribute.attacl` — needs heap re-sync, see [[pg_attribute_alter_needs_heap_resync]]),
database GRANT (`pg_database.datacl` — new store + dumpDatabase path),
REVOKE-of-default modelling, reserved-keyword-named-role quoting. Extended-protocol
commit-time deferral stays architecturally entangled (auto-commit-per-statement).
