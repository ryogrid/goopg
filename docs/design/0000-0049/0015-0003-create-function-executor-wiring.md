# 0015-0003 — CREATE / DROP FUNCTION Executor Wiring

**Status:** accepted (step 3 — analyzer pass-through + planner DDL
pass-through + executor handlers; PL/pgSQL parser / interpreter
deferred)
**Milestone:** [0015 — PL/pgSQL Stored Routines (Function-First Delivery)](../../milestones/0015-plpgsql-stored-routines-function-first.md)
**Spans seam:** analyzer (drop step-1 reject), planner (DDL
pass-through), executor (CREATE FUNCTION / DROP FUNCTION
operators), catalog interface (`Routines()`).
**Cross-links:**
[0015-0001](0015-0001-create-function-parser-and-ast.md)
(parser surface),
[0015-0002](0015-0002-pg-proc-catalog-and-routine-registry.md)
(catalog registry).

## Context

Step 1 added the parser surface and an analyzer reject. Step 2
added the catalog `Routines` registry and the
`pg_catalog.pg_proc` virtual view. This step wires the two
together: a `CREATE FUNCTION` statement now flows
parser → planner → executor and lands a row in
`cat.Routines()`; the same path runs `DROP FUNCTION` against
the registry.

The PL/pgSQL parser, interpreter, and function-invocation resolver
remain deferred — `cat.Routines()` rows are catalog-only metadata.
A `CREATE FUNCTION ... LANGUAGE plpgsql` statement succeeds, but
calling that function from a SELECT still surfaces the existing
"unknown function" diagnostic.

## Path through the stack

```
parser.CreateFunctionStmt
   ↓ analyzer.Analyze (default arm — DDL is pass-through, like CreateTableStmt)
   ↓ planner.Plan      → &planner.DDL{Stmt: stmt}
   ↓ executor.Build    → newDDLOp
   ↓ ddlOp.Next switch → execCreateFunction
   ↓ ctx.Catalog.Routines().Create(routine, orReplace)
```

Symmetrically for DROP — argument list determines whether
`Drop(name, argTypes)` or `DropByName(name)` runs.

## Catalog interface change

`Catalog` (the in-package interface) gains:

```go
Routines() *Routines
```

`*InMemory` (the only implementation) returns the registry it owns.
Future on-disk-backed implementations can return either a
process-local `*Routines` or a wrapper that persists.

This addition is small and additive; no existing call sites change.

## Executor handlers

`internal/executor/operators_ddl.go` grows two methods:

### execCreateFunction

1. Look up `o.ctx.Catalog.Routines()` — return XX000 if nil.
2. Validate the `LANGUAGE` clause:
   - Empty → SQLSTATE 42P13 ("function definition error").
   - Anything other than `plpgsql` / `sql` → SQLSTATE 42704
     ("undefined object") with a Stage-A allowlist message. Future
     loops drop the allowlist as built-in language handlers land.
3. Translate `parser.FunctionArg` → `catalog.Routine` fields,
   lower-casing type names (matches the existing CREATE TABLE
   handler's convention).
4. Call `Routines.Create(routine, s.OrReplace)`. Map
   `ErrRoutineExists` → SQLSTATE 42723 ("duplicate function").
   Other errors → XX000.

### execDropFunction

1. Look up the registry as above.
2. If `s.Args == nil` (no parenthesised arg list), call
   `DropByName(name)`. Otherwise build the argTypes vector and
   call `Drop(name, argTypes)`.
3. Map errors:
   - `ErrRoutineNotFound` → SQLSTATE 42883 ("undefined function"),
     swallowed when `IF EXISTS` was given.
   - `ErrRoutineAmbiguous` → SQLSTATE 42725 ("ambiguous function")
     — happens only on the bare-name path.
   - Others → XX000.

## SQLSTATE mapping

| Path                              | SQLSTATE | Reason                          |
|-----------------------------------|----------|---------------------------------|
| Missing `LANGUAGE`                | 42P13    | Invalid function definition     |
| Unsupported `LANGUAGE`            | 42704    | Undefined object                |
| Duplicate without `OR REPLACE`    | 42723    | Duplicate function              |
| `DROP` on missing (no `IF EXISTS`)| 42883    | Undefined function              |
| Bare-name `DROP` with > 1 overload| 42725    | Ambiguous function              |
| Catalog plumbing failure          | XX000    | Internal error                  |

These are all upstream-canonical SQLSTATEs.

## Tests

`internal/executor/operators_function_test.go` (8 cases):

- `TestExecCreateFunctionRegistersInCatalog` — happy path; verifies
  every `Routine` field maps correctly and the schema defaults to
  `public`.
- `TestExecCreateFunctionDuplicateRejected` — SQLSTATE 42723.
- `TestExecCreateOrReplaceFunctionUpdatesBody` — OID preservation
  + body update.
- `TestExecCreateFunctionRejectsUnsupportedLanguage` — SQLSTATE
  42704 for `LANGUAGE c`.
- `TestExecDropFunctionRemovesEntry` — happy path with explicit
  arg list.
- `TestExecDropFunctionMissingNoIfExistsErrors` — SQLSTATE 42883.
- `TestExecDropFunctionIfExistsSwallowsMissing` — `IF EXISTS`
  swallow.
- `TestExecDropFunctionAmbiguousBareName` — SQLSTATE 42725.

`internal/analyzer/analyzer_test.go` updated:

- The step-1 `TestAnalyzeCreateFunctionRejected` /
  `TestAnalyzeDropFunctionRejected` are replaced by
  `TestAnalyzeCreateFunctionPassesThrough` — pins that the
  analyzer no longer gates these statements (it never gated DDL
  in any other case).

Full `go test ./...` green.

## Out of scope

- PL/pgSQL parser + AST for routine bodies — step 4. Today a
  registered routine is opaque-text catalog metadata.
- PL/pgSQL interpreter and SPI bridge — step 5.
- Function invocation in expression contexts (the FuncCall
  resolver path) — step 6. Calling a registered routine from a
  SELECT still surfaces "unknown function" until the resolver
  reads `Routines.Lookup`.
- Persistence (durable `pg_proc` rows surviving restart) — pairs
  with the broader on-disk-catalog work.
- WAL replay support for `CREATE FUNCTION` — replication of routine
  DDL arrives once persistence does.
- Multi-target `DROP FUNCTION` (comma-separated names) — Stage A
  defers this; single-name DROP is sufficient for migration tools.
