Task: M0118-0009 `aborted-keyrevoke` — LANDED this loop (design 0118-0014).

Resumed a previous loop's WIP (uncommitted edits + a /tmp debug log left in
operators_lockrows.go). Removed debug, found + fixed the missing root cause.

ROOT CAUSE: `SAVEPOINT` issued before the txn's first write (BEGIN; SAVEPOINT f;
UPDATE…) ran `AllocateSubXid(sess.tx.XID)` while tx.XID was still lazily-0, so the
sub-XID was registered with parent=0 in the pg_subtrans SubxactMap. parent=0 →
cross-session TopLevelXid(subxid)=0 → s2's snapshot treats the savepoint's
uncommitted NEW tuple as VISIBLE (returned phantom key=2 instead of waiting) and
xidActiveWithSubxact reports it dead so the row-lock wait never fires.
delete-abort-savept escaped this only because its FOR KEY SHARE assigned the XID
before the savepoint.

FIX (operators_tx.go execSavepoint): call o.ctx.MaterializeWriterXID() before
AllocateSubXid when sess.tx.XID == Invalid → subxid→parent link non-zero from
birth. Strict no-op once a top-level XID exists; nested savepoints inherit it.
Kept the previous loop's supporting twins: sub-XID xmin in
writeHeapRowReturning{,PG}+tryApplyHOTUpdate; HEAP_KEYS_UPDATED on the single-xid
old-tuple stamp in stampUpdaterXmaxNonHOT; descendant-direction subxact
self-visibility in isCurrentTxXID (subxact_visibility.go).

Files: internal/executor/operators_tx.go (THE fix), operators_storage.go,
operators_lockrows.go (debug removed), internal/mvcc/subxact_visibility.go,
.ralph/fix_plan.md, docs/design/0118-0014-*.md + README.md.

Gates run: build clean; -race mvcc+multixact PASS; executor+storage unit PASS;
TestPort_IsolationAbortedKeyrevoke 14/14 PASS; siblings delete-abort-savept /
tuplelock-upgrade-no-deadlock / lock-update-delete still PASS; pgbench smoke
0-failed; ralph-state-guard OK.

NEXT loop: pick a fresh M0118-0009 misc spec. Closest unblocked siblings:
- `delete-abort-savept-2`: FOR NO KEY UPDATE pure-lock upgrade restore on the
  row-lock path (lockRowsOp must preserve+restore an outer-level lock-only member
  across ROLLBACK TO — mirror of the producer fix in 0118-0013). Currently SKIP.
- `multixact-no-forget`: whole-txn ROLLBACK of an updater member must retain the
  surviving locker on the read path. Currently SKIP.

GOTCHAS: CSV rationale comma-free; never gofmt -w (go1.25 vs local 1.26);
isolation specs run goopg as a SUBPROCESS (debug → file); cd /home/ryo/work/goopg/goopg
first. tpch-spotcheck INFRA-BLOCKED; pgbench smoke is the live guard.
