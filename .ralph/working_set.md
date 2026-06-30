(idle — nothing in flight)

Loop #93 COMPLETE: M0119-0004 DU-002 slice 362 — a compound (`a > 0 AND b > 0`)
or function-call (`length(name) > 0`) table-level CHECK constraint now dumps with
PostgreSQL's per-node parenthesization (`CHECK (((a > 0) AND (b > 0)))`,
`CHECK ((length(name) > 0))`) instead of goopg's legacy token-text wrap
`CHECK ((<raw>))`. goopg stores a CHECK body as token-reconstructed raw text
(parser.parseCheckExpr); the new renderCheckPredicate re-parses it and deparses
via the fully-parenthesizing defaultExprToSQL (the same renderer the index-
predicate / expression-index / partition-key paths use), wrapping once as
`CHECK (%s)`. A re-parse round-trip guard falls back to the raw wrap if the
deparse is not re-parseable (so no non-SQL garbage). Single-comparison slices
127-129 are byte-unchanged. Byte-verified vs a throwaway PG 18.3 cluster.

Files: internal/executor/operators_ddl.go (renderCheckPredicate, after
defaultExprToSQL), internal/executor/expr.go (table-NamedChecks render site now
calls it), internal/executor/check_predicate_render_test.go (new unit),
internal/testport/pgdump_connsetup_test.go (slice 362 chkand/chkor/chkfn DDL +
assertions + negative guards), docs/design/0110-0001-pg-dump-tap-port.md
(slice 362 entry), .ralph/deferral_ledger.md.

Deferred (ledgered): (a) domain CHECK predicates still use the legacy raw wrap
(domain path also owns the VALUE IN ScalarArrayOp renderer + VALUE keyword → own
slice); (b) type-blind literal casts inside a CHECK (`'_x'` not `'_x'::text`,
same gap as slice 360(a) — defaultExprToSQL has no operand type in scope).

Gates run: TestRenderCheckPredicate{,Fallback} PASS; TestPort_PgDumpConnectionSetup
PASS (5.9s); executor+parser+catalog unit PASS; go vet clean; pgbench smoke =
pre-commit hook.

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidates: convert the domain
CHECK path to renderCheckPredicate (deferred (a) above); the slice-360(a) typed
literal cast inside a function-arg/CHECK key (needs operand-type threading);
action-command CREATE RULE (milestone-sized reverse-compiler); or another
catalog-view gap.
