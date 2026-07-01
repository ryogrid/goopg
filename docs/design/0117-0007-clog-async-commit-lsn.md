# 0117-0007 — CLOG async-commit LSN tracking (gap G8)

Status: **accepted (Part A landed 2026-06-29; Part B landed 2026-07-02 — see
"Part B" below for the scope actually delivered vs. the remaining latency gap)**
Milestone: M0117-0007

## Problem (gap G8)

PostgreSQL supports **asynchronous commit** (`synchronous_commit=off`): a
committing backend records the commit in the CLOG and returns to the client
*without* first flushing its commit WAL record to disk. Durability is preserved
by a different barrier: a CLOG page is never allowed to reach disk while still
claiming a commit whose WAL record has not yet been flushed. To enforce that, PG
tracks, per small group of XIDs sharing a CLOG page, the **highest commit-record
LSN** any of them produced (`group_lsn`), and `XLogFlush`es up to that LSN
immediately before the page is written back.

goopg's CLOG has no LSN association at all: every status change is durably
written and fsynced inline (the M0117-0005 group-commit path still fsyncs the
SLRU segment per group). There is therefore no way to (a) return a "safe to
flush to here" LSN for a transaction's status, nor (b) defer the per-commit WAL
fsync under a future `synchronous_commit=off` path without risking a torn
durability guarantee. This is gap **G8**.

(Source: `docs/analysis/clog-goopg-gaps-and-remediation-2026-06-14.md` §G8.)

## PG reference

- `CLOG_XACTS_PER_LSN_GROUP = 32` (power of 2) — `clog.c:92`.
- `CLOG_LSNS_PER_PAGE = CLOG_XACTS_PER_PAGE / CLOG_XACTS_PER_LSN_GROUP`
  (= 32768/32 = **1024** entries per page) — `clog.c:93`.
- `GetLSNIndex(slotno, xid) = slotno*CLOG_LSNS_PER_PAGE +
  (xid % CLOG_XACTS_PER_PAGE)/CLOG_XACTS_PER_LSN_GROUP` — `clog.c:95-96`.
- `TransactionIdSetPageStatusInternal` (`clog.c:702-716`): after writing the
  status bits, *iff* `lsn` is valid, bumps
  `group_lsn[GetLSNIndex(slotno,xid)] = Max(group_lsn[…], lsn)`. The comment
  notes `lsn` is **invalid during recovery**, so the group LSN is only updated on
  the live commit path; replayed pages re-acquire their LSN on the next live
  change.
- `TransactionIdGetStatus(xid, *lsn)` (`clog.c:734-758`): returns the status and
  loads `*lsn = group_lsn[GetLSNIndex(slotno,xid)]` — "an LSN late enough that
  flushing to it guarantees the commit record is on disk" (may be a later
  transaction's LSN in the same group, never earlier).
- `SlruPhysicalWritePage` / `SimpleLruWritePage`: before a dirty SLRU page is
  written, PG `XLogFlush`es the max `group_lsn` over the page so the WAL backing
  every claimed commit is durable first (the async-commit write barrier).
- `group_lsn` is **shared-memory only** — never persisted; a page already on
  disk implies its WAL is already flushed, so a faulted-in page starts with a
  zero (invalid) group LSN.

## Decomposition

G8 builds on the M0117-0006 SLRU buffer pool: the `group_lsn` array is, in PG,
part of the same `SimpleLruCtl` shared state as the page buffers, and the
write-barrier fires exactly where a buffer-pool page is flushed. Since the pool
itself is **not yet wired into the live CLOG path** (M0117-0006 Part B is
deferred to a dedicated full-gate session), the LSN tracking lands on the pool
as composing infrastructure with **nil live blast radius**, mirroring the
`m0074` precedent for central / high-risk subsystems under autonomous worktree
isolation (where the TPC-H Q12/Q13 spot-check and standby-visibility E2E SKIP).

### Part A — LSN-group tracking on the buffer pool + WAL-flush barrier (THIS slice, landed)

1. **Per-LSN-group constants** (`clog_bufferpool.go`): `clogXactsPerLSNGroup =
   32`, `clogLSNsPerPage = clogXactsPerPage / clogXactsPerLSNGroup` (= 1024),
   and `lsnIndexInPage(xid) = (xid % clogXactsPerPage) / clogXactsPerLSNGroup`.
   This is `GetLSNIndex` **minus the `slotno*CLOG_LSNS_PER_PAGE` term**: PG keeps
   one flat `group_lsn` array across all slots, whereas each goopg
   `clogPageSlot` carries its own `groupLSN [clogLSNsPerPage]uint64`, so the
   per-slot index is just the intra-page group number.

2. **`clogPageSlot.groupLSN`**: a `[]uint64` of length `clogLSNsPerPage`,
   allocated lazily with the page `data` and **zeroed on every fault-in**
   (faithful: an on-disk page's WAL is already durable ⇒ invalid/zero group
   LSN). LRU eviction and the resident-page bound are unaffected (the array
   travels with its slot).

3. **`setStatusWithLSN(xid, status, lsn)`**: identical to `setStatus` but, when
   `lsn != 0`, also bumps `slot.groupLSN[lsnIndexInPage(xid)] = max(…, lsn)`.
   `setStatus` is kept as `setStatusWithLSN(xid, status, 0)` so the existing
   (LSN-free) callers and tests are byte-identical. `lsn == 0` is goopg's
   `InvalidXLogRecPtr` (the WAL writer treats position 0 as "nothing to flush",
   `writer.go:705`), so a zero LSN is a no-op on the group array — matching PG's
   recovery branch.

4. **`groupLSNFor(xid)`**: the read side ≙ the `*lsn` out-parameter of
   `TransactionIdGetStatus`; returns the group LSN for a resident page (faulting
   it in if needed). Used by a future status-with-LSN lookup.

5. **WAL-flush write barrier**: the pool gains an injected
   `flushWAL func(lsn uint64) error` hook (nil ⇒ disabled, the default — so the
   not-yet-live pool and every unit test are unchanged). When set,
   `writePageToDisk` and `flushDirty` compute the **max group LSN over the page**
   and call `flushWAL(maxLSN)` *before* the page's bytes are written, enforcing
   the async-commit ordering (WAL durable ⇒ then the committing CLOG page). The
   hook is wired to `wal.Writer.FlushUpTo` by the CLog layer when the pool goes
   live (Part B); injection keeps `mvcc` free of a `wal` import.

### Part B — live `synchronous_commit=off` activation (LANDED 2026-07-02)

**What landed.** `internal/initdb/open.go`'s `EnablePGSLRUMirror` call site now
wires `clog.SetFlushWALHook(walWriter.FlushUpTo)` immediately after the pool is
created — the Part A barrier, previously never connected to a live WAL writer
(dead code outside its own unit tests), is now live and exercised on every
dirty-page write-back. `mvcc.Manager` gained a `CommitAsync` alongside `Commit`
(`finish` takes a new `waitLocalFlush bool`, forwarded to the `xactMarker` hook,
whose signature grew the same parameter); the hook in `open.go` skips the
inline `walWriter.FlushUpTo(endLSN)` when `waitLocalFlush` is false and calls
the new `CLog.SetCommittedWithLSN(xid, endLSN)` (→ `setStatusWithLSN`) instead
of `SetCommitted`, associating the commit LSN with the XID's CLOG page. A new
`executor.Context.AsyncCommit` (true only for the literal GUC value `"off"` —
`SyncCommitMode`/`SyncRepOff` intentionally collapses `"off"` and `"local"`
together for the *remote*-wait decision, which is not the distinction needed
here) is read by a new `sessionAsyncCommit` helper (`internal/server/
dispatch.go`) and threaded through every live interactive commit call site via
one new `Context.CommitTransaction(tx)` method (`operators_tx.go`'s explicit
COMMIT, both `dispatch.go` autocommit branches including the PL/pgSQL commit
chain, `dispatch_extended.go`'s extended-protocol autocommit — 2PC's
`COMMIT PREPARED` reuses these same paths via `executeOneSimpleStmt`, so it is
covered without a separate call site).

**COPY's own commit call sites (LANDED 2026-07-02, loop #50).** The 4
commit sites in `internal/server/copy.go` flagged as a follow-up in the
previous "Still open" section are now wired: `dispatchCopyViaExecutor`
computes `asyncCommit := sessionAsyncCommit(sess)` (a new `sess
*config.SessionRegistry` parameter, threaded from `handleQueryOrCopy` which
already had it), sets `ectx.AsyncCommit`, and its `CopyTo` / file-based
`CopyFrom` branches now call `ectx.CommitTransaction(tx)` instead of calling
`s.cfg.TxnMgr.Commit(tx)` directly. The two `copyInState`-based streaming
`COPY FROM STDIN` commits (in `handleCopyInFrame`, reached from a later wire
frame with no `ectx` in scope) go through a new package-level
`commitCopyTx(mgr, tx, asyncCommit)` helper — the same
`if asyncCommit { CommitAsync } else { Commit }` shape as
`Context.CommitTransaction` — keyed off a new `copyInState.asyncCommit bool`
field snapshotted at construction time. `runInlineCopy`/
`runInlineCopyFromStdin` (the multi-statement-batch COPY path) were already
covered before this loop: they share the batch's `ectx`, which already had
`AsyncCommit` set and is committed once by the dispatch loop via
`ectx.CommitTransaction`.

**Why this alone does not yet reduce commit latency (read before assuming
`synchronous_commit=off` is fast in goopg).** M0117-0005's group-commit design
calls `CLog.groupUpdate` → `pool.flushDirty()` **synchronously, on every single
commit** (batched across concurrent committers, but never deferred past the
triggering commit) — unlike real PG, whose CLOG SLRU page stays dirty in
shared memory until eviction or the next checkpoint. Because `flushDirty`
itself invokes the write barrier (`flushWALBeforeWriteLocked`) before writing
a dirty page, and `SetCommittedWithLSN` gives that barrier a valid (non-zero)
LSN, the barrier fires **immediately, inside the same commit call**, forcing
exactly the same `walWriter.FlushUpTo` that the (now-skipped) inline call
would have made. So today, skipping the explicit flush and relying on the
barrier are latency-equivalent: the client still waits for the same fsync
round trip before the commit call returns. (For a *synchronous* commit,
`SetCommittedWithLSN` is deliberately **not** used — see below — precisely to
avoid adding a second, redundant `FlushUpTo` round trip on top of the existing
explicit one.)

Making `synchronous_commit=off` actually cut latency requires a further,
distinctly-scoped change: an async commit's `groupUpdate` call must be able to
mark its CLOG page dirty **without** forcing `flushDirty` right away, leaving
the write-back (and hence the barrier) to a *later* event — a subsequent
synchronous commit's group flush (which flushes every currently-dirty page,
not just its own), LRU eviction (`pinPageLocked`'s eviction path already
barrier-guards any dirty victim, sync or async), or a checkpoint. The last of
these is the blocker: goopg's checkpointer (`internal/wal/checkpointer.go`,
`DirtyPageFlusher` interface, `FlushAll() error`) currently flushes only the
heap buffer pool (`*storage.Pool`) — `CLog`/`clogBufferPool` implements no
such interface and is never registered with a `Checkpointer`. Without a
checkpoint-driven CLOG flush, an all-async workload could leave CLOG pages
dirty in memory indefinitely (bounded only by the resident-page eviction
budget), which — unlike a crash (where WAL replay legitimately reconstructs
CLOG state from scratch, the accepted PG async-commit risk) — would make a
*clean* shutdown/restart cycle rely on a potentially large WAL replay to
recover CLOG state that a checkpoint should have bounded. That is a distinct,
larger change (touching the checkpoint subsystem, not just CLOG+WAL) and is
the real remaining item — see "Still open".

Landed regardless, and correct/necessary independent of the latency question:
the write barrier is real infrastructure (previously entirely dark in
production), the LSN association is real and tested, and `synchronous_commit`
is read for the local-flush decision for the first time anywhere in the
commit path (previously read only for the remote sync-rep wait). This is the
prerequisite plumbing the checkpoint-integration follow-up needs; it does not
by itself change any query's observed behaviour or performance under
`synchronous_commit=on` (the default, and every existing gate), and is
inert-safe under `=off` (correct, not yet faster).

### Still open (Part B continuation, NOT landed this loop)

- **Lazy CLOG write-back for async commits + checkpoint-driven CLOG flush.**
  `CLog.setStatusWithLSN`'s call to `c.groupUpdate` would need to become
  conditional (skip the durable write-back for an async commit, leaving the
  page dirty), and `CLog`/`clogBufferPool` needs a `FlushAll` (or equivalent)
  registered with the `wal.Checkpointer` as a second `DirtyPageFlusher`
  alongside the heap pool, so a checkpoint bounds how long an async-committed
  CLOG page can stay unflushed. This is the change that actually removes the
  fsync round trip from a `synchronous_commit=off` commit's critical path.
  Resume point: `internal/mvcc/clog_groupcommit.go` (`groupUpdate`),
  `internal/mvcc/clog.go` (add an exported flush entry point alongside
  `SetFlushWALHook`), `internal/wal/checkpointer.go` (`DirtyPageFlusher`),
  wiring site `internal/initdb/open.go` (wherever the primary's `Checkpointer`
  is constructed). Needs the full TPC-H spot-check + crash-recovery / standby
  E2E gate this design doc already calls for, since it changes when CLOG
  durability actually happens.
- ~~**COPY's own commit call sites**~~ — **LANDED 2026-07-02, loop #50**, see
  the "COPY's own commit call sites" paragraph above. This item is closed.

## Faithfulness / divergence notes

- **Index divergence** (documented above): per-slot `groupLSN` array drops PG's
  `slotno*CLOG_LSNS_PER_PAGE` base. Semantically identical — same LSN for the
  same XID — and avoids a global array that would have to be re-indexed on every
  eviction.
- **max-LSN monotonicity**: `setStatusWithLSN` only ever raises a group entry,
  never lowers it (≙ PG's `if (group_lsn[i] < lsn) group_lsn[i] = lsn`). A later
  transaction in the same 32-XID group can raise the barrier for an earlier one;
  that is the documented PG behavior ("might return the LSN of a later
  transaction in the same group"), always conservative (flush further, never
  less).
- **Not persisted**: `groupLSN` is in-memory only; reopening the pool zeroes it,
  exactly as PG reconstructs `group_lsn` from zero after restart.

## Testing

`internal/mvcc/clog_bufferpool_lsn_test.go`:

- `TestLSNIndexInPage` — pins `lsnIndexInPage` against PG's `GetLSNIndex`
  intra-page term across group boundaries (XID 0, 31, 32, 33, last group of a
  page, first XID of the next page).
- `TestGroupLSNMaxSemantics` — set/raise/no-lower within a group; a second XID in
  the same group raises the shared barrier; a zero LSN is a no-op.
- `TestGroupLSNZeroedOnReopen` — set an LSN, flush, reopen the pool ⇒ group LSN
  reads back zero (not persisted), while the **status bits survive** (the data
  page is durable). Guards the "WAL already flushed for on-disk pages" invariant.
- `TestFlushWALBarrierFiresBeforeWrite` — install a `flushWAL` spy; assert it is
  called with the page's max group LSN and (via an ordering flag) *before* the
  page bytes hit disk, for both `flushDirty` and eviction-driven
  `writePageToDisk`. nil hook ⇒ never called (default-off).

Part A gate: `go build ./...`; `go test -race ./internal/mvcc/...`; `go test
./internal/config/... ./internal/initdb/... ./internal/server/...`;
`TestE2E_PhysicalReplication{,Sync}`; gofmt/vet.

**Part B (2026-07-02, loop #49) adds:**

- `internal/mvcc/clog_asynccommit_test.go` —
  `TestCLogSetFlushWALHookBeforePoolExistsIsNoop` (out-of-order call safety),
  `TestCLogSetCommittedWithLSNFiresBarrierOnFlush` (the full CLog-level chain:
  `SetCommittedWithLSN` → barrier fires with the right LSN → status readable),
  `TestCLogSetCommittedNoLSNNeverFiresBarrier` (the sync-commit invariant: a
  plain `SetCommitted` must never trigger the barrier, so a synchronous commit
  never pays a second `FlushUpTo` round trip).
- `internal/mvcc/manager_test.go`'s `TestCommitAsyncPassesWaitLocalFlushFalse`
  (only `CommitAsync` passes `waitLocalFlush=false`; `Commit`/`Rollback` pass
  `true`).
- `internal/executor/commit_async_test.go`'s
  `TestContextCommitTransactionRespectsAsyncCommit` (`Context.CommitTransaction`
  dispatches to `CommitAsync`/`Commit` per `AsyncCommit`).
- `internal/server/sync_commit_test.go`'s `TestSessionAsyncCommit` (`"off"` and
  its boolean-ish spellings ⇒ true; `"local"` ⇒ false, distinct from
  `sessionSyncCommitMode`'s off/local collapse for the remote-wait decision).

Part B gate (all PASS): `go build ./...`; `go vet ./...`; `gofmt -l` clean on
every touched file; `go test -count=1 ./internal/mvcc/... ./internal/executor/...
./internal/server/... ./internal/initdb/...`; `go test -race
./internal/mvcc/... ./internal/wal/...`; `TestE2E_PhysicalReplication{,Sync}`,
`TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart`,
`TestE2E_ChecksumStreamingGoopgToPG` (`internal/testport`); TPC-H spot-check
(`scripts/tpch-spotcheck.sh`) Q12=2/Q13=33 PASS; pgbench smoke via the
pre-commit hook.

**Part B / COPY follow-up (2026-07-02, loop #50) adds:**

- `internal/server/copy_async_commit_test.go`'s
  `TestCommitCopyTxRespectsAsyncCommit` — pins the same
  waitLocalFlush-sequence contract as
  `TestContextCommitTransactionRespectsAsyncCommit`, but for the new
  package-level `commitCopyTx` helper (`asyncCommit=false` → `waitLocalFlush=
  true` via `Commit`; `asyncCommit=true` → `waitLocalFlush=false` via
  `CommitAsync`).

COPY follow-up gate (all PASS): `go build ./...`; `go vet ./...`; `gofmt -l`
clean on `internal/server/copy.go` + the new test; `go test -count=1
./internal/server/... ./internal/mvcc/... ./internal/executor/...`; `go test
-race ./internal/mvcc/... ./internal/wal/...`; TPC-H spot-check
(`scripts/tpch-spotcheck.sh`) Q12=2/Q13=33 PASS.

## Status / merge

Landed directly on `align-data-structure-with-pg` (Part A: commit `1f1100e8`;
M0117-0006 Parts A–C: commits through `0ab77d45`; Part B mechanical wiring:
loop #49; Part B / COPY commit-site follow-up: loop #50). The "Still open"
section above (lazy CLOG write-back + checkpoint-driven CLOG flush) is the
actual remaining scope for the fix_plan box to close — not a merge
formality.
