(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 353** (same-privilege multi-grantee table,
`pg_class.relacl`, round-trip in pg_dump). Test-only — NO engine change. Two grantees
granted the SAME privilege (`GRANT SELECT … TO sg_role_a` then `GRANT SELECT … TO
sg_role_b`) materialize relacl `{postgres=arwdDxtm/postgres,sg_role_a=r/postgres,
sg_role_b=r/postgres}`; pg_dump's buildACLCommands fans out one GRANT SELECT line per
grantee → BOTH lines, NO merge (verified byte-identical vs real PG 18.3 in
./postgres/local_install). Companion to slice 352 (differing-priv): same-priv is the
most tempting grantee-merge case, so adds explicit negative assert vs `TO a, b`. Each
GRANT records under OID-keyed `tableACLs`; `relaclTextLockedFor` sorts grantees via
sort.Strings (here == PG grant order). Fixture + asserts added to
`TestPort_PgDumpConnectionSetup`. Design 0119-0004-same-priv-multi-grantee-table-relacl-pgdump.md.
Gates: connsetup slice 353 PASS; catalog suite PASS; build clean; pgbench smoke (pre-commit).

KNOWN DIVERGENCE (not yet chased): goopg's relaclTextLockedFor renders grantees in
sort.Strings (alphabetical) order, but PG preserves GRANT ORDER in the relacl aclitem
array. They agree only when grants are issued alphabetically (all fixtures so far). A
reverse-grant-order fixture (GRANT to z-role before a-role) would expose pg_dump line
ORDER divergence. Fixing = preserve insertion order across ALL ACL stores (table/seq/
schema/function) + re-verify every existing slice fixture — bigger change, deferred.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — entangled).
- database GRANT (`datacl`, only under `--create`; harness runs --no-sync only).
- TYPE/DOMAIN GRANT (`pg_type.typacl`): grant_ddl.go:232 bails on non-table/sequence,
  so typacl is UNMODELLED — genuine new ACL surface (engine work, more risk).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
