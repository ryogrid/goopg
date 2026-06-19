(idle — nothing in flight)

Last landed: DU-002 slice 257 (loop #23) — a composite-type field with an explicit
per-field `COLLATE` (`CREATE TYPE x AS (a text COLLATE "C", …)`) round-trips through
pg_dump. Previously the parser folded the `COLLATE "C"` into ColType, mis-resolving the
type and silently dropping the collation. Fix mirrors the table-column path (slice 188).

Mechanism:
- Parser (parseCreateType composite loop, ddl.go): type-token loop now also stops at a
  top-level `COLLATE` ident keyword (Kind-agnostic break, like ALTER ATTRIBUTE), so the
  type stays clean (`text`); optional trailing `COLLATE <name>` parsed via
  parseCollationName (bare `"C"` + schema-qualified `pg_catalog."C"` → bare last
  component) into new TypeField.Collation.
- AST/catalog: parser.TypeField + catalog.CompositeField each +Collation string;
  executor CREATE TYPE path (operators_ddl.go ~10257) propagates f.Collation.
- Heap row (buildUserPGAttributeRowForCompositeField, pg18_user_catalog_rows.go):
  attcollation applies the per-field override exactly as the table-column builder
  (slice 188) — collatable type (typcollation!=0) + collationNameToOID hit (C→950,
  POSIX→951) → stamp override; COLLATE on non-collatable (int, typcoll 0) suppressed.
  Flows through syncCompositeTypeToCatalogHeap (same builder). No pg_dump-side change.

Files: internal/parser/ast.go (TypeField.Collation), internal/catalog/catalog.go
(CompositeField.Collation), internal/parser/ddl.go (parseCreateType COLLATE break+parse),
internal/parser/m0097_0017_test.go (+TestCompositeFieldCollateParsing),
internal/executor/operators_ddl.go (propagate f.Collation),
internal/executor/pg18_user_catalog_rows.go (attcollation override),
internal/executor/pg18_user_catalog_rows_test.go (+TestUserPGAttributeCompositeFieldCollation),
internal/testport/pgdump_connsetup_test.go (coll_comp create + assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 257), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; parser+catalog+executor unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.3s, real pg_dump round-trips coll_comp); pgbench
pre-commit smoke runs on commit.

Next (slice 258+): multi-subcommand ALTER TYPE (`ADD …, DROP …` in one statement) and
per-attribute COLLATE via `ALTER TYPE … ADD ATTRIBUTE`.
