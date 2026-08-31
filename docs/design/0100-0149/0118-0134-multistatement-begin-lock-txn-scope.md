# 0118-0134 — `BEGIN; LOCK …` in one simple-query message must be transaction-scoped

Milestone: **M0118-0009** (misc / system-level isolation specs) — regression fix.
Status: **landed** — restores `vacuum-concurrent-drop` + `vacuum-skip-locked` to green.

## Symptom

Two pass-required (`runIsoSpecStrict`) isolation specs —
`vacuum-concurrent-drop` and `vacuum-skip-locked` — were failing at HEAD even
though both were promoted to `pass` (designs 0118-0033 / 0118-0035). The failure
was that the maintenance step never **blocked**: in every permutation the
`<waiting ...>` / `<... completed>` markers were absent, e.g.

```
expected: step analyze_specified: ANALYZE part1, part2; <waiting ...>
actual:   step analyze_specified: ANALYZE part1, part2;            (completes immediately)
```

`ANALYZE part1, part2` did not wait behind the concurrent `LOCK part1 IN SHARE
MODE` held by the other session, so the post-wait drop re-check (and its
`relation no longer exists` WARNING) never fired.

## Root cause (bisected to commit d1f40e28, design 0118-0090 async-notify)

`vacuum-concurrent-drop` / `vacuum-skip-locked` define their lock step as a
**single two-statement step**:

```
step "lock" { BEGIN; LOCK part1 IN SHARE MODE; }
```

Before the async-notify promotion, the isolation runner split a multi-statement
step and sent each statement as its **own** simple-query message. So `BEGIN`
arrived first (the connection entered an explicit transaction block), and the
following `LOCK part1 IN SHARE MODE` arrived as a separate message — by which
time `connTx.InExplicit()` was already true, so `LOCK` took a real
transaction-scoped heavyweight `ShareLock` (`acquireRelLockTxn` under
`TxnLockBackendID`).

Design 0118-0090 changed the runner (`execMultiStatement`) to send a
multi-statement step as **one** simple-query message — required for NOTIFY
de-duplication and `ROLLBACK TO SAVEPOINT` discard, which need the statements to
share one implicit transaction (upstream `PQexec` semantics). That exposed a
latent server bug:

`dispatchSimpleQueryViaExecutor` seeds `ectx.TxnLockBackendID` **once**, before
the per-statement loop, from the transaction state *as it stands at message
entry*:

```go
if connTx != nil && connTx.InExplicit() {     // false for "BEGIN; LOCK ..."
    ectx.TxnLockBackendID = connTx.LockBackendID
}
```

For a `BEGIN; LOCK …` message the connection is still in autocommit when this
runs, so `TxnLockBackendID` stays 0. When `LOCK` then executes later in the same
loop, `acquireRelLockTxn` sees `TxnLockBackendID == 0` and degrades to a
**display-only no-op** (the documented autocommit behaviour) — no real lock is
taken, so the concurrent `ANALYZE`/`VACUUM` never blocks.

The bug only manifests when `BEGIN` and a transaction-scoped-lock statement
share one message. Specs that put `BEGIN` in a separate step from the lock were
unaffected, which is why most lock/DDL specs stayed green and the regression was
mis-attributed to a "300 ms blocking-detection timing flake" across loops.

## Fix

`internal/server/dispatch.go`: refresh `ectx.TxnLockBackendID` at the top of the
per-statement loop so it tracks the **live** transaction state across statements
in one simple-query message:

```go
for i, stmt := range stmts {
    if connTx != nil && connTx.InExplicit() {
        ectx.TxnLockBackendID = connTx.LockBackendID
    } else {
        ectx.TxnLockBackendID = 0
    }
    ...
}
```

After `BEGIN` executes (iteration 0) `connTx.active` becomes true, so the next
iteration (`LOCK`) sees `InExplicit() == true` and `LOCK` takes the real
transaction-scoped `ShareLock`. `LockBackendID` is a stable per-connection
identity (assigned at connection setup, never 0), so the lock survives the
per-statement `ReleaseAll` and is released only at COMMIT/ROLLBACK — exactly as
when the statements arrived as separate messages.

This is strictly more PG-faithful: a `BEGIN; LOCK …` issued in one `PQexec`
opens the block and the LOCK is held to end-of-transaction. The `else` branch
keeps autocommit statements display-only (e.g. after a `COMMIT` earlier in the
same message). Blast radius is confined to multi-statement simple-query messages
that change transaction state mid-message; single-statement steps and
separate-message BEGIN/lock sequences compute the identical value they did
before.

## Verification

- `TestPort_IsolationVacuumConcurrentDrop` + `TestPort_IsolationVacuumSkipLocked`
  strict PASS (were failing at HEAD; bisected first-bad = d1f40e28).
- Full `TestPort_Isolation*` strict suite re-run — no regressions.
- `pgbench` smoke via the pre-commit hook.

## Oracle

Upstream `postgres/src/test/isolation/isolationtester.c` sends each step body as
a single `PQexec`, and `BEGIN; LOCK …` in one `PQexec` opens a transaction block
with the LOCK held to commit (`postgres/src/backend/tcop/postgres.c`
`exec_simple_query` loops over the parsetree list within one transaction
command). This change makes goopg's simple-query path match that for the
transaction-scoped lock identity.
