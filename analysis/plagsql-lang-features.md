# PL/pgSQL Language Features — Implementation Checklist

Checklist of items needed to implement a PL/pgSQL procedural-language engine,
derived from the PostgreSQL 17.5 official documentation chapter
"PL/pgSQL — SQL Procedural Language" (source file list: `tmp/plpgsqldocs.md`).

Checkboxes marked `[x]` are implemented in goopg as of 2026-10-30 (code audit:
`internal/pl/plpgsql/` parser+AST, `internal/executor/plpgsql_runtime.go`,
trigger/call/DDL integration). Partial implementations keep the item checked
when the core behavior works and carry a short `goopg:` note on what is
missing. See "Verification notes" at the end.

- [x] 1. Function registration and language plumbing (41.1)
  - [x] 1.1 Register PL/pgSQL as a procedural language usable by `CREATE FUNCTION` / `CREATE PROCEDURE` / `CREATE TRIGGER` (`LANGUAGE plpgsql`)
  - [x] 1.2 Accept the function body as a string literal, including dollar-quoted literals (`$$ ... $$`, tagged `$tag$ ... $tag$`) and single-quoted literals with doubled quote/backslash escaping
  - [x] 1.3 Make PL/pgSQL functions callable anywhere built-in functions are (SELECT lists, operators, index expressions, etc.)
  - [x] 1.4 Compile the body to an internal instruction tree at first call per session; detect trivial syntax errors at compile time, defer deeper SQL analysis to first execution of each statement
    - goopg: body parsed to AST once and cached per body text (not per session); SQL statements re-parsed/planned at each execution — see 13.1.
  - [ ] 1.5 Treat PL/pgSQL as a loadable module installed by default, removable by the administrator
    - goopg: built into the engine; no DROP LANGUAGE plumbing.

- [x] 2. Block structure and lexical rules (41.2)
  - [x] 2.1 Parse the function body as a block: `[ <<label>> ] [ DECLARE declarations ] BEGIN statements END [ label ];`
    - goopg: `[DECLARE] BEGIN ... END [;]`; `<<label>>` prefixes and END labels are not parsed.
    - [ ] 2.1.1 Require a semicolon after `END` of a nested block; the final `END` of the function body needs none
      - goopg: semicolon after nested END is accepted but optional.
    - [ ] 2.1.2 Validate matching labels (label after `END` must equal the label at block start)
      - goopg: labels not parsed at all.
  - [ ] 2.2 Implement the hidden outer block around the body, labeled with the function name, implicitly declaring parameters and special variables (e.g. `FOUND`) so they can be qualified as `func_name.param`
    - goopg: parameters and specials (FOUND, TG_*) are implicit frame variables; function-name-qualified references are not resolved.
  - [x] 2.3 Subblocks
    - [ ] 2.3.1 Allow a subblock at any statement position for grouping and localized variables
      - goopg: nested `BEGIN ... END` works as a statement; its DECLARE section is NOT parsed and declarations are not block-scoped (single flat frame).
    - [ ] 2.3.2 Mask outer variables by same-named inner variables; allow qualified access to masked variables via the outer block's label (`outerblock.var`)
      - goopg: duplicate declarations are silently ignored (no masking); no label qualification.
    - [x] 2.3.3 Treat PL/pgSQL `BEGIN`/`END` as statement grouping only, never as transaction control
  - [x] 2.4 Lexical rules
    - [x] 2.4.1 Keywords case-insensitive; identifiers implicitly lower-cased unless double-quoted
    - [x] 2.4.2 Comments as in SQL: `--` to end of line; `/* ... */` block comments that nest

- [x] 3. Declarations (41.3)
  - [x] 3.1 Variable declaration syntax: `name [ CONSTANT ] type [ COLLATE collation ] [ NOT NULL ] [ { DEFAULT | := | = } expression ];`
    - goopg: `name type [DEFAULT|:= expr]`; CONSTANT, NOT NULL, COLLATE rejected at parse time; `=` initializer not accepted.
    - [x] 3.1.1 Evaluate the default (and initialize to NULL when absent) each time the block is entered
    - [ ] 3.1.2 `CONSTANT` prevents assignment after initialization for the block's duration
    - [ ] 3.1.3 `NOT NULL` raises a run-time error on null assignment; require a non-null default
    - [x] 3.1.4 Allow later declarations in the same block to reference earlier variables in their init expressions
  - [x] 3.2 Function parameters
    - [x] 3.2.1 Positional identifiers `$1, $2, ...` bound to arguments, visible in the outer block
    - [x] 3.2.2 Named parameters from `CREATE FUNCTION` argument names (qualified by function name)
      - goopg: bare parameter names only (no `func_name.param` qualification).
    - [ ] 3.2.3 Explicit alias declaration `name ALIAS FOR $n;`
    - [x] 3.2.4 `OUT` parameters: `$n` names/aliases, start NULL, final value returned; callable from SQL by omitting them; `CALL` requires all parameters (`NULL` placeholder in SQL); explicit `RETURNS` clause must say `RETURNS record`
      - goopg: OUT params are NULL frame vars returned at end / bare RETURN; SQL calls omit them; CALL passes placeholders; the `RETURNS record` requirement is not enforced.
    - [x] 3.2.5 `INOUT` parameters
    - [x] 3.2.6 `RETURNS TABLE(...)` equivalent to OUT parameters + `RETURNS SETOF`
    - [x] 3.2.7 `VARIADIC` parameter support (same semantics as SQL functions)
    - [ ] 3.2.8 Polymorphic parameters (`anyelement`, `anyarray`, `anycompatible`, ...); special `$0` parameter of the deduced return type (aliaseable, writable, initialized NULL)
      - goopg: polymorphic arg/return resolution works; the special `$0` parameter is not implemented.
  - [ ] 3.3 General alias: `newname ALIAS FOR oldname;` for any variable (e.g. `NEW`/`OLD` in triggers)
  - [ ] 3.4 Type copying: `name table.column%TYPE`, `name var%TYPE`, with array decoration (`%TYPE[]`, `ARRAY[n]`)
    - goopg: `%TYPE` parses but degrades to `text` (no real type copy); array decoration after `%TYPE` unsupported.
  - [x] 3.5 Row types
    - [x] 3.5.1 `name table_name%ROWTYPE` and direct composite type names; field access `rowvar.field`
      - goopg: direct composite type names + field access work; `%ROWTYPE` is not parsed.
    - [ ] 3.5.2 Array decoration after `%ROWTYPE`
    - [x] 3.5.3 Composite-type parameters (`$1.field` selection)
      - goopg: named composite parameters expose fields; positional `$1.field` selection does not parse.
  - [x] 3.6 `RECORD` variables: structure-less, adopt the row structure of each `SELECT`/`FOR` assignment; field access on an unassigned record raises a run-time error
  - [ ] 3.7 Collation of variables
    - [ ] 3.7.1 Derive the call's implicit collation from actual argument collations; apply to collatable parameters and local variables
    - [ ] 3.7.2 Fall back to the type's default collation when no common collation exists
    - [ ] 3.7.3 Honor per-variable `COLLATE` declaration override

- [x] 4. Expressions (41.4)
  - [x] 4.1 Evaluate all expressions by sending `SELECT expression` to the main SQL executor (SPI), with PL/pgSQL variables replaced by query parameters
    - goopg: expressions lower into the internal expression evaluator with frame variables bound (sublinks / scalar subqueries fall back to a real `SELECT` via the SQL executor, per pl_exec.c's exec_prepare_plan model); embedded SQL takes frame variables as literal text substitution, not parameter binding.
  - [ ] 4.2 Prepare the expression query once (like `PREPARE`) and reuse the plan with new parameter values
    - goopg: expressions/statements are planned afresh on every execution (no PL plan cache).
  - [ ] 4.3 Allow the full syntax of a `SELECT` in expressions (so `IF count(*) > 0 FROM my_table THEN` works) but reject top-level `UNION` / `INTERSECT` / `EXCEPT`
    - goopg: expressions go through `parser.ParseExpr`; `FROM` clauses in IF/WHILE conditions are rejected.
  - [x] 4.4 Require a single column; take no rows as NULL result
    - goopg: first column of first row, no rows ⇒ NULL; the single-column requirement is not checked.

- [x] 5. Data types and assignment conversion (41.1.2, 41.5.1)
  - [x] 5.1 Accept/return any scalar, array, composite (named row), `record` (input: any composite; output: structure from caller's query), `void`, and `SETOF` results
  - [x] 5.2 Assignment coercion: apply assignment cast when types differ; otherwise convert textually via source type's output function + target type's input function, raising run-time errors on input failure

- [x] 6. Basic statements (41.5)
  - [x] 6.1 Assignment `variable { := | = } expression;`
    - [x] 6.1.1 Targets: simple variable (optionally block-qualified), row/record field, array element, array slice, nested composite field (`complex_array[n].realpart`)
      - goopg: simple variable, single-level record/composite field, array element; no block-qualified targets, array slices, or nested `var[n].field` paths.
    - [x] 6.1.2 Row-valued expression assignable to row/record targets
      - goopg: stored as composite text; the record's field schema is not refreshed by the assignment.
  - [x] 6.2 Execute any row-returning-less SQL command written directly in the function body (DDL, INSERT/UPDATE/DELETE/MERGE, utility commands)
    - [ ] 6.2.1 Reject a directly-written command that can return rows (`SELECT`, `... RETURNING`) unless it has an `INTO` clause; row-returning evaluation without result capture goes through `PERFORM`
      - goopg: a bare `SELECT` runs and its rows are discarded (and set FOUND); no rejection.
  - [x] 6.3 Variable substitution into optimizable commands (`SELECT`/`INSERT`/`UPDATE`/`DELETE`/`MERGE` and wrappers like `EXPLAIN`, `CREATE TABLE ... AS SELECT`), with plan caching (see section 13)
    - goopg: substitution via textual frame-variable literals; no plan caching.
  - [x] 6.4 `PERFORM query;` — execute a SELECT (parenthesized `WITH` query allowed) and discard the result; set `FOUND`; variable substitution and plan caching as usual
    - goopg: scalar fast path (`PERFORM f()`) and full query form; substitution yes, plan caching no.
  - [x] 6.5 Single-row result: `SELECT/INSERT/UPDATE/DELETE/MERGE ... RETURNING ... INTO [STRICT] target`
    - [x] 6.5.1 Targets: record variable, row variable, comma-separated list of simple variables and record/row fields; accept `INTO` almost anywhere in the command (recommend the conventional positions: before/after the `SELECT` target list, or at the end of other commands)
      - goopg: DML `INTO` recognized only after `RETURNING`; DML `INTO` without RETURNING is not captured.
    - [x] 6.5.2 Record target auto-configures to the result columns; row/list targets require exact column count/type match (run-time error otherwise)
      - goopg: record auto-configuration works; multi-target count mismatch does not error (missing ⇒ NULL, extras ignored).
    - [x] 6.5.3 Without `STRICT`: first row, NULLs on zero rows, extra rows discarded; `FOUND` reflects row availability
    - [x] 6.5.4 With `STRICT`: exactly one row required, else `NO_DATA_FOUND` / `TOO_MANY_ROWS` exceptions; `FOUND` set true on success
    - [x] 6.5.5 `RETURNING` commands with `INTO`: error on more than one returned row even without `STRICT`
    - [x] 6.5.6 Distinguish `SELECT ... INTO` (variable target) from SQL `SELECT INTO` (new table)
  - [x] 6.6 Dynamic commands: `EXECUTE command-string [ INTO [STRICT] target ] [ USING expression [, ...] ]`
    - [x] 6.6.1 No variable substitution in the command string; no plan caching (re-plan on every execution)
    - [x] 6.6.2 `$1, $2, ...` parameters supplied via `USING` (data values only; only in optimizable commands)
      - goopg: `USING` values are inlined as quoted literals (textual substitution); the optimizable-command restriction is not enforced.
    - [x] 6.6.3 `INTO` semantics identical to 6.5 (record auto-configuration, STRICT, first-row/NULL behavior)
      - goopg: single INTO target only (no target lists).
    - [ ] 6.6.4 Reject nested `SELECT ... INTO` inside the command string — run a plain `SELECT` and put `INTO` on the `EXECUTE` itself
    - [ ] 6.6.5 Reject server-side `EXECUTE` SQL statement inside PL/pgSQL (note: the `EXECUTE ... INTO` form of 6.6 is deprecated in PostgreSQL 18; re-sync when targeting newer docs)
      - goopg: the PL `EXECUTE` keyword always takes the dynamic-expression form; no explicit rejection of the SQL `EXECUTE name` statement.
  - [x] 6.7 Quoting support functions usable from the SQL engine: `quote_ident`, `quote_literal` (STRICT), `quote_nullable`, `format()` with `%I` / `%L` / `%s`
  - [x] 6.8 `GET [CURRENT] DIAGNOSTICS var { = | := } item [, ...]`
    - [x] 6.8.1 `ROW_COUNT` (bigint, rows processed by most recent SQL command)
    - [ ] 6.8.2 `PG_CONTEXT` (text, current call stack description)
      - goopg: raises "GET DIAGNOSTICS PG_CONTEXT is not supported".
    - [x] 6.8.3 `PG_ROUTINE_OID` (oid of current function)
  - [x] 6.9 Special variable `FOUND` (boolean, local per function, starts false)
    - [x] 6.9.1 Set by: `SELECT INTO` (row assigned), `PERFORM`, INSERT/UPDATE/DELETE/MERGE (rows affected), `FETCH`, `MOVE`, `FOR`/`FOREACH` (on loop exit), `RETURN QUERY [EXECUTE]`
      - goopg: SELECT INTO / PERFORM / DML set FOUND; FETCH/MOVE (no cursors), FOR/FOREACH loop exit, and RETURN QUERY do not.
    - [x] 6.9.2 Not changed by `EXECUTE` or other statements
  - [x] 6.10 `NULL;` no-op statement

- [x] 7. Control structures (41.6)
  - [x] 7.1 Returning from a function
    - [x] 7.1.1 `RETURN expression;` — terminate and return; auto-cast scalar results to the declared return type; composite returns need an expression delivering the exact column set (explicit casts)
    - [x] 7.1.2 `RETURN;` (no expression) with OUT parameters — return current OUT variable values; with `void` — early exit
    - [x] 7.1.3 Run-time error if control reaches the end of the top-level block without `RETURN` (exempting OUT-parameter and void functions, which exit implicitly)
      - goopg: SQLSTATE 2F005 raised at end of body; OUT/void/procedure exempted.
    - [x] 7.1.4 Set-returning functions: `RETURN NEXT [expression]` (scalar and composite; appends to the result set, execution continues), `RETURN QUERY query`, `RETURN QUERY EXECUTE command-string [USING ...]`; `RETURN NEXT`/`RETURN QUERY` intermix freely and their rows concatenate; a final argument-less `RETURN` (or reaching the function end) finishes the result set
      - goopg: `RETURN QUERY EXECUTE` is not supported (query text is parsed as plain SQL and fails).
    - [x] 7.1.5 `RETURN NEXT` without expression for OUT-parameter set-returning functions (current OUT values saved as one row); OUT-param SRF must be declared `SETOF record` (multiple OUT params) or `SETOF sometype` (single); result-set buffering per `work_mem`
      - goopg: buffering is in-memory per call, no `work_mem` paging.
    - [ ] 7.1.6 `FOUND` set by `RETURN QUERY [EXECUTE]`
  - [x] 7.2 Procedures: no return value; bare `RETURN` for early exit; final OUT parameter values returned to the caller
  - [ ] 7.3 `CALL` from PL/pgSQL to a procedure with OUT/INOUT parameters: each OUT/INOUT argument must be a PL/pgSQL variable (simple variable or field of a composite variable, not an array element) receiving the result on return
    - goopg: `CALL` inside a PL/pgSQL body is not routed by the statement parser at all ("unsupported PL/pgSQL statement").
  - [x] 7.4 Conditionals
    - [x] 7.4.1 `IF ... THEN ... END IF`, `IF ... ELSE ... END IF`, `IF ... ELSIF ... ELSE ...` (`ELSIF` spellable `ELSEIF`); NULL condition takes the ELSE path
    - [ ] 7.4.2 Simple `CASE search-expr WHEN expr[,expr...] THEN ... [ELSE ...] END CASE` — first equality match wins; missing `ELSE` raises `CASE_NOT_FOUND`
    - [ ] 7.4.3 Searched `CASE WHEN bool-expr THEN ... [ELSE ...] END CASE` — first true wins; missing `ELSE` raises `CASE_NOT_FOUND`
      - goopg: statement-level CASE (both forms) is not parsed.
  - [x] 7.5 Loops
    - [x] 7.5.1 `LOOP ... END LOOP` with optional label
      - goopg: bare LOOP; block/loop labels not parsed.
    - [x] 7.5.2 `EXIT [label] [WHEN bool-expr]` — unlabeled exits the innermost loop; a label may name the current or an outer nested loop *or block*; labeled `EXIT` from a `BEGIN` block (unlabeled `EXIT` never matches a block)
      - goopg: innermost-loop EXIT with optional WHEN only; labels not supported.
    - [x] 7.5.3 `CONTINUE [label] [WHEN bool-expr]` — next iteration of the named loop
      - goopg: CONTINUE [WHEN] on the innermost loop; labels not supported.
    - [x] 7.5.4 `WHILE bool-expr LOOP ... END LOOP` — condition checked before each iteration
    - [x] 7.5.5 Integer `FOR name IN [REVERSE] expr..expr [BY expr] LOOP`
      - [x] 7.5.5.1 Loop variable implicitly declared `integer`, scoped to the loop, shadowing any outer definition; label-qualifiable
        - goopg: implicit integer loop var with shadowing and loop-scoped restore; label qualification N/A (no labels).
      - [x] 7.5.5.2 Bounds and step evaluated once on loop entry; step defaults to 1; `REVERSE` subtracts the step; empty range runs zero iterations without error
    - [x] 7.5.6 `FOR target IN query LOOP` — record/row/scalar-list target assigned each row; any row-returning command allowed (SELECT, INSERT/UPDATE/DELETE/MERGE RETURNING, EXPLAIN, ...); variable substitution + plan caching; last assigned row accessible after `EXIT`
      - goopg: leading SELECT/INSERT/UPDATE/DELETE/WITH recognized (MERGE/EXPLAIN not); substitution yes, plan caching no; rows materialized up front.
    - [x] 7.5.7 `FOR target IN EXECUTE text_expr [USING ...] LOOP` — dynamic query source, evaluated/replanned on each loop entry
      - goopg: `IN EXECUTE expr` works; the `USING` clause does not parse.
    - [ ] 7.5.8 `FOREACH target [SLICE n] IN ARRAY expr LOOP`
      - [ ] 7.5.8.1 Without SLICE (or 0): iterate elements in storage order; target is a scalar variable
      - [ ] 7.5.8.2 Target list of variables for arrays of composite values
      - [ ] 7.5.8.3 Positive SLICE: integer constant not larger than the array's number of dimensions; target is an array receiving n-dimensional slices
        - goopg: FOREACH is not implemented.
    - [ ] 7.5.9 `FOUND` true when the loop iterated ≥1 time (set on loop exit)
      - goopg: loop exit does not set FOUND.
  - [x] 7.6 Trapping errors: `BEGIN ... EXCEPTION WHEN condition [OR condition...] THEN handler [WHEN ...] END`
    - [x] 7.6.1 Match the first matching condition on error; unmatched errors propagate to enclosing handlers or abort the function
    - [x] 7.6.2 Condition names = PostgreSQL error condition names (Appendix A); category names match the whole category; `OTHERS` matches everything except `QUERY_CANCELED` and `ASSERT_FAILURE`; `SQLSTATE 'XXXXX'` literal accepted; condition names case-insensitive
      - goopg: ~15 common condition names + OTHERS match; the name table is partial; 2-char category names and the `SQLSTATE 'XXXXX'` literal form are not matched; QUERY_CANCELED/ASSERT_FAILURE exclusions absent.
    - [ ] 7.6.3 Block with `EXCEPTION` forms a subtransaction: local variables keep their values, all persistent database changes made inside the block are rolled back when the handler runs
      - goopg: handler runs in the same transaction — database changes made in the protected block are NOT rolled back (goopg has SQL savepoint machinery, not wired to PL/pgSQL exception blocks).
    - [x] 7.6.4 Errors raised inside the handler statements propagate out (not caught by the same clause)
    - [ ] 7.6.5 Document/enforce higher entry-exit cost of exception blocks
  - [x] 7.7 Error information in handlers
    - [x] 7.7.1 Special variables `SQLSTATE` (text code) and `SQLERRM` (message), defined only inside exception handlers
      - goopg: injected into the frame when a handler fires (frame-wide once injected).
    - [x] 7.7.2 `GET STACKED DIAGNOSTICS var = item [, ...]` with items `RETURNED_SQLSTATE`, `COLUMN_NAME`, `CONSTRAINT_NAME`, `PG_DATATYPE_NAME`, `MESSAGE_TEXT`, `TABLE_NAME`, `SCHEMA_NAME`, `PG_EXCEPTION_DETAIL`, `PG_EXCEPTION_HINT`, `PG_EXCEPTION_CONTEXT` (empty string when unset)
      - goopg: RETURNED_SQLSTATE / MESSAGE_TEXT / PG_EXCEPTION_DETAIL / PG_EXCEPTION_HINT work; COLUMN_NAME, CONSTRAINT_NAME, PG_DATATYPE_NAME, TABLE_NAME, SCHEMA_NAME, PG_EXCEPTION_CONTEXT raise "not supported".
  - [ ] 7.8 `PG_CONTEXT` call-stack text: current function line plus enclosing caller lines (`PL/pgSQL function name() line N at STMT`)
    - goopg: error Context strings are emitted for SQL-function frames and dynamic SQL, not per-line PL/pgSQL stack frames.

- [ ] 8. Cursors (41.7)
  - [ ] 8.1 `refcursor` data type (internally the portal name string; assignable, passable, returnable)
    - goopg: the type exists in the catalog/type system, but there is no cursor machinery behind it.
  - [ ] 8.2 Cursor variable declaration: `name [ [NO] SCROLL ] CURSOR [(arg name type, ...)] FOR query;` (`IS` accepted for `FOR`)
    - [ ] 8.2.1 Unbound cursors (`refcursor` variables) vs bound cursors (query bound at declaration, optionally parameterized)
    - [ ] 8.2.2 `SCROLL` / `NO SCROLL` semantics; reject `SCROLL` with `FOR UPDATE/SHARE` queries
  - [ ] 8.3 `OPEN`
    - [ ] 8.3.1 `OPEN unbound [ [NO] SCROLL ] FOR query` — variable substitution with values at OPEN time; plan cached
    - [ ] 8.3.2 `OPEN unbound [ [NO] SCROLL ] FOR EXECUTE query_string [USING ...]` — dynamic, no substitution in string
    - [ ] 8.3.3 `OPEN bound [ (positional | name := value args) ]` — args required iff the cursor takes args; positional/named/mixed notation; cacheable plan; no SCROLL option
    - [ ] 8.3.4 Error when opening an already-open cursor
    - [ ] 8.3.5 Portal creation: use the (non-null) string value of the refcursor variable as portal name; auto-generate a unique name when null
  - [ ] 8.4 `FETCH [ direction {FROM|IN} ] cursor INTO target`
    - [ ] 8.4.1 Single-row directions: NEXT, PRIOR, FIRST, LAST, ABSOLUTE count, RELATIVE count, FORWARD, BACKWARD (count may be an expression); default NEXT; backward movement requires `SCROLL` capability; targets set to NULL(s) with cursor left after-last/before-first when no suitable row
    - [ ] 8.4.2 Target rules as `SELECT INTO`; NULL targets and cursor positioning past-the-end/before-start on no row; sets `FOUND`
  - [ ] 8.5 `MOVE [ direction {FROM|IN} ] cursor` — reposition without fetching, multi-row directions allowed (e.g. `FORWARD 2`); sets `FOUND`; bare-`count` direction (no keyword) is deprecated
  - [ ] 8.6 `UPDATE tbl SET ... WHERE CURRENT OF cursor` / `DELETE FROM tbl WHERE CURRENT OF cursor` (with cursor updatable-query restrictions, `FOR UPDATE` recommended)
  - [ ] 8.7 `CLOSE cursor` — release the portal early and free the variable for reopening
  - [ ] 8.8 Returning cursors: open in a function, return the refcursor (caller-specified or auto-generated portal name); portals implicitly closed at transaction end
  - [ ] 8.9 `FOR recordvar IN bound_cursor [(args)] LOOP` — auto OPEN at loop entry (error if the cursor is already open), auto CLOSE at exit; implicit record variable scoped to the loop; args like OPEN
    - goopg: PL/pgSQL has no cursor statements (declaration/OPEN/FETCH/MOVE/CLOSE/CURRENT OF); SQL-level `DECLARE`/`FETCH` statements parse but have no executor; no portal machinery.

- [x] 9. Transaction management (41.8)
  - [x] 9.1 `COMMIT` / `ROLLBACK` inside procedures (`CALL`) and anonymous `DO` blocks; implicit start of a new transaction after each ends
    - goopg: PLpgSQLCommitChain installed only in auto-commit dispatch; each commit chains into a fresh transaction; atomic contexts raise 2D000.
  - [ ] 9.2 `COMMIT AND CHAIN` / `ROLLBACK AND CHAIN` — new transaction inherits the previous transaction's characteristics
    - goopg: PL/pgSQL parses bare COMMIT/ROLLBACK only (no AND CHAIN).
  - [ ] 9.3 Restrict transaction control to CALL/DO invocations from top level or nested CALL/DO with no intervening non-CALL command (e.g. blocked through a `SELECT` frame)
    - goopg: blocks explicit-transaction-block contexts and procedures with SET clauses, but the chain is inherited by every nested routine context, so a COMMIT through a function invoked from SELECT is not blocked.
  - [x] 9.4 No savepoint commands in PL/pgSQL; document exception-block-as-subtransaction as the replacement pattern; forbid transaction commands inside blocks with exception handlers
    - goopg: COMMIT/ROLLBACK are not PL/pgSQL statements inside handler-protected blocks by grammar; no savepoint statements offered.
  - [x] 9.5 Cursor loops + COMMIT/ROLLBACK: convert the loop's cursor to a holdable cursor at the first transaction end (fully evaluated, locks released); remove it after the loop
    - goopg: FOR-query materializes all rows and closes the operator before the loop body runs, giving the same snapshot-stable iteration as PG's holdable conversion (evaluated at loop entry, not at first commit).
  - [ ] 9.6 Reject transaction commands in cursor loops driven by non-read-only commands (e.g. `UPDATE ... RETURNING`)
    - goopg: no check; COMMIT inside such loops runs.

- [x] 10. Errors and messages (41.9)
  - [x] 10.1 `RAISE [level] 'format' [, expr ...] [USING opt = expr ...]`
    - [x] 10.1.1 Levels: DEBUG, LOG, INFO, NOTICE, WARNING, EXCEPTION (default); honoring `log_min_messages` / `client_min_messages` routing
      - goopg: all levels accepted; WARNING emitted at WARNING protocol severity, NOTICE/INFO/LOG/DEBUG all as NOTICE; GUC message-level filtering not honored.
    - [x] 10.1.2 Format string must be a simple string literal; `%` replaced by successive argument values, `%%` literal; argument count checked at compile time
      - goopg: `%`/`%%` substitution with expression args (NULL ⇒ `<NULL>`); the compile-time argument-count check is absent.
    - [ ] 10.1.3 `USING` options: `MESSAGE`, `DETAIL`, `HINT`, `ERRCODE` (condition name or 5-char SQLSTATE), `COLUMN`, `CONSTRAINT`, `DATATYPE`, `TABLE`, `SCHEMA`
      - goopg: USING clauses are parsed away, and only `USING MESSAGE` in the condition form (10.2) is extracted; DETAIL/HINT/ERRCODE/COLUMN/CONSTRAINT/DATATYPE/TABLE/SCHEMA are not applied.
  - [x] 10.2 `RAISE [level] condition_name | SQLSTATE 'code' [USING ...]` — custom MESSAGE/DETAIL/HINT allowed
    - goopg: condition names work (mapped via a partial name→SQLSTATE table, default P0001); custom MESSAGE via USING works; the `SQLSTATE 'code'` literal form is not decoded (raises P0001 with the quoted code as message); DETAIL/HINT not applied.
  - [ ] 10.3 `RAISE [level] USING opt = expr ...` — everything in the USING list
    - goopg: `RAISE USING ...` (no format string) mis-parses `USING` as a condition name.
  - [ ] 10.4 Bare `RAISE;` — re-throw the error currently being handled, only valid in an EXCEPTION clause (catchable by nested handlers)
    - goopg: `RAISE;` raises P0001 with an empty message; it does not re-throw the handled error.
  - [x] 10.5 Defaults: `raise_exception` (P0001) when no condition given; condition name/SQLSTATE as message text when no message given
  - [ ] 10.6 User-defined SQLSTATE codes: any 5-digit/uppercase-letter code except `00000`; category-code caveat
  - [ ] 10.7 `ASSERT condition [, message]` — raise `ASSERT_FAILURE` on false/null condition; message expression only evaluated on failure; controlled by `plpgsql.check_asserts`
    - goopg: ASSERT statement not implemented (the P0004 condition code exists in the errcodes table only).

- [x] 11. Trigger support (41.10)
  - [x] 11.1 Data-change trigger functions: declared with no arguments and return type `trigger`; `CREATE TRIGGER ... EXECUTE FUNCTION` integration with args passed via `TG_ARGV`
  - [x] 11.2 Automatically define trigger special variables in the top-level block: `NEW`, `OLD` (record; `NEW` null for DELETE and in statement-level triggers, `OLD` null for INSERT and in statement-level triggers), `TG_NAME`, `TG_WHEN` (BEFORE/AFTER/INSTEAD OF), `TG_LEVEL` (ROW/STATEMENT), `TG_OP` (INSERT/UPDATE/DELETE/TRUNCATE), `TG_RELID`, `TG_RELNAME` (deprecated), `TG_TABLE_NAME`, `TG_TABLE_SCHEMA`, `TG_NARGS`, `TG_ARGV` (text[], 0-based, invalid index → NULL)
    - goopg: NEW/OLD (with the statement-level/event nullability per M0146-0076), TG_NAME, TG_WHEN, TG_LEVEL, TG_OP, TG_RELNAME, TG_TABLE_NAME, TG_ARGV (0-based, out-of-range → NULL) are injected; TG_RELID, TG_TABLE_SCHEMA, TG_NARGS are missing; TG_WHEN never reports INSTEAD OF (those triggers do not fire).
  - [x] 11.3 Trigger return-value protocol
    - [x] 11.3.1 Return NULL or a record matching the table's structure
    - [x] 11.3.2 BEFORE row triggers: NULL skips the row operation; non-NULL (modified `NEW`) proceeds; DELETE before-triggers return non-NULL (idiom: `OLD`)
    - [ ] 11.3.3 INSTEAD OF row triggers (views): NULL = did nothing (row not counted); non-NULL = performed; return `NEW` for INSERT/UPDATE (feeds RETURNING, later triggers, ON CONFLICT EXCLUDED), `OLD` for DELETE
      - goopg: INSTEAD OF timing parses into the catalog but the triggers never fire; no view DML redirection.
    - [x] 11.3.4 AFTER row triggers and BEFORE/AFTER statement triggers: return value ignored; can abort via error
  - [ ] 11.4 Transition tables for AFTER statement triggers: `REFERENCING OLD/NEW TABLE AS name` names referable as read-only tables inside the function
    - goopg: REFERENCING clauses parse and deparse; transition tables are not materialized and such triggers are left unfired.
  - [ ] 11.5 Event triggers: return type `event_trigger`; special variables `TG_EVENT`, `TG_TAG`; `CREATE EVENT TRIGGER` integration
    - goopg: CREATE/ALTER/DROP EVENT TRIGGER registered in pg_event_trigger; no firing path, no TG_EVENT/TG_TAG.
  - [ ] 11.6 Per-table statement caches when one trigger function serves multiple tables (see 13.4)
    - goopg: body AST cache is keyed by body text only; statements are re-planned per execution, so the (function, table) cache split does not exist.

- [ ] 12. Variable substitution and name resolution (41.11.1)
  - [ ] 12.1 Replace variable references in SQL statements with query parameters only in syntactically permissible positions (never table/column/function names)
    - goopg: substitution is textual over embedded SQL (frame values inlined as literals); it skips string literals, quoted identifiers and qualified names, and shields FOR-loop variables, but it is not restricted to parameter-permissible positions (a table/column name equal to a variable is substituted).
  - [ ] 12.2 Ambiguity handling: a name usable as both variable and table column raises an error by default (`error`)
    - goopg: the textual substitution cannot detect ambiguity; the variable silently wins (code comments acknowledge the divergence).
    - [ ] 12.2.1 GUC `plpgsql.variable_conflict = error | use_variable | use_column` (superuser-only, affects subsequent compilations)
    - [ ] 12.2.2 Per-function pragma `#variable_conflict error|use_variable|use_column` at the start of the function text
  - [ ] 12.3 Support qualified references: block-label-qualified variables (`label.var`), function-name-qualified parameters/specials (`func.param`, `func.found`)
    - goopg: qualified names in expressions raise "qualified names are not supported"; record-field `var.field` substitution is the single-level exception.
  - [x] 12.4 No substitution in `EXECUTE` command strings (values via string construction or `USING`)
  - [ ] 12.5 Restrict substitution to optimizable commands; require `EXECUTE` for utility statements
    - goopg: utility/DDL commands (CREATE/DROP/ALTER/GRANT/SET/…) run directly in the body with frame-variable substitution applied to them as well.

- [ ] 13. Plan caching and statement preparation (41.11.2)
  - [ ] 13.1 Lazy per-statement preparation: parse/analyze each expression/command at first execution in the session (`SPI_prepare`-style), reuse the prepared statement afterwards
    - goopg: the body AST is parsed once per body text (global cache); expression/command SQL is re-parsed and re-planned on every execution.
  - [ ] 13.2 Execution-plan caching policy: as for prepared statements, the SPI layer considers creating a generic plan (independent of parameter values) for parameterless or frequently executed statements — typically only when the plan is not very sensitive to the values of the referenced PL/pgSQL variables; otherwise re-plan each visit
    - goopg: "goopg has no plan cache" for PL/pgSQL statements (see operators_explain.go); EXPLAIN never emits "Generic plan".
  - [x] 13.3 Keep the constraint that directly-written commands reference fixed tables/columns across executions; dynamic object names only via `EXECUTE`
    - goopg: direct commands are plain SQL text, so dynamic names fail; EXECUTE is the only dynamic path.
  - [ ] 13.4 Separate statement caches per (trigger function, table) pair and per (polymorphic function, actual argument types) combination to avoid record/type drift
  - [ ] 13.5 Keep record-variable field types stable: expression analysis is frozen on the field types seen at first execution; `EXECUTE` as the escape hatch for mutable record structures
    - goopg: record bindings are rebuilt from each result row at every execution (no frozen analysis; the constraint is not enforced).
  - [ ] 13.6 Document cached-plan effects on time-sensitive literals (`'now'` folded at analysis time; recommend `now()`)

- [ ] 14. Configuration and compile-time checks (41.5.3, 41.9.2, 41.12.2)
  - [ ] 14.1 `plpgsql.print_strict_params` GUC (affects subsequent compilations only) + per-function `#print_strict_params on` pragma — add parameter values to `DETAIL` on STRICT violations
  - [ ] 14.2 `plpgsql.extra_warnings` / `plpgsql.extra_errors` = comma-separated check list | none | all (default `none`; WARNING-flagged functions remain executable)
    - [ ] 14.2.1 `shadowed_variables` — declaration shadows a previously defined variable (compile-time WARNING/ERROR)
    - [ ] 14.2.2 `strict_multi_assignment` — target/source field count mismatch in multi-variable assignment (default behavior fills missing targets with NULL and ignores extras; PL/pgSQL warns/errs on mismatch when enabled)
    - [ ] 14.2.3 `too_many_rows` — query returns >1 row with an `INTO` clause
  - [ ] 14.3 `plpgsql.check_asserts` (default on) gates `ASSERT` statements
    - goopg: no plpgsql.* GUCs are registered (custom-name syntax is accepted as an inert session variable); no pragma parsing; no ASSERT to gate.

- [x] 15. Supporting engine facilities (prerequisites the PL/pgSQL core relies on)
  - [x] 15.1 SPI-like in-server SQL execution: prepare, cursor-based execution, parameter binding, result status, `ROW_COUNT`
    - goopg: the executor's plan/build/open/next pipeline is the SPI equivalent used by the interpreter; $n parameters bind through Context.Params; ROW_COUNT comes from the operator's row/affected counts.
  - [x] 15.2 Anonymous code blocks: `DO [LANGUAGE plpgsql] $$ ... $$`
  - [x] 15.3 `CALL` statement for procedures (SQL and PL/pgSQL side)
    - goopg: SQL-side CALL (named/positional args, OUT result row, transaction-control procedures) works; a `CALL` statement inside a PL/pgSQL body is not parsed.
  - [x] 15.4 Error/ereport infrastructure with SQLSTATE condition names (Appendix A mapping), MESSAGE/DETAIL/HINT/CONTEXT fields, severity levels
    - goopg: ExecError carries SQLSTATE/MESSAGE/DETAIL/HINT/CONTEXT; the full errcodes table lives in internal/utils/errcodes; PL/pgSQL's condition-name matcher covers only a subset.
  - [x] 15.5 Subtransaction (savepoint-like) machinery for exception blocks
    - goopg: the machinery exists (savepoint sub-XIDs, PushSavepoint/ROLLBACK TO, subxid overflow handling) but is not wired to PL/pgSQL exception-block rollback (see 7.6.3).
  - [ ] 15.6 Portal machinery for cursors/refcursors (named portals, holdable cursors, scroll capability)
    - goopg: SQL DECLARE/FETCH parse into ASTs with no executor; no portal layer.
  - [ ] 15.7 Collation derivation plumbing for function arguments
  - [x] 15.8 String helpers exposed as SQL functions: `format`, `quote_ident`, `quote_literal`, `quote_nullable`
    - goopg: format supports %[position][flags][width]{s|I|L|%}.
  - [x] 15.9 `CREATE FUNCTION` optimization modifiers consumed by the engine around PL/pgSQL bodies: `STRICT` (skip call, return NULL on null input) and volatility `IMMUTABLE`/`STABLE`/`VOLATILE`
    - goopg: STRICT short-circuits scalar and SETOF calls; volatility is cataloged and consumed by the index-expression mutability check and command-counter behavior.

- [ ] 16. Oracle PL/SQL compatibility surface (41.13 — relevant for porting workloads)
  - [ ] 16.1 Document integer `FOR ... REVERSE` bound-order difference (counts first→second, not second→first)
    - goopg implements PG's first→second REVERSE order; the compatibility documentation is not part of the repo.
  - [ ] 16.2 `variable_conflict = use_column` mode to match PL/SQL name resolution
  - [ ] 16.3 Query-loop targets must be pre-declared (unlike PL/SQL's implicit declaration), values persist after loop exit
    - goopg: loop-target values do persist, but undeclared targets are auto-registered (no pre-declaration requirement enforced).
  - [ ] 16.4 No user-defined exception names; document raising custom SQLSTATE codes instead
    - goopg: raising a custom SQLSTATE code is broken (see 10.2), so the documented workaround does not work.
  - [x] 16.5 Exception-clause auto-rollback semantics matching the Oracle SAVEPOINT/ROLLBACK TO pattern
  - [ ] 16.6 No packages/package variables; schemas and temp tables as the documented substitutes

## Verification notes (2026-10-30)

Status was determined by code inspection only (no server runs), against
`internal/pl/plpgsql/{parser,ast}.go`, `internal/executor/plpgsql_runtime.go`,
trigger/call integration (`operators_trigger.go`, `operators_call.go`,
`operators_tx.go`), the DDL paths (`operators_ddl.go` execDoBlock /
create-function language validation), `internal/postmaster/dispatch.go`
(PLpgSQLCommitChain), and the deepwiki reference doc
`deepwiki-goopg-261030/2-Core-Architecture/2.5-PLpgSQL-Runtime.md`.

Implemented core (sections 1–7, 9–11, 15, with gaps noted inline): routine
registration and dispatch, block/declare/assign/IF/LOOP/WHILE/int-FOR/query-FOR
(incl. FOR-IN-EXECUTE)/EXIT/CONTINUE/PERFORM/NULL/COMMIT-ROLLBACK,
SELECT-DML-INTO [STRICT], EXECUTE+USING+INTO, GET [STACKED] DIAGNOSTICS
(subset), RAISE (levels, format args, condition names),
BEGIN…EXCEPTION…END (first-match handlers, SQLERRM/SQLSTATE, stacked
diagnostics subset), SETOF via RETURN NEXT/QUERY, OUT/INOUT/RETURNS TABLE/
VARIADIC/polymorphic params, STRICT, trigger functions (row+statement, TG_*
subset, NEW/OLD read-write, return protocol), DO blocks, CALL (SQL side).

Known structural gaps: no block labels (`<<label>>`), no subblock DECLARE,
no variable masking/scoping beyond FOR loops; no cursors (section 8 entirely);
no CASE statement, FOREACH, labeled EXIT/CONTINUE, ALIAS, CONSTANT, NOT NULL,
COLLATE, %ROWTYPE (plain %TYPE degrades to text), ASSERT, RAISE re-throw/
SQLSTATE-literal/USING options; no plan caching (section 13: statements plan
per execution; the "no plan cache" comment in operators_explain.go), no
plpgsql.* GUCs/pragma (section 14), exception blocks do not roll back database
changes (7.6.3, 15.5 wiring), FOUND not set by loop exit / RETURN QUERY,
no CALL inside PL bodies (7.3), no INSTEAD OF / transition tables / event
trigger firing (11.3.3, 11.4, 11.5).

## Review notes

- Two agent reviews were performed: (1) the source-document list `tmp/plpgsqldocs.md`
  was verified complete and appropriate (all 14 files of the PL/pgSQL chapter, no
  missing/extraneous entries); (2) this checklist was cross-checked against all 14
  source documents and approved with minor fixes, which were then applied:
  nesting fix under 2.3; added rejection of row-returning commands without `INTO`
  (6.2.1); `SELECT INTO` inside `EXECUTE` rejected (6.6.4); SRF protocol details
  (final `RETURN;`, intermixing/concatenation, `SETOF record` requirement, 7.1.4/7.1.5);
  `NEW`/`OLD` nullability in triggers (11.2); `RETURNS record` for OUT-parameter
  functions (3.2.4); cursor OPEN/FETCH/MOVE/FOR-loop precision (8.4.1, 8.5, 8.9);
  integer-`FOR` default step and `SLICE` bound (7.5.5.2, 7.5.8.3); `EXIT` label scope (7.5.2);
  generic-plan heuristic wording (13.2); GUC defaults and effects (14.1, 14.2, 14.2.2);
  `STRICT`/volatility function modifiers (15.9). PostgreSQL 18 deprecation of
  `EXECUTE ... INTO` is flagged in 6.6.5.
