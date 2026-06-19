(idle — nothing in flight)

Last landed: DU-002 slice 200 (loop #13) — second REAL-typed storage parameter
(`autovacuum_analyze_scale_factor`, 0.0–100.0) round-trips through pg_dump.

What happened: pure reuse of slice 199's float path. autovacuum_analyze_scale_factor
is also RELOPT_TYPE_REAL (reloptions.c: default -1, range 0.0–100.0). No parser
change needed — slice 199 already opened TokenNumericLit in parseWithOptions.
Mechanism: executor strconv.ParseFloat + `!(f>=0 && f<=100)` bounds-check (rejects
NaN/±Inf; above-range/non-numeric → 22023; negatives are a parser syntax error);
separate AutovacuumAnalyzeScaleFactorSet flag so explicit 0.0 round-trips; persist
catalog.Table.AutovacuumAnalyzeScaleFactor (float64); pg_class virtual view appends
`autovacuum_analyze_scale_factor=F` after autovacuum_vacuum_scale_factor, F via
FormatFloat(f,'g',-1,64); pg_dump renders WITH (autovacuum_analyze_scale_factor='0.05').
Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumAnalyzeScaleFactor/…Set ~L455
+ render ~L2206), internal/executor/operators_ddl.go (extract/parse ~L1083 + persist
~L1169), operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumAnalyzeScaleFactorSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optaasf fixture ~L759 + assertion ~L2491),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 200), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet catalog/executor/testport
clean; catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next (slice 201 candidates): (1) remaining REAL reloptions reuse the float path —
autovacuum_vacuum_insert_scale_factor / autovacuum_vacuum_cost_delay (both
RELOPT_TYPE_REAL). NOTE: cost_delay range is 0–100 too (per reloptions.c default
-1, 0.0, 100.0 — verify). (2) int autovacuum knob autovacuum_analyze_threshold
(same Set-flag int pattern as slice 198). (3) composite types (CREATE TYPE AS) —
larger, pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453).
