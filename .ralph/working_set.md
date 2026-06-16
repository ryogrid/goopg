Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 51 COMPLETE
(committing this loop). NEXT loop starts on slice 52.
NOTHING in flight after commit.

=== DONE (loop #6) — DU-002 slice 51 (FOREIGN KEY constraints) ===
Enriched the fixture: added `parent_id integer REFERENCES public.foo(id)` self-FK
and a table-level `UNIQUE (code)`. UNIQUE already dumped (index-backed path), but
the FK was silently dropped.
ROOT CAUSE: catalog.ForeignKey had no Name/OID, so pg_constraint emitted no
contype='f' row; pg_dump's getConstraints (`JOIN pg_constraint c ON
src.tbloid=c.conrelid WHERE contype='f' AND conparentid=0`, pg_dump.c:8172) found
nothing. Even if it had, pg_get_constraintdef handled only index-backed constraints.
FIX (3 parts, sibling paths kept in sync):
 (1) catalog.ForeignKey += Name+OID; auto-named <table>_<col>_fkey at DDL time in
     BOTH the CREATE TABLE inline-REFERENCES path (operators_ddl.go ~line 991) and
     the ALTER TABLE ADD FOREIGN KEY path (~line 3349, honours explicit name).
 (2) pg_constraint.VirtualRows (catalog.go ~line 2960) emits the contype='f' row:
     conkey/confkey ordinals, confrelid=ref table OID, confupdtype/confdeltype via
     new fkActionChar helper, confmatchtype='s', conparentid=0. Also added
     fkActionChar helper near the ForeignKey type.
 (3) pg_get_constraintdef (expr.go ~line 6395) gained an FK branch +
     buildForeignKeyDefString/fkActionClause (expr.go ~line 4189) mirroring
     ruleutils.c: `FOREIGN KEY (cols) REFERENCES public.tbl(refcols)` fully
     schema-qualified (search_path=''), ON UPDATE/ON DELETE/DEFERRABLE only when
     non-default. NOTE: no space before the paren — `public.foo(id)` not `foo (id)`.
Files: internal/catalog/catalog.go, internal/executor/expr.go,
internal/executor/operators_ddl.go, internal/executor/operators_fk_constraintdef_test.go
(new unit test), internal/testport/pgdump_connsetup_test.go (fixture + assertions),
docs/design/0110-0001-pg-dump-tap-port.md (slice 51 section + guard list).
Gates: gofmt/vet clean; build clean; catalog+executor+initdb+parser+server+planner
PASS; TestForeignKeySurfacesInPgConstraint PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench CI-parity smoke runs in pre-commit hook.

=== NEXT STEP — DU-002 slice 52 ===
Enrich the fixture further to find the next REAL pg_dump gap. Candidates:
  - Multi-column FK / FK with ON DELETE CASCADE (exercise the action clauses just
    added — verify ON DELETE CASCADE round-trips, currently untested).
  - SEQUENCE / serial column — sequences skipped from pg_class virtual view
    (Virtual && no View), so getTables never sees relkind='S'; larger slice.
  - a second user schema (CREATE SCHEMA s; table in it).
  - real reloptions WITH (fillfactor=70).
  - COMMENT ON TABLE/COLUMN round-trip.
Known orthogonal pre-existing: plpgsql user functions can't be dumped (plpgsql
absent from pg_language → prolang=0 → dumpFunc join 0 rows).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).

NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
Record progress here + deferral_ledger only.
