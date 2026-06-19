(idle — nothing in flight)

Last landed: DU-002 slice 239 (loop #5) — `toast.autovacuum_vacuum_insert_scale_factor`
(RELOPT_KIND_HEAP|TOAST, REAL range 0.0–100.0, default -1), sixteen-element TOAST
reloptions array. The `toast.*` reloption surface is now COMPLETE. Same pattern as the
toast-real slice 227 (toast.autovacuum_vacuum_scale_factor); mirrors parent heap arm slice 201.

Files: internal/executor/operators_ddl.go (toast real gather block after the slice-238
toast.autovacuum_vacuum_max_threshold arm; ParseFloat + !(f>=0 && f<=100) → 22023),
internal/testport/pgdump_connsetup_test.go (optoast fixture = 16 options incl.
insert_scale_factor=1.5 + combined-WITH assertion x2),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 239), .ralph/fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor reloption suite PASS;
TestPort_PgDumpConnectionSetup PASS (cgroup wrapper, -count=1, 3.44s); pgbench pre-commit smoke on commit.

Next: toast.* reloption surface is COMPLETE. Remaining pg_dump work → composite types
(CREATE TYPE AS; pg_class.reltype hardcoded 0 — larger structural task, not a one-slice arm).
