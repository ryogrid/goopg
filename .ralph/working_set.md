(idle — nothing in flight)

Last landed: DU-002 slice 259 (loop #25) — a per-attribute `COLLATE` on
`ALTER TYPE … ALTER ATTRIBUTE attname TYPE newtype COLLATE "POSIX"` now round-trips
through pg_dump. Slice 256 re-typed a composite field in place but stub-consumed the
trailing COLLATE, so a re-typed attribute's collation was silently dropped from the dump.
Reuses slice 256 (ALTER ATTRIBUTE re-type re-sync) + slice 257 (field.Collation →
attcollation) machinery.

Mechanism:
- Parser (parseAlterType ALTER ATTRIBUTE branch, ddl.go ~5715): the type-token loop
  already stopped at a top-level COLLATE ident keyword (slice 256); instead of
  stub-consuming it, the branch now parses optional `COLLATE <name>` via
  parseCollationName into new AlterTypeStmt.AlterAttrCollation. USING/CASCADE/RESTRICT
  stay stub-consumed.
- AST: parser.AlterTypeStmt +AlterAttrCollation string.
- Executor (execAlterType ALTER ATTRIBUTE, operators_ddl.go ~10455): alongside
  newFields[idx].ColType = s.AlterAttrType, now sets newFields[idx].Collation =
  s.AlterAttrCollation (PG semantics: re-type replaces type → prior collation gone;
  explicit COLLATE sets new, absence resets to default/empty). OID-stable re-sync stamps
  attcollation from field.Collation (C→950/POSIX→951, non-collatable suppressed).
- No pg_dump-side change: dumpCompositeType re-emits COLLATE inline.

Files: internal/parser/ast.go (AlterAttrCollation), internal/parser/ddl.go (ALTER
ATTRIBUTE COLLATE capture), internal/parser/m0097_0017_test.go
(TestAlterTypeAlterAttributeParsing +collation assertions),
internal/executor/operators_ddl.go (propagate s.AlterAttrCollation),
internal/testport/pgdump_connsetup_test.go (re-type cc → text COLLATE "POSIX" + flip dump
assertion C→POSIX), docs/design/0110-0001-pg-dump-tap-port.md (Slice 259), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; parser+executor unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.7s); pgbench pre-commit smoke runs on commit.

Next (slice 260+): multi-subcommand ALTER TYPE (`ADD …, DROP …` in one statement).
