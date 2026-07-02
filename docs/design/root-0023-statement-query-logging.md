# root-0023 — Statement / query logging (`GOOPG_LOG_STATEMENT`)

**Status:** accepted
**Filed:** 2026-07-02

## Problem

goopg had no way to log the SQL it receives. The `log_statement` and
`log_min_duration_statement` GUCs were registered
(`internal/config/defaults.go`) purely as inert stubs so that clients issuing
`SET log_statement = 'all'` (psql, connection pools) would not error — nothing
ever read them, and the dispatch path threaded no logger. The only logging was
`log/slog` connection-lifecycle events (`connection established`, listener
bound, checkpoints).

That gap blocks the WordPress-on-goopg verification work (milestones M0120 /
M0121, `wp/verification/`). To verify that WordPress operations behave
correctly on goopg we must capture the exact SQL WordPress (through the PG4WP
MySQL→PostgreSQL translation layer) issues — for **successful** operations as
much as failing ones — and correlate it with the goopg-side transaction. There
was no server-side switch to emit that stream.

## Design

Add an environment-variable-controlled, PostgreSQL-faithful statement log.

### Control surface

`GOOPG_LOG_STATEMENT` ∈ `none | ddl | mod | all` (default `none`), mirroring
PostgreSQL's `log_statement` GUC and its severity ordering
(`none < ddl < mod < all`, where `mod` is a superset of `ddl`). See
`postgres/src/backend/tcop/postgres.c` (`check_log_statement`) and
`postgres/official_docs_in_md/` (`log_statement`).

The value is read once in `cmd/goopg/main.go` (`run()`, alongside the existing
`GOOPG_*` toggles such as `GOOPG_DISABLE_NLI`) and passed as
`server.Config.LogStatement`. `server.New` parses it once into an unexported
`Server.logStmtLevel`; an unrecognised value logs a `WARN` and disables logging
(treated as `none`), an enabled level logs an `INFO` at startup. Keeping the
knob an env var (not the per-session GUC) gives a single global server-side
toggle, which is what a verification run wants — the whole server's traffic,
not one client session's `SET`.

### Where statements are logged

`(*Server).logStatement(proto, sql, connTx)` (`internal/server/statement_log.go`)
is a no-op (a single enum compare) when disabled, so it is safe on the hot path.

- **Simple protocol** — `internal/server/query.go`, `handleQuery`, immediately
  after the query string is extracted and the empty-query case is handled. This
  is the earliest point the full string is known and precedes every downstream
  route (the string-match fast path, the CREATE/DROP DATABASE|ROLE parse-error
  intercepts, and the parser-driven executor), so **all** simple-query messages
  are captured — matching PG, which logs in `exec_simple_query` before parse.
- **Extended protocol** — `internal/server/dispatch_extended.go`,
  `executeExtendedQueryViaExecutor`, right after a successful `parser.Parse`. It
  logs the portal's source query at Execute (not per Bind), so a reused
  parameterised portal is logged once, and the log shows the query with its `$N`
  placeholders, mirroring PG's `LOG: execute <portal>: <query>`.

### What is logged

An `slog` `INFO` record `msg="statement"` with attributes:

- `proto` — `"simple"` or `"extended"`.
- `statement` — the trimmed SQL text (placeholders intact for extended).
- `xid` — the assigned transaction id, **only** when the connection is in an
  explicit transaction (`connTx.InExplicit()`) and the xid has been assigned
  (`mvcc.Transaction.XID != 0`). goopg assigns xids lazily (M0093): the first
  write in a transaction assigns it, so a `BEGIN` and the first `INSERT` may log
  no xid while the following `UPDATE`/`COMMIT` log the now-assigned id. This is
  correct and still groups a transaction's statements by their shared xid.

Output rides the existing `slog` text handler → stderr, which the WordPress
stack already redirects to `wp/goopg-wp.log`, so no new sink/logfile GUC is
introduced.

### Classification

`mod`/`ddl` filtering uses `leadingKeyword` — the upper-cased first SQL keyword
after skipping leading `--` / `/* */` comments — checked against `ddlKeywords`
and `writeDMLKeywords`. This is deliberately a cheap lexical bucket rather than
node-tag classification: the primary verification use is `all` (which logs
unconditionally), and exact node-tag matching would couple the log to parser
internals for little gain. `mod` logs write-DML (`INSERT/UPDATE/DELETE/MERGE/
COPY`) plus every DDL verb; `ddl` logs only DDL.

## Testing

- `internal/server/statement_log_test.go` — unit tests for
  `parseLogStatementLevel`, `leadingKeyword` (incl. comment skipping), and the
  per-level `shouldLog` matrix.
- End-to-end (2026-07-02): a capped goopg on `127.0.0.1:5533` with
  `GOOPG_LOG_STATEMENT=all` logged every statement of a scripted psql session —
  `CREATE`, `BEGIN`, `INSERT`, `UPDATE` (`xid=4`), `COMMIT` (`xid=4`), `SELECT`,
  `DELETE` on the simple path, and a `\bind`-driven `SELECT … WHERE id = $1` on
  the extended path (`proto=extended`).
- `go test ./internal/server/ ./internal/config/` green; `go build ./...` green.

## Follow-up: per-session `log_statement` GUC (loop #38)

**LANDED.** A client `SET log_statement = 'all'` (or `ddl`/`mod`) now takes
effect even when `GOOPG_LOG_STATEMENT` is quieter or unset, matching
PostgreSQL's single `log_statement` GUC (the env var is just this server's
boot-time default for it, not a ceiling).

`(*Server).effectiveLogStatementLevel(sess)` (`internal/server/statement_log.go`)
reads the session's effective `log_statement` value via `sess.Get`, parses it
with the existing `parseLogStatementLevel`, and returns whichever of the env
level and the session level is louder (`none < ddl < mod < all`, so a plain
integer `>` comparison on the enum works). `logStatement` now takes `sess
*config.SessionRegistry` and calls this helper instead of reading
`s.logStmtLevel` directly; both call sites (`query.go:handleQuery`,
`dispatch_extended.go:executeExtendedQueryViaExecutor`) already had `sess` in
scope, so no new plumbing was needed. `sess == nil` (not expected on either
live call site, but defensive) falls back to the env level alone.

Test: `TestEffectiveLogStatementLevel` (`internal/server/statement_log_test.go`)
covers env-louder, session-louder, and both-equal-but-different-verb cases
plus the nil-session fallback.

## Follow-up: `log_min_duration_statement` GUC (loop #40)

**LANDED.** A client `SET log_min_duration_statement = <ms>` now emits a
duration line after each statement, mirroring PostgreSQL's
`check_log_duration` (`postgres/src/backend/tcop/postgres.c`): `-1` (the
GUC's own `BootVal`) disables duration logging entirely, `0` logs every
statement's duration unconditionally, and a positive value requires the
elapsed time to be `>=` the threshold.

`sessionLogMinDurationStatement(sess)` (`internal/server/statement_log.go`)
reads the session's effective `log_min_duration_statement` value and returns
it in milliseconds, with `-1` as the shared disabled/missing/unparseable
sentinel. `exceedsLogMinDuration(elapsedMs, thresholdMs)` is the pure
threshold comparison. `(*Server).logDuration(start, wasLogged, proto, sql,
sess, connTx)` times the statement and emits a `"duration"` log record: when
`wasLogged` is `true` (the `logStatement` call for the same statement already
printed the text) it emits a bare `duration_ms` line; when `false` it also
includes the `statement` text, exactly as PG's `check_log_duration` avoids
double-printing when `log_statement` already logged the text but folds it in
when only `log_min_duration_statement` fired. `logStatement` now returns a
`bool` (`wasLogged`) for this purpose instead of `void`.

Both live call sites (`query.go:handleQuery`,
`dispatch_extended.go:executeExtendedQueryViaExecutor`) wrap the statement
with `stmtStart := time.Now()` / `defer s.logDuration(...)` right after the
`logStatement` call, so every return path (string-match fast paths, executor
dispatch, error returns) is timed — mirroring PG's placement of
`check_log_duration` at the end of `exec_simple_query`/`exec_execute_message`
regardless of which code path a statement takes.

Tests (`internal/server/statement_log_test.go`): `TestSessionLogMinDurationStatement`
(GUC read + sentinel), `TestExceedsLogMinDuration` (threshold matrix),
`TestLogDurationEmitsCombinedOrBareLine` (a `capturingHandler` `slog.Handler`
asserts the actual emitted record — bare vs combined line, disabled, and
below-threshold cases), `TestLogStatementReturnsWasLogged`.

## Follow-up: `log_line_prefix` GUC (loop #42)

**LANDED (partial subset).** `log_line_prefix` is registered
(`internal/config/defaults.go`) as `ContextSigHup` with `BootVal` `"%m [%p] "`,
matching upstream `guc_tables.c` exactly: like real PostgreSQL it is
config-file-only (a client `SET log_line_prefix = ...` is rejected, since
`ContextSigHup` is below the `SET`-allowed threshold), so it is picked up
through `postgresql.conf`/`-c log_line_prefix=...` via the existing
`ParseConfigFile`/`ApplyConfigEntries` path (`cmd/goopg/main.go`), not through
any new env var.

`formatLogLinePrefix(format, logLineFields)` (`internal/server/statement_log.go`)
expands the format string against a small field struct, mirroring
`postgres/src/backend/utils/error/elog.c`'s `log_status_format`. It supports
the subset of `%`-escapes goopg's per-statement logger has real data for at
its two call sites:

- `%m` — timestamp (`time.Now()`, `"2006-01-02 15:04:05.000 MST"`).
- `%p` — backend PID (`connTx.BackendPID`).
- `%u` — user (`sess.Get("session_authorization")`; approximates PG's
  connection-frozen `MyProcPort->user_name` — this drifts after `SET SESSION
  AUTHORIZATION`, an accepted simplification for a log-correlation aid).
- `%d` — database (`connTx.DBName`).
- `%a` — application name (`sess.Get("application_name")`).
- `%x` — top-level transaction id (`connTx.Tx().XID`, `0` when none assigned,
  matching `GetTopTransactionIdIfAny`).
- `%%` — literal `%`.
- Numeric padding (`%-10p`-style) is honoured, including PG's negative-width
  left-justify convention.

Every other escape (`%l`, `%c`, `%e`, `%r`, `%h`, `%i`, `%t`, `%n`, `%s`, `%v`,
`%P`, `%b`, `%L`, `%Q`, or any unrecognised letter) is dropped — goopg's
structured logger has no backend-type/remote-host/per-process-line-counter
context at these call sites, and PG itself silently ignores an unrecognised
specifier (`elog.c`'s `default: /* format error - ignore it */`).

`(*Server).logLinePrefix(sess, connTx)` reads the registry's current
`log_line_prefix` value and formats it; `(*Server).prefixAttr(...)` wraps
that as a leading `"prefix"` slog attr, or an empty slice when the GUC
expands to `""` (PG's own "no prefix" default). Both `logStatement` and
`logDuration` (`internal/server/statement_log.go`) prepend this attr.

Tests: `TestFormatLogLinePrefix` (the pure formatter — every supported
escape, padding, unknown-field placeholders, unrecognised-escape drop,
trailing `%`), `TestServerLogLinePrefix` (registry-driven attr attach/omit
through `logStatement`).

## Out of scope / deferred

- `log_line_prefix`'s remaining escapes (`%l` line number, `%c` session id,
  `%e` SQL state, `%r`/`%h` remote host, `%i` ps display, `%t`/`%n`/`%s`
  timestamps, `%v` vxid, `%P` parallel leader pid, `%b` backend type, `%L`
  local host, `%Q` query id) are not expanded — they need per-connection
  state (remote address, backend type, a per-process log-line counter, ...)
  goopg's statement/duration logger doesn't carry today.
- `log_line_prefix` is wired only into the two `root-0023` statement/duration
  log lines, not goopg's other `slog` output (connection lifecycle, WAL,
  checkpoints, ...) — real PostgreSQL applies it to every server log line.
- A `logging_collector`/`log_directory` file sink remains unimplemented.

## PostgreSQL references

- `postgres/src/backend/tcop/postgres.c` — `check_log_statement`,
  `check_log_duration`, `exec_simple_query` (logs before parse),
  `exec_execute_message` (`LOG: execute …`).
- `postgres/src/backend/utils/error/elog.c` — `log_status_format` (the
  `log_line_prefix` `%`-escape expander).
- `postgres/src/backend/utils/misc/guc_tables.c` — `log_line_prefix`'s
  `PGC_SIGHUP` context and `"%m [%p] "` `BootVal`.
- `postgres/official_docs_in_md/` — `log_statement`,
  `log_min_duration_statement`, and `log_line_prefix` GUC semantics.
