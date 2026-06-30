(idle — nothing in flight)

Loop #95 COMPLETE: M0119-0004 DU-002 slice 364 — a unary minus applied DIRECTLY
to a numeric literal now dumps as PG's folded `'-N'::type` Const form everywhere
the two pg_dump deparse twins feed (CHECK table+domain, DEFAULT, expr-index key,
partition-key, func-arg default). RESOLVES the long-recurring negative-literal
deferral behind slices 302/360(a)/362(b)/363.

Key insight that unblocked it: the cast type is the LITERAL's own (negated)
magnitude type (get_const_expr/make_const), NOT the column type — so NO
operand-type threading was needed (the prior deferral's assumed blocker). bigint
col `<> -100` → `'-100'::integer`; `DEFAULT -9000000000` → `'-9000000000'::bigint`;
`DEFAULT -2147483648` (INT_MIN) → `'-2147483648'::integer` (boundary mag<=1<<31).

New shared helper `parser.NegatedLiteralSQL` (internal/parser/expr.go) renders
`'-N'::{integer,bigint,numeric}` for a bare IntegerConst/NumericConst, "" else.
Both twins (executor.defaultExprToSQL, catalog.formatExprForAttrdef) call it in
their OpUnaryNeg arm; compound `(- (operand))` fallback unchanged (negdef.nb/nc).

Files: internal/parser/expr.go (+NegatedLiteralSQL +strconv import),
internal/executor/operators_ddl.go (UnaryOp arm), internal/catalog/catalog.go
(UnaryOp arm), internal/executor/default_validate_test.go +
internal/catalog/catalog_test.go (unit `-1`→`'-1'::integer`),
internal/testport/pgdump_connsetup_test.go (slice-364 dchkneg/neglit/neglit_ix
fixture + assertions), docs/design/0110-0001-pg-dump-tap-port.md (Slice 364),
.ralph/deferral_ledger.md (slice-363 row→resolved + slice-364 resolved row).

Gates run: parser+catalog+executor unit suites PASS; TestPort_PgDumpConnectionSetup
PASS (5.9s, byte-identical vs real PG 18.3); go vet clean; pgbench smoke=pre-commit.

Remaining negative-literal-adjacent gap STILL open: typed STRING-literal cast in
an operator arg (`name || '_x'` → PG `'_x'::text`) — slices 360(a)/362(b) sub-(b);
needs operator-arg type (a string literal does not self-describe its type the way
a numeric does).

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidates: the typed
string-literal cast in an operator arg (the now-isolated remaining gap);
action-command CREATE RULE (milestone-sized reverse-compiler); reserved-keyword
role quoting; or another catalog-view getter gap.
