Task: M0118-0009 `timeouts` (row-level half) — `lock_timeout` GUC + statement-timeout-
aware lock waits. COMMITTING this loop. Design 0118-0017.

WHAT LANDED:
- NEW leaf pkg internal/lockwait: WithTimeout(ctx,d)/Timeout(ctx) carries the
  lock_timeout budget as a DURATION (re-armed per wait, like ProcSleep), + sentinel
  ErrLockTimeout. Imports stdlib only → no cycle (lockmgr+mvcc depend on it).
- lockmgr.acquire: new lock-timeout `select` case → ErrLockTimeout; shared splice
  cleanup factored into unparkWaiter (also used by ctx.Done()).
- mvcc.WaitForXID: arms time.AfterFunc(broadcast) + checks lock deadline after ctx.Err().
- dispatch.go: sessionLockTimeout + attaches lockwait.WithTimeout when lock_timeout>0
  (independent of statement_timeout).
- executor/context.go: lockWaitTimeoutError (ErrLockTimeout→"lock timeout",
  DeadlineExceeded→"statement timeout") + lockWaitCancelError (adds Canceled→"user
  request"). Fixed the 3 acquireRel*/Tuple/RelTxn sites (were ALL "user request").
- epqWait now returns (deadlock bool, timeout *ExecError); ~11 call sites in
  operators_storage.go + operators_merge.go surface the timeout (plain client cancel
  still swallowed→retry). lockRowsOp 4 WaitForXID sites use o.rowWaitTimeoutError.

KEY INSIGHT: plain DELETE/UPDATE row-wait goes through epqWait (operators_storage.go),
NOT lockRowsOp — epqWait did `_ = WaitForXID` then spun into a spurious 40001. That was
the real fix needed for the row-level permutations.

Gates: TestPort_TimeoutsRowLevel 4/4 (statement/lock/lock-wins/statement-wins, msg +
~300ms timing); TestLockManagerLockTimeout* -race; full row-lock/savepoint/merge/
multixact/deadlock/skip-locked/nowait isolation batch no regression; -race executor/
mvcc/lockmgr; executor unit; pgbench smoke 0-failed; ralph-state-guard OK.

NOT promoting timeouts.spec to `port` (CSV stays failed): table-level half (rdtbl…
locktbl) needs plain SELECT to take txn-scoped ACCESS SHARE on tableLockMgr so LOCK
TABLE conflicts — perf-sensitive design reversal, DEFERRED (ledger 2026-06-22). The
lock_timeout machinery already covers the LOCK TABLE wait; that half needs only the
scan-side ACCESS SHARE acquisition (gate on TxnLockBackendID like LOCK TABLE).

NEXT loop: either (a) the table-level ACCESS SHARE work to finish+promote timeouts.spec
(measure pgbench!), or (b) pick another M0118-0009 misc spec — async-notify (no
LISTEN/NOTIFY executor support yet), subxid-overflow (needs plpgsql), prepared-
transactions (2PC), horizons/freeze-the-dead/inplace-inval/intra-grant-inplace (vacuum/
freeze/inplace-update internals) — all need substantial new subsystems.

GOTCHAS: never gofmt -w (go1.25 repo vs local 1.26). Isolation specs run goopg as a
SUBPROCESS. CSV rationale must be comma-free. cd /home/ryo/work/goopg/goopg first.
tpch-spotcheck INFRA-BLOCKED; pgbench smoke is the live guard. Untracked postgres/ +
weekly_loc.* + requirements.txt are stray artifacts — leave them.
