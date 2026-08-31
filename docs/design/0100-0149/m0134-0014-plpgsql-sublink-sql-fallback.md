# M0134-0014 — PL/pgSQL expressions containing sublinks: fall back to the SQL engine

*Status: implemented. Task: `M0134-0014` (regress case `mvcc.sql`).*

## Problem

`postgres/src/test/regress/sql/mvcc.sql` fails on goopg with a 17-line diff and a
single root cause. The case runs a `DO` block whose loop body contains

```sql
IF EXISTS(SELECT * FROM clean_aborted_self WHERE key > 0 AND key < 100) THEN
```

goopg answers

```
ERROR:  EXISTS is not supported in PL/pgSQL expressions in v0
```

which aborts the enclosing transaction, so the next statement cascades into
`ERROR: current transaction is aborted, commands ignored until end of transaction
block`. The second diff hunk is purely secondary to whatever the DO block raises.

**Correction (post-implementation).** The first diagnosis read this as one bug.
It is two, serially masked: fixing `EXISTS` let the loop body reach its next
statement for the first time, which then failed on its own, previously-unreachable
bug. See §"The second root cause" below — the case remains PARKED.

## Why goopg diverges

goopg evaluates PL/pgSQL expressions with a **bespoke in-process interpreter**:
`lowerPLpgSQLExpr` (`internal/executor/plpgsql_runtime.go:2174`) walks a
`parser.Expr` and lowers it to an `optimizer.Expr`, which `evalExpr` then evaluates
against the frame's variable slots. That lowering has no representation for a
sub-`SELECT`, so three node kinds are rejected outright:

| node | site | message |
|---|---|---|
| `*parser.ExistsExpr` | `:2237` | `EXISTS is not supported in PL/pgSQL expressions in v0` |
| `*parser.SubqueryExpr` | `:2239` | `subqueries are not supported in PL/pgSQL expressions in v0` |
| `*parser.InExpr` with `Subquery != nil` | `:2220` | `IN (subquery) is not supported in PL/pgSQL expressions in v0` |

There is exactly **one** escape hatch to the real SQL engine today:
`evalPLpgSQLExpr` (`:2108`) special-cases a `*parser.SubqueryExpr` **at the
expression root** and routes it to `evalScalarSubquery` (`:2135`), which does
`optimizer.Plan` → `Build` → `Open/Next` and takes the first column of the first
row (NULL when empty, 21000 when >1 row). That hatch never fires for `EXISTS`, and
never for a subquery nested anywhere below the root (`(SELECT …) + 1`).

## What PostgreSQL actually does

PG has **no bespoke PL/pgSQL expression interpreter at all**. Every plpgsql
expression is a real, SPI-planned SQL query:

- `exec_prepare_plan` (`postgres/src/pl/plpgsql/src/pl_exec.c`, ~4173) builds the
  literal text `SELECT <expr>` and hands it to `SPI_prepare_extended`.
- `exec_eval_expr` (`:5664`) either takes the `exec_eval_simple_expr` fast path —
  which is still a *planned* `Expr` run through `ExecEvalExpr`, not a restricted
  node-kind interpreter — or falls through to `exec_run_select` (`:5752`), which
  executes the full SPI-planned `SELECT`.
- `exec_eval_boolean` (`:5642`) is a thin cast wrapper over `exec_eval_expr`.

So `EXISTS` is never special-cased in PG: it is ordinary SQL reaching the ordinary
planner. **goopg's interpreter is the divergence; the node-kind allow-list is an
artifact of it.**

goopg's *SQL* layer already implements sublinks fully and generally — planning in
`internal/optimizer/subplan_lower.go`, `unnest.go`, `exists_to_any.go`,
`predp.go`; execution in `internal/executor/subplan.go` and `expr.go` /
`expr_batch.go`. Nothing is missing from the engine. This is a **routing gap in the
PL/pgSQL layer**, not a missing feature.

## Design

Converging fully on PG (delete the interpreter, plan every expression as
`SELECT <expr>`) is REFACTOR-tier and would put every currently-working plpgsql
expression at risk. This change takes the contained step in that direction:

> When — and only when — lowering fails *because the expression contains a
> sublink*, evaluate the whole expression the way PG always does: wrap it in a
> synthetic `SELECT <expr>` and run it through `optimizer.Plan` / `Build`.

Mechanics:

1. The three rejection sites in `lowerPLpgSQLExpr` return an error that wraps a
   package-level sentinel (`errPLpgSQLExprNeedsSQL`) in addition to carrying their
   existing `*ExecError`. The user-visible message is unchanged when nothing
   catches it.
2. `evalPLpgSQLExpr` keeps its existing root-`SubqueryExpr` hatch (unchanged
   behaviour for `x := (SELECT …)`), then, if `lowerPLpgSQLExpr` returns an error
   satisfying `errors.Is(err, errPLpgSQLExprNeedsSQL)`, routes the *original*
   `parser.Expr` to a new `evalExprViaSQL` helper instead of failing.
3. `evalExprViaSQL` builds a one-target, no-`FROM` `SELECT` whose target is the
   original expression, plans and executes it with the same
   `optimizer.Plan`/`Build`/`Open`/`Next` sequence `evalScalarSubquery` already
   uses and the same `ctx`, and returns the single result Datum (NULL when the
   synthetic select yields no row). `IF EXISTS(…)` therefore receives a boolean
   Datum and the existing boolean coercion at the `IF` site is untouched.
4. If the SQL route itself fails, its error is returned — it is strictly more
   informative than the blanket `0A000`.

### Why this is regression-safe

The new path is reachable **only** through the three sites that unconditionally
`return nil, ExecError` today. Every expression that works now still takes the
identical interpreter path, byte for byte. The change can only turn a hard error
into a result or into a different error; it cannot alter a successful evaluation.

### Known limitation (deferred, see the ledger)

`evalExprViaSQL` plans the raw parser expression with **no PL/pgSQL frame-variable
substitution** — exactly the pre-existing limitation of `evalScalarSubquery`, which
also plans `sq.Inner` untouched. So `IF EXISTS(SELECT … WHERE key > i)` where `i`
is a plpgsql variable will fail with a `42703 column "i" does not exist` from the
planner rather than resolving `i` from the frame. That is a *changed message*, not
a changed outcome: the same expression errors with `0A000` today. PG resolves such
references by passing the frame's variables to SPI as query parameters
(`exec_eval_expr` → `PLpgSQL_expr.plan` with `paramLI`); implementing that in goopg
requires threading frame values into `optimizer.Plan` as bound parameters and is
recorded as a deferral with its resume point. `mvcc.sql`'s subquery references no
plpgsql variable, so the case is unblocked without it.

Also unchanged: plans are built per evaluation, not cached (`evalScalarSubquery`
already behaves this way). PG caches via SPI plan reuse; not in scope here.

## Verification

- `scripts/pg-regress-runner.sh --verbose mvcc` — 17-line diff / 2 `^+ERROR` at
  HEAD must go to a clean PASS.
- Targeted Go tests in `internal/executor/` covering `IF EXISTS(...)`,
  `IF NOT EXISTS(...)`, a nested (non-root) scalar subquery, and `IN (subquery)`,
  each demonstrated FAIL-pre / PASS-post.
- Standard executor-change gates: units pre-commit suite, `scripts/tpch-spotcheck.sh`
  (canonical Q12/Q13 row counts), pre-commit pgbench smoke.
- On PASS, `docs/test-port/postgres-oracle-target-inventory.csv` flips `mvcc` to
  `pass` / `pass_required=yes` and `make regen-testport` runs in the same commit.


## The second root cause (found only after the fix landed) — case PARKED

With the sublink routing fixed, the `DO` block's loop advances past the `IF` and
reaches

```sql
INSERT INTO clean_aborted_self SELECT g.i, 'rolling back in a sec'
  FROM generate_series(1, 100) g(i);
```

which fails with

```
ERROR:  PL/pgSQL embedded SQL parse error: syntax error at or near
        "expected identifier (got 1)" (byte 98)
```

`substitutePlpgsqlFrameVarsInSQL` (`internal/executor/plpgsql_runtime.go:3157`)
rewrites a plpgsql statement's SQL **as text**, before parsing, by scanning bytes
for identifiers and replacing any that match a frame variable with a literal. The
enclosing `FOR i IN 1..100 LOOP` binds `i`, so the scanner rewrites the `i` in the
FROM-item **column-alias list** `g(i)` into `g(1)` — an alias list position where
only an identifier is legal. The statement never had a chance to parse. This was
unreachable before: every loop iteration aborted at the `IF` first.

### Why this is not a bolt-on fix — and why it is the same bug as the §"Known limitation"

Both defects are the same design fault seen from opposite sides. goopg binds
plpgsql variables into SQL by **textual substitution before parsing**, which has no
grammatical context, so it necessarily:

- **over-applies** — it rewrites identifiers in positions that are not value
  expressions at all (the `g(i)` alias list here; the same hazard exists for column
  aliases after `AS`, `ORDER BY` output names, `WITH` CTE names, and window names), and
- **under-applies** — `evalExprViaSQL` and `evalScalarSubquery` plan their parser AST
  directly and so never substitute at all, which is why a plpgsql variable inside a
  sublink resolves to `42703 column does not exist`.

PostgreSQL has neither problem because it never substitutes text. `exec_prepare_plan`
(`postgres/src/pl/plpgsql/src/pl_exec.c`, ~4173) passes the statement source to
`SPI_prepare_extended` with a **parser hook** (`plpgsql_parser_setup` →
`plpgsql_pre_column_ref` / `plpgsql_param_ref_hook`, `pl_comp.c`), so plpgsql
variables are recognised *by the grammar, in expression positions only*, and become
`PARAM_EXTERN` nodes fed at execution time through `paramLI`. Position-correctness
is the parser's job, and the value is a bound parameter, never a literal splice.

The real fix is therefore to replace textual substitution with **parse-then-bind**:
resolve plpgsql variables against the frame during/after parsing and feed them as
bound parameters into `optimizer.Plan`. That is a cross-cutting change to the single
funnel every plpgsql embedded statement passes through, i.e. REFACTOR-tier, and a
mis-tuned heuristic there would silently corrupt arbitrary plpgsql SQL across the
whole engine. It is explicitly **not** attempted here; a narrower "skip identifiers
inside a parenthesised alias list" patch is rejected as an unbounded-blast-radius
guess at grammar from a byte scanner.

`mvcc.sql` is therefore **PARKED** (the M0134-0008..0013 pattern): this loop ships
the one contained, regression-safe cause and records the rest. The CSV row stays
`failed` and `make regen-testport` is NOT run.
