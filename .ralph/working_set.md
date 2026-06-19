(idle — nothing in flight)

Last landed: DU-002 slice 201 (loop #14) — third REAL-typed storage parameter
(`autovacuum_vacuum_insert_scale_factor`, 0.0–100.0) round-trips through pg_dump.

What happened: pure reuse of slice 199's float path. autovacuum_vacuum_insert_scale_factor
is RELOPT_TYPE_REAL (reloptions.c:411: default -1, range 0.0–100.0). No parser change
(slice 199 already opened TokenNumericLit). Mechanism: executor strconv.ParseFloat +
`!(f>=0 && f<=100)` bounds-check (rejects NaN/±Inf; above-range/non-numeric → 22023;
negatives are a parser syntax error); separate AutovacuumVacuumInsertScaleFactorSet flag
so explicit 0.0 round-trips; persist catalog.Table.AutovacuumVacuumInsertScaleFactor
(float64); pg_class virtual view appends `autovacuum_vacuum_insert_scale_factor=F` after
autovacuum_analyze_scale_factor, F via FormatFloat(f,'g',-1,64); pg_dump renders
WITH (autovacuum_vacuum_insert_scale_factor='0.2'). Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumVacuumInsertScaleFactor/…Set ~L473
+ render ~L2209), internal/executor/operators_ddl.go (extract/parse ~L1108 + persist
~L1171), operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumVacuumInsertScaleFactorSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optavisf fixture ~L770 + assertion ~L2515),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 201), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet catalog/executor/testport
clean; catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next (slice 202 candidates): (1) LAST REAL reloption autovacuum_vacuum_cost_delay
(RELOPT_TYPE_REAL, reloptions.c:393, default -1, range 0.0–100.0 — same float path).
(2) int autovacuum knob autovacuum_analyze_threshold (same Set-flag int pattern as
slice 198). (3) composite types (CREATE TYPE AS) — larger, pg_class.reltype
hardcoded 0 (pg18_user_catalog_rows.go:453).
