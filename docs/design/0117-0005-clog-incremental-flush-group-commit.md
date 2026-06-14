# 0117-0005 — CLOG incremental flush + group commit (gap G7)

Status: accepted
Milestone: M0117 (CLOG ↔ PostgreSQL subsystem alignment)
Author: Ralph (loop, 2026-06-15)
Supersedes: none
Builds on: 0117-0002 (visibility fallback), 0117-0004 (SUB_COMMITTED lane)

## Problem (gap G7)

goopg's `CLog` had two per-commit costs that scale badly with the size of the
commit log and with concurrency:

1. **Whole-file flat flush.** `CLog.flush` (`internal/mvcc/clog.go`) collected
   *every* bank into one contiguous slice and rewrote the entire
   `global/pg_xact` flat file with `os.WriteFile` on **every** `setStatus`
   call. For a clog with N XIDs that is O(N) bytes written per single-XID
   status change — pathological once the log grows past a few MB.

2. **One fsync per committing XID.** `setStatus` calls `mirrorToSLRUUnlocked`,
   which `f.Sync()`s the touched `pg_xact/` segment on every call. Under
   concurrent commits each backend serialises its own fsync; there is no
   amortisation.

PostgreSQL solves (1) implicitly (its SLRU only ever writes the dirty 8 kB
page) and (2) explicitly with the **group XID status update** optimisation
(`clog.c:TransactionGroupUpdateXidStatus`, PG 18.3): concurrent committers
enqueue onto a lock-free list; the first arrival (the *leader*) drains the
whole list under a single bank-lock acquisition and applies every queued
update together, then wakes the followers.

This task brings goopg to parity for both.

## Design

### Part A — incremental flat-file flush

The flat file is XID-indexed: byte offset `i` holds the status of XID `i`. A
bank covers `xidsPerBank = 128*1024` contiguous XIDs, so a fixed 8192-byte
*flat page* (`clogFlatPageSize`) always lies entirely inside one bank
(`128K / 8192 = 16` pages per bank). Flat page `p` maps to file offset
`p*8192` and to bank `p/16` at intra-bank offset `(p%16)*8192`.

`setStatus` now records the touched flat page in a `dirtyPages` set
(`markFlatDirty`) instead of triggering a whole-file rewrite. The durable
write happens in the group-commit leader path (Part B) via
`flushDirtyPagesLocked`, which writes **only** the dirty pages back with
`WriteAt` (extending the file with a zero gap if needed — zero == Unknown,
harmless). The flat file is still written **without fsync** (unchanged
contract: the fsynced `pg_xact/` SLRU is the durable source of truth — see
`EnablePGSLRUMirror`'s comment; the flat file is a fast in-process cache that
`MarkUnknownAsAborted` repairs after a crash).

The bulk/relayout callers keep the whole-file rewrite, which is correct and
infrequent: `InitializeAsCommitted`, `MarkUnknownAsAborted`, and
`TruncateCLOG` still call `flush()`. `flush()` now (a) acquires `flushMu` so a
full rewrite never interleaves with an incremental page write, and (b) clears
`dirtyPages` (the full rewrite supersedes every pending page).

### Part B — group commit (`clog_groupcommit.go`)

A faithful Go port of `TransactionGroupUpdateXidStatus` using a Treiber stack
(`atomic.Pointer[clogGroupNode]` head, mirroring `ProcGlobal->clogGroupFirst`):

- `setStatus` sets the in-memory bank byte (idempotent fast path unchanged),
  then calls `groupUpdate(xid, status)` for the durable part.
- `groupUpdate` allocates a node `{xid, status, done chan error}` and CAS-pushes
  it onto `groupHead` (mirrors the `clogGroupNext`/`clogGroupFirst` CAS loop).
  - If the previous head was non-nil → **follower**: block on `<-node.done`
    (mirrors sleeping on the proc semaphore until `clogGroupMember` clears).
  - If the previous head was nil → **leader**: run `runLeader(node)`.
- `runLeader` acquires `flushMu` (the single global serialisation point — goopg
  has one flat file and one SLRU writer, so unlike PG we need no per-bank lock
  juggling), `Swap`s the whole stack to nil in one atomic op (mirrors PG's
  `pg_atomic_exchange_u32(&clogGroupFirst, INVALID)` — avoids the ABA problem
  of popping one at a time), applies the batch durably
  (`applyGroupBatchLocked`), releases `flushMu`, then signals every follower's
  `done` channel (wakeups happen **after** the lock is dropped, exactly as PG
  wakes semaphores outside the bank lock).

`applyGroupBatchLocked` does the two batched writes:
1. `flushDirtyPagesLocked` — the incremental flat-page write (Part A).
2. `mirrorGroupToSLRULocked` — groups the batch's XIDs by `pg_xact/` segment
   and performs **one** `f.Sync()` per touched segment (vs one per XID). The
   per-lane OR semantics match `mirrorToSLRUUnlocked` /
   `TransactionIdSetStatusBit` (a lane only ever advances toward a terminal
   state). `node.status` is used directly (the value just written to the bank).

Because goopg serialises all durable writes on `flushMu`, we drop PG's
same-page grouping restriction (PG bails out with `return false` when a later
arrival targets a different page so the leader keeps a single bank lock). Our
leader can mix pages/segments freely in one batch, which is strictly more
batching, not less.

### Concurrency / correctness notes

- The Treiber stack is loss-free: a node either wins its CAS before the
  leader's `Swap` (and is drained by that leader) or, if its CAS races the
  `Swap` to nil, the CAS fails, it reloads `nil`, and becomes a new leader.
- Two leaders can run `runLeader` concurrently (a new group forms while the
  previous leader is still applying); `flushMu` serialises the durable apply,
  and their node sets are disjoint, so there is no data race or lost update.
- The leader's own node is the tail of its stack (its push saw `old == nil`),
  so the drain traversal always includes it. The leader returns its own error
  directly and sends the same error to every follower's buffered `done` chan.
- All new `CLog` fields are zero-value-ready (`atomic.Pointer`, `sync.Mutex`,
  a lazily-created map), so the direct `&CLog{}` / `&CLog{path:…}` construction
  used by `OpenCLog` and the existing tests needs no constructor change.

## Files

- `internal/mvcc/clog.go` — `setStatus` routes the durable write through group
  commit; `flush()` acquires `flushMu` + clears dirty pages; new
  `markFlatDirty` / `flushDirtyPagesLocked`; `dirtyPages` + `flushMu` +
  `groupHead` fields; `clogFlatPageSize` const.
- `internal/mvcc/clog_groupcommit.go` — NEW: `clogGroupNode`, `groupUpdate`,
  `runLeader`, `applyGroupBatchLocked`, `mirrorGroupToSLRULocked`.
- `internal/mvcc/clog_groupcommit_test.go` — NEW: concurrent group-commit
  correctness + incremental-flush round-trip.

## Oracle

`postgres/src/backend/access/transam/clog.c`:
- `TransactionGroupUpdateXidStatus` (lines 441-653) — the group-update algorithm.
- `TransactionIdSetStatusBit` — the OR-into-lane semantics mirrored by
  `mirrorGroupToSLRULocked`.

## Gates

- `go test -race ./internal/mvcc/...` (group-commit concurrency).
- Commit-throughput sanity: `clog_groupcommit_test.go` drives N concurrent
  committers and asserts every XID's status is durably readable after a
  re-open (flat file) and decodes correctly from the SLRU.
- TPC-H spot-check SKIPs under worktree isolation (runtime data dir lives in
  the main tree); the change is durability-path-only and behaviour-preserving
  for status reads, so it is a verified no-op for the bench.

## Known follow-ups

- Async-commit LSN tracking (`CLOG_XACTS_PER_LSN_GROUP`) is M0117-0007; the
  group node carries no LSN yet (goopg's commit path does not thread a
  per-commit LSN into CLOG).
- SLRU buffer pool / 2-bit collapse is M0117-0006; the flat file remains fully
  resident in banks for now.
