# Milestone 0049 — Protocol parity: cancellation, error detail, SCRAM, COPY binary

**Status:** planned
**Depends on:** root-0002 (wire protocol), root-0013 (extended query), root-0014 (COPY), root-0003 (auth).
**Drives:** Close the wire-protocol gaps catalogued in `docs/reference/ref-021-protocol.md`. Specifically: query cancellation (`CancelRequest`), full ErrorResponse fields (Detail / Hint / Position / Where / SchemaName / TableName / ColumnName), SCRAM-SHA-256 authentication, and binary COPY format.

## 1. Context

`root-0013` shipped the extended query state machine and `root-0014` shipped the simple COPY path, but four protocol-level gaps remain before goopg behaves like a real PostgreSQL server to off-the-shelf clients:

1. **No query cancellation.** A long-running query cannot be interrupted from psql's `Ctrl-C`. The client tears the connection down instead. Upstream uses the `CancelRequest` message on a *secondary* connection, keyed by `(BackendPID, SecretKey)` issued in `BackendKeyData` at startup; the server-side cancel sets a flag the executor's tight-loop checks at safe points.
2. **Bare-bones ErrorResponse.** goopg emits only `S` (severity), `C` (SQLSTATE), `M` (message). pgx, psql, the JDBC driver, and almost every framework expect at least `D` (Detail), `H` (Hint), and `P` (statement Position) for diagnostic UX. Without them, syntax errors print without the caret-pointer line that users expect.
3. **No SCRAM-SHA-256.** Authentication options today are `trust` and `md5`. SCRAM is the upstream default since PostgreSQL 14. Cloud-managed clients in particular often *require* SCRAM and refuse to connect over MD5.
4. **No binary COPY.** `COPY ... WITH (FORMAT BINARY)` is rejected with `feature_not_supported`. Bulk-load tools (pg_dump, pg_restore, `\copy`-binary) cannot use goopg as a target. Text COPY works but parses 5–10× slower for typical schemas.

This milestone closes all four. They are independent and can be parallelised.

## 2. Required Design Docs

1. `docs/design/0049-0001-query-cancellation.md` — `CancelRequest` listener (separate goroutine that accepts plain TCP, parses the magic protocol header, looks up the backend by `(pid, secretKey)`, and sets a cancel flag). `Executor.checkCancellation()` poll points in tight loops (Open/Next loops, sort, hash build). New SQLSTATE 57014 (`query_canceled`).
2. `docs/design/0049-0002-error-response-fields.md` — extend `protocol.ErrorResponse` writer with Detail / Hint / Position / Where / SchemaName / TableName / ColumnName. Parser surfaces position from the lexer offset; analyzer surfaces schema/table/column from the binding context. SQLSTATE generation script gains an upstream-aligned hint table for top-N codes.
3. `docs/design/0049-0003-scram-sha-256.md` — RFC 5802 SCRAM client/server message exchange. Iteration count + salt stored alongside the password hash in `pg_hba.conf` / `pg_authid` (M0030). `auth-method = scram-sha-256` keyword. Channel-binding (tls-server-end-point) deferred unless trivial.
4. `docs/design/0049-0004-copy-binary-format.md` — binary COPY framing: 19-byte header (`PGCOPY\n\377\r\n\0` + 4 flag bytes + 4 extension-area-length), per-row int16 field-count + `[int32 length, length-prefixed bytes]` per column, trailer `0xFFFF`. Wire codecs reuse the existing per-type binary encoders from extended-query Bind/Execute.

## 3. Definition of Done

### 3.1 Query cancellation
- Cancel listener bound to the same address (PG protocol multiplexes cancel onto the same port via a magic startup code: `80877102 = 1234 * 65536 + 5678`).
- BackendKeyData carries a non-zero `(pid, secretKey)` per session.
- `psql` Ctrl-C against `SELECT pg_sleep(60)` returns within 200ms with SQLSTATE 57014.
- Regression test: cancel during a TPC-H query returns control to the dispatcher cleanly; subsequent simple queries on the same session work.

### 3.2 Error detail fields
- Syntax error from the parser includes `P` (1-based byte position into the original query) and `D` (a one-sentence what-was-expected).
- `relation does not exist` includes `T` (table name) and `n` (schemaName).
- psql renders the caret pointer line at the right column.
- Regression test: parser test suite asserts position on each diagnostic; analyzer test suite asserts table/column on each name-resolution failure.

### 3.3 SCRAM-SHA-256
- New `auth-method = scram-sha-256` recognised in `pg_hba.conf`.
- Server-side SCRAM exchange (SCRAM-SHA-256, no channel binding for the first cut) wired into `internal/auth`.
- pgx / psql / JDBC clients connect successfully against a SCRAM-only `pg_hba.conf` rule.
- Regression test: golden-file replay of a SCRAM exchange against a fixed iteration count + salt.

### 3.4 COPY binary
- `COPY ... TO ... WITH (FORMAT BINARY)` and `COPY ... FROM ... WITH (FORMAT BINARY)` accepted by parser & executor.
- Round-trip test: `COPY t TO 'binary'` then `COPY t FROM 'binary'` reproduces the original rows for every type currently supported by text COPY (int4, int8, numeric, text, varchar, char, date, timestamp, bool, bytea).
- pg_dump-style tool against goopg in binary mode loads ≥ 3× faster than the text path on a 1M-row table.

### 3.5 No regression
- `make ralph-state-guard` green every loop.
- All existing protocol / COPY / auth tests still green.
- TPC-H integration suite unchanged.

## 4. Out of scope

- SCRAM channel binding (`SCRAM-SHA-256-PLUS`) — separate follow-up.
- Async notify (`LISTEN` / `NOTIFY` integration with cancellation listener) — separate milestone.
- Logical-replication-aware `CopyBoth` mode (M0008 territory).
- Extended-query plan cache improvements (statement re-plan invalidation when stats / catalogs change).

## 5. Reference

- `postgres/src/backend/tcop/postgres.c` — `ProcessInterrupts`, cancel-flag check points.
- `postgres/src/backend/postmaster/postmaster.c` — `processCancelRequest`.
- `postgres/src/backend/utils/error/elog.c` — ErrorData fields.
- `postgres/src/backend/libpq/auth-scram.c` — SCRAM server.
- `postgres/src/backend/commands/copyfromparse.c`, `copyto.c` — binary COPY framing.
- `docs/reference/ref-021-protocol.md` — gap inventory.
- `docs/design/root-0002-wire-protocol.md`, `root-0013-extended-query-protocol.md`, `root-0014-copy.md`, `root-0003-authentication.md`.
