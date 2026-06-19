(idle — nothing in flight)

Last landed: DU-002 slice 214 (loop #29) — boolean `user_catalog_table` storage
parameter round-trips through pg_dump.

What happened: user_catalog_table is RELOPT_TYPE_BOOL, RELOPT_KIND_HEAP, default false
(reloptions.c:1909). Boolean carries no zero-detectable default, so UserCatalogTableSet
guards presence (slice-196 autovacuum_enabled pattern). Executor parses via
parseReloptionBool; non-boolean → 22023. Persist catalog.Table.UserCatalogTable (bool);
pg_class virtual view appends `user_catalog_table=true|false` after
autovacuum_vacuum_cost_limit; pg_dump renders `WITH (user_catalog_table='true'|'false')`.
Advisory catalog/dump-only (no logical decoding in goopg); base-table-only.

Files: internal/catalog/catalog.go (Table.UserCatalogTable/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestUserCatalogTableSurfacesInPgClassReloptions + …InvalidValueRejected),
internal/testport/pgdump_connsetup_test.go (optuct fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 214), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: toast.* namespace reloptions; or composite types (CREATE TYPE AS; larger,
pg_class.reltype hardcoded 0).
