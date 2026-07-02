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

## Out of scope / deferred

- Making the per-session `log_statement` GUC functional (reading it via
  `sess.Get` and OR-ing with the env-var level) — recorded in
  `.ralph/deferral_ledger.md`. The env var is the single global toggle for now.
- `log_min_duration_statement` (duration-threshold logging), log-line prefix
  (`log_line_prefix`), and a `logging_collector`/`log_directory` file sink
  remain unimplemented GUC stubs.

## PostgreSQL references

- `postgres/src/backend/tcop/postgres.c` — `check_log_statement`,
  `exec_simple_query` (logs before parse), `exec_execute_message`
  (`LOG: execute …`).
- `postgres/official_docs_in_md/` — `log_statement` GUC semantics.
