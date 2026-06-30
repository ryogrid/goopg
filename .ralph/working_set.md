(idle — nothing in flight)

Loop #21 COMPLETE: M0119-0004 DU-002 slice 382 — `quote_ident` reserved-keyword
quoting (cross-cutting builtin fix discovered in slice 379).

`pgQuoteIdent` (internal/executor/expr.go ~11191 — server-side quote_ident
builtin, %I in format(), column/trigger-transition identifier rendering) claimed
to skip reserved words but had NO keyword check, so quote_ident('user') → bare
`user` vs PG's `"user"`. Mirrors ruleutils.c quote_identifier(): quote every
keyword whose category != UNRESERVED_KEYWORD.

Fix:
- NEW internal/executor/quote_ident_keywords.go: pgReservedQuoteKeywords (164
  kwlist.h PG18.3 entries, category != UNRESERVED). Generated from
  postgres/src/include/parser/kwlist.h via grep -v UNRESERVED_KEYWORD.
- pgQuoteIdent: after char-class loop, a safe all-lowercase ident in the map →
  unsafe → double-quoted. All callers (%I, builtin, triggers, col lists) fixed
  at once since they funnel through pgQuoteIdent.

Files: internal/executor/quote_ident_keywords.go (new),
internal/executor/quote_ident_keywords_test.go (new TestPgQuoteIdentReservedKeywords),
internal/executor/expr.go (pgQuoteIdent), internal/testport/pgdump_connsetup_test.go
(slice-382 fixture goopg_srv_kw + `"user" 'remote'` assert; slice-379 comment
updated), .ralph/deferral_ledger.md, docs/design/0110-0001-pg-dump-tap-port.md
(Slice 382).

Gates: TestPgQuoteIdentReservedKeywords PASS; full executor suite PASS (1.7s);
TestPort_PgDumpConnectionSetup PASS (5.2s); go build ./... clean; go vet testport
clean; gofmt clean. pgbench smoke = pre-commit hook. No TPC-H (metadata-only
builtin/virtual-catalog change). Deferral row added for two sibling quoters
(quoteViewIdent, quoteCollationIdent) that still lack the keyword check.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: fix quoteViewIdent +
quoteCollationIdent keyword quoting (needs leaf-pkg for the keyword set to avoid
catalog→executor import cycle); array-metachar quoting in optionsArrayLiteral
(cross-cutting w/ reloptions); ALTER SERVER/FDW OPTIONS; range types; aggregates;
operators; text-search configs; CREATE COLLATION.
