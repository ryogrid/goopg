(idle — nothing in flight)

Last landed: DU-002 slice 196 (loop #9) — boolean table-level storage parameter
(`autovacuum_enabled`) round-trips through pg_dump.

What happened: slices 54/195 made two *integer* reloptions (fillfactor,
parallel_workers) round-trip. autovacuum_enabled is the most common
non-fillfactor reloption in real dumps and the first BOOLEAN one — new code path
(value parsing, not bounds-checking). goopg validated the lowercase WITH key but
never extracted it, so `CREATE TABLE … WITH (autovacuum_enabled=false)` succeeded
and silently dropped the option. Fix: new `parseReloptionBool` helper mirroring
PG's parse_bool (parse_bool_with_len) — accepts case-insensitive PREFIXES of
true/false/yes/no plus on/of/off/1/0; unrecognized → 22023 "invalid value for
boolean option". Like parallel_workers the bool has no zero-detectable default →
catalog.Table gets `AutovacuumEnabled bool` + `AutovacuumEnabledSet bool`; only
the flag surfaces it. pg_class virtual view appends
`autovacuum_enabled=true|false` (strconv.FormatBool) after the two int options;
pg_dump's appendReloptionsArray renders `WITH (autovacuum_enabled='false')`.
goopg has no autovacuum → advisory catalog/dump-only (runtime unchanged).
Base-table-only.

Files: internal/catalog/catalog.go (Table fields ~L405 + ordered render ~L2125),
internal/executor/operators_ddl.go (parse/persist ~L996 + tbl set ~L1066 +
parseReloptionBool helper ~L5216), operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumEnabledSurfacesInPgClassReloptions + ...InvalidValueRejected),
internal/testport/pgdump_connsetup_test.go (optav fixture ~L698 + assertion
~L2376), docs/design/0110-0001-pg-dump-tap-port.md (Slice 196), fix_plan.md.

Gates: gofmt OK; go build ./... clean; go vet testport clean; catalog+executor
reloption tests PASS; TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit
smoke on commit.

Next (slice 197 candidates): (1) more reloptions same pattern —
`toast_tuple_target` (int 128–8160), `autovacuum_vacuum_scale_factor` (real
0–100, needs float parse + 0-as-valid), `autovacuum_vacuum_threshold` (int).
(2) composite types (CREATE TYPE AS) — larger, pg_class.reltype hardcoded 0
(pg18_user_catalog_rows.go:453). (3) per-column attfdwoptions (foreign-table only).
