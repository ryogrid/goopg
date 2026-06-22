Task: M0118-0009 `delete-abort-savept-2` — LANDED this loop (design 0118-0015).

ROOT CAUSE: a SELF row-lock upgrade inside a savepoint (s1 holds FOR KEY SHARE at
top level; SAVEPOINT f; FOR NO KEY UPDATE under the sub-XID) overwrote the xmax
with a single sub-XID member, discarding the outer top-level KEY SHARE. After
ROLLBACK TO f aborts the sub-XID the row had NO surviving lock, so a conflicting
s2 FOR UPDATE woke and completed immediately instead of waiting until s1 COMMIT.
`stampLockInner` only combined into a MultiXact for FOREIGN holders
(`!isSelfXID(xmax)`); the self-preserving producer `stampMultiLock` (from
0118-0012) was never reached on the self path.

FIX (operators_lockrows.go): new predicate `hasOuterSelfLockMember(hdr)` (true iff
the lock-only xmax holds an active self member with xid != current writerXID —
i.e. an outer savepoint/top-level lock). Widened the MultiXact-producer gate from
`!isSelfXID(xmax)` to `!isSelfXID(xmax) || hasOuterSelfLockMember(hdr)`.
stampMultiLock already keeps members whose xid != writerXID() as survivors, so
s1d now builds {top KEY SHARE, sub-XID NO KEY UPDATE}; ROLLBACK TO drops only the
sub-XID; conflictingLockHolders waits s2's FOR UPDATE on the surviving KEY SHARE.
Same-level self re-lock → no survivor → combined=false → unchanged single stamp
(hot path untouched); strict no-op outside a savepoint.

Files: internal/executor/operators_lockrows.go (THE fix), docs/design/0118-0015-*.md
+ README.md, .ralph/fix_plan.md, .ralph/deferral_ledger.md.

Gates run: build clean; TestPort_IsolationDeleteAbortSavept2 4/4 PASS; siblings
DeleteAbortSavept/AbortedKeyrevoke/TuplelockUpgradeNoDeadlock/LockUpdateDelete/
SkipLocked*/Nowait* no regression; -race mvcc+multixact PASS; executor+storage
unit PASS; pgbench smoke 0-failed; ralph-state-guard pending final.

NEXT loop: `multixact-no-forget` (TestPort_IsolationMultixactNoForget, currently
SKIP). Whole-txn ROLLBACK (NOT a savepoint) of an updater that formed a
{locker, updater} multi: the surviving s1 KEY SHARE locker must be retained while
the aborted s2 updater is forgotten on the READ path. Observed divergence: s3
FOR NO KEY UPDATE WAITS in goopg but should complete immediately (KEY SHARE is
compatible with NO KEY UPDATE) — goopg over-conflicts, likely treating the
aborted updater member as live or the multi's aggregate strength as the updater's.
Start at lockRowsOp.stampLockInner lock-only conflict branch + conflictingLockHolders
membership/abort filtering for a top-level (non-subxact) aborted updater member.

GOTCHAS: CSV not used for individual isolation specs (design doc + fix_plan track
them); never gofmt -w (go1.25 vs local 1.26); isolation specs run goopg as a
SUBPROCESS (debug → file); cd /home/ryo/work/goopg/goopg first. tpch-spotcheck
INFRA-BLOCKED; pgbench smoke is the live guard. Untracked postgres/ + weekly_loc.*
+ requirements.txt are stray artifacts, NOT my work — leave them.
