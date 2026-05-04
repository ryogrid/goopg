# 0051-0001 — Keyword categorisation

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0051 — Planner expression-level improvements
**Supersedes:** —

## Context

`internal/parser/keywords.go` today has a flat list — every keyword is
reserved. A column called `name`, `value`, `type`, `user`, `data`, …
forces double-quoting in DDL. Real-world frameworks generate DDL that
uses these names without quoting. Upstream splits the keyword space:

- **Reserved.** Cannot be used as an identifier anywhere.
- **type_func_name.** Reserved in type/function-name positions only.
- **col_name.** Reserved in some contexts but usable as a column name.
- **unreserved.** Always usable as an identifier.

`postgres/src/include/parser/kwlist.h` is the canonical table.

## Plan

1. Replace the flat keyword list with `[]KeywordEntry{{Name, Token,
   Category}}`. Categories: `reserved`, `type_func_name`, `col_name`,
   `unreserved`.
2. Lexer always returns the keyword's token. Parser-level helper
   `acceptIdentOrColNameKeyword()` decides whether the current token
   is acceptable as an identifier in the current production.
3. Audit each parser production where an identifier is expected and use
   the new helper; generate a regression matrix from the upstream
   `kwlist.h` so we don't drift.
4. CREATE TABLE / CREATE INDEX / CREATE VIEW / CREATE FUNCTION /
   ALTER TABLE all gain the unquoted col_name keyword forms.

## Definition of Done

- A user-defined column named `name`, `value`, `type`, `user`, `data`,
  `time`, `month`, `year`, `version` (the col_name set) parses without
  quoting.
- DDL round-trip with each col_name keyword as a column name.
- Reserved keywords still error when used as identifiers (regression).

## Upstream reference

- `postgres/src/include/parser/kwlist.h` — canonical table.
- `postgres/src/backend/parser/keywords.c`,
  `parser.c::ScanKeywordLookup` — categorisation usage.

## goopg references

- `internal/parser/keywords.go`, `internal/parser/parser.go`.
- `docs/design/root-0010-parser.md`.
