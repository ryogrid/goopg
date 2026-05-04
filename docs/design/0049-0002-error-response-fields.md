# 0049-0002 — Full ErrorResponse fields

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0049 — Protocol parity
**Supersedes:** —

## Context

goopg's `ErrorResponse` carries only `S` (severity), `C` (SQLSTATE), `M`
(message). psql / pgx / JDBC / framework output expects at least `D`
(Detail), `H` (Hint), `P` (statement Position). Without `P`, psql's
caret-pointer line is blank; without `D`/`H`, error messages lose
context that the runtime already knows.

## Plan

1. Extend `internal/protocol/errors.go::ErrorPayload`:
   ```go
   type ErrorPayload struct {
       Severity string  // S, V (V is non-localised severity)
       SQLState string  // C
       Message  string  // M
       Detail   string  // D — what specifically went wrong
       Hint     string  // H — what to do about it
       Position int     // P — 1-based byte offset into original query
       Where    string  // W — context stack (PL/pgSQL frame, etc.)
       Schema   string  // s
       Table    string  // t
       Column   string  // c
       File     string  // F — source file
       Line     int     // L — source line
       Routine  string  // R — source function
   }
   ```
2. **Position propagation.** Parser tokens already carry byte offsets
   (or grow them if not). Analyzer-level errors carry the offset of the
   first token of the offending construct. New `internal/parser`
   helper: `ErrorAt(offset int, code, msg string) error`.
3. **Schema/table/column.** Analyzer's name-resolution failures already
   know the target name; thread it into the error payload.
4. **Hint table.** A small map of upstream-aligned hints for common
   SQLSTATEs (e.g. 42P01 / `relation does not exist` → "Did you mean to
   use a table-qualified name?"). Generated alongside the existing
   SQLSTATE table.
5. **File/Line/Routine.** Pulled from `runtime.Caller` at error-creation
   time. Useful for goopg developers; unobtrusive to clients (most
   clients ignore these).
6. **Encoder.** `ErrorPayload.Encode()` writes one `(byte, NUL-terminated
   string)` pair per non-empty field, then a trailing NUL — matches
   upstream format byte-for-byte.

## Definition of Done

- Syntax error from the parser includes `P` and a human-quality `D`.
- "relation does not exist" includes `t` and `s`.
- psql renders the caret pointer line at the correct column.
- Wire round-trip test: golden-file replay of an ErrorResponse with all
  fields populated.

## Upstream reference

- `postgres/src/backend/utils/error/elog.c` — `ErrorData` field set.
- `postgres/src/backend/libpq/pqcomm.c` — wire format.
- `postgres/src/include/libpq/protocol.h` — error field tags.

## goopg references

- `internal/protocol/errors.go`.
- `internal/sqlstate/` — generated SQLSTATE table; gains hint column.
- `internal/parser/lexer.go` — token offset.
- `internal/analyzer/` — bind-failure error sites.
