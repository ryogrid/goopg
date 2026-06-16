Task: DU-002 slice 90 — user-defined DOMAIN survives pg_dump (COMPLETE, commit pending)

Files:
- internal/catalog/catalog.go — LookupDomainByOID + Catalog interface (LookupDomain/LookupDomainByOID)
- internal/executor/pg18_user_catalog_rows.go — buildUserPGTypeRowForDomain, pgTypeCategoryForOID, domain branch in buildUserPGAttributeRow
- internal/executor/operators_ddl.go — syncDomainTypeToCatalogHeap, execCreateDomain wire, execDropDomain xmax stamp
- internal/executor/expr.go — pg_get_expr(NULL)→NULL; format_type domain branch
- internal/executor/pg18_user_catalog_rows_test.go — TestUserPGAttributeDomainColumn
- internal/testport/pgdump_connsetup_test.go — fixture CREATE DOMAIN zipcode + dom table + asserts
- docs/design/0110-0001-pg-dump-tap-port.md — slice 90 narrative

Key symbols: buildUserPGTypeRowForDomain, syncDomainTypeToCatalogHeap, LookupDomainByOID,
Column.DeclaredTypeName (domain columns store base name + declared domain name).

Findings: domain columns are stored with Type.Name=base (ResolveColumnType) and
DeclaredTypeName=domain; key the attr-row remap off DeclaredTypeName, not Type.Name.
pg_get_expr(NULL) must return NULL (not "") or dumpDomain emits spurious `DEFAULT `.

Gates run: catalog/analyzer/planner/parser/executor PASS; TestPort_PgDumpConnectionSetup PASS;
partition + analyzer pub_query (pg_get_expr) PASS. Pre-commit hook (pgbench smoke) runs on commit.

Next step: commit. Then slice 91 = add next object type (composite type / range / domain CHECK)
to the fixture, run the test, find the real next blocker.
