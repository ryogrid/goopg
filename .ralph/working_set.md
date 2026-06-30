Task: M0119-0004-ACLHEAP — ACL re-sync from the GRANT path for heap-backed catalogs.
Loop #85 landed the SECOND building block (step-1 parser-capture half),
behaviour-neutral, committed. The milestone continues — the dispatch reroute +
heap-write half remains (a dedicated full-gate loop).

LANDED loop #85 (parser capture, behaviour-neutral — no consumer reads it yet):
  - `parser.CompatNoopStmt.TypeACL *TypeACLChange` (internal/parser/ast.go) — set ONLY
    for `GRANT`/`REVOKE … ON TYPE|DOMAIN …`. Fields: {Revoke, IsDomain, Privileges,
    TypeNames []ObjectName, Grantees, WithGrantOption}.
  - GRANT/REVOKE scan in internal/parser/parser.go gained explicit `ON TYPE`/`ON DOMAIN`
    cases (BEFORE the grantNonTableClass catch-all) → captures token run →
    `buildTypeACLChange` + helpers (tokIndexOf, splitTokRuns, splitTokPrivileges,
    splitTokObjectNames, splitTokRoles, objectNameFromTokens). DatabaseACL/TableACL
    capture UNCHANGED (ON TYPE/DOMAIN already a non-table no-op → TableACL=="").
  - Tests internal/parser/op_grant_typeacl_test.go: TestParseGrantTypeACL +
    TestParseGrantNonTypeLeavesTypeACLNil. NOTE: lexer lower-cases unquoted idents, so
    PUBLIC → "public" (catalog folds it case-insensitively).
  - Gates: full internal/parser suite PASS; `go build ./...` clean; pgbench smoke =
    pre-commit hook. Design doc + fix_plan updated with loop-#85 Progress; box UNCHECKED.

PRIOR (loop #84): renderer `catalog.InMemory.TypeACLText(typeOID)` + typeACLPrivOrder
{USAGE/'U'} + ownerTypeACLString "U", in the Catalog interface (relacl_test.go goldens).

NEXT (the high-blast-radius half — a dedicated full-gate loop):
  1. query.go:69-87 — flip the short-circuit so an autocommit `GRANT/REVOKE … ON
     TYPE|DOMAIN` falls through to `dispatchSimpleQueryViaExecutor` (keep the fast path
     for table/seq/schema/function). Detection: upper has " ON TYPE " / " ON DOMAIN ".
  2b. execCompatNoop (operators_ddl.go:12342): when `s.TypeACL != nil`, resolve each
     TypeName→OID (LookupEnum/Domain/CompositeType[ByOID], catalog.go:9803/10097/10215),
     update OID-keyed ACL store like recordFunctionGrant but USAGE (seed owner+PUBLIC
     USAGE via MaterializeOwnerACL + GrantTablePrivilege when TypeACLText==""), then
     re-sync the pg_type heap row: deleteTypeFromCatalogHeap(ctx,dbOid,oid,xmax) +
     rebuild via buildUserPGTypeRowFor{Enum,Domain,Composite} with row[last]=ACL datum
     from TypeACLText(oid), writeHeapRowCanonical. Expand ALL via per-class default {USAGE}.
  3. mirrorCatalogRelToPostgresDB(ctx, TypeRelationId).
  4. Gates (MANDATORY): new DU-002 connsetup TYPE-grant slice (CREATE TYPE; GRANT USAGE
     ON TYPE → assert `GRANT USAGE ON TYPE` in pg_dump stdout vs PG 18.3) +
     TestE2E_PhysicalReplication + TPC-H Q12/Q13 + pgbench + executor/catalog/parser
     suites. Re-init data dir if pg_type row layout touched.
NOTE: executor test fixture (newVMFixture) has catalogHeapSyncAvailable=false, so the
heap re-sync CANNOT be cheaply unit-tested in isolation — its real gate is the DU-002
connsetup pg_dump round-trip. `attacl` follows same template; `datacl` deferred (--create only).
