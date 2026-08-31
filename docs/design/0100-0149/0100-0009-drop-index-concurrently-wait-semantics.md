# Design: DROP INDEX CONCURRENTLY Wait Semantics (M0100-0009)

## Problem

PostgreSQL's `DROP INDEX CONCURRENTLY` must block until all transactions that
were active *at the time of the DROP command* have committed or rolled back.
This ensures no query in-flight at that moment holds a reference to the
soon-to-be-dropped index.

Before this change, goopg executed `DROP INDEX CONCURRENTLY` identically to
plain `DROP INDEX` — it removed the index immediately with no waiting. The
IsolationRunner test `drop-index-concurrently-1.spec` therefore failed because
the expected `<waiting ...>` / `<... completed>` markers were never emitted.

## Solution

### 1. Parser — `DropIndexStmt.Concurrent`

`DropIndexStmt` in `internal/parser/ast.go` gained a `Concurrent bool` field.
`internal/parser/ddl.go` sets it when the `CONCURRENTLY` keyword is present.

### 2. MVCC — `WaitForOlderSlotsToCommit`

A new method on `mvcc.Manager`:

```go
func (m *Manager) WaitForOlderSlotsToCommit(ctx context.Context, selfHandle TxnHandle) error
```

**Algorithm:**
1. Snapshot all `procArray` slots with `inTxn == 1`, excluding the caller's own slot (`selfHandle - 1`).
2. If the snapshot is empty, return immediately.
3. Spin on `commitCond.Wait()` until all snapshotted slots have `inTxn == 0` or `ctx` is cancelled.

**Why `inTxn` and not XID?**  
XID is lazily assigned on the first write. A read-only `BEGIN` session has no XID but its slot is still marked `inTxn = 1`. Using `inTxn` correctly covers such sessions — matching PostgreSQL's behaviour where even a read-only transaction delays concurrent DDL.

**Why snapshot once, not re-scan?**  
We only need to wait for transactions that *predate* the DROP command.
Transactions that start after the DROP can safely proceed without being aware
of the index. Snapshotting avoids indefinitely blocking behind a busy system.

**Wake-up mechanism:** `commitCond` is broadcast on every `finish()` (COMMIT
or ROLLBACK). A background goroutine calls `commitCond.Broadcast()` when
`ctx.Done()` fires so the lock-hold loop wakes promptly.

### 3. Executor — `execDropIndex`

`internal/executor/operators_ddl.go` was extended with two early checks:

1. **Transaction-block guard**: If `s.Concurrent == true` and the session is
   inside an explicit transaction, return SQLSTATE `25001`
   (`active_sql_transaction`) — matching PostgreSQL's error.

2. **Wait**: If `s.Concurrent == true`, call
   `WaitForOlderSlotsToCommit(ctx, tx.Handle)` before removing the index entry
   from the catalog.

## Files Changed

| File | Change |
|---|---|
| `internal/parser/ast.go` | `Concurrent bool` field on `DropIndexStmt` |
| `internal/parser/ddl.go` | Set `Concurrent` when `CONCURRENTLY` keyword present |
| `internal/mvcc/manager.go` | `WaitForOlderSlotsToCommit` method |
| `internal/executor/operators_ddl.go` | Transaction-block guard + wait in `execDropIndex` |

## Tests

| Test | Location | What it covers |
|---|---|---|
| `TestWaitForOlderSlotsToCommit_NoActive` | `internal/mvcc/wait_for_slots_test.go` | Returns immediately with no other active transactions |
| `TestWaitForOlderSlotsToCommit_BlocksUntilOtherCommits` | same | Blocks until other transaction commits |
| `TestWaitForOlderSlotsToCommit_CancelContext` | same | Context cancellation unblocks waiter |
| `TestWaitForOlderSlotsToCommit_DoesNotWaitForSelf` | same | Self-exclusion prevents deadlock |
| `TestWaitForOlderSlotsToCommit_MultipleOthers` | same | Waits for all multiple concurrent transactions |
| `TestDropIndexConcurrentlyWaitsForOpenTransaction` | `internal/server/drop_index_concurrent_test.go` | End-to-end: DROP blocks then unblocks after COMMIT |
| `TestDropIndexConcurrentlyBlockedInExplicitTx` | same | Returns error when issued inside explicit transaction |
| `TestPort_IsolationDropIndexConcurrently1` | `internal/testport/isolation_port_test.go` | PG isolation spec: `<waiting>` / `<completed>` markers |
