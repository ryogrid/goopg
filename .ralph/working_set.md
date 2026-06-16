Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 53 COMPLETE
(committing this loop). NEXT loop starts on slice 54.
NOTHING in flight after commit.

=== DONE (loop #8) — DU-002 slice 53 (table-level / composite FOREIGN KEY) ===
A table-level `FOREIGN KEY (a,b) REFERENCES t (x,y)` in the CREATE TABLE body
was a parser NO-OP (both the CONSTRAINT-named case skipped tokens, and there was
no anonymous branch at all), so composite FKs never reached the catalog,
pg_constraint, or pg_dump. The inline column path only ever did single-column.
FIX (sibling paths kept in sync — mirror the inline column REFERENCES path):
 (1) parser/ast.go: new TableForeignKeyDef + CreateTableStmt.TableForeignKeys.
 (2) parser/ddl.go: new parseTableForeignKey(name) helper parsing
     FOREIGN KEY (cols) REFERENCES t [(refcols)] [ON DELETE/UPDATE act]
     [[NOT] DEFERRABLE [INITIALLY …]]; wired into BOTH the CONSTRAINT-named
     case (was no-op at ~1908) AND a new anonymous KwForeign branch (~1809).
 (3) executor/operators_ddl.go (~1016): loop over s.TableForeignKeys building
     catalog.ForeignKey, auto-naming anonymous as <table>_<firstcol>_fkey.
NO deparse/pg_constraint change needed — buildForeignKeyDefString already
strings.Join's multiple cols; conkey/confkey loops already iterate fk.Columns/
fk.RefColumns (verified internal/catalog/catalog.go ~2990).
Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/executor/operators_ddl.go,
internal/executor/operators_fk_constraintdef_test.go (new
TestCreateTableTableLevelCompositeForeignKey — anonymous+named, multi-col
conkey/confkey, composite deparse), internal/testport/pgdump_connsetup_test.go
(fixture: bar composite PK + baz composite FK; assertions),
docs/design/0110-0001-pg-dump-tap-port.md (slice 53 + guard list).
Gates: gofmt clean (my files); vet clean; parser+executor full suites PASS;
TestCreateTableTableLevelCompositeForeignKey PASS; TestPort_PgDumpConnectionSetup
PASS (exit-0, composite PK+FK round-trip); pgbench CI-parity smoke via pre-commit.

=== NEXT STEP — DU-002 slice 54 ===
Enrich the fixture further to find the next REAL pg_dump gap. Candidates:
  - a second user schema (CREATE SCHEMA s; table in it) — search_path / schema
    qualification across namespaces (likely real gaps in CREATE SCHEMA emit).
  - real reloptions WITH (fillfactor=70) — slice 47 made empty reloptions NULL;
    a non-empty one must actually surface.
  - COMMENT ON TABLE/COLUMN round-trip — needs pg_description population.
  - SEQUENCE / serial column — sequences skipped from pg_class virtual view
    (Virtual && no View), so getTables never sees relkind='S'; larger slice.
Known orthogonal pre-existing: plpgsql user functions can't be dumped (plpgsql
absent from pg_language → prolang=0 → dumpFunc join 0 rows).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).

NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
Record progress here + deferral_ledger only.
RUN TestPort_PgDumpConnectionSetup after each fixture add to find the REAL blocker.
