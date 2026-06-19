(idle — nothing in flight)

Last landed: DU-002 slice 202 (loop #15) — fourth (and final) REAL-typed storage
parameter (`autovacuum_vacuum_cost_delay`, 0.0–100.0) round-trips through pg_dump.

What happened: pure reuse of slice 199's float path. autovacuum_vacuum_cost_delay is
RELOPT_TYPE_REAL (reloptions.c:393/1901: default -1, range 0.0–100.0). No parser
change. Mechanism: executor strconv.ParseFloat + `!(f>=0 && f<=100)` bounds-check
(rejects NaN/±Inf; above-range/non-numeric → 22023; negatives are a parser syntax
error); separate AutovacuumVacuumCostDelaySet flag so explicit 0.0 round-trips;
persist catalog.Table.AutovacuumVacuumCostDelay (float64); pg_class virtual view
appends `autovacuum_vacuum_cost_delay=F` after autovacuum_vacuum_insert_scale_factor,
F via FormatFloat(f,'g',-1,64); pg_dump renders WITH (autovacuum_vacuum_cost_delay='2.5').
Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumVacuumCostDelay/…Set ~L487
+ render ~L2229), internal/executor/operators_ddl.go (extract/parse ~L1133 + persist
~L1199), operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumVacuumCostDelaySurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optavcd fixture ~L784 + assertion ~L2539),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 202), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet catalog/executor/testport
clean; catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next (slice 203 candidates): ALL four REAL reloptions now round-trip. (1) int
autovacuum knob autovacuum_analyze_threshold (RELOPT_TYPE_INT, same Set-flag int
pattern as slice 198). (2) int autovacuum_vacuum_insert_threshold (same int path).
(3) composite types (CREATE TYPE AS) — larger, pg_class.reltype hardcoded 0
(pg18_user_catalog_rows.go:453).
