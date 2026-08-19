# Working set — M0134-0014 PARKED; real engine fix LANDED; next is M0134-0015

**Task:** M0134-0014 (`mvcc.sql`). Commit `d2460abe`.

**The standing rule was applied first and paid off differently than expected.**
`mvcc` is one of the "possible regression, verify" cases, so the loop ran
`scripts/pg-regress-runner.sh --verbose mvcc` at HEAD BEFORE any implementation.
It **still fails** (17 diff lines / 2 `^+ERROR`) — NOT a stale status. So no CSV
flip, row stays `failed`, **no `make regen-testport`**. The one remaining
verify-first case is `reindex_catalog`.

**What landed.** goopg evaluates PL/pgSQL expressions with a bespoke
`parser.Expr`→`optimizer.Expr` interpreter (`lowerPLpgSQLExpr`,
`internal/executor/plpgsql_runtime.go:2174`) that has no representation for a
sub-`SELECT`, so it rejected `ExistsExpr` / `SubqueryExpr` / `InExpr`-with-subquery
outright — while the SQL layer implements sublinks fully and generally. Routing
gap, not a missing feature. The three sites now wrap a sentinel; `evalPLpgSQLExpr`
catches it and re-evaluates the ORIGINAL expression as a synthetic no-`FROM`
`SELECT <expr>` via `optimizer.Plan`/`Build` (new `evalExprViaSQL`, mirroring the
pre-existing `evalScalarSubquery`). Regression-safe by construction: reachable
only through sites that unconditionally errored, so no working expression changes
path. Design: `docs/design/m0134-0014-plpgsql-sublink-sql-fallback.md` (indexed).

**PG oracle that decided the design:** PG has NO plpgsql expression interpreter at
all. `exec_prepare_plan` (`pl_exec.c:4173`) literally builds `SELECT <expr>` and
SPI-plans it for EVERY expression; `exec_eval_simple_expr` is a planned-`Expr`
fast path, not a node-kind allow-list. goopg's allow-list is an artifact.

**The second cause — found only AFTER the fix, and the loop's real insight.**
With EXISTS fixed the loop body reached, for the first time,
`INSERT ... FROM generate_series(1,100) g(i)` and died on
`PL/pgSQL embedded SQL parse error ... "expected identifier (got 1)"`.
`substitutePlpgsqlFrameVarsInSQL` (`:3157`) binds variables into embedded SQL by
**textual substitution before parsing**, so the `FOR i` loop variable is spliced
into the FROM-item column-alias list `g(i)` → `g(1)`. This and the shipped path's
missing frame-variable resolution are **one design fault from opposite sides**:
text substitution over-applies in non-expression positions and under-applies in
AST-planned paths, because position-correctness is the parser's job and goopg does
it pre-parse. PG avoids both with parser hooks (`plpgsql_pre_column_ref` /
`plpgsql_param_ref_hook`, `pl_comp.c`) making variables `PARAM_EXTERN` bound
parameters. Fix = parse-then-bind; REFACTOR-tier (single funnel for ALL plpgsql
embedded SQL). A narrow "skip identifiers in a parenthesised alias list" patch was
explicitly REJECTED as guessing grammar from a byte scanner — do not re-open.

**Three deferral rows appended** (2026-08-20, M0134-0014): the alias-list
corruption + parse-then-bind resume point; the new path's absent frame-variable
binding (pinned by `TestPlpgSQLSublinkExprFrameVariableDeferred`); and converging
on PG by deleting the interpreter (sequence AFTER parse-then-bind).

**Next step:** select **M0134-0015 (`join.sql`)** — a plain `failed` case, no
verify-first rule. Size it at HEAD with `scripts/pg-regress-runner.sh --verbose
join` before designing.

**Gates run:** `scripts/pg-regress-runner.sh --verbose mvcc` at HEAD 17 lines /
2 `^+ERROR`, and 17 lines / 2 `^+ERROR` post-fix — same counts, **different
error** (EXISTS rejection → alias-list parse error); do not read the equal count
as "no progress". `go build ./...`, `go vet ./internal/executor/`,
`go test ./internal/executor/` PASS (5 new tests in
`plpgsql_sublink_expr_test.go`; FAIL-pre demonstrated verbatim for each).
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS.
`scripts/tpch-spotcheck.sh` PASS with **Q12=2 / Q13=35 exactly**. Pre-commit
pgbench smoke PASS.

**Delegation:** `tmp/ralph-handoffs/M0134-0014a` (tester, verify-at-HEAD, 1 round,
DONE), `M0134-0014b` (researcher, sizing + PG oracle, 1 round, DONE),
`M0134-0014c` (implementer, 1 round, NEEDS-DECISION → coordinator parked the case;
same dir holds the tester's `gates.md`).
**In-flight:** none.
