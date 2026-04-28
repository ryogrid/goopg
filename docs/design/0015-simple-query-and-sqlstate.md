# 0015 — Simple Query Path (v0) and SQLSTATE Strategy

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

> Numbered out of the milestone-aligned 0010-0014 slots reserved in
> `.ralph/fix_plan.md` because this is a stop-gap for milestone 2 — most
> of the simple Query path will be re-derived from the parser+planner+
> executor design docs (0010-0012) once those land. We deliberately keep
> the higher numbers free for those.

## Context

Milestone 2 in `.ralph/fix_plan.md` calls for two things:

1. The simple Query message path returning a hand-rolled
   `RowDescription` / `DataRow` / `CommandComplete` / `ReadyForQuery`
   sequence for `SELECT 1`.
2. `ErrorResponse` for unrecognised statements with realistic
   `SQLSTATE` codes sourced from
   `postgres/src/backend/utils/errcodes.txt`.

There is no parser, planner, or executor yet. v0 has to fake just enough
to make a libpq client believe it is talking to PostgreSQL when it asks
for `SELECT 1`, while still emitting structurally-correct error responses
for anything else.

References into upstream:

- `postgres/src/backend/tcop/postgres.c:1011` — `exec_simple_query` is
  the entry point we conceptually mirror.
- `postgres/src/backend/access/common/printtup.c:166` —
  `SendRowDescriptionMessage` defines the per-attribute layout.
- `postgres/src/backend/access/common/printtup.c:303` — `printtup`
  emits one DataRow per tuple.
- `postgres/src/backend/tcop/dest.c:168` — `EndCommand` writes
  `CommandComplete`.
- `postgres/src/backend/tcop/cmdtag.c:121` —
  `BuildQueryCompletionString` formats the tag (e.g. "SELECT 1").
- `postgres/src/include/catalog/pg_type.dat` — `int4` is OID 23,
  `typlen=4`.
- `postgres/src/backend/utils/errcodes.txt` — the canonical SQLSTATE
  table.

## Decision

### Simple Query path

`internal/protocol` grows three encoders — `WriteRowDescription`,
`WriteDataRow`, `WriteCommandComplete` — plus
`WriteEmptyQueryResponse`. Each is a thin wire-format mapper, no
semantics. A `FieldDescription` struct mirrors upstream's per-attribute
layout (name, table OID, column attnum, type OID, type size, type
modifier, format).

`internal/server` grows `handleQuery`. The v0 dispatcher recognises:

- `SELECT 1` (case-insensitive, optional trailing `;`) — returns a
  one-row, one-column result. The column is `?column?` (matching what
  upstream's `parse_target.c:1721` returns for an unnamed expression),
  type `int4` (OID 23, typlen 4), text format, value `"1"`. Command tag
  is `"SELECT 1"`.
- Empty / whitespace-only query — returns
  `EmptyQueryResponse` followed by `ReadyForQuery('I')`, matching
  `postgres/src/backend/tcop/dest.c:NullCommand`.
- Anything else — `ErrorResponse` with SQLSTATE
  `feature_not_supported` (0A000) and a message that explains v0's
  capability honestly, followed by `ReadyForQuery('I')` so the client
  can keep going.

The dispatcher is a temporary shim, not a parser. The matcher will be
deleted the same loop the milestone-6 parser arrives — the `_test.go`
that covers `SELECT 1` becomes a parser-level expectation at that
point.

`MsgQuery` ('Q') joins `MsgTerminate` as a recognised frontend message;
unrecognised frontend message types still get the same
`feature_not_supported` ErrorResponse + ReadyForQuery treatment.

### SQLSTATE strategy

A new package `internal/sqlstate` exposes the full SQLSTATE table from
`postgres/src/backend/utils/errcodes.txt`. The package is **generated**,
not hand-written:

- A small CLI under `cmd/gen-sqlstate` parses the upstream file,
  converts each `errcode_macro_name` into a Go identifier
  (`ERRCODE_FEATURE_NOT_SUPPORTED` → `FeatureNotSupported`), and emits
  `internal/sqlstate/codes.go`.
- The generator produces:
  - One named `Code` constant per macro (so callers refer to
    `sqlstate.FeatureNotSupported`, not the magic string `"0A000"`).
  - A `severities` map keyed by SQLSTATE for `Lookup`.
  - A `Class` helper that returns the leading two characters.
- The generated file is committed (a regenerate-on-build approach is
  rejected for this milestone — a build that depends on the
  upstream tree's presence at a given path is a worse trade than
  re-running the generator manually when upstream changes).

When upstream's errcodes.txt has duplicate SQLSTATEs (six pairs at the
time of writing — e.g. `2202E` is shared by
`ERRCODE_ARRAY_ELEMENT_ERROR` and `ERRCODE_ARRAY_SUBSCRIPT_ERROR`), both
macros become Go constants but the `severities` map keeps a single
entry. Severities for the duplicate pairs are identical anyway.

`internal/server` and `cmd/gen-sqlstate` are the only two packages that
care about SQLSTATEs; downstream packages should import
`internal/sqlstate` directly when they need to emit a typed code rather
than reading magic strings out of test fixtures.

### Why not extend 0002?

`docs/design/0002-wire-protocol.md` covers the listener and the
startup handshake; it explicitly defers the simple Query path to its
own doc. The simple Query path is heavier (parser, planner, executor
in real builds) so it gets its own design lineage starting here, and
the proper coverage will arrive in `0010-parser.md` /
`0011-planner.md` / `0012-executor.md` when those land. This doc
records the *interim* decisions so they can be cleanly retired.

## Alternatives Considered

- **Hand-write the SQLSTATE constants we need.** Rejected: the
  generated table is small (~270 entries, ~15 KB), the generator is
  ~150 lines, and "every SQLSTATE goopg ever emits comes from this
  table" is much easier to enforce when typed constants are the only
  way to spell a code in Go.
- **Run the generator at `go generate` time and shell out to it from
  the build.** Rejected for v0: it adds a build-time dependency on the
  upstream tree's presence at a specific relative path. Re-running the
  generator manually when the upstream pin moves is rare enough
  (errcodes.txt churns slowly) that the simpler model wins.
- **Implement a tiny SQL parser to handle `SELECT 1` properly.**
  Rejected: that's milestone 6 work, and a half-finished parser is
  worse than a string match. The shim deletes cleanly when the real
  parser arrives.
- **Skip `EmptyQueryResponse` until the parser lands.** Rejected:
  libpq sends a heartbeat-style empty Query in some test harnesses;
  emitting EmptyQueryResponse correctly is one extra branch.

## Consequences

- A v0 client running `SELECT 1` against goopg now sees the same
  RowDescription / DataRow / CommandComplete / ReadyForQuery sequence
  it would see from real PostgreSQL, with the same column metadata
  (`?column?`, OID 23, typlen 4) and the same command tag (`SELECT 1`).
- Anything other than `SELECT 1` produces an honest
  `feature_not_supported` error with a clear message. Clients that
  probe with arbitrary catalog queries (e.g. `psql \d`) will get errors
  but the connection stays alive — milestone 2 explicitly punts that.
- `internal/sqlstate` is now the source of truth for SQLSTATEs across
  the codebase. The two existing magic-string call sites in
  `internal/server` (`0A000`, `08P01`, `57P01`) have been replaced with
  typed constants.
