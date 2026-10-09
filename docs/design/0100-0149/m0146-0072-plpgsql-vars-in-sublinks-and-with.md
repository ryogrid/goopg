# M0146-0072 — PL/pgSQL variables bind inside sublinks and WITH clauses

Status: done 2026-10-06 (`1710dabb6`).

## Symptom

Inside a PL/pgSQL function, with `i` an argument and `r` a loop record:

| statement | goopg before | PG 18.3 |
|---|---|---|
| `v := (WITH x AS (SELECT g FROM generate_series(10,11) g) SELECT (sum(x.g) * i)::text FROM x)` | 42703 `column "i" does not exist` | `42` |
| `acc := acc \|\| (WITH x AS (SELECT r.g * 10 AS k) SELECT k::text FROM x)` | 42703 | `10,20,` |
| `WITH x AS (… WHERE g <= i) SELECT count(*) INTO n FROM x` | `syntax error at or near "NULL"` | `3` |
| `IF (WITH x AS (SELECT i + 1 AS k) SELECT k FROM x) = 6 THEN` | 42703 | taken |
| `v := (SELECT (sum(g) * i)::text FROM generate_series(10,11) g)` | 42703 | `42` |
| `RETURN exists (SELECT 1 FROM generate_series(1,3) g WHERE g = i)` | 42703 | `t` |

As the last two rows show, the gap was not specific to WITH.

## Causes

1. **Expressions.** An expression whose sublink cannot be lowered to the
   interpreter's tree is planned as SQL:
   - `evalScalarSubquery` handles a top-level scalar subquery;
   - `evalExprViaSQL` handles any other sublink (M0134-0014).

   Both planned the parser tree untouched, with no variable binding. The
   M0134-0014 design doc recorded this as a known limitation.
2. **Statements.** The PL/pgSQL body parser (`parseSQLStmt`) recognised the
   `INTO target` variables clause only when the statement started with
   SELECT. For a WITH-led query the INTO stayed in the SQL text, and the
   statement path's text substitution replaced the target `n` with its
   value, giving `INTO NULL`.

## PG

- PL/pgSQL plans each expression and statement through SPI with a parser
  hook (`plpgsql_parser_setup`, `plpgsql_param_ref`, pl_comp.c). A variable
  is recognised in expression positions only, and becomes a `PARAM_EXTERN`
  of the variable's type.
- `make_execsql_stmt` (pl_gram.y) recognises an INTO variables clause in
  any command. The INTO right after INSERT or MERGE is the main grammar's.

## Change

- **`internal/executor/plpgsql_bind_ast.go`.**
  `bindPlpgsqlFrameVarsInExpr` / `bindPlpgsqlFrameVarsInSelect` return a
  copy of the parser tree in which:
  - a bare `ColumnRef` naming a frame variable becomes a literal of its
    current value, cast to the variable's declared type (a record value
    stays untyped);
  - `rec.field` of a record variable becomes the field's literal.

  The rewrite is reflection-based and copy-on-write: only nodes on a
  changed path are cloned, so the routine's cached body AST is never
  modified. Because `ColumnRef` nodes exist only in expression positions,
  it cannot over-apply to aliases, CTE names or column lists the way the
  text substitution can (the M0134-0014 `g(i)` hazard).
- **`evalPLpgSQLExpr`** binds the frame before both SQL paths.
- **`parseSQLStmt`** takes the INTO clause for WITH-led commands too, and
  skips the INTO right after INSERT or MERGE.

## Verification

- `TestPlpgsqlVariablesBindInsideSublinksAndWith`: ten function calls,
  every value PG 18.3's. It fails 42703 at HEAD.
- `TestPlpgSQLSublinkExprFrameVariableDeferred` pinned the old limitation
  and asked to be updated when binding landed. It is now
  `…FrameVariableBound` and pins PG's `yes`.
- A server probe against PG on :5534 is identical.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96 with no runtime moves;
  - ea-ratchet PASS.
- Regress A/B over 8 files (plpgsql, with, subselect, triggers,
  rangefuncs, polymorphism, create_function_sql, returning): 7
  byte-identical, plpgsql within its known flap.

## Not covered

- **Literals instead of parameters.** A bound variable is spliced into
  the tree as a literal, not a `PARAM_EXTERN`. The plan is rebuilt per
  evaluation (unchanged), so the effect is on caching only.
- **Precedence on name conflicts.** A variable wins over a same-named
  column, as in the statement path's text substitution. PG's default
  `plpgsql.variable_conflict = error` raises `column reference is
  ambiguous` instead.
- **Embedded statements** still use text substitution
  (`substitutePlpgsqlFrameVarsInSQL`), with its over-apply hazard in alias
  positions (M0134-0014 §"second root cause"). Moving them to this AST
  binder would need the statement's parse tree at execution time.
- **`WITH … INSERT/UPDATE/DELETE … RETURNING … INTO v`** now reaches the
  `SelectIntoStmt` path. A DML statement not led by WITH (`UPDATE …
  RETURNING … INTO`) keeps its existing path.
- **Found while testing:** repeated `CREATE OR REPLACE FUNCTION` across a
  set of functions fails `catalog update: freshly extended page did not
  accept tuple` on the second run. It is pre-existing (HEAD fails
  identically) and filed as M0146-0078.
