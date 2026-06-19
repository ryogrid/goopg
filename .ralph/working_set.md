(idle — nothing in flight)

Last landed: DU-002 slice 188 (loop #156) — per-column COLLATE round-trip AND
closed a silent slice-187 regression of TestPort_PgDumpConnectionSetup.

What happened: slice 187 (populating pg_collation) silently regressed the pg_dump
TAP test. goopg's virtual pg_attribute reported attcollation=100 for
text/varchar/bpchar, but the bootstrapped pg_type heap hardcoded typcollation=0.
Once pg_collation had OID 100, findCollationByOid(100) resolved and pg_dump
spuriously emitted `COLLATE pg_catalog."default"` on EVERY collatable column.
Discovered while adding slice 188's explicit-COLLATE test (the d-column check fired);
confirmed pre-existing by `git stash` + re-run at HEAD.

Fix (two coupled parts):
 (a) Parser now captures `COLLATE <name>` (was discard) via parseCollationName →
     ColumnDef.Collation → both CREATE TABLE paths in operators_ddl.go →
     catalog.Column.Collation → buildUserPGAttributeRow resolves name→OID
     (collationNameToOID) and reports attcollation (collatable types only).
 (b) pg_type heap typcollation set to PG-canonical via pgTypeCollationForOID
     (name→950, text/bpchar/varchar/_text→100, else 0) — matches pg_type.dat AND
     executor.userTypeAttrsForOID's attcollation (sibling-path invariant).

Files: internal/parser/{ast.go,ddl.go,ddl_test.go}, internal/catalog/catalog.go,
internal/executor/{operators_ddl.go,pg18_user_catalog_rows.go},
internal/initdb/{pg_type_bootstrap.go,pg_type_bootstrap_test.go},
internal/testport/pgdump_connsetup_test.go, docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt OK; build clean; parser/catalog/initdb/executor PASS;
TestPort_PgDumpConnectionSetup PASS (was FAILING at HEAD); pgbench smoke on commit.

Next (slice 189 candidates): (1) MINVALUE/MAXVALUE keyword-AST-node slice (HIGHER
RISK: partition routing). (2) attfdwoptions (foreign-table only, NULL today).
(3) audit other built-in pg_type.typcollation vs userTypeAttrsForOID for any
remaining mismatches (e.g. pg_node_tree=100 in PG but 0 in goopg — currently
harmless since pg_dump never sees system-catalog columns).
