(idle — nothing in flight)

Last landed: DU-002 slice 206 (loop #20) — integer `log_autovacuum_min_duration`
storage parameter round-trips through pg_dump.

What happened: found WIP for slice 206 in the tree (working_set said idle) that was
complete EXCEPT the pg_dump dump-output assertion for optlamd. Added that assertion,
verified the whole slice, then committed. Pure reuse of slice-198 int path.
RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST, default -1, range -1–INT_MAX
(reloptions.c:324/1897); 0 logs every autovacuum action. Executor parses via
strconv.Atoi (reject non-int/out-of-range → 22023); separate
LogAutovacuumMinDurationSet flag (−1 and 0 both valid explicit values). Persist
catalog.Table.LogAutovacuumMinDuration (int); pg_class virtual view appends
`log_autovacuum_min_duration=N` after vacuum_truncate; pg_dump renders
`WITH (log_autovacuum_min_duration='N')`. Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.LogAutovacuumMinDuration/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
operators_fillfactor_reloptions_test.go (NEW
TestLogAutovacuumMinDurationSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optlamd fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 206), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet catalog/executor clean;
catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next: more reloption families remain. Candidates: (1) the `toast.*` namespace
reloptions (toast_tuple_target etc.). (2) composite types (CREATE TYPE AS) — larger,
pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453).
