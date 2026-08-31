# 0118-0049 — PL/pgSQL transaction control (`COMMIT`/`ROLLBACK` in a DO block)

**Milestone:** M0118-0008 (DDL / VACUUM / maintenance-concurrency tail)
**Status:** accepted — **enabler, NOT a spec promotion**
**Spec advanced:** `plpgsql-toast`

## Summary

Implements PL/pgSQL **transaction control** — bare `COMMIT;` and `ROLLBACK;`
statements inside a **non-atomic** PL/pgSQL routine (a top-level `DO` block, or a
procedure invoked outside an explicit transaction block). After the
commit/rollback the routine continues in a **fresh** transaction. In an *atomic*
context (a `DO` run inside an explicit `BEGIN … COMMIT` block) the statement is
rejected with SQLSTATE `2D000` *invalid transaction termination*, matching
PostgreSQL.

This is the first blocker of `plpgsql-toast`. It is an **enabler**: with `COMMIT`
now executing, that spec's first divergence moves forward from the lex/parse
error (`unsupported PL/pgSQL statement`) to the *next* real gap — PL/pgSQL
`SELECT … INTO var` / record handling (see Deferred). The spec stays open.

## Why goopg needs only the transaction-control half for this spec

`plpgsql-toast` exists to prove that values held in PL/pgSQL variables are free of
external TOAST references across a `COMMIT` (otherwise a concurrent `VACUUM` could
reclaim the chunks → *missing chunk number … for toast value*). goopg stores
`text` **inline** in the Datum — a PL/pgSQL variable never holds an external TOAST
pointer — so the detoast-after-commit hazard the spec stresses does not arise; the
captured values survive a commit by construction. Advisory locks
(`pg_advisory_lock`/`pg_advisory_unlock`, used by the spec to time the `VACUUM`)
already work. Hence the only engine capability the spec needs from goopg is the
ability to `COMMIT` mid-`DO` and keep running.

## The architecture problem

goopg's transaction *lifecycle* (begin / commit / rollback) is owned by the
server's per-connection `connTxState` and `dispatchSimpleQueryViaExecutor`, not by
the executor. A `DO` block runs inside a single auto-commit transaction allocated
by the dispatch; the executor only sees `ctx.Tx` / `ctx.Snap`. To `COMMIT`
mid-`DO`, the PL/pgSQL runtime must reach back up to the dispatch to commit the
current transaction and start a new one — and the dispatch's own bookkeeping
(`tx`, `snap`, the trailing auto-commit at statement end, the per-statement RC
snapshot refresh) must follow the new transaction.

## Design

A new optional callback on the executor `Context`:

```go
// commit (rollback=false) or roll back (rollback=true) the current tx, then
// begin a fresh one, updating ctx.Tx / ctx.Snap in place.
PLpgSQLCommitChain func(rollback bool) error
```

* **Parser** (`internal/plpgsql/parser.go`, `ast.go`): `parseStmt` recognises the
  unreserved keywords `COMMIT` / `ROLLBACK` and emits a new `TxControlStmt{Rollback}`
  node.
* **Runtime** (`internal/executor/plpgsql_runtime.go`): a `case *plpgsql.TxControlStmt`
  invokes `ctx.PLpgSQLCommitChain`. When the callback is **nil** (atomic context)
  it raises `ExecError{Code: "2D000", Message: "invalid transaction termination"}`.
* **Dispatch** (`internal/server/dispatch.go`): the callback is installed **only
  when `autoCommit` is true** (i.e. not inside an explicit `BEGIN` block). The
  closure commits/rolls back the dispatch's current `tx`, releases **xact-scoped**
  advisory locks only (session-scoped `pg_advisory_lock` survives, as in PG),
  begins a fresh RC transaction + snapshot, and assigns the outer `tx` / `snap`
  and `ctx.Tx` / `ctx.Snap`. Because it updates the outer `tx`, the trailing
  `if autoCommit { TxnMgr.Commit(tx) }` and the per-statement RC snapshot refresh
  both operate on the *latest* chained transaction; a post-commit error rolls back
  only the post-commit work (the committed transaction is already durable).

### Blast radius

Zero for existing paths. The dispatch change only *adds* a callback-field
assignment; nothing in the system invokes `PLpgSQLCommitChain` except the new
`TxControlStmt` runtime case, which exists only in `DO`/procedure bodies that
contain `COMMIT`/`ROLLBACK`. Every other query path is byte-for-byte unchanged
(pgbench / regress / existing isolation specs unaffected).

## Tests / gates

* `internal/plpgsql`: `TestParseTransactionControl` (COMMIT/ROLLBACK → `TxControlStmt`).
* `internal/testport`:
  * `TestPlpgSQLDoCommitChainDurability` — insert 1, `COMMIT`, insert 2, `RAISE` →
    row 1 survives, row 2 rolled back (commit chained, pre-commit work durable).
  * `TestPlpgSQLDoRollbackChain` — insert 1, `ROLLBACK`, insert 2 → row 1
    discarded, row 2 committed.
  * `TestPlpgSQLDoCommitInExplicitBlockRejected` — `BEGIN; DO $$ begin commit; end $$;`
    → SQLSTATE `2D000`.
* No regression: `TestPort_IsolationSubxidOverflow` (heavy PL/pgSQL),
  `TestPort_IsolationFreezeTheDead` strict PASS; full `internal/executor` +
  `internal/server` unit suites PASS; `go vet` clean. pgbench smoke = pre-commit hook.

## Deferred (keeps `plpgsql-toast` open)

The next blocker is PL/pgSQL **`SELECT … INTO var`** (scalar + record), which goopg
currently captures as a raw embedded-SQL statement and mis-parses as SQL
`SELECT … INTO <table>`. The spec also exercises record/composite types
(`r record`, `r test2`), record-field assignment (`r.b := …`), and `FOR rec IN
SELECT … LOOP` across its 7 permutations, plus the post-`COMMIT` advisory-lock
wait. These are a separate Effort-L slice (PL/pgSQL `SELECT INTO` + record
binding). Resume point: add a PL/pgSQL `SELECT … INTO` statement form (strip the
`INTO` target list before re-parsing the query, bind the first row to the named
variable(s)), then record handling.
