# REF-012: PL/pgSQL Runtime

## Overview

The PL/pgSQL runtime interprets stored functions and procedures written in PostgreSQL's PL/pgSQL language. goopg supports the most common control-flow constructs (IF, LOOP, WHILE, FOR, assignment, RETURN, PERFORM) and can call user-defined functions from SQL expressions.

## goopg Implementation

**Packages:** `internal/plpgsql/` (parser), `internal/executor/plpgsql_runtime.go` (interpreter)

### Parser (`internal/plpgsql/`)

Parses PL/pgSQL routine bodies into an AST:

```
body       ::= DECLARE … BEGIN … END
statement  ::= RETURN expr
             | IF cond THEN … ELSIF … ELSE … END IF
             | LOOP … END LOOP
             | WHILE cond LOOP … END LOOP
             | FOR var IN [REVERSE] lower..upper [BY step] LOOP … END LOOP
             | EXIT [WHEN cond]
             | CONTINUE [WHEN cond]
             | PERFORM expr
             | assignment (target := expr)
```

### Interpreter (`plpgsql_runtime.go`)

Key types and functions:

- `plpgsqlFrame` — local variable frame (mutable row). Names are case-insensitive. Shadowing is supported for loop variables.
- `executePLpgSQLRoutine` — entry point for function calls. Parses the body, builds the frame from arguments and declarations, executes the statement list, and returns a Datum.
- `executePLpgSQLStmt` — dispatches on statement type:
  - `ReturnStmt`: evaluate expr, return via `controlFlow(flowReturn)`.
  - `IfStmt`: evaluate condition, execute matching branch.
  - `LoopStmt`: infinite loop with EXIT check.
  - `WhileLoop`: condition + body loop.
  - `ForLoop`: integer range iteration.
  - `AssignStmt`: evaluate target expression, assign to frame variable.
  - `PerformStmt`: evaluate and discard.
- `lowerPLpgSQLExpr` — converts `parser.Expr` to `planner.Expr` for evaluation by `evalExpr`.

### Expression Context

PL/pgSQL expressions can reference frame variables (via `lookup`) and SQL expressions. The `evalPLpgSQLExpr` function wraps `lowerPLpgSQLExpr` + `evalExpr` so that variable references resolve to the frame's current row.

### SP Integration

Procedures (no return value) are invoked via `callOp.Next()` which:
1. Evaluates CALL arguments.
2. Looks up the procedure in the routine registry.
3. Executes `execPLpgSQLProcedure` (same as function execution but without the RETURN check).

## PostgreSQL Implementation

PostgreSQL's PL/pgSQL (`pl_exec.c`) is a bytecode-compiled interpreter:

- **Compilation** — on first call, the source text is compiled into
  a `PLpgSQL_function` containing a `Datum` array of compiled
  instructions (PLpgSQL_stmt_*). goopg interprets the AST directly
  without a compilation step.
- **Expression caching** — SQL expressions within PL/pgSQL are
  cached as prepared statements after the first evaluation.
  goopg's `lowerPLpgSQLExpr` converts the AST each time.
- **SPI** — PostgreSQL's Server Programming Interface (SPI)
  provides cursor-based access to SQL queries from within PL/pgSQL.
  goopg does not have SPI; expressions are evaluated via the
  normal `evalExpr` path.
- **Exception blocks** — PostgreSQL supports `BEGIN … EXCEPTION
  … END` for error handling. goopg does not.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Compilation | AST interpretation | Bytecode compilation |
| Expression caching | None (re-lower on each call) | Cached as prepared statement |
| SPI | Not implemented | Full SPI interface |
| Exception handling | Not implemented | BEGIN … EXCEPTION … END |
| SQL-language routines | Stub (0A000) | Full support |

## References

- goopg: `internal/plpgsql/` (parser)
- goopg: `internal/executor/plpgsql_runtime.go`
- PG PL/pgSQL: `postgres/src/pl/plpgsql/src/pl_exec.c`
- PG PL/pgSQL grammar: `postgres/src/pl/plpgsql/src/pl_gram.y`
