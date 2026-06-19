(idle — nothing in flight)

Last landed: DU-002 slice 258 (loop #24) — a per-attribute `COLLATE` on
`ALTER TYPE … ADD ATTRIBUTE col type COLLATE "C"` now round-trips through pg_dump.
Previously the ADD ATTRIBUTE parser branch stub-consumed the trailing COLLATE, so the
new field's collation was silently dropped from the dump. Reuses slice 253 (ADD
ATTRIBUTE re-sync) + slice 257 (composite-field COLLATE → attcollation) machinery.

Mechanism:
- Parser (parseAlterType ADD ATTRIBUTE branch, ddl.go ~5549): type-token loop now stops
  at a top-level `COLLATE` ident keyword (Kind-agnostic break, like slice 257's CREATE
  TYPE loop) so the type stays clean; optional trailing `COLLATE <name>` parsed via
  parseCollationName into new AlterTypeStmt.AddAttrCollation.
- AST: parser.AlterTypeStmt +AddAttrCollation string.
- Executor (execAlterType ADD ATTRIBUTE, operators_ddl.go ~10326): appended
  catalog.CompositeField carries Collation: s.AddAttrCollation. Downstream unchanged —
  OID-stable re-sync (RegisterCompositeTypeWithFields + syncCompositeTypeToCatalogHeap)
  runs buildUserPGAttributeRowForCompositeField, which already (slice 257) stamps
  attcollation from field.Collation (C→950/POSIX→951, non-collatable suppressed).
- No pg_dump-side change: dumpCompositeType already re-emits COLLATE inline.

Files: internal/parser/ast.go (AddAttrCollation), internal/parser/ddl.go (ADD ATTRIBUTE
COLLATE break+parse), internal/parser/m0097_0017_test.go (+TestAlterTypeAddAttributeCollateParsing),
internal/executor/operators_ddl.go (propagate s.AddAttrCollation),
internal/testport/pgdump_connsetup_test.go (alt_comp ADD ATTRIBUTE cc text COLLATE "C" + assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 258), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; parser+executor unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.7s, real pg_dump round-trips alt_comp.cc); pgbench
pre-commit smoke runs on commit.

Next (slice 259+): multi-subcommand ALTER TYPE (`ADD …, DROP …` in one statement) and
per-attribute COLLATE via `ALTER TYPE … ALTER ATTRIBUTE … COLLATE`.
