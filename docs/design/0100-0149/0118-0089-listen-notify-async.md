# 0118-0089 — LISTEN / NOTIFY / UNLISTEN + pg_notify (M0118-0009 slice: async-notify)

Status: accepted (engine-side subsystem; full `async-notify.spec` promotion deferred — see "Deferred").

## Problem

`async-notify.spec` (D-002, M0118-0009) exercises PostgreSQL's asynchronous
notification subsystem: `LISTEN`, `NOTIFY [, payload]`, `UNLISTEN [channel|*]`,
and the `pg_notify(channel, payload)` SQL function, including cross-session
delivery, self-notification, transaction-scoped visibility (notifications become
visible only at the notifying transaction's COMMIT), and per-transaction
de-duplication of identical notifications.

Before this change goopg had none of it: every `LISTEN`/`NOTIFY`/`UNLISTEN`
statement failed at the parser ("syntax error … unsupported statement"), and
`pg_notify` raised `42883 function does not exist`. The whole spec diverged on
its first NOTIFY.

Upstream reference: `postgres/src/backend/commands/async.c` (the SLRU-backed
shared notification queue, `Async_Notify`/`Async_Listen`, commit-time
`PreCommit_Notify` → `AtCommit_Notify`, and `ProcessIncomingNotify` delivery at
a command boundary). The `'A'` NotificationResponse wire message is
`src/backend/commands/async.c` → `pq_putmessage('A', ...)`.

## Design

goopg multiplexes every connection inside one OS process, so the cross-process
SLRU queue collapses to an in-memory, mutex-guarded hub.

### Components

1. **Parser** (`internal/parser`): new `ListenStmt`, `NotifyStmt`,
   `UnlistenStmt` AST nodes; `LISTEN`/`NOTIFY`/`UNLISTEN` are identifier-led
   statements wired into `parseStatement`'s ident switch. The channel is parsed
   as a regular identifier (unquoted → lexer-folded; double-quoted → case
   preserved); NOTIFY accepts an optional `, 'payload'` string literal; UNLISTEN
   accepts `*`.

2. **`notifyHub`** (`internal/server/notify.go`): the server-wide exchange.
   `listeners[channel] = set<*config.SessionRegistry>` and
   `pending[session] = []Notification`, both under one mutex. `Listen` /
   `Unlisten` / `UnlistenAll` mutate registrations; `Notify(channel, payload,
   srcPID)` appends to every listener's inbox (including the sender when it
   listens — PostgreSQL delivers self-notifications); `DrainPending` removes and
   returns a session's queued notifications; `RemoveSession` clears everything
   for a session at connection teardown. Sessions are keyed by the stable
   per-connection `*config.SessionRegistry` (the same identity used as the
   advisory-lock owner), so registrations survive across per-statement executor
   contexts.

3. **Transaction-scoped visibility** (`internal/server/conn_tx.go`): `NOTIFY`
   does **not** publish immediately — it buffers into
   `connTxState.pendingNotify` via `bufferNotify`, which collapses an exact
   `(channel,payload)` duplicate already buffered by the same transaction
   (async.c de-duplication). On a successful COMMIT (autocommit batch end in
   `dispatchSimpleQueryViaExecutor`, or explicit `COMMIT` in
   `executeOneSimpleStmt`) the server calls `publishPendingNotify`, which flushes
   the buffer into the hub. On ROLLBACK / failure the buffer is discarded
   (`connTxState.End` clears it).

4. **Delivery** (`internal/server/dispatch.go`): just before each trailing
   `ReadyForQuery`, `deliverNotifications` drains this session's hub inbox and
   writes one `'A'` `NotificationResponse` per entry (new
   `protocol.WriteNotificationResponse`: int32 source PID, channel C-string,
   payload C-string). This is the point at which PostgreSQL delivers queued
   notifications to an otherwise-idle backend. Self-notifications published during
   the same command are therefore delivered at that command's own ReadyForQuery;
   cross-session notifications are delivered when the listening session next
   reaches a command boundary.

5. **`pg_notify(channel, payload)`** (`internal/executor/expr.go`): the
   SQL-function form. It buffers via a new `Context.QueueNotify` callback that the
   server wires to `connTxState.bufferNotify`, so it shares the exact
   commit-time publish path as the `NOTIFY` statement. A NULL payload is treated
   as the empty payload; returns void.

### Blast radius

Additive. The only change to existing code paths is the `deliverNotifications`
call before `ReadyForQuery` (writes zero frames unless notifications are
pending — never on the normal OLTP/TPC path) and the plan-cache precompute now
skips LISTEN/NOTIFY/UNLISTEN (the planner has no node for them; they are handled
in `executeOneSimpleStmt` before planning). No MVCC / storage / WAL / codec
surface is touched.

## Verification

- `TestParseListenNotifyUnlisten` (6 subtests) — grammar incl. quoted-channel
  case preservation, optional payload, `UNLISTEN *`.
- `TestNotifyHub` — cross-session delivery, self-notify, no-listener drop,
  UNLISTEN/UNLISTEN-*/RemoveSession cleanup, destructive drain.
- `TestConnTxBufferNotifyDedup` — per-transaction `(channel,payload)`
  de-duplication.
- Wire path confirmed against `async-notify.spec`: every `LISTEN`/`NOTIFY`/
  `UNLISTEN`/`pg_notify` statement now executes and returns PG-identical step
  output (previously a syntax error on the first NOTIFY); the engine emits `'A'`
  frames on the listening session's next command boundary.
- pgbench TPC-B smoke (mandatory pre-commit) — 0-failed, no TPS regression
  (delivery path is a no-op on the hot path).

## Deferred (ledger 2026-06-24)

Full `async-notify.spec` promotion `defer`→`pass` needs **test-harness** work,
not engine work: `internal/testport/framework/IsolationRunner` must capture the
`'A'` notifications (now unblocked — lib/pq exposes
`pq.ConnectorWithNotificationHandler`, which fires synchronously while reading a
query response, matching goopg's command-boundary delivery) and emit them in
isolationtester's `<recv-session>: NOTIFY "<channel>" with payload "<payload>"
from <src-session>` format with byte-exact placement (source PID → session-name
mapping, ordering relative to step output). Also deferred: asynchronous push of
notifications to a genuinely *idle* backend (goopg delivers only at the next
command boundary); sufficient for isolationtester (every step is a command) but
a real interactive `LISTEN`-then-wait client sees a notification only after its
next statement.
