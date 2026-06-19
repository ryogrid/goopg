(idle — nothing in flight)

Last landed: DU-002 slice 255 (loop #21) — `ALTER TYPE … DROP ATTRIBUTE [IF EXISTS]
attname` removes a composite type field in place; the dropped attribute disappears
from pg_dump. Previously `DROP ATTRIBUTE` had no parser branch and fell through to the
generic ALTER-TYPE stub (consumed to `;` as a silent no-op), so the field still dumped.

Mechanism:
- Parser (parseAlterType, ddl.go): new `DROP` branch (`DROP` is reserved `KwDrop`) with
  an `attribute` sub-branch parsing optional `IF EXISTS` + name (lower-cased) +
  stub-consumed CASCADE/RESTRICT trailer → AlterTypeStmt.DropAttrName/DropAttrIfExists.
- Executor (execAlterType, operators_ddl.go): DropAttrName != "" → LookupCompositeType
  (42704 absent); scan ct.Fields — missing field + IF EXISTS → PG NOTICE
  `column "%s" of relation "%s" does not exist, skipping` + return; else 42703
  `column "%s" of relation "%s" does not exist`. On hit, splice the field out and
  RE-SYNC heap (same as slices 253/254: composite OIDs stable, stamp xmax on
  pg_type×2 + pg_class/pg_attribute, re-run syncCompositeTypeToCatalogHeap — dropped
  pg_attribute row just doesn't reappear). No pg_dump-side change.

Files: internal/parser/ast.go (DropAttrName/DropAttrIfExists), internal/parser/ddl.go,
internal/parser/m0097_0017_test.go (+TestAlterTypeDropAttributeParsing),
internal/executor/operators_ddl.go (execAlterType DROP ATTRIBUTE block),
internal/testport/pgdump_connsetup_test.go (DROP c + IF EXISTS no-op + neg assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 255), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; executor+parser+catalog unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.6s, real pg_dump round-trips
`a integer, b_renamed text`, c numeric(10,2) gone); pgbench pre-commit smoke on commit.

Next (slice 256+): ALTER TYPE … ALTER ATTRIBUTE … TYPE (re-resolve one field's type)
and multi-subcommand ALTER TYPE statements — remaining attribute subcommands.
