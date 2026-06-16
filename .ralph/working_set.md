Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 54 COMPLETE
(committing this loop). NEXT loop starts on slice 55.
NOTHING in flight after commit.

=== DONE (loop #9) — DU-002 slice 54 (non-empty reloptions / fillfactor) ===
`WITH (fillfactor=N)` was parsed + bounds-validated but NEVER persisted, so
pg_class.reloptions always read NULL and pg_dump emitted a bare CREATE TABLE.
FIX:
 (1) internal/catalog/catalog.go: new Table.Fillfactor field; pg_class virtual
     view reloptions cell (idx 32) now emits `{fillfactor=N}` text[] literal
     when set, "" (→ NULL via TypedVirtualCell, slice 47) otherwise.
 (2) internal/executor/operators_ddl.go: CREATE TABLE path extracts
     s.With["fillfactor"], bounds-checks (22023 outside 10–100, mirroring CREATE
     INDEX), assigns tbl.Fillfactor.
pg_dump renders the array back as `WITH (fillfactor='70')` — confirmed by the
real binary in the TAP test.
ALSO added a cross-namespace regression guard (CREATE SCHEMA s + s.widget — it
already round-trips; now guarded). Tightened the slice-47 "foo has no options"
guard to the exact empty-element signature `WITH ("` so a legit fillfactor WITH
clause doesn't trip it.
Files: internal/catalog/catalog.go, internal/executor/operators_ddl.go,
internal/executor/operators_fillfactor_reloptions_test.go (new:
TestFillfactorSurfacesInPgClassReloptions + TestFillfactorOutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (opt table + schema s fixture +
assertions), docs/design/0110-0001-pg-dump-tap-port.md (slice 54 + guard list).
Gates: gofmt clean; vet clean; catalog+parser+executor full suites PASS;
TestFillfactor* PASS; TestPort_PgDumpConnectionSetup PASS (exit-0, fillfactor +
schema round-trip); pgbench CI-parity smoke via pre-commit hook.

=== NEXT STEP — DU-002 slice 55 ===
Enrich the fixture further to find the next REAL pg_dump gap. Candidates:
  - COMMENT ON TABLE/COLUMN round-trip — needs pg_description population
    (likely a real gap; pg_dump emits COMMENT statements).
  - other reloptions beyond fillfactor (autovacuum_*, toast.*) — currently only
    fillfactor is parsed into s.With from CREATE TABLE; others discarded.
  - SEQUENCE / serial column — sequences skipped from pg_class virtual view
    (Virtual && no View), so getTables never sees relkind='S'; larger slice.
  - a real DEFAULT that is non-trivial (now `qty integer DEFAULT 0` works).
Known orthogonal pre-existing: plpgsql user functions can't be dumped (plpgsql
absent from pg_language → prolang=0 → dumpFunc join 0 rows).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).

NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
Record progress here + deferral_ledger only.
RUN TestPort_PgDumpConnectionSetup after each fixture add to find the REAL blocker.
