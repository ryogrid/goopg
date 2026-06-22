Loop #53: M0118-0008 — `pg_backend_pid()` scalar built-in LANDED (enabler, NOT
a promotion; design 0118-0055). Self-contained, broadly-useful, fully-unit-tested.

Done: `pg_backend_pid()` was seeded in pg_proc (OID 2026, int4) but had NO
executor handler → SQL call fell through evalFuncCall→evalStoredRoutineFuncCall
→ hard `function pg_backend_pid does not exist`. That was the FIRST divergence
(step s2snitch, line 1 of every perm) of detach-partition-concurrently-3 AND -4.
Added a nullary case in evalFuncCall (beside current_database) returning the
per-connection integer PID via ctx.backendPID() (activity registry, the prod
path) → ctx.GetSetting("goopg.backend_pid") fallback → 0 (PG never NULLs it).

Files: internal/executor/expr.go (+case ~L5818), internal/executor/
pg_backend_pid_test.go (NEW TestPgBackendPID), docs/design/0118-0055-pg-backend-pid.md
+ README index. (Throwaway zz_probe_test.go used to rank the tail, then deleted.)

Gates: TestPgBackendPID PASS (registry 4242 / GUC 7 / unwired 0); full
internal/executor PASS; go build ./... + go vet (executor, testport) clean;
live probe confirms s2snitch no longer errors (first divergence now L12
DETACH-CONCURRENTLY `<waiting>` + L14 `pg_cancel_backend does not exist`).
pgbench smoke = pre-commit hook. COMMITTING.

Next step (M0118-0008 hard tail — probed & ranked this loop, all Effort-L, one
new subsystem each, one slice per loop):
- detach-partition-concurrently-3/4: next blockers = `pg_cancel_backend(pid)`
  cross-backend cancellation (seeded OID 2171, unwired — use s.cancelReg) THEN
  DETACH … CONCURRENTLY two-phase `<waiting>` (wait out old snapshots before the
  partition disappears; relpartbound→NULL flips at commit).
- detach-partition-concurrently-1/2 + partition-concurrent-attach + alter-table-4:
  transactional partition/DDL cross-session catalog visibility COUPLED to the
  `<waiting>` blocking (goopg's single shared in-memory catalog makes DETACH/
  ATTACH/INHERIT immediately+globally visible; PG defers per-snapshot).
- partition-drop-index-locking: real pg_locks + pg_stat_activity view population
  (joins l.pid=s.pid, l.relation=c.oid; DROP INDEX cascade AccessExclusive locks).
- reindex-concurrently-toast: real auto-created TOAST relations (reltoastrelid
  is 0 for plain text cols today) + `allow_system_table_mods` GUC.
Suggested: pg_cancel_backend is the next bounded enabler (pairs with this loop;
the cancel registry already exists). Re-probe with a throwaway zz_probe_test.go.
