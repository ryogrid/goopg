(idle — nothing in flight)

Last loop (#56): M0119-0004 **`GRANT … ON SEQUENCE` (sequence `relacl`)
round-trip in pg_dump** (DU-002 slice 333) — LANDED, committed, pushed.

pg_dump treats sequences as relations: getTables reads `c.relacl` for relkind
'S' and diffs against `acldefault('s', owner)` = `{postgres=rwU/postgres}`;
dumpTableSchema passes objtype "SEQUENCE" to dumpACL. goopg lost the privilege
two ways — grant_ddl.go bailed on `ON SEQUENCE` (no-op), and relaclTextLocked
was hard-wired to the table privilege order (dropped USAGE, wrong owner
baseline). Fix:
- catalog.go: named type `aclPrivLetter`; new `sequenceACLPrivOrder` (r/w/U) +
  `ownerSequenceACLString` "rwU"; refactor `relaclTextLocked` →
  `relaclTextLockedFor(relOID, privOrder, ownerStr)` core + `relaclTextLockedSeq`
  sibling; builder calls the seq variant when `t.IsSequence`. Grant-option `*`
  logic shared.
- grant_ddl.go: remove `sequence` from `nonTableGrantObjects`; strip a leading
  `SEQUENCE` keyword; expand ALL→`allSequencePrivileges` (USAGE/SELECT/UPDATE);
  `parseGrantPrivileges` takes the applicable set as a param.
- Shared OID-keyed ACL store → `truncate-conflict` enforcement untouched.

Files: internal/catalog/catalog.go, internal/catalog/relacl_test.go (new
TestRelaclTextSequence), internal/server/grant_ddl.go,
internal/testport/pgdump_connsetup_test.go (slice-333 fixture+assert),
docs/design/0119-0004-sequence-grant-relacl-pgdump.md (+README 0119-0004aj),
.ralph/fix_plan.md.

Gates: TestRelaclTextSequence + slice-333 TestPort_PgDumpConnectionSetup
(byte-identical vs real pg_dump 18.3, 4.8s) PASS; catalog/server +
truncate-conflict isolation PASS; build clean. (pgbench smoke = pre-commit hook.)

NEXT loop — further M0119-0004 pg_dump slices: column-level GRANT
(`pg_attribute.attacl` — needs heap re-sync, see [[pg_attribute_alter_needs_heap_resync]]),
schema GRANT (`pg_namespace.nspacl`), database GRANT (`pg_database.datacl`),
REVOKE-of-default modelling, reserved-keyword-named-role quoting.
Extended-protocol commit-time deferral stays architecturally entangled
(auto-commit-per-statement).
