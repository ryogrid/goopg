Task: M0117-0006 (SLRU buffer pool / 2-bit collapse, gap G6; P2, Effort-L) —
Part A DONE this loop. Part B/C DEFERRED (see deferral ledger 2026-06-15).

CONTEXT / GROUND TRUTH: main HEAD still carries the foreign M0100-0010 catalog WIP
(unreconciled). All M0117 work continues in stacked isolated worktrees off the
previous M0117 tip — never touching the main tree's foreign WIP.

WHAT LANDED (M0117-0006 Part A): branch `m0117-0006-clog-slru-buffer-pool` off
`5fcdb27b` (M0117-0005), worktree `.claude/worktrees/m0117-0006`. Files:
  - internal/config/defaults.go + postgresql.conf.sample: `transaction_buffers`
    GUC (PGC_POSTMASTER, boot_val 0, max 1GiB/8192; unit-less raw buffer count).
  - internal/mvcc/clog_bufferpool.go (NEW): EffectiveCLOGBuffers (port of
    clog.c:CLOGShmemBuffers + SimpleLruAutotuneBuffers); clogBufferPool — bounded
    LRU page cache over the 2-bit SLRU repr, backed by pg_xact/ segment files
    (pinPageLocked fault-in, evictVictimLocked LRU + dirty writeback, getStatus,
    setStatus clear-then-set ≙ TransactionIdSetStatusBit, flushDirty per-segment
    fsync). NOT wired into live CLOG — blast radius nil.
  - internal/mvcc/clog_bufferpool_test.go (NEW): 5 tests incl. encode↔encode
    equivalence vs mirrorToSLRUUnlocked (sibling-path guard).
  - design 0117-0006 + README index; fix_plan M0117-0006 updated (Part A/B/C);
    deferral ledger line.

KEY NOTES: Part A is purely additive (no live caller of the pool yet), so it
cannot regress visibility. Pool encoding pinned byte-identical to the existing
SLRU writer. clear-then-set is the PG-faithful primitive; the live wiring (Part B)
must reconcile it with the legacy OR-mirror durability invariant (M0117-0004).

Gates run: build ./... PASS; -race ./internal/mvcc/... PASS; config (GUC+sample)
PASS; initdb+server PASS; gofmt/vet clean; ralph-state-guard. TPC-H spotcheck SKIPs
under worktree isolation (no-op — no live CLOG path changed).

Next step: M0117-0006 Part B in a DEDICATED session that can run the full TPC-H
Q12/Q13 + standby-visibility gate: wire CLog.GetStatus/setStatus through
clogBufferPool, settling the design-doc open questions (mirror-disabled fallback,
OR-vs-clear-then-set, truncation-via-page-invalidation); then Part C drops the
resident banks + flat file. OR pick the next P2 item: M0117-0007 (async-commit LSN)
or M0117-0008 (datfrozenxid persistence).

PENDING HUMAN: reconcile foreign M0100-0010 WIP, then merge stacked chain in order
m0117-0001 → -0002 → -0003 → -0004 → -0005 → -0006.
