# M0134-0033: DROP PROCEDURE not-found error attaches the wrong HINT

Status: accepted

## Sizing (at HEAD, before this fix)

`create_procedure.sql` sized via `scripts/pg-regress-runner.sh --verbose
create_procedure`: **131 diff lines** (36 non-context `+`/`-` lines), **2
`^+ERROR` / 1 `^-ERROR`**. Small case overall — five independent root causes,
none catastrophic, but the two largest (buckets 1 and 2 below) are
REFACTOR-tier.

## Root-cause buckets

1. **Missing `LINE N: … / ^` error-position pointer (10 sites, ~20 diff
   lines) — REFACTOR-tier.** `ExecError.Pos` is populated correctly
   (`internal/executor/operators_call.go:60-89`), but the wire-encoding guard
   in `internal/postmaster/copy.go:824-825,856-857,878-879,895-896` and
   `internal/postmaster/txn_verb.go:352-353` treats `Pos == 0` as "unset",
   colliding with the legitimate 1-based wire position that points at the
   very first character of a statement. Fixing the sentinel (e.g. -1-means-
   unset) requires auditing every `ExecError{...}`/`PlanError{...}`
   construction site that currently relies on the zero-value default — too
   broad for one loop. Ledgered.

2. **`pg_get_functiondef` BEGIN-ATOMIC body deparse is raw-text
   substitution, not an AST re-deparse — REFACTOR-tier.**
   `internal/executor/expr.go:15826-15849` splits the original source text on
   `;` and does literal `$N`→argname replacement instead of re-deparsing the
   parsed statement, diverging on column-list expansion, operand
   qualification, and spacing versus PG's `pg_get_functiondef` →
   `deparse_context_for` in `postgres/src/backend/utils/adt/ruleutils.c`.
   Ledgered.

3. **DROP PROCEDURE's deferred registry removal isn't visible to the same
   session's own subsequent lookups inside an explicit transaction —
   borderline CONTAINED but touches more than one call site (routine
   listing/lookup paths need to consult the session's pending-drop set), so
   deferred behind bucket 4.** Same pattern as the already-known "DROP INDEX
   deferred removal until COMMIT" (see `.ralph/deferral_ledger.md`). Ledgered.

4. **DROP PROCEDURE on a genuinely-nonexistent name attaches the wrong HINT
   — CONTAINED, shipped this loop.** See "Fix" below.

5. **`CALL` on a procedure with `EXECUTE` revoked never raises "permission
   denied for procedure" — REFACTOR-tier, engine-wide missing subsystem
   (no `ACL_EXECUTE` enforcement at call time anywhere in
   `internal/executor`).** PG oracle: `pg_proc_aclcheck` /
   `ExecuteCallStmt` (`postgres/src/backend/executor/functions.c`,
   `postgres/src/backend/catalog/aclchk.c`). Ledgered.

## Fix (bucket 4)

`internal/executor/operators_ddl.go:16743-16753`, the DROP-PROCEDURE/
DROP-FUNCTION-not-found branch, unconditionally attached:

```go
Hint: "No procedure matches the given name and argument types. You might need to add explicit type casts.",
```

That hint is correct for `CALL`'s candidate-matching failure path (PG's
`func_get_detail` in `postgres/src/backend/parser/parse_func.c`) but PG never
emits it for `DROP PROCEDURE`/`DROP FUNCTION` name resolution — verified by
grepping every `.out` file under `postgres/src/test/regress/expected/` for
the hint string co-occurring with a `DROP` statement (zero hits). PG's own
`DROP` object-address lookup (`LookupFuncName` via
`postgres/src/backend/catalog/namespace.c`) never sets a hint here; compare
`postgres/src/test/regress/expected/create_procedure.out:425-426`:

```
DROP PROCEDURE nonexistent();
ERROR:  procedure nonexistent() does not exist
```

with no `HINT:` line. Fix: remove the `Hint:` field from the not-found
`ExecError` literal in that branch (`Code`/`Pos`/`Message` unchanged).

## Result

Removes the spurious HINT from DROP PROCEDURE/DROP FUNCTION not-found errors,
matching PG exactly for that path. Buckets 1, 2, 3, 5 remain — ledgered in
`.ralph/deferral_ledger.md` for future M0134 loops or their own milestones
(bucket 5 in particular is a standalone ACL-enforcement gap, not specific to
this regress case).
