(idle — nothing in flight)

Loop #13 COMPLETE: M0119-0004 DU-002 slice 374 — typed table
`CREATE TABLE name OF composite_type` now round-trips through pg_dump as the
`OF type` form with NO inline column list (PRODUCTION fix).

Bug: goopg's CREATE TABLE parser had no `OF` arm → syntax error, so a typed
table could not be created or dumped. PG records pg_class.reloftype; pg_dump's
dumpTableSchema appends ` OF <type>` and skips every type-derived column (the
reloftype attr-loop branch), so the dump is `CREATE TABLE public.typedtab OF
public.addr2type;`.

Fix (end-to-end, dump-fidelity): parser arm → CreateTableStmt.OfType (new AST
*ObjectName). execCreateTable looks up the composite (LookupCompositeType),
synthesizes a ColumnDef per field (compositeFieldColumnType parses the stored
ColType token string) through the normal column-build path, and stamps
catalog.Table.OfTypeOID. Surfaced as pg_class.reloftype in BOTH the virtual
VirtualRows builder (pg_dump reads this) and the heap buildUserPGClassRow
sibling. PG keeps attislocal=true on OF-type columns (makeColumnDef default;
transformOfType does not clear it) — pg_dump skips them via the reloftype check,
not attislocal — so no inheritance plumbing. Columns are real: COPY (a,b) +
data row 7\tseven round-trip.

Files:
- internal/parser/ast.go (CreateTableStmt.OfType)
- internal/parser/ddl.go (parseCreateTableTail OF arm; rejects (col WITH OPTIONS))
- internal/catalog/catalog.go (Table.OfTypeOID + virtual pg_class reloftype via relOfType local)
- internal/executor/operators_ddl.go (execCreateTable OF block + compositeFieldColumnType helper)
- internal/executor/pg18_user_catalog_rows.go (heap pg_class reloftype sibling)
- internal/executor/pg18_user_catalog_rows_test.go (TestUserPGClassRowOfType + TestCompositeFieldColumnType)
- internal/testport/pgdump_connsetup_test.go (public.addr2type/typedtab fixture + asserts)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 374)
- .ralph/fix_plan.md + .ralph/deferral_ledger.md (slice 374)

Gates: unit + TestPort_PgDumpConnectionSetup PASS (5.9s, byte-identical vs real
pg_dump 18.3, ref /tmp/du374_pgdata); parser/catalog/executor suites PASS;
build/gofmt(my lines)/vet clean; pgbench smoke = pre-commit hook. No TPC-H
(metadata-only catalog-row builder; typed tables absent from TPC-H schema).

Deferred (ledger): per-column `OF type (col WITH OPTIONS …)` form rejected;
non-public-schema composite OF uncovered; pg_class.reltype stays 0.

Next loop: pick a fresh M0119-0004 pg_dump slice via empirical probe (throwaway
zz_probe_test.go dumping a feature-rich schema → diff vs real pg_dump 18.3).
Coverage is very deep; typed tables was the cleanest remaining divergence found.
