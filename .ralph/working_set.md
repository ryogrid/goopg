(idle — nothing in flight)

Last landed: DU-002 slice 198 (loop #11) — integer autovacuum-namespace
storage parameter (`autovacuum_vacuum_threshold`, 0–INT_MAX) round-trips
through pg_dump.

What happened: slices 54/195/196/197 made fillfactor, parallel_workers,
autovacuum_enabled, toast_tuple_target round-trip. autovacuum_vacuum_threshold
is the most common per-table autovacuum knob and extends coverage to the
autovacuum reloption namespace. PG reloption range 0–INT_MAX, default -1
(reloptions.c) → 0 is a valid explicit value, so it reuses parallel_workers'
Set-flag pattern (AutovacuumVacuumThresholdSet, not a zero check). goopg
validated the lowercase WITH key but never extracted it, so
`WITH (autovacuum_vacuum_threshold=100)` silently dropped it. Fix:
extract/bounds-check (0–2147483647; overflow/non-int → 22023; negatives are a
parser syntax error) on base-table CREATE; persist
catalog.Table.AutovacuumVacuumThreshold; pg_class virtual view appends
`autovacuum_vacuum_threshold=N` after toast_tuple_target; pg_dump renders
`WITH (autovacuum_vacuum_threshold='100')`. Advisory catalog/dump-only;
base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumVacuumThreshold/…Set field
~L429 + render ~L2168), internal/executor/operators_ddl.go (extract/parse ~L1034
+ persist ~L1108), operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumVacuumThresholdSurfacesInPgClassReloptions +
…OutOfBoundsRejected), internal/testport/pgdump_connsetup_test.go (optavt
fixture ~L727 + assertion ~L2434), docs/design/0110-0001-pg-dump-tap-port.md
(Slice 198), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet testport clean;
catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next (slice 199 candidates): (1) real-typed reloption
`autovacuum_vacuum_scale_factor` (real 0–100, needs float parse + 0-as-valid →
new float Set-flag path — first real-typed reloption). (2) another int
autovacuum knob `autovacuum_analyze_threshold` or
`autovacuum_vacuum_insert_threshold` (same Set-flag int pattern as this slice).
(3) composite types (CREATE TYPE AS) — larger, pg_class.reltype hardcoded 0
(pg18_user_catalog_rows.go:453).
