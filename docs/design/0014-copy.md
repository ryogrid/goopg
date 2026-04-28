# 0014 — COPY FROM/TO (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

`pgbench -i` and ecosystem tools depend on PostgreSQL COPY protocol:

- `COPY ... FROM STDIN`
- `COPY ... TO STDOUT`

Upstream uses dedicated protocol messages (`CopyInResponse`,
`CopyOutResponse`, `CopyData`, `CopyDone`, `CopyFail`) and routes COPY through
executor interfaces rather than regular DataRow framing.

References:

- `postgres/src/backend/commands/copy.c`
- `postgres/src/backend/tcop/postgres.c` (COPY protocol loop)

goopg currently has no COPY wire path.

## Decision

### SQL surface (v0)

Supported forms:

- `COPY table [(col, ...)] FROM STDIN [WITH (...)]`
- `COPY (SELECT ...) TO STDOUT [WITH (...)]`

Options (v0 subset):

- `FORMAT text` (default)
- `FORMAT binary` for wire framing only (CSV remains deferred)
- `DELIMITER`, `NULL` for text mode

Deferred:

- CSV mode
- `HEADER`, `QUOTE`, `ESCAPE`, `ENCODING`
- `PROGRAM`, server-local files

### Wire state machine

For `COPY FROM STDIN`:

1. Server sends `CopyInResponse`.
2. Client sends 0..N `CopyData` frames.
3. Client ends with `CopyDone` or aborts with `CopyFail`.
4. Server sends `CommandComplete` + `ReadyForQuery` (or ErrorResponse path).

For `COPY TO STDOUT`:

1. Server sends `CopyOutResponse`.
2. Server streams 0..N `CopyData` frames.
3. Server sends `CopyDone` + `CommandComplete` + `ReadyForQuery`.

### Execution seams

Introduce a dedicated `internal/copyexec` package:

- `FromStream(ctx, table, cols, options, nextChunk)`
- `ToStream(ctx, planNode, options, emitChunk)`

`FromStream` decodes rows and forwards them into executor insert path.
`ToStream` runs a plan and encodes each output row as COPY payload.

This avoids overloading regular `DataRow` code and keeps COPY framing
concerns local.

### Type encoding policy

Text mode:

- Reuse `executor.Datum.Format()` for outbound values.
- Reuse parser/type coercion helpers for inbound values.
- `\N` maps to NULL unless overridden by `NULL '...'` option.

Binary mode:

- Follow PostgreSQL file signature and per-column length/value format.
- v0 supports built-in scalar types already present in executor codec
  (`int4/int8/bool/text/timestamp`).

### Transaction behavior

COPY participates in current transaction state:

- Outside explicit transaction: statement-level implicit transaction.
- Inside explicit transaction: rows are visible/committed with the enclosing
  transaction.
- On mid-stream error: abort current COPY statement; in explicit transaction,
  mark transaction failed until ROLLBACK (upstream behavior).

## Alternatives Considered

- Implement COPY as syntactic sugar over batched INSERT/SELECT over `Q`.
  - Rejected: clients require COPY protocol messages and stream semantics.
- Implement TO first and defer FROM.
  - Rejected: `pgbench -i` depends on inbound bulk load path.
- Restrict to text and never add binary.
  - Rejected: binary is required by milestone goal; design keeps a clear seam.

## Consequences

- Server loop needs COPY-specific mode transitions and message guards.
- COPY decode/encode becomes a dedicated subsystem rather than ad-hoc string
  parsing in server query handler.
- pgbench initialization path can move from row-by-row DML to streaming bulk
  load once implemented.

## Implementation Status

- Landed: protocol message constants/writers for COPY responses/data and a
  minimal server-side COPY mode for simple-query flow:
  - `COPY (SELECT 1) TO STDOUT`
  - `COPY <name> FROM STDIN` with text-row counting and `COPY n` completion
- Pending: parser/planner/executor-backed relation COPY, binary format mode,
  and full pgbench-`-i` compatibility.
