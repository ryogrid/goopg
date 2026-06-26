# 0118-0090 — async-notify.spec PROMOTED (M0118-0009 closure)

Status: accepted — `async-notify.spec` promoted `failed`→`pass` (all 6 permutations
byte-identical to PG 18.3). Builds on the engine subsystem from
[0118-0089](0118-0089-listen-notify-async.md).

## Problem

After 0118-0089 landed the engine-side LISTEN/NOTIFY/UNLISTEN/pg_notify subsystem,
`async-notify.spec` executed every statement PG-identically but could not be
promoted: the deferral was "harness-only" in the working set, but probing
(`RunAndCompare`) surfaced four distinct gaps, two of them engine bugs the
syntax-only wiring had hidden:

1. **(harness)** the `IsolationRunner` never captured the asynchronous `'A'`
   NotificationResponse messages, so none of the `notifier: NOTIFY "c1" …`
   lines appeared.
2. **(engine)** `pg_notification_queue_usage()` was registered in `pg_proc` but
   unimplemented (the `usage` step would error / return nothing).
3. **(harness + engine)** multi-statement steps (`notifyd1`, `notifys1`) were
   split by the runner into one `QueryContext` per statement — each its own
   autocommit transaction — defeating same-transaction NOTIFY de-duplication and
   making every statement publish/deliver separately (3 lines where PG emits 2).
4. **(engine)** the NOTIFY buffer was a flat list with no savepoint awareness, so
   `ROLLBACK TO SAVEPOINT` did not discard the notifications queued since the
   savepoint (`notifys1` emitted the rolled-back `rpayload*` notifications).

Upstream reference: `postgres/src/backend/commands/async.c`
(`asyncQueueUsage`, the per-subtransaction `pendingNotifies` stack with
`AtSubCommit_Notify`/`AtSubAbort_Notify`), and `isolationtester.c`
`try_complete_step` which polls `PQnotifies()` on every connection after each
step.

## Design

### 1. Harness: per-step notification capture (`isolation_runner.go`)

Each session's `*sql.DB` connector is now chained with
`pq.ConnectorWithNotificationHandler` on top of the existing
`ConnectorWithNoticeHandler`. lib/pq invokes the handler **synchronously** while
reading a query response to ReadyForQuery (`recv1Buf`'s `NotificationResponse`
case), so by the time a step's goroutine returns, every notification the server
delivered on that connection during the step is in a thread-safe
`sessionNotifyQueue`.

After each step, `drainAllNotifications` drains every session's queue in
session-declaration order and emits

```
<receiving-session>: NOTIFY "<channel>" with payload "<payload>" from <source-session>
```

resolving the source session from the notification's `BePid` via a
`pidToSession` map built once per permutation (`SELECT pg_backend_pid()` on each
session connection). This mirrors isolationtester polling `PQnotifies()` on all
connections at the end of `try_complete_step`. A no-op for every spec that does
not use LISTEN/NOTIFY (all queues empty).

goopg delivers a session's pending notifications at its own command boundary
(before ReadyForQuery), so self-notifies surface during the notifying step and
cross-backend notifies surface when the listener next runs a command
(`lcheck`/`COMMIT`) — exactly the timing the spec's `select 1` / commit steps
force.

### 2. Engine: `pg_notification_queue_usage()`

New `notifyHub.QueueUsage()` sums the undelivered notifications across all
listener inboxes and divides by a presentation denominator
(`notifyQueueCapacity`), returning a fraction in [0, 1]; wired to the executor
via `Context.NotifyQueueUsage` and the new `pg_notification_queue_usage` builtin
in `expr.go`. Returns 0.0 when the queue is empty (the common case).

The result is rendered with `strconv.FormatFloat(usage, 'g', -1, 64)` so an empty
queue renders as `"0"`, **not** `"0.000000000000000"`: a float8 *string* whose
fractional part is all zeros compares incorrectly against an integer literal in
goopg's text-vs-int comparison path (`'0.0'::float8 > 0` wrongly yields true,
while `'0'::float8 > 0` and `0.0::float8 > 0` are correct). The `'g'`/`-1`
minimal form sidesteps that pre-existing cast/compare bug and matches PG's float8
rendering.

### 3. Harness: multi-statement steps run as one implicit transaction

`execStep` now sends a multi-statement step body as a **single** simple-query
message (`execMultiStatement` → one `QueryContext`, iterating every result set
via `NextResultSet`), instead of splitting on `;` into separate queries. This
matches upstream isolationtester's `PQexec` and is the prerequisite for
transaction-scoped behaviors: the statements run in one implicit transaction, so
NOTIFY de-dup and ROLLBACK TO SAVEPOINT discard apply. Single-statement steps
keep the verbatim-body path (so `pg_stat_activity.query` matches PG byte-for-byte
— design 0118-0073).

A consequence surfaced and fixed in the server: `connTx.Begin` lazily creates the
`BasicSession`, so when `BEGIN` is the **first** statement of a multi-statement
message the session did not exist at dispatch entry (`ectx.Session` was wired
nil), and a later same-batch `SAVEPOINT` failed with "transaction statements
require Session in Context". The dispatch loop now re-wires `ectx.Session` from
`connTx.Session()` at the top of each statement iteration (and the `BEGIN`
handler sets it immediately after promotion).

### 4. Engine: savepoint-aware NOTIFY buffer (`conn_tx.go`)

`connTxState.pendingNotify` changed from a flat `[]Notification` to a stack of
`notifyLevel{name, notifs}` mirroring async.c's per-subtransaction
`pendingNotifies`:

- `pendingNotify[0]` is the transaction top level (name `""`); `SAVEPOINT name`
  pushes a new empty level (`notifySavepoint`).
- `bufferNotify` de-duplicates by `(channel,payload)` across **all** active
  levels (async.c dedups across the subtransaction stack) and appends new
  entries to the innermost level.
- `RELEASE SAVEPOINT name` (`notifyReleaseSavepoint`) merges the named level and
  everything above it into the enclosing level.
- `ROLLBACK TO SAVEPOINT name` (`notifyRollbackToSavepoint`) discards the named
  level's notifications and everything above it, keeping the savepoint active as
  the current level.
- `takePendingNotify` (at COMMIT) flattens all levels in order.

These are driven from the dispatch simple-query loop, which calls the matching
`connTx` method after each successful `SavepointStmt`/`ReleaseSavepointStmt`/
`RollbackToSavepointStmt`. Also: `pg_notify` now returns a non-NULL void
(`NewStringDatum("")`) so `count(pg_notify(...))` counts every row (the
`bignotify` step expects 1000, not 0 from `count` skipping NULLs); it still
renders as an empty field in a scalar SELECT.

## Blast radius

- Notification capture and `drainAllNotifications` are no-ops for every spec that
  does not LISTEN/NOTIFY (queues stay empty); the notify-hub `pending` is empty
  for all other specs, so `QueueUsage`/`deliverNotifications` are no-ops too.
- The savepoint-aware buffer is exercised only by NOTIFY-bearing transactions;
  the flat→stack change is behaviorally identical for the common single-level
  case.
- The multi-statement-as-one-query change is the broadest: it affects every spec
  with a multi-statement step. Verified by running the full
  `TestPort_Isolation*` suite (no regressions) plus the pgbench smoke.

## Verification

- `TestPort_IsolationAsyncNotify` strict PASS — all 6 permutations byte-identical.
- Full `TestPort_Isolation*` suite PASS (multi-statement execution-model change
  regression guard).
- `go build ./...`, `go vet`, gofmt clean on touched files.
- pgbench smoke via the pre-commit hook.

## Files

- `internal/testport/framework/isolation_runner.go` — `sessionNotifyQueue`,
  notification-handler chaining, `pidToSession`, `drainAllNotifications`,
  `execMultiStatement`/`scanResultSet`, multi-statement single-query routing.
- `internal/server/notify.go` — `notifyHub.QueueUsage`.
- `internal/server/conn_tx.go` — `notifyLevel` stack + savepoint-aware buffer.
- `internal/server/dispatch.go` — savepoint-marker wiring + `ectx.Session`
  re-wire.
- `internal/executor/context.go` — `NotifyQueueUsage` callback.
- `internal/executor/expr.go` — `pg_notification_queue_usage` builtin; `pg_notify`
  void return.
- `docs/test-port/postgres-oracle-target-inventory.csv` — async-notify
  `failed`→`pass`.
