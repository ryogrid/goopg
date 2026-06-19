(idle — nothing in flight)

Last landed: DU-002 slice 238 (loop #4) — `toast.autovacuum_vacuum_max_threshold`
(RELOPT_KIND_HEAP|TOAST, INT range -1–INT_MAX, default -2, floor -1 not 0), fifteen-element
TOAST reloptions array. Same pattern as slice 237 insert_threshold.

Files: internal/executor/operators_ddl.go (toast int gather block after the slice-237
toast.autovacuum_vacuum_insert_threshold arm, floor -1), internal/testport/pgdump_connsetup_test.go
(optoast fixture = 15 options incl. max_threshold=2000 + combined-WITH assertion x2),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 238), .ralph/fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor reloption suite PASS;
TestPort_PgDumpConnectionSetup PASS (cgroup wrapper, -count=1, 3.45s); pgbench pre-commit smoke on commit.

Next: ONE HEAP|TOAST insert-vacuum option remains, then toast.* surface is COMPLETE:
- toast.autovacuum_vacuum_insert_scale_factor (REAL, range 0.0–100.0, default -1; reloptions.c:411/1905) — slice 239
  (real toast arm pattern = toast.autovacuum_vacuum_scale_factor slice 227; mirror parent heap arm slice 201/216).
After slice 239 the toast.* surface is genuinely complete → composite types (CREATE TYPE AS;
pg_class.reltype hardcoded 0 — larger structural task).
