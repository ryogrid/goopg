(idle — nothing in flight)

Last landed: DU-002 slice 253 (loop #19) — `ALTER TYPE … ADD ATTRIBUTE col type`
appends a field to an existing composite type; the new attribute round-trips
through pg_dump. Previously the parser's `ADD` branch only knew the enum
`ADD VALUE` form and stub-consumed `ADD ATTRIBUTE` to `;` (silent no-op).

Mechanism:
- Parser (parseAlterType, ddl.go): new `attribute` sub-branch after `ADD` parses
  name + space-joined type tokens (paren-depth tracked for typmod/`[]`, like the
  composite-field collection slice 247) → AlterTypeStmt.AddAttrName/AddAttrType.
- Executor (execAlterType, operators_ddl.go): AddAttrName != "" → LookupCompositeType
  (42704 absent / 42701 dup attr / 42P16 unknown), append field, RE-SYNC heap. The
  composite's 3 OIDs (type/_name array/pg_class relation) are stable across
  RegisterCompositeTypeWithFields, so stamp xmax on existing pg_type×2 + pg_class/
  pg_attribute rows (mirrors execDropType composite branch) + re-run
  syncCompositeTypeToCatalogHeap with the new field list. No pg_dump-side change.

Files: internal/parser/ast.go (AddAttrName/AddAttrType), internal/parser/ddl.go,
internal/parser/m0097_0017_test.go (+TestAlterTypeAddAttributeParsing),
internal/executor/operators_ddl.go (execAlterType ADD ATTRIBUTE block),
internal/testport/pgdump_connsetup_test.go (alt_comp fixture + 3-field asserts),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 253), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; executor+parser+catalog unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.45s, real pg_dump round-trips
`a integer, b text, c numeric(10,2)`); pgbench pre-commit smoke on commit.

Next (slice 254+): ALTER TYPE … DROP/RENAME/ALTER ATTRIBUTE (remaining attribute
subcommands). DROP needs attisdropped handling; RENAME is a pg_attribute attname
re-sync; ALTER … TYPE is a re-resolve of one field's type.
