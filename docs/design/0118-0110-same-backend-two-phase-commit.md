# 0118-0110 — Same-backend two-phase commit (PREPARE TRANSACTION / COMMIT PREPARED / ROLLBACK PREPARED)

Status: accepted
Milestone: M0118-0009 (Upstream isolation spec suite pass-through)
Type: **Enabler — NOT a spec promotion**

## Problem

Three remaining M0118-0009 isolation specs — `prepared-transactions`,
`prepared-transactions-cic`, and `stats` (its `s1_prepare_a` /
`s1_commit_prepared_a` / `s1_rollback_prepared_a` steps) — exercise PostgreSQL's
**two-phase commit (2PC)**:

```
step p1 { PREPARE TRANSACTION 's1'; }
step c1 { COMMIT PREPARED 's1'; }
step r1 { ROLLBACK PREPARED 's1'; }
```

goopg had **no 2PC support at all**: the parser did not even recognise these
statements (`PREPARE TRANSACTION 's1'` was lexed as the prepared-statement
`PREPARE` with name `TRANSACTION` followed by a stray string literal and failed
to parse / planned to "unsupported statement type 0A000"). Every permutation of
all three specs diverged at the first `PREPARE TRANSACTION` step.

## What upstream does

`PREPARE TRANSACTION 'gid'` splits `COMMIT` into two phases. The prepare phase
durably records the transaction's work under a global transaction identifier
(gid) in `pg_twophase` but does **not** finalise it; its locks and SSI
predicate-lock state persist, owned by a "prepared" `SERIALIZABLEXACT` /
`GlobalTransaction` rather than a live backend. A later `COMMIT PREPARED 'gid'`
or `ROLLBACK PREPARED 'gid'` — possibly from a *different* backend, possibly
after a crash and recovery — finalises it. The commit-time SSI
dangerous-structure check runs at `COMMIT PREPARED`, so in
`prepared-transactions.spec` it is the COMMIT PREPARED of the first committer
(s3) that triggers the abort of one of the three overlapping SERIALIZABLE
transactions.

## What this enabler implements

The **same-backend subset** the isolation specs actually exercise. In every
target spec the gid is PREPAREd and then COMMIT/ROLLBACK PREPAREd from the
**same session**, which does nothing else on that connection in between. So
rather than detaching the prepared transaction from its backend (the genuinely
hard part — a separable `GlobalTransaction` with its own lock owner and SSI
xact, plus `pg_twophase` durability), goopg keeps the prepared transaction
**open** as the connection's active transaction:

- **Parser** (`internal/parser`): new AST nodes `PrepareTransactionStmt{Gid}`,
  `CommitPreparedStmt{Gid}`, `RollbackPreparedStmt{Gid}`. `parsePrepare`
  branches to `PREPARE TRANSACTION 'gid'` when the `TRANSACTION` keyword follows
  `PREPARE` (disambiguating it from the prepared-statement `PREPARE name AS …`).
  `parseCommit` / `parseRollback` branch to the `PREPARED` forms when the
  unreserved word `PREPARED` (lexed as an identifier) follows. The gid is a
  string literal via `parseStrLit`.

- **Connection state** (`internal/server/conn_tx.go`): a `preparedGid` field on
  `connTxState` plus `MarkPrepared` / `PreparedGid` / `ClearPrepared`. `End()`
  clears it.

- **Execution** (`internal/server/twophase.go`): `execTwoPhaseStmt`, invoked from
  `executeOneSimpleStmt` right after the LISTEN/NOTIFY hook (the planner has no
  node for these):
  - `PREPARE TRANSACTION 'gid'` requires an explicit, non-aborted transaction
    block (else `25P01` / `25P02`); it records the gid and leaves the
    transaction **open** so its writes, heavyweight locks and SSI predicate-lock
    state all persist. The connection staying "in a transaction block" from
    goopg's view is invisible to the isolation tester (it does not inspect the
    `ReadyForQuery` status byte) and lets the finalisation reuse the canonical
    path with the session/locks fully wired.
  - `COMMIT PREPARED 'gid'` / `ROLLBACK PREPARED 'gid'` validate the gid against
    the connection's prepared transaction (a gid this connection did not prepare
    is `42704 … does not exist` — cross-backend lookup is deferred), clear the
    marker, then **re-enter `executeOneSimpleStmt` with a synthetic
    `CommitStmt` / `RollbackStmt`**. This runs the *canonical* commit/rollback
    code path verbatim — the same SSI pre-commit dangerous-structure check,
    deferred DROP INDEX / ATTACH PARTITION / inheritance / DROP TABLE
    application, NOTIFY publication, and `connTx.End()` — so there is **no
    parallel commit path** to drift out of sync (sibling-paths discipline). The
    commit-time SSI check therefore fires at `COMMIT PREPARED`, as upstream.

- `isTwoPhaseStmt` keeps the new statements out of the single-statement
  plan-cache pre-plan (`dispatch.go`), exactly as `isNotifyStmt` does, since
  `planner.Plan` has no node for them.

## Blast radius

Essentially nil for everything outside 2PC. `execTwoPhaseStmt` returns
`handled=false` for every statement that is not one of the three new types, so
every existing path is byte-identical. The only shared-path edit is adding
`!isTwoPhaseStmt(stmt)` to the plan-cache guard, which only affects the three
new types. No existing `port` spec uses these statements.

## Verification

- `TestParseTwoPhaseCommit` (parser): the three statements parse to their nodes
  and the ordinary `PREPARE name AS …` / `COMMIT` / `ROLLBACK` /
  `ROLLBACK TO SAVEPOINT` still parse to their original nodes.
- `TestPort_TwoPhaseCommitSameBackend` (e2e, real server over the wire):
  - `COMMIT PREPARED` makes the prepared INSERT visible (and a *concurrent*
    session does **not** see it before `COMMIT PREPARED` — cross-session
    isolation of the still-uncommitted prepared work);
  - `ROLLBACK PREPARED` discards it;
  - `PREPARE TRANSACTION` outside a transaction block → `25P01`;
  - `COMMIT PREPARED` of an unknown gid → `42704`.
- `internal/parser` + `internal/server` unit suites PASS; `go build` + `go vet`
  clean; pgbench smoke = pre-commit hook.

## Why the specs stay deferred

A probe of `prepared-transactions-cic.spec` (single permutation
`w1 p1 cic2 c1 r2`) confirms the mechanism works end-to-end: with the prepared
transaction held open, its MVCC slot stays active, so `CREATE INDEX
CONCURRENTLY` correctly **waits** for it (and unblocks at `COMMIT PREPARED`).
The first divergence has advanced from "parse error at `p1`" all the way to the
final `cic2`/`c1` timing — the **only** residual gap is that goopg's CIC
wait-for-active-slots (`mvcc.WaitForSlotsToCommit`, design 0118-0031) does not
honour `lock_timeout`, whereas PG's `WaitForLockers`/`VirtualXactLock` does, so
PG cancels `cic2` with `55P03 canceling statement due to lock timeout`
(`lock_timeout = 10`) instead of waiting.

Remaining work before promotion:

1. **`prepared-transactions-cic`**: make the CIC active-slot wait honour the
   session `lock_timeout` and abort with `55P03`.
2. **`prepared-transactions`**: full-permutation (1500) verification that the
   commit-time SSI check across held prepared transactions matches PG
   byte-for-byte; the mechanism is in place (the held xact keeps its SSI
   predicate locks), but the large permutation set must be validated and any
   first-committer / conflict-ordering gaps closed.
3. **`stats`**: needs the cumulative pg_stat_* subsystem in addition to 2PC.

Deferred 2PC scope (not needed by any `port` spec): cross-backend `COMMIT
PREPARED`, the `pg_prepared_xacts` view, `pg_twophase` durability across a
restart, and detaching a prepared xact so the originating connection can run
further transactions while it stays prepared.
