(idle — nothing in flight)

Last landed: DU-002 slice 183 (loop #151) — per-column COMPRESSION method
(`COMPRESSION <m>` inline / `ALTER COLUMN ... SET COMPRESSION <m>`) now round-trips through pg_dump.
Exact analogue of slice 182 (SET STORAGE) for the sibling pg_attribute.attcompression column.

pg_dump's dumpTableSchema emits `ALTER TABLE ONLY <t> ALTER COLUMN <c> SET COMPRESSION <method>;`
when attcompression is 'p' (pglz) or 'l' (lz4); default '\0' emits nothing. Parser previously
DISCARDED the COMPRESSION keyword and buildUserPGAttributeRow hardcoded attcompression="". Fixed in
3 layers (mirroring 182):
  1. Parser: new normalizeCompressionMethod helper (pglz/lz4; default/unknown→""); inline COMPRESSION
     arm stores ColumnDef.Compression; new SET COMPRESSION ALTER arm → AlterTableSetCompression action.
  2. buildUserPGAttributeRow: new compressionNameToAttCode (pglz→'p', lz4→'l'); overrides hardcoded
     default when Column.Compression set. CREATE TABLE threads ColumnDef.Compression→catalog.Column
     .Compression in BOTH column-builder paths (operators_ddl.go ~712 and ~899).
  3. LOAD-BEARING: AlterTableSetCompression executor arm flushes via delete-old-rows +
     syncTableToCatalogHeap re-sync (pg_dump scans the persisted heap, not the live catalog object).
New fields: catalog.Column.Compression, parser.ColumnDef.Compression, AlterTableAction.CompressionType,
AST kind AlterTableSetCompression. Dump-fidelity only (goopg doesn't TOAST/compress).

Files: internal/catalog/catalog.go, internal/parser/ast.go, internal/parser/ddl.go,
internal/executor/pg18_user_catalog_rows.go, internal/executor/operators_ddl.go,
internal/executor/pg18_user_catalog_rows_test.go (TestUserPGAttributeCompressionOverride),
internal/parser/alter_test.go (TestParseAlterTableSetCompression + TestParseCreateTableColumnCompression),
internal/testport/pgdump_connsetup_test.go (cmprcol fixture, 2 positive + 1 negative assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 183), .ralph/fix_plan.md (loop-151 PROGRESS).
Gates: gofmt OK; go vet parser+catalog+executor clean; full parser/catalog/executor PASS;
TestPort_PgDumpConnectionSetup PASS (3.21s); pgbench pre-commit smoke on commit.

Next (slice 184 candidates): (1) deferred MINVALUE/MAXVALUE keyword-AST-node slice (HIGHER RISK:
partition routing). (2) close validateDefaultExpr array/row/CASE/InExpr recursion gap (executor
semantic change — own gates). (3) other pg_dump per-column attribute gaps (attoptions / attfdwoptions
are NULL today; attstattarget dump fidelity).
