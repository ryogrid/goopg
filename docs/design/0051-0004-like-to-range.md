# 0051-0004 — LIKE prefix to range translation

**Status:** accepted
**Date:** 2026-05-05
**Milestone:** 0051 — Planner expression-level improvements
**Supersedes:** —

## Context

`WHERE col LIKE 'foo%'` cannot use a B-tree on `col` today because the
planner does not understand that the predicate is equivalent to
`col >= 'foo' AND col < 'fop'`. Every TPC-H query with a prefix LIKE
(Q14, Q16, Q20) does a SeqScan even when an index exists.

The translation is local to the analyzer: derive the prefix, build a
synthetic range predicate, *add* it to the WHERE clause as a redundant
predicate. The original LIKE stays — it is necessary for correctness of
patterns like `'foo%bar'`.

## Implementation (landed 2026-05-05)

### New file: `internal/planner/likeprefix.go`

Three exported functions:

1. **`ExtractLikePrefix(pattern string) (prefix string, exact bool, ok bool)`**
   — walks the LIKE pattern character by character, consuming literal characters
   until the first unescaped `%` or `_` wildcard. SQL's default escape `\` is
   handled: `\%`, `\_`, and `\\` produce their literal character in the prefix.
   Returns `ok=false` when no prefix can be derived (pattern starts with `%` or
   `_`). `exact=true` when the entire pattern is literal (equivalent to `=`).

2. **`IncrementString(s string) (string, bool)`** — returns the smallest string
   strictly greater than `s` under C-collation byte ordering (the ordering
   goopg's B-tree uses). Algorithm: scan right-to-left for the first byte < 0xFF,
   increment it, drop all subsequent bytes. Returns `("", false)` for empty
   strings and all-0xFF strings.

3. **`injectLikeRangePredicates(where parser.Expr) parser.Expr`** — walks the
   top-level AND conjuncts of a WHERE clause. For each `col LIKE 'prefix%'`
   conjunct with a derivable prefix, appends:
   - `col >= 'prefix'` (inclusive lower bound)
   - `col < 'successor'` (exclusive upper bound, when a successor exists)
   The original LIKE is preserved — it acts as the post-filter guard (e.g.
   `'foo%bar'` patterns or exact patterns that the range over-approximates).
   NOT LIKE predicates are not transformed.

### Modified: `internal/planner/planner.go` — `planSelect()` WHERE branch

For the simple-single-table path, before calling `planIndexScanFromWhere`, the
WHERE expression is transformed by `injectLikeRangePredicates`. The result
(`whereForIndex`) is passed to `planIndexScanFromWhere`; the original `s.Where`
is used for the Filter predicate (so the LIKE check is always evaluated).

`tryRangeIndexScan` (M0039-0002) picks up the injected `>=` / `<` conjuncts and
builds `Filter(IndexScan{LowKey, HighKey}, fullPred)` automatically — no changes
to the range-scan infrastructure were needed.

### Collation gate

For v0, the transformation is always applied (gated on the default C-collation
assumption). Goopg's B-tree uses bytewise key encoding for text/varchar/char
(`EncodeVarchar` / `EncodeChar` in M0044), which matches C-collation byte order.

### Tests: `internal/planner/likeprefix_test.go` (10 tests)

- `TestExtractLikePrefix`: 16 cases — prefix patterns, exact, escape sequences,
  starts-with-wildcard (no prefix), underscore wildcard, empty pattern.
- `TestIncrementString`: 9 cases — simple increment, carry propagation, all-0xFF,
  empty string, TPC-H `PROMO` pattern.
- `TestLikeToRangeDoD_PrefixPattern`: DoD — `LIKE 'foo%'` with index →
  `Filter(IndexScan{LowKey:'foo', HighKey:'fop'}, pred)`.
- `TestLikeToRangeDoD_ExactPattern`: DoD — `LIKE 'foo'` (exact) with index →
  `Filter(IndexScan{LowKey:'foo', HighKey:'fop'}, pred)`.
- `TestLikeToRangeDoD_NoPrefix`: DoD — `LIKE '%foo%'` → SeqScan+Filter (no index).
- `TestLikeToRangeDoD_UnderscoreWildcard`: `LIKE '_foo%'` → no index.
- `TestLikeToRangeDoD_NoIndex`: `LIKE 'foo%'` without an index → SeqScan fallback.
- `TestLikeToRangeTPCHQ14Shape`: DoD — `p_type LIKE 'PROMO%'` with B-tree →
  `Filter(IndexScan{LowKey:'PROMO', HighKey:'PROMP'}, pred)`.

## Original Plan

1. New analyzer helper `internal/analyzer/likeprefix.go` with `ExtractPrefix`
   and `IncrementString`.
2. Analyzer's WHERE-clause pass adds synthetic range predicates.
3. Planner's index-scan selection picks up the range automatically.
4. Collation gate on C/POSIX ordering.

## Definition of Done

- TPC-H Q14 / Q20 against pgbench/HammerDB-shape data: IndexScan plan
  instead of SeqScan.
- Regression matrix:
  - `LIKE 'foo%'` → IndexScan with range predicate.
  - `LIKE 'foo'` → IndexScan with equality predicate.
  - `LIKE '%foo%'` → SeqScan (no prefix derivable).
  - `LIKE 'foo\%bar%'` → IndexScan with prefix `'foo\\'` → wait, need
    to handle escape correctly. Tests must cover this.
- Result equivalence with the original LIKE preserved (the
  redundant-predicate form returns the same rows).

## Upstream reference

- `postgres/src/backend/utils/adt/like_match.c`,
  `like.c` — pattern → prefix.
- `postgres/src/backend/utils/adt/selfuncs.c::make_greater_string` —
  successor-string construction.
- `postgres/src/backend/utils/adt/varlena.c` —
  `text_pattern_ops` opclass for non-C collations.

## goopg references

- `internal/analyzer/`, `internal/planner/scan.go`.
- `docs/design/0003-0011-like-pattern-matching.md` —
  current LIKE matcher (no planner integration).
