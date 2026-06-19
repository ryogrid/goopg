(idle — nothing in flight)

Last landed: DU-002 slice 254 (loop #20) — `ALTER TYPE … RENAME ATTRIBUTE old TO new`
renames a composite type field in place; the renamed attribute round-trips through
pg_dump. Previously the parser's `RENAME` branch knew only `RENAME VALUE`/`RENAME TO`
and stub-consumed `RENAME ATTRIBUTE …` to `;` (silent no-op).

Mechanism:
- Parser (parseAlterType, ddl.go): inside the `RENAME` branch, new `attribute`
  sub-branch parses `old TO new` (two bare idents, lower-cased), stub-consumes a
  CASCADE/RESTRICT trailer → AlterTypeStmt.RenameAttrOld/RenameAttrNew.
- Executor (execAlterType, operators_ddl.go): RenameAttrOld != "" → LookupCompositeType
  (42704 absent; 42703 `column … does not exist` if old name missing; 42701 collision
  with new name), copy field slice, rewrite the one field's Name, RE-SYNC heap (same as
  slice 253 ADD ATTRIBUTE: composite OIDs stable across RegisterCompositeTypeWithFields,
  stamp xmax on pg_type×2 + pg_class/pg_attribute, re-run syncCompositeTypeToCatalogHeap).
  No pg_dump-side change.

Files: internal/parser/ast.go (RenameAttrOld/RenameAttrNew), internal/parser/ddl.go,
internal/parser/m0097_0017_test.go (+TestAlterTypeRenameAttributeParsing),
internal/executor/operators_ddl.go (execAlterType RENAME ATTRIBUTE block),
internal/testport/pgdump_connsetup_test.go (RENAME b→b_renamed + dump assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 254), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; executor+parser+catalog unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.5s, real pg_dump round-trips
`a integer, b_renamed text, c numeric(10,2)`); pgbench pre-commit smoke on commit.

Next (slice 255+): ALTER TYPE … DROP ATTRIBUTE (needs attisdropped handling) and
ALTER ATTRIBUTE … TYPE (re-resolve one field's type) — remaining attribute subcommands.
