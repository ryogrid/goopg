(idle — nothing in flight)

Loop #94 COMPLETE: M0119-0004 DU-002 slice 363 — compound/function-call GENERIC
domain CHECK now dumps with PG's per-node parenthesization (resolves slice-362
deferred-(a)). `CHECK (VALUE > 0 AND VALUE < 100)` → `CHECK (((VALUE > 0) AND
(VALUE < 100)))`; `CHECK (length(VALUE) > 0)` → `CHECK ((length(VALUE) > 0))`,
instead of the legacy token-text wrap `CHECK ((<raw>))`. New
renderDomainCheckPredicate (domain twin of renderCheckPredicate) re-parses the
stored raw text + deparses via the fully-parenthesizing defaultExprToSQL, same
re-parse round-trip fallback guard. upcaseDomainValuePlaceholder walks the
re-parsed tree and rewrites every bare `value` ColumnRef back to uppercase
`VALUE` (lexer case-folds on re-parse; PG deparses CoerceToDomainValue uppercase).
Dump site routes ONLY generic CHECKs through it; `VALUE IN (...)` form
(len(d.CheckInValues)>0) keeps the legacy raw wrap (pre-synthesized byte-exact
ScalarArrayOp deparse). Single-comparison slice-96 domains byte-unchanged.
Byte-verified vs throwaway PG 18.3 (/tmp/du363_ref).

Files: internal/executor/operators_ddl.go (renderDomainCheckPredicate +
upcaseDomainValuePlaceholder, after renderCheckPredicate), internal/executor/
expr.go (AllDomains dump branch routes generic vs VALUE-IN), internal/executor/
check_predicate_render_test.go (TestRenderDomainCheckPredicate), internal/
testport/pgdump_connsetup_test.go (slice 363 dchkand/dchkfn DDL + dom-table cols
+ assertions), docs/design/0110-0001-pg-dump-tap-port.md (slice 363 entry),
.ralph/deferral_ledger.md, .ralph/fix_plan.md.

Deferred (ledgered): negative literal in a domain CHECK (`VALUE < -5` → PG
`'-5'::integer`) still byte-diverges — type-blind defaultExprToSQL emits bare
`-5`; re-parse guard keeps it on the legacy fallback (no garbage). Same gap as
slice-360(a)/362(b); needs operand-type threading into defaultExprToSQL.

Gates run: TestRenderDomainCheckPredicate + TestRenderCheckPredicate{,Fallback}
PASS; TestPort_PgDumpConnectionSetup PASS (5.6s); executor+parser+catalog unit
PASS; go vet clean; pgbench smoke = pre-commit hook.

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidates: the typed-literal
cast inside a CHECK/expression-index key (deferred 360(a)/362(b)/363 — needs
operand-type threading); action-command CREATE RULE (milestone-sized reverse-
compiler); reserved-keyword-named-role quoting; or another catalog-view gap.
