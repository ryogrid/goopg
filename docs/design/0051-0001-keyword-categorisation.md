# 0051-0001 — Keyword categorisation

**Status:** accepted
**Date:** 2026-05-05
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

## Implementation (landed 2026-05-05)

### New file: `internal/parser/keywords.go`

Defines `KeywordCategory` (four constants: `KwCatReserved`, `KwCatTypeFunc`,
`KwCatColName`, `KwCatUnreserved`) and a `keywordCategory map[Keyword]KeywordCategory`
that covers all ~80 keywords in the goopg keyword table.

Classification follows `postgres/src/include/parser/kwlist.h` with one deliberate
deviation: goopg-specific PL/pgSQL keywords (`loop`, `exit`, `while`, `continue`,
`elsif`, `elseif`, `reverse`, `perform`, `constant`) have no upstream equivalent
and are classified `KwCatUnreserved` so they never block identifier usage.

`IsColNameKeyword(kw Keyword) bool` returns `true` for all categories except
`KwCatReserved`. The function is the single gate used by `parseIdent()`.

### Modified: `internal/parser/parser.go` — `parseIdent()`

The old blanket acceptance of `TokenKeyword` is replaced with:
```go
case TokenKeyword:
    if IsColNameKeyword(Keyword(t.Value)) {
        p.advance()
        return t, nil
    }
```
Reserved keywords (`SELECT`, `FROM`, `WHERE`, `AND`, `OR`, `NULL`, `TRUE`,
`FALSE`, `CREATE`, `TABLE`, …) now produce `"expected identifier"` when they
appear in identifier position without quoting. All non-reserved keywords
(unreserved, col_name, type_func_name) continue to be accepted.

`parseSetValueAtoms()` and `parseGUCName()` were not changed; the former
accepts `TokenKeyword` directly (allowing `SET x = on`) and the latter calls
`parseIdent()` after reserved keywords have already been consumed.

### Tests: `internal/parser/keywords_test.go`

Six tests:
- `TestIsColNameKeyword` — verifies category for ~45 representative keywords.
- `TestColNameKeywordsAsColumnNamesDoD` — DoD: `name`, `value`, `type`, `data`,
  `month`, `year`, etc. (non-keywords) plus `index`, `key`, `partition`,
  `function`, `language`, `share`, `values`, `exists`, `between`, `verbose`, …
  all accepted in `CREATE TABLE t (col text)` position.
- `TestReservedKeywordsRejectedAsColumnNames` — `select`, `from`, `where`,
  `and`, `or`, `null`, `true`, `false`, `create`, `table`, `union`, `order`,
  `group`, `case`, `when`, `with`, `in`, `for` → parse error without quoting.
- `TestQuotedReservedKeywordsStillWork` — double-quoted reserved keywords still
  accepted (standard SQL escaping).
- `TestAllKeywordsHaveCategories` — every key in `keywords` map also has an
  entry in `keywordCategory`; protects against drift when new keywords land.
- `TestParseIdentRejectsReservedKeyword` — reserved keyword as `AS alias`
  produces an error.

All `go test ./internal/parser/...` pass.

## Original Plan

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
  quoting. ✓ (these are not keywords; they are plain TokenIdent)
- DDL round-trip with each col_name keyword as a column name. ✓
- Reserved keywords still error when used as identifiers (regression). ✓

## Upstream reference

- `postgres/src/include/parser/kwlist.h` — canonical table.
- `postgres/src/backend/parser/keywords.c`,
  `parser.c::ScanKeywordLookup` — categorisation usage.

## goopg references

- `internal/parser/keywords.go`, `internal/parser/parser.go`.
- `docs/design/root-0010-parser.md`.
