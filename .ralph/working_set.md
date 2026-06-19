(idle — nothing in flight)

Last landed: DU-002 slice 235 (loop #51) — sixth `RELOPT_KIND_TOAST` autovacuum-age
*integer* reloption (`toast.autovacuum_multixact_freeze_table_age`, INT 0–2000000000);
twelve-element TOAST reloptions array. Committing+pushing now.

Files: internal/executor/operators_ddl.go (int gather arm after slice-234 max_age, ~line 1737),
internal/testport/pgdump_connsetup_test.go (optoast fixture = 12 options + updated combined-WITH
assertion + comment blocks), docs/design/0110-0001-pg-dump-tap-port.md (Slice 235), .ralph/fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor+parser+catalog PASS;
TestPort_PgDumpConnectionSetup PASS (cgroup wrapper, -count=1, 3.5s); pgbench pre-commit smoke on commit.

PG oracle (reloptions.c, verified): autovacuum_multixact_freeze_table_age
RELOPT_KIND_HEAP|TOAST, default -1, range 0–2000000000 (line 316/1895). 0 valid; explicit -1 rejected.

Next (last TOAST int option, then composites):
  slice 236: toast.log_autovacuum_min_duration (INT, range -1–INT_MAX; -1 IS valid → floor is -1 not 0).
    One-line gather appended after the slice-235 arm; fixture extended by one option; assertion extended.
toast.autovacuum_analyze_* is RELOPT_KIND_HEAP ONLY → PG rejects toast. prefix; do NOT add. After: composite types.
