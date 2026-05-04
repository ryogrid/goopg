# 0051-0004 — LIKE prefix to range translation

**Status:** draft
**Date:** 2026-05-04
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

## Plan

1. New analyzer helper `internal/analyzer/likeprefix.go`:
   - `ExtractPrefix(pattern string) (prefix string, exact bool)` —
     walks the pattern. Returns `("", false)` if the first char is `%`
     or `_` or `\`. Otherwise consumes literal chars until a `%`/`_`
     boundary; the `exact` flag is true iff the entire pattern is
     literal (no metachars at all).
   - `IncrementString(prefix string) (string, ok)` — returns the
     smallest string strictly greater than `prefix` under `bytea`-style
     comparison: increment the last byte, propagating carries. Fails
     (`ok=false`) when the prefix is `\xff\xff\xff…` (no successor).
2. Analyzer's WHERE-clause pass:
   - For each `LIKE`/`NOT LIKE` predicate where the RHS is a string
     literal:
     - Run `ExtractPrefix`.
     - If `exact == true`: the LIKE is equivalent to `=`; emit `col =
       prefix` as the redundant predicate.
     - Else if prefix non-empty: emit `col >= prefix AND col <
       successor`. (For `NOT LIKE`, no range translation — the planner
       cannot help.)
   - Wrap the original LIKE and the synthetic range in an `AND`. The
     range is `IndexCondition`-eligible; the LIKE remains as the post-
     filter for tail correctness.
3. Planner's index-scan selection (already handles `>=`/`<` predicates)
   picks up the synthetic range automatically.
4. Collation note: this translation is correct only under
   `C` / `POSIX` byte-collation. Under linguistic collations, the byte-
   order successor isn't necessarily the collation-order successor.
   For v0, gate the translation on the column's collation being `C`
   (or `<default>`); upstream's `make_greater_string` plus pattern_ops
   opclasses are the long-term path.

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
