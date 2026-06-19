(idle — nothing in flight)

Last landed: DU-002 slice 233 (loop #49) — fourth `RELOPT_KIND_TOAST` autovacuum-age
*integer* reloption (`toast.autovacuum_multixact_freeze_min_age`); ten-element TOAST reloptions array.
About to commit+push.

Files: internal/executor/operators_ddl.go (int gather after slice-232 freeze_table_age arm),
internal/testport/pgdump_connsetup_test.go (optoast fixture = 10 options + updated combined-WITH
assertion), docs/design/0110-0001-pg-dump-tap-port.md (Slice 233), .ralph/fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor+parser+catalog PASS;
TestPort_PgDumpConnectionSetup PASS (cgroup wrapper, -count=1, 3.4s); pgbench pre-commit smoke on commit.

PG oracle (reloptions.c lines verified this loop):
  autovacuum_multixact_freeze_min_age   RELOPT_KIND_HEAP|TOAST, default -1, range 0–1000000000      (line 286) ← slice 233 DONE
  autovacuum_multixact_freeze_max_age   RELOPT_KIND_HEAP|TOAST, default -1, range 10000–2000000000  (line 304) ← slice 234 NEXT
  autovacuum_multixact_freeze_table_age RELOPT_KIND_HEAP|TOAST, default -1, range 0–2000000000       (line 320) ← slice 235
  log_autovacuum_min_duration           RELOPT_KIND_HEAP|TOAST, default -1, range -1–INT_MAX         (line 329) ← slice 236 (-1 IS valid; special-case floor)

Next (each a one-line gather reusing the slice-230..233 int path, appended after the slice-233 arm
in operators_ddl.go ~line 1694, fixture extended by one option, assertion extended):
  slice 234: toast.autovacuum_multixact_freeze_max_age (INT, 10000–2000000000; -1 rejected — copy slice-231 max_age arm)
  slice 235: toast.autovacuum_multixact_freeze_table_age (INT, 0–2000000000; 0 valid, -1 rejected)
  slice 236: toast.log_autovacuum_min_duration (INT, -1–INT_MAX; -1 IS valid → floor is -1 not 0)
toast.autovacuum_analyze_* is RELOPT_KIND_HEAP ONLY → PG rejects toast. prefix; do NOT add. After: composite types.
