(idle — nothing in flight)

Last landed: DU-002 slice 199 (loop #12) — first REAL-typed storage parameter
(`autovacuum_vacuum_scale_factor`, 0.0–100.0) round-trips through pg_dump.

What happened: slices 54/195/196/197/198 were all int/bool reloptions. This is
the first RELOPT_TYPE_REAL knob. The real value surfaced a parser gap: a
fractional literal (`0.2`) lexes as TokenNumericLit, which parseWithOptions
rejected ("expected option value"), so the option never reached the executor.
Fix: (1) parser accepts TokenNumericLit in parseWithOptions (raw text kept);
(2) executor ParseFloat + bounds-check `!(f>=0 && f<=100)` (also rejects
NaN/±Inf; above-range/non-numeric → 22023; negatives are a parser syntax error);
0.0 is valid explicit → separate AutovacuumVacuumScaleFactorSet flag (not zero
check); (3) persist catalog.Table.AutovacuumVacuumScaleFactor (float64);
(4) pg_class virtual view appends `autovacuum_vacuum_scale_factor=F` after
threshold, F via FormatFloat(f,'g',-1,64) (0.2→"0.2", 0→"0"); pg_dump renders
`WITH (autovacuum_vacuum_scale_factor='0.2')`. Advisory catalog/dump-only;
base-table-only.

Files: internal/parser/ddl.go (parseWithOptions ~L2937 accepts TokenNumericLit),
internal/catalog/catalog.go (Table.AutovacuumVacuumScaleFactor/…Set ~L440 +
render ~L2187), internal/executor/operators_ddl.go (extract/parse ~L1057 +
persist ~L1140), operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumVacuumScaleFactorSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optavsf fixture ~L742 + assertion
~L2465), docs/design/0110-0001-pg-dump-tap-port.md (Slice 199), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet parser/catalog/testport
clean; catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup
PASS; pgbench pre-commit smoke on commit.

Next (slice 200 candidates): (1) the now-unblocked REAL reloptions reuse the
float path — autovacuum_analyze_scale_factor / autovacuum_vacuum_insert_scale_factor
/ autovacuum_vacuum_cost_delay (all RELOPT_TYPE_REAL in reloptions.c). (2) int
autovacuum knob autovacuum_analyze_threshold (same Set-flag int pattern as slice
198). (3) composite types (CREATE TYPE AS) — larger, pg_class.reltype hardcoded
0 (pg18_user_catalog_rows.go:453).
