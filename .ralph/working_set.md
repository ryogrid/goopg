Loop #27: **M0118-0009 async-notify.spec PROMOTED `failed`→`pass`** (all 6 perms
byte-identical to PG 18.3, strict `TestPort_IsolationAsyncNotify`; design 0118-0090).
Closed last loop's resume point on top of the 0118-0089 engine. COMMITTED.

Four gaps fixed (two were engine bugs the syntax-only wiring had hidden):
- HARNESS (isolation_runner.go): chain `pq.ConnectorWithNotificationHandler` per
  session → `sessionNotifyQueue`; `drainAllNotifications` after each step emits
  `<recv>: NOTIFY "<ch>" with payload "<p>" from <src>` in session order (src via
  `pg_backend_pid()`→session map). No-op for non-LISTEN specs.
- ENGINE (notify.go/context.go/expr.go): `pg_notification_queue_usage()`
  (`notifyHub.QueueUsage`→`Context.NotifyQueueUsage`), rendered `FormatFloat('g',-1)`
  so empty queue = "0" (a float8 string of all-zero fraction wrongly compares `>0`
  true — pre-existing cast bug sidestepped). `pg_notify` returns non-NULL void so
  `count(pg_notify(...))`=1000.
- HARNESS+ENGINE: multi-statement steps run as ONE simple-query message
  (`execMultiStatement` iterating result sets) → one implicit transaction (PQexec
  semantics). Required re-wiring `ectx.Session` in the BEGIN handler (connTx.Begin
  lazily creates the session).
- ENGINE (conn_tx.go/dispatch.go): `pendingNotify` is now a savepoint-aware
  `notifyLevel` stack (push SAVEPOINT, merge RELEASE, discard ROLLBACK TO; de-dup
  across levels), wired from the dispatch loop after each savepoint stmt.

Gates: `TestPort_IsolationAsyncNotify` strict PASS; full `TestPort_Isolation*` =
101 pass. The 3 run-to-run "failures" (update-locked-tuple, vacuum-skip-locked,
vacuum-concurrent-drop, tuplelock-upgrade-no-deadlock) are a PRE-EXISTING 300ms
blocking-detection timing flake under WSL2 load — VERIFIED update-locked-tuple
fails 3/3 on clean HEAD too (git stash test); the failing set varies per run; all
pass isolated on an unloaded machine. NOT a regression. `-race` server clean;
build/vet/gofmt clean; pgbench smoke = pre-commit hook.

Next step: M0118-0009 has more sub-specs deferred (each a distinct subsystem):
timeouts (statement_timeout/lock_timeout interplay), stats
(pg_stat_force_next_flush + cumulative counters), horizons (EXPLAIN FORMAT json
ANALYZE + json ops + IOS pruning), intra-grant-inplace (GRANT tuple-xmax
lock-wait), temp-schema-cleanup (pg_my_temp_schema + per-session temp namespace),
prepared-transactions{,-cic} (2PC). Pick one next loop.
