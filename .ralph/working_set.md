(idle — nothing in flight)

Last landed: DU-002 slice 237 (loop #3) — `toast.autovacuum_vacuum_insert_threshold`
(RELOPT_KIND_HEAP|TOAST, INT range -1–INT_MAX, floor -1 not 0), fourteen-element TOAST
reloptions array. Corrected the slice-236 "surface complete" claim: three HEAP|TOAST
insert-vacuum options were actually missed (only the analyze_* pair is HEAP-only).

Files: internal/executor/operators_ddl.go (toast int gather block after the slice-236
toast.log_autovacuum_min_duration arm, floor -1), internal/testport/pgdump_connsetup_test.go
(optoast fixture = 14 options incl. insert_threshold=1000 + combined-WITH assertion x2),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 237), .ralph/fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor reloption suite PASS;
TestPort_PgDumpConnectionSetup PASS (cgroup wrapper, -count=1, 3.4s); pgbench pre-commit smoke on commit.

Next: TWO HEAP|TOAST insert-vacuum options remain on the SAME pattern:
- toast.autovacuum_vacuum_max_threshold (INT, range -1–INT_MAX, default -2; reloptions.c:236/1877) — slice 238
- toast.autovacuum_vacuum_insert_scale_factor (REAL, range 0.0–100.0, default -1; reloptions.c:411/1905) — slice 239
  (real toast arm pattern = toast.autovacuum_vacuum_scale_factor slice 227; mirror parent heap arms slice 215/201).
After those THREE the toast.* surface is genuinely complete → composite types (CREATE TYPE AS;
pg_class.reltype hardcoded 0 — larger structural task).
