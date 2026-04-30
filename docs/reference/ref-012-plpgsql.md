# REF-012: PL/pgSQL Runtime

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### Bytecode Compilation

PostgreSQL compiles PL/pgSQL source into a `PLpgSQL_function`
containing a flat array of `PLpgSQL_stmt` instruction nodes.
Control-flow structures (IF, LOOP, WHILE) are compiled into
instructions with resolved branch targets (like a simple VM).
This is done once, on the first call to the function.

Key benefits of bytecode compilation:
- No per-call parsing overhead.
- Branch targets are resolved at compile time.
- Expression ASTs are cached as prepared statements.
- Function arguments and local variables are accessed by slot
  index, not by name lookup.

goopg interprets the PL/pgSQL AST directly on every execution.
Parsing the body happens on every call. Variable lookups use
a name-to-index map (linear scan for shadowed variables).

### Expression Caching

Each SQL expression in a PL/pgSQL function (`RETURN x + 1`,
`IF x > 0`, etc.) is compiled to a `plpgsql_expr` that caches
the analysed/planned state. The first time the expression is
evaluated, it goes through parse → analyse → plan. Subsequent
evaluations reuse the cached plan.

goopg's `lowerPLpgSQLExpr` converts the expression AST on every
evaluation. There is no caching.

### SPI (Server Programming Interface)

PL/pgSQL executes SQL statements through the SPI interface:
- `SPI_execute(query, read_only)` — runs a query and returns
  the result set.
- `SPI_fetchrow` — retrieves one row from the result set.
- `SPI_modifytuple` — creates a modified copy of a tuple.

SPI provides full plan caching and handles transactions.
goopg's PL/pgSQL calls `evalExpr` directly, bypassing any
plan-caching layer.

### Exception Blocks

PL/pgSQL supports `BEGIN … EXCEPTION WHEN … THEN … END` blocks.
When an exception occurs inside a block, the handler rolls back
the subtransaction and executes the exception handler code.

PostgreSQL implements this via subtransactions (`Savepoint` /
`ReleaseSavepoint`). Each `BEGIN ... EXCEPTION` block creates
a subtransaction.

goopg does not support exception blocks.

### Cursor Support

PL/pgSQL can open, fetch from, and close cursors:

```plpgsql
DECLARE c CURSOR FOR SELECT * FROM t;
FETCH NEXT FROM c;
CLOSE c;
```

goopg does not implement cursor support in PL/pgSQL.

### RETURN NEXT / RETURN QUERY

PL/pgSQL `RETURN NEXT` appends a row to the function's result
set without terminating. `RETURN QUERY` runs a SELECT and
appends its result rows. These are used for set-returning
functions.

goopg does not support `RETURN NEXT` or `RETURN QUERY`.

## goopg Improvement Analysis

### P1: Expression Caching

Cache the result of `lowerPLpgSQLExpr` for each expression
within a routine body. On the first call, store the lowered
`planner.Expr` in a side table. On subsequent calls, reuse it.

**Impact:** Eliminates repeated parse/analyse/plan of the same
expressions. Estimated 2–5× speedup for routines with loops.

### P1: Bytecode Compilation (Minimal)

Instead of full bytecode, compile the AST into a `[]stmt` slice
with resolved variable slots and branch targets once, then
execute the compiled form on each call.

**Impact:** No repeated parsing. No repeated name-to-slot
resolution.

### P2: SPI Skeleton

Add a minimal SPI that can plan and execute SQL statements,
returning a row interface. Use it in PL/pgSQL expression
evaluation to enable plan caching.

### P3: Exception Blocks

Implement subtransactions in the MVCC layer. Add exception-block
parsing and execution to the PL/pgSQL runtime.

## References

- goopg: `internal/plpgsql/` (parser)
- goopg: `internal/executor/plpgsql_runtime.go`
- PG PL/pgSQL executor: `postgres/src/pl/plpgsql/src/pl_exec.c`
- PG PL/pgSQL grammar: `postgres/src/pl/plpgsql/src/pl_gram.y`
- PG PL/pgSQL compilation: `postgres/src/pl/plpgsql/src/pl_comp.c`
