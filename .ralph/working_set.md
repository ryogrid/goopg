(idle — nothing in flight)

Last landed: DU-002 slice 195 (loop #8) — second table-level storage parameter
(`parallel_workers`) round-trips through pg_dump.

What happened: a `WITH (...)` clause can carry multiple storage params, but goopg
only extracted `fillfactor` (slice 54) — `execCreateTable` validated every WITH
key as lowercase, then read fillfactor alone and dropped the rest. So
`CREATE TABLE … WITH (parallel_workers=4)` succeeded but lost the option
(reloptions stayed fillfactor-only; pg_dump never re-emitted it). Fix adds
`parallel_workers` as a second persisted reloption (bounds 0–1024 per PG
reloptions.c). KEY nuance: 0 is a VALID explicit value (PG default -1=unset), so
a zero-check can't distinguish set-to-0 from unset → catalog.Table now has BOTH
`ParallelWorkers int` and `ParallelWorkersSet bool`; only the flag decides
surfacing. pg_class reloptions render is now an ordered list (fillfactor first,
then parallel_workers) → `{fillfactor=70,parallel_workers=4}`, which pg_dump's
appendReloptionsArray renders as `WITH (fillfactor='70', parallel_workers='4')`.
goopg has no parallel query — advisory catalog/dump-only (runtime unchanged).
Base-table-only (partitioned tables reject WITH; leaf path keeps fillfactor).

Files: internal/catalog/catalog.go (Table fields + ordered render ~L2106),
internal/executor/operators_ddl.go (extract/persist ~L973 + tbl set ~L1027),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestParallelWorkersSurfacesInPgClassReloptions + ...OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optpw fixture ~L684 + assertion
~L2349), docs/design/0110-0001-pg-dump-tap-port.md (Slice 195), fix_plan.md.

Gates: gofmt OK; go build ./... clean; go vet testport clean; catalog + parser +
executor PASS; TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next (slice 196 candidates): (1) more reloptions on the same pattern
(`autovacuum_enabled`, `toast_tuple_target`, `autovacuum_vacuum_scale_factor`).
(2) composite types (CREATE TYPE AS) — larger, pg_class.reltype hardcoded 0
(pg18_user_catalog_rows.go:453). (3) per-column attfdwoptions (foreign-table only).
