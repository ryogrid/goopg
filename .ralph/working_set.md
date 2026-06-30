(idle — nothing in flight)

Loop #22 COMPLETE: M0119-0004 DU-002 slice 383 — sibling identifier-quoter
reserved-keyword fix. Closes the two siblings deferred in slice 382.

Lifted the 164-entry kwlist.h reserved-quote set out of internal/executor into a
NEW leaf pkg `internal/sqlkeywords` (no goopg-internal imports) exposing
`IsReservedForQuoting(s)`. This resolves the package-placement blocker: catalog
cannot import executor (import cycle), so both now import the leaf pkg.

Fix:
- NEW internal/sqlkeywords/keywords.go (set + IsReservedForQuoting), keywords_test.go.
- DELETED internal/executor/quote_ident_keywords.go (map moved to leaf pkg).
- pgQuoteIdent (executor/expr.go) delegates to sqlkeywords.IsReservedForQuoting.
- quoteViewIdent (executor/expr.go) + quoteCollationIdent (catalog/catalog.go)
  gained the same check after their char-class loop → a view alias / collation
  named `select` now renders "select" not bare. Removed the stale "reserved-word
  quoting is not reproduced" concession comment in quoteCollationIdent.

Files: internal/sqlkeywords/{keywords.go,keywords_test.go} (new),
internal/executor/quote_ident_keywords.go (deleted),
internal/executor/expr.go (import + pgQuoteIdent delegate + quoteViewIdent),
internal/catalog/catalog.go (import + quoteCollationIdent),
internal/executor/quote_ident_keywords_test.go (+TestQuoteViewIdentReservedKeywords),
internal/executor/viewdef_aliases_test.go (+reserved-keyword alias case),
internal/catalog/quote_collation_ident_test.go (new),
.ralph/deferral_ledger.md (slice 382→resolved + slice 383 resolved row),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 383 section).

Gates: leaf+executor+catalog pkg tests PASS; TestApplyViewColumnAliases PASS;
TestPort_PgDumpConnectionSetup PASS (5.3s, refactor preserved behaviour);
go build ./... clean (no import cycle); go vet clean; gofmt clean on edits
(expr.go/catalog.go gofmt hunks are pre-existing go1.25/1.26 version-mismatch
noise, not in my regions). pgbench smoke = pre-commit hook. No TPC-H (rendering-
only, no executor/planner row-path change).

Next loop: fresh M0119-0004 pg_dump slice. Candidates: array-metachar quoting in
optionsArrayLiteral (cross-cutting w/ reloptions, slice 380 deferral); ALTER
SERVER/FDW/USER MAPPING OPTIONS; external-binary pg_dump fixture for keyword view
alias/collation (low value); range types; aggregates; operators; text-search
configs; CREATE COLLATION.
