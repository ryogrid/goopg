Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 52 COMPLETE
(committing this loop). NEXT loop starts on slice 53.
NOTHING in flight after commit.

=== DONE (loop #7) — DU-002 slice 52 (FK referential actions) ===
FK ON DELETE / ON UPDATE actions now survive pg_dump on BOTH the inline
column path (already worked) and the ALTER TABLE ADD FOREIGN KEY path (was
broken at 3 layers).
ROOT CAUSE (ALTER path): parser never consumed the ON DELETE/UPDATE clause
(syntax would error before comma/EOS), AlterTableAction AST had no
OnDelete/OnUpdate field, executor's AlterTableAddForeignKey branch never set
catalog.ForeignKey.OnDelete/OnUpdate. (Deparser fkActionClause + fkActionChar
already existed from slice 51.)
FIX (sibling paths kept in sync — mirror the inline column path):
 (1) parser/ast.go: AlterTableAction += OnDelete/OnUpdate FKAction.
 (2) parser/ddl.go (~line 4668, ALTER ADD FK): parse ON DELETE/ON UPDATE
     ahead of the [NOT] DEFERRABLE trailer, reusing parseFKAction.
 (3) executor/operators_ddl.go (~line 3360): copy act.OnDelete/OnUpdate into
     the catalog FK struct.
Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/executor/operators_ddl.go,
internal/executor/operators_fk_constraintdef_test.go (new unit test
TestAlterTableAddForeignKeyCapturesActions),
internal/testport/pgdump_connsetup_test.go (fixture: inline self-FK gains
ON DELETE CASCADE; new ALTER-added foo_mgr_fkey ON UPDATE CASCADE ON DELETE
SET NULL; assertions), docs/design/0110-0001-pg-dump-tap-port.md (slice 52).
Gates: gofmt/vet clean; build clean; parser+catalog+executor PASS;
TestForeignKeySurfacesInPgConstraint + TestAlterTableAddForeignKeyCapturesActions
PASS; TestPort_PgDumpConnectionSetup PASS (exit-0, both FK actions round-trip);
pgbench CI-parity smoke runs in pre-commit hook.

=== NEXT STEP — DU-002 slice 53 ===
Enrich the fixture further to find the next REAL pg_dump gap. Candidates:
  - Multi-column FK (composite REFERENCES (a,b)) — exercises conkey/confkey
    ordinal arrays + multi-col deparse join.
  - a second user schema (CREATE SCHEMA s; table in it) — search_path / schema
    qualification across namespaces.
  - real reloptions WITH (fillfactor=70).
  - COMMENT ON TABLE/COLUMN round-trip.
  - SEQUENCE / serial column — sequences skipped from pg_class virtual view
    (Virtual && no View), so getTables never sees relkind='S'; larger slice.
Known orthogonal pre-existing: plpgsql user functions can't be dumped (plpgsql
absent from pg_language → prolang=0 → dumpFunc join 0 rows).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).

NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
Record progress here + deferral_ledger only.
