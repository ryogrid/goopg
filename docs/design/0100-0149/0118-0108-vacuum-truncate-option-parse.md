# 0118-0108 — `VACUUM (TRUNCATE …)` option parse (index-only-bitmapscan enabler)

**Milestone:** M0118-0002 / M0118-0009 (isolation spec pass-through)
**Status:** Landed — **enabler, NOT a spec promotion**
**Date:** 2026-06-25 (loop #47)

## Summary

`VACUUM (TRUNCATE false) <table>` — documented PostgreSQL syntax — was rejected
by goopg with `ERROR: unrecognised VACUUM option (got truncate)`. The
parenthesised VACUUM option list already had a `truncate` case, but it matched
only via `acceptIdentKeyword("truncate")`, which consumes a `TokenIdent`. The
lexer classifies `TRUNCATE` as the unreserved keyword `KwTruncate` (it also
leads the `TRUNCATE TABLE` statement), so the case never fired and parsing fell
through to the `default` error.

Fix: accept the keyword token too —
`case p.acceptKeyword(KwTruncate) || p.acceptIdentKeyword("truncate"):` — in
`internal/parser/parser.go` (`parseVacuum` option loop). `TRUNCATE` is the only
VACUUM option word that is also a SQL keyword; the other identifier-style
options (`process_main`, `index_cleanup`, `skip_locked`, …) are unaffected.

## Behavioral semantics

`v.NoTruncate` is recorded in the AST for parity and future work, but goopg's
VACUUM (`internal/vacuum/vacuum.go` `vacuumCore`) never physically truncates
trailing empty pages — it prunes/redirects line pointers and emits
`RecordKindHeapPruneOpt`, but issues no `smgr` relation truncation. Therefore
both `TRUNCATE false` and `TRUNCATE true` are behaviorally no-ops today, and
`NoTruncate` is honoured trivially. When relation truncation is eventually added
to VACUUM, `NoTruncate` is the existing hook to gate it. (Upstream: VACUUM's
truncation phase takes `AccessExclusiveLock`; `TRUNCATE false` exists precisely
to skip that lock — see `postgres/src/backend/commands/vacuumlazy.c`
`lazy_truncate_heap` / `vacuum.md`.)

## Why this is an enabler, not a promotion

This clears the **first** of the `index-only-bitmapscan.spec` divergences (the
`s2_vacuum` step `VACUUM (TRUNCATE false) ios_bitmap;`). Two Effort-L blockers
remain, so the spec stays `defer`:

1. `EXPLAIN (COSTS OFF) DECLARE foo … CURSOR FOR <query>` — the EXPLAIN executor
   does not handle `*parser.DeclareCursorStmt` (`unsupported statement type`).
2. The expected plan is a **`BitmapOr` over two `Bitmap Index Scan`s** under a
   `Bitmap Heap Scan` + `WindowAgg`; goopg does not currently generate or
   EXPLAIN a bitmap-OR plan for `a > 0 OR b > 0`. Producing and formatting that
   plan byte-for-byte is the spec's core requirement.

The cursor `FETCH` path and row output already match PG 18.3; only the EXPLAIN
plan and the cursor-EXPLAIN statement type stand between this enabler and a
promotion.

## Verification

- `go test -run TestParseVacuum ./internal/parser/` — PASS (new
  `TestParseVacuumTruncateOption`: `TRUNCATE false/FALSE/true/<bare>` and mixed
  with `VERBOSE`/`ANALYZE`).
- `go build ./...` clean.
- Re-probe of `index-only-bitmapscan.spec`: first divergence moved from the
  `s2_vacuum` syntax error to the `s1_explain` `DeclareCursorStmt` blocker,
  confirming the VACUUM step now parses and runs.

## Files

- `internal/parser/parser.go` — `parseVacuum` TRUNCATE option case.
- `internal/parser/parser_test.go` — `TestParseVacuumTruncateOption`.
