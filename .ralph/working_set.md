Task: DU-002 slice 300 — COMPLETE, committing + pushing.

Last landed: PRODUCTION fix for the nested-arithmetic PARTITION-KEY EXPRESSION deparse
context — the THIRD context `executor.defaultExprToSQL` feeds, reached via
`pg_get_partkeydef(oid)` (after slice 298 index-predicate / slice 299 index-column).

Unlike 298/299 (fixture-only), this had a REAL byte divergence: real PG's
`pg_get_partkeydef_worker` (ruleutils.c) wraps each NON-FUNCTION expression key in `(%s)`
(the `looks_like_function` branch); goopg's `pg_get_partkeydef` emitted
`defaultExprToSQL(keyExpr)` with NO wrap → `RANGE (((a + b) * c))`, ONE PAREN SHORT of real
pg_dump 18.3's `RANGE ((((a + b) * c)))`.

FIX (internal/executor/expr.go, pg_get_partkeydef case, ~L6840): after
`part = defaultExprToSQL(keyExpr)`, wrap `part = "(" + part + ")"` UNLESS keyExpr is a
`*parser.FuncCall`. goopg represents EVERY callable form as *parser.FuncCall
(COALESCE/GREATEST/NULLIF are generic FuncCall; niladic value funcs = 0-arg FuncCall →
bare uppercase keyword), so that single type check mirrors PG's node-tag switch.
Opclass/collation suffixes appended AFTER the wrap (PG's append order).

EMPIRICALLY verified vs a live PG 18.3 instance (spun up on :5599, then torn down):
`PARTITION BY RANGE (((a + b) * c))` → dumps `((((a + b) * c)))` (4 parens);
`RANGE (abs(a))` → stays `abs(a)` (no wrap); cast `(a::bigint)` → `(((a)::bigint))`.

Files:
- internal/executor/expr.go — the `(%s)` wrap in pg_get_partkeydef.
- internal/testport/pgdump_connsetup_test.go — NEW `pexpr` table DDL (~L1164); NEW
  assertion for the 4-paren clause + negative guard `RANGE ((a + b * c))` (~L4296).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 300 section (paren-nesting table + fix).
- .ralph/fix_plan.md (loop #68 progress); .ralph/deferral_ledger.md (slice 300 landed, 301 deferred).

Key symbols: pg_get_partkeydef (executor/expr.go ~L6799), defaultExprToSQL +
binaryOpSymbolForDefault (operators_ddl.go:3435/3397), looks_like_function +
pg_get_partkeydef_worker (postgres ruleutils.c — the oracle).

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (4.6s vs real pg_dump
18.3); executor/parser/catalog pkg tests PASS; make ralph-state-guard; pgbench pre-commit
smoke (enforced by .githooks/pre-commit).

NOTE: main tree has foreign WIP (isolation-suite docs, cmd/gen-oracle-inventory) line-disjoint
from this slice — stage ONLY my files when committing; do NOT git add -A. Single loop confirmed
(PPID 604991←4451 is the portable_timeout subshell, not a peer).

Next (slice 301+): func-arg-default binary-op fixture (`((1+2)*3)` via pg_get_function_arguments)
— last unfixtured defaultExprToSQL context; renderer already parenthesizes (check if pg_get_function_arguments
adds a wrap like partkeydef did → may be another production fix, not just a fixture).
