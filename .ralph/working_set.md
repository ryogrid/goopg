Task: M0117-0007 (async-commit LSN tracking, gap G8; P2, Effort-L) — Part A DONE
this loop. Part B DEFERRED (see deferral ledger 2026-06-15).

CONTEXT / GROUND TRUTH: main HEAD still carries the foreign M0100-0010 catalog WIP
(unreconciled). All M0117 work continues in stacked isolated worktrees off the
previous M0117 tip — never touching the main tree's foreign WIP.

WHAT LANDED (M0117-0007 Part A): branch `m0117-0007-clog-async-commit-lsn` off
`318f38c8` (M0117-0006 Part A), worktree `.claude/worktrees/m0117-0007`. Files:
  - internal/mvcc/clog_bufferpool.go: + `clogXactsPerLSNGroup=32`,
    `clogLSNsPerPage=clogXactsPerPage/32=1024`, `lsnIndexInPage` (≙ GetLSNIndex
    minus slotno base); `clogPageSlot.groupLSN []uint64` (zeroed on fault-in);
    `flushWAL func(uint64) error` hook on the pool; `setStatusWithLSN` (raises
    groupLSN to max, `setStatus` now delegates with lsn=0); `groupLSNFor`;
    `maxGroupLSNLocked`; `flushWALBeforeWriteLocked`; barrier wired into the
    eviction path (pinPageLocked) + flushDirty (flush page max BEFORE write).
  - internal/mvcc/clog_bufferpool_lsn_test.go (NEW): 4 tests — GetLSNIndex
    arithmetic, max-LSN monotonicity + same-group sharing, zeroed-on-reopen vs
    durable status bits, barrier-fires-before-write (flushDirty + eviction) +
    nil-hook default-off.
  - design 0117-0007 + README index; fix_plan M0117-0007 (Part A/B); ledger line.

KEY NOTES: Part A is purely additive — the pool has NO live caller yet
(M0117-0006 Part B deferred), so the LSN tracking + barrier cannot regress
visibility/durability. `flushWAL` nil by default ⇒ no-op. `lsn==0` ≙
InvalidXLogRecPtr (matches PG recovery branch). group_lsn NOT persisted (≙ PG).

Gates run: build ./... PASS; -race ./internal/mvcc/... PASS; config + initdb +
server PASS; TestE2E_PhysicalReplication{,Sync} PASS; gofmt/vet clean; TPC-H
spotcheck SKIPS under worktree isolation (no-op — no live CLOG path changed);
ralph-state-guard.

Next step: M0117-0007 Part B in a DEDICATED full-gate session (wire flushWAL →
wal.Writer.FlushUpTo, thread commit LSN through commit path, skip inline fsync
under synchronous_commit=off) — defers WITH M0117-0006 Part B. OR pick the next
P2 item: M0117-0008 (datfrozenxid persistence — note: catalog tuple-format change,
touches the contaminated foreign-WIP area).

PENDING HUMAN: reconcile foreign M0100-0010 WIP, then merge stacked chain in order
m0117-0001 → -0002 → -0003 → -0004 → -0005 → -0006 → -0007.
