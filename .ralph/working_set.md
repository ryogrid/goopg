(idle — nothing in flight)

Last loop (#53): M0119-0004 **named-role `CREATE POLICY ... TO <role>` round-trip
in pg_dump** (DU-002 slice 330) — LANDED, committed.

Built the per-role OID registry (the prerequisite the working set flagged for both
named-role policies AND GRANT/ACL relacl): `InMemory.roles` map[string]struct{} →
map[string]uint32; `RegisterRole` mints a catalog-counter OID idempotently; new
`Catalog.RoleOID(name)(uint32,bool)` (postgres→10). `pg_roles` VirtualRows now
exposes every registered role. `execCreatePolicy` resolves each TO role → OID
(42704 unknown; PUBLIC/empty → {0}) into pg_policy.polroles.

Also fixed a latent bug: the `quote_ident` SQL builtin (expr.go:8376)
unconditionally double-quoted, so pg_dump's getPolicies ARRAY(SELECT quote_ident
…) resolver emitted ` TO "pol_role"` not bare ` TO pol_role`; now delegates to the
existing conditional-quoting `pgQuoteIdent`.

The pg_policy projection + goopg's ARRAY(SELECT … = ANY(arr))/array_to_string/
quote_ident stack already worked (PUBLIC fixtures exercised the `CASE … = '{0}'`
short-circuit), so only the registry + pg_roles exposure + quote_ident fix were
missing.

Files: internal/catalog/catalog.go (roles map type, RegisterRole/RoleOID,
pg_roles VirtualRows, interface), internal/executor/operators_ddl.go
(execCreatePolicy), internal/executor/expr.go (quote_ident → pgQuoteIdent),
internal/catalog/role_oid_test.go (new), internal/testport/pgdump_connsetup_test.go
(slice-330 fixture+assert), docs/design/0119-0004-named-role-policy-pgdump.md
(+README 0119-0004ag).

Gates: catalog/executor/parser/server suites PASS; TestPort_PgDumpConnectionSetup
PASS (real pg_dump 18.3, byte-identical, 4.7s); go build ./... clean; pgbench
smoke via pre-commit hook.

NEXT loop — **GRANT/ACL relacl** is now UNBLOCKED (per-role OID registry exists).
pg_dump reads relacl as an aclitem[]; getTableAttrs/dumpACL needs per-relation ACL
projection + the aclexplode/quote_ident name resolution. Or richer CREATE RULE
(action deparse), or reserved-keyword-named-role quoting (needs a keyword table).
Extended-protocol commit-time deferral stays architecturally entangled.
