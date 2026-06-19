(idle — nothing in flight)

Last landed: DU-002 slice 236 (loop #52) — `toast.log_autovacuum_min_duration`
(RELOPT_KIND_HEAP|TOAST, INT range -1–INT_MAX, floor -1 not 0), thirteen-element
TOAST reloptions array. First SIGNED reloption value → also fixed a parser gap
(storage-param list now accepts an optional leading '+'/'-', mirroring PG's
def_arg=NumericOnly). Committing+pushing now.

Files: internal/parser/ddl.go (optional-sign block before value-token switch in the
storage-param list parser), internal/parser/ddl_test.go (TestParseCreateTableSignedReloption),
internal/executor/operators_ddl.go (int gather arm after slice-235, floor -1),
internal/testport/pgdump_connsetup_test.go (optoast fixture = 13 options incl. signed -1
+ combined-WITH assertion), docs/design/0110-0001-pg-dump-tap-port.md (Slice 236), .ralph/fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; parser+executor+catalog PASS;
TestPort_PgDumpConnectionSetup PASS (cgroup wrapper, -count=1, 3.5s); pgbench pre-commit smoke on commit.

Next: the toast.* autovacuum reloption surface is COMPLETE. autovacuum_analyze_threshold /
autovacuum_analyze_scale_factor are RELOPT_KIND_HEAP-ONLY → PG rejects the toast. prefix; do NOT add them.
Next DU-002 work item: composite types (CREATE TYPE ... AS (...)) in pg_dump.
