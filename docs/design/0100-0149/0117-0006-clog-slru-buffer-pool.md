# 0117-0006 — CLOG SLRU buffer pool / 2-bit collapse (gap G6)

Status: **accepted (Part A + Part B + Part C landed; Part C landed 2026-07-01, loop #47)**
Milestone: M0117-0006
Branch: `m0117-0006-clog-slru-buffer-pool` (off the M0117-0005 tip `5fcdb27b`)

> **Part B LANDED 2026-06-29 (loop #11).** The buffer pool is now the live
> in-memory CLOG store on the production path; see the "Part B — LANDED"
> section below. The Part-B "deferred" reason on record across loops #7–#10 —
> *"the mandatory gates SKIP in the autonomous WSL2 loop"* — was **empirically
> disproven this loop**: the heterogeneous PG-standby E2E
> (`TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart`,
> `TestE2E_ChecksumStreamingGoopgToPG`), `-race ./internal/mvcc/... ./internal/wal/...`
> (incl. `xlog_replay`), AND the TPC-H Q12/Q13 spot-check (runnable since
> 0117-0009 + 0117-0010) all RUN and PASS here. Part C (drop the resident banks +
> flat file) remains deferred.

## Problem (gap G6)

PostgreSQL holds the commit log (CLOG) in a bounded SLRU **buffer pool**: a fixed
set of `BLCKSZ` pages of the 2-bit-per-XID status representation, paged in and out
of the `pg_xact/` segment files on demand, sized by the `transaction_buffers`
GUC. Memory is therefore bounded regardless of how many XIDs have ever been
assigned.

goopg's `CLog` (`internal/mvcc/clog.go`) instead keeps the **entire** status
array resident as per-bank byte slices (`clogBank.data`, one *byte* per XID —
`internal/mvcc/clog.go:53`). Consequences:

- Memory scales linearly with `NextXID` (≈1 byte/XID — **16× denser in PG**,
  which packs 4 XIDs/byte).
- There is no eviction; combined with G1 (now fixed: truncation lands in
  M0117-0001) the resident array was the practical memory ceiling.
- The `pg_xact/` SLRU mirror files are touched per-update with no page cache
  (`mirrorToSLRUUnlocked` opens/reads/writes/fsyncs the segment file every call).

(Source: `docs/analysis/clog-goopg-gaps-and-remediation-2026-06-14.md` §G6.)

## PG reference

- `transaction_buffers` GUC — `postgres/src/backend/utils/misc/guc_tables.c:2499`
  (PGC_POSTMASTER, RESOURCES_MEM, `GUC_UNIT_BLOCKS`, boot_val 0, max
  `SLRU_MAX_ALLOWED_BUFFERS`).
- `CLOGShmemBuffers()` — `clog.c:768`: `transaction_buffers==0 ⇒
  SimpleLruAutotuneBuffers(512, 1024)`, else `Min(Max(16, transaction_buffers),
  CLOG_MAX_ALLOWED_BUFFERS)`.
- `SimpleLruAutotuneBuffers(divisor, max)` — `slru.c:231`:
  `Min(max-(max%SLRU_BANK_SIZE), Max(SLRU_BANK_SIZE, NBuffers/divisor - …))`.
- `SLRU_BANK_SIZE = 16`, `SLRU_MAX_ALLOWED_BUFFERS = 1GiB/BLCKSZ` —
  `postgres/src/include/access/slru.h`.
- The 2-bit page layout (`clogXactsPerByte=4`, `clogXactsPerPage=BLCKSZ*4`,
  `slruPagesPerSegment=32`) already lives in `clog.go` and is reused verbatim.

## Decomposition

G6 is an Effort-**L** memory-model rewrite of CLOG — the highest-blast-radius
subsystem in the project (silent visibility corruption is the most expensive
historical failure mode). It is split into independently-verifiable slices so the
risky live swap is isolated and lands with the full TPC-H/visibility gate in a
dedicated session, rather than blind under worktree isolation (where the TPC-H
spot-check SKIPs). This mirrors the documented `m0074` precedent: central / high-
risk changes get infrastructure-only scope in an autonomous loop, full wiring
deferred.

### Part A — GUC + standalone buffer-pool component (THIS slice, landed)

1. **`transaction_buffers` GUC** (`internal/config/defaults.go`,
   `postgresql.conf.sample`): registered PGC_POSTMASTER, boot_val `0`, min 0, max
   `1GiB/8192`. goopg has no 8 kB-block config unit, so it is stored unit-less as
   a raw buffer count — identical to PG's `GUC_UNIT_BLOCKS` stored value, since
   one block == one CLOG buffer upstream.

2. **`EffectiveCLOGBuffers(transactionBuffers, sharedBuffers)`**
   (`internal/mvcc/clog_bufferpool.go`): faithful port of
   `CLOGShmemBuffers + SimpleLruAutotuneBuffers` resolving the GUC to a resident-
   page budget (auto: `sharedBuffers/512`, floored at one bank, capped at 1024,
   bank-aligned; explicit: `Min(Max(16, n), 1GiB/BLCKSZ)`).

3. **`clogBufferPool`** (`internal/mvcc/clog_bufferpool.go`): a bounded LRU page
   cache over the 2-bit SLRU representation, backed by the `pg_xact/` segment
   files. At most `nslots` `BLCKSZ` pages resident; reads fault a page in from
   its segment file (missing/short file ⇒ zero-filled = in-progress, mirroring PG
   faulting an unwritten page); a full pool evicts the LRU slot, writing it back
   first if dirty. `getStatus`/`setStatus` read/update a single 2-bit lane;
   `setStatus` uses PG's **clear-then-set** (`TransactionIdSetStatusBit`) lane
   update and reports whether the lane actually changed (idempotent on a repeat).
   `flushDirty` writes every dirty page back and fsyncs each touched **segment**
   exactly once (matching the M0117-0005 group-commit "one fsync per segment"
   goal). All state under one mutex (≙ PG's per-bank SLRU lock; the pool is not
   lock-free).

   The component is the genuine production data structure the Part-B wiring will
   drop in — not a throwaway. Its on-disk encoding is pinned **byte-for-byte**
   equal to the existing `mirrorToSLRUUnlocked` writer by an
   encode↔encode equivalence test, so it cannot drift from the canonical format
   the rest of `CLog` and an attached PG standby read.

### Part B — wire reads/writes through the pool (deferred)

Replace `CLog.GetStatus`/`setStatus` (and the bulk callers
`InitializeAsCommitted`/`MarkUnknownAsAborted`/`TruncateCLOG`,
`HighestKnownXID`, `loadFromSLRU`, `distributeToBanks`) so the pool — not the
resident `banks` — is the in-memory store. Open questions to settle **in that
slice** (each is a known trap):

- **Mirror-disabled fallback.** The pool is backed by the `pg_xact/` segment
  files; today the SLRU mirror can be disabled (`slruDir == ""`), in which case
  the `banks` are the only store. Part B must either require the SLRU as the
  backing store unconditionally or keep a flat-file-backed pool variant for the
  no-mirror tests.
- **OR vs clear-then-set.** The legacy mirror uses strict OR
  (`mirrorToSLRUUnlocked`) for a deliberate durability reason (preserve a durable
  committed bit against a stale in-memory abort, M0117-0004). The pool primitive
  is clear-then-set (PG-faithful). On a fresh write onto a zero (in-progress)
  lane the two are byte-identical (proven by the equivalence test), but Part B
  must decide the semantics for the live overwrite path and keep the
  M0117-0004 visibility invariant.
- **Truncation.** `TruncateCLOG` drops whole banks and removes segment files;
  with the pool it must instead invalidate resident pages below the cutoff.
- **Flat file.** Once the pool is the store, the `global/pg_xact` flat file and
  its incremental-flush machinery (M0117-0005 Part A) become redundant with the
  SLRU; Part C removes them (the 2-bit "collapse" — dropping the 1-byte/XID
  store entirely).

### Part B — LANDED (2026-06-29, loop #11)

The buffer pool is the live in-memory CLOG store whenever the SLRU mirror is
enabled (every production path). Implementation, matching the blueprint below
with the deviations noted:

- **Promotion point.** `CLog.pool` is an `atomic.Pointer[clogBufferPool]`
  (race-free for the startup store vs concurrent commit-path loads), set by
  `EnablePGSLRUMirror` **last** — after `loadFromSLRU` + the
  `mirrorTerminalRangeBatchedUnlocked` backfill — so it faults pages from a
  `pg_xact/` directory in which every terminal bank entry (incl. flat-file-only
  ones) has already been projected. The `&CLog{}` unit-test path with no mirror
  keeps `banks` (`pool == nil`).
- **Reads/writes.** `GetStatus` → `pool.getStatus`; `setStatus` →
  `pool.setStatus` (PG clear-then-set, idempotent no-op skips the group-commit
  round-trip) then `groupUpdate`; bootstrap/frozen XIDs (`< FirstNormalTransactionID`)
  keep their `pg_xact/0000` lanes zero, matching the legacy
  `mirrorGroupToSLRULocked` short-circuit (basebackup byte-equality).
- **Durability.** `applyGroupBatchLocked` → `pool.flushDirty()` (one fsync per
  touched segment), **replacing both** `flushDirtyPagesLocked` (flat file) and
  `mirrorGroupToSLRULocked` (bank→SLRU). The async-commit `flushWAL` barrier is
  left nil this slice — synchronous commit flushes the commit WAL record before
  `setStatus`, so no barrier is needed; wiring it is M0117-0007 Part B.
- **Bulk callers re-pointed** (all run AFTER `EnablePGSLRUMirror` in
  `initdb.Open`, so they MUST go through the single store):
  `InitializeAsCommitted` / `MarkUnknownAsAborted` sweep via `pool.getStatus` +
  `pool.setStatus` then one `pool.flushDirty()` (the M0117-0004 "only
  Unknown→Aborted" guard is preserved, read through the pool);
  `HighestKnownXID` → new `highestSLRUXID()` scans the on-disk segments
  (descending, tail-first) for the maximum terminal lane, since the banks are
  vestigial.
- **Truncation.** `TruncateCLOG` keeps `truncateSLRUSegments(cutoffPage)` and
  adds `pool.invalidateBelow(cutoffPage)` (drops resident pages below the cutoff
  WITHOUT writeback — their segment file was just unlinked; compacts `slots`/
  `pageMap` so freed slots are reused). The vestigial flat-file `flush()` is
  skipped when the pool is live.
- **Flat file retired.** With the pool live the goopg-legacy `global/pg_xact`
  flat file is no longer written (it was never fsynced — the SLRU was always the
  durable store; basebackup already excluded it, and PG itself has no such
  file). `bootstrapCLog`'s SetCommitted(1)/(2) now no-op (SLRU-bypassed), so the
  flat file is simply never created; `OpenCLog` already tolerates its absence and
  `loadFromSLRU` repopulates from the authoritative SLRU. Two flat-file-reopen
  test views (`TestGroupCommitConcurrent`, `TestCLogDualStoreConsistency`) and
  the bootstrap flat-file assertion (`TestBootstrapCLog_WritesPGCanonicalSLRU`)
  were updated to the production recovery path (`OpenCLog` + `EnablePGSLRUMirror`,
  reconstructing from the SLRU) — strictly stronger than the flat-file-only
  reopen they replaced.
- **Pool sizing.** `EffectiveCLOGBuffers(c.clogBuffers, 0)`; `clogBuffers`
  defaults to 0 (auto = 16 pages, bank-aligned) and is settable via
  `SetCLOGBuffers` before `EnablePGSLRUMirror`. Auto sizing is correctness-safe —
  eviction writes back + re-faults on demand — and the TPC-H/pgbench working sets
  touch few CLOG pages, so 16 resident pages do not thrash.
  - **Follow-up LANDED 2026-06-29 (loop #12):** the `transaction_buffers` GUC
    value is now threaded into `SetCLOGBuffers` from `initdb.Open`. `cmd/goopg
    start` reads the GUC (`intGUC(registry, "transaction_buffers", 0)`) into the
    new `OpenOptions.TransactionBuffers` field; `Open` calls
    `clog.SetCLOGBuffers(opts.TransactionBuffers)` immediately before
    `EnablePGSLRUMirror` (a no-op once the pool exists). The boot default 0 keeps
    the auto-16 floor (behaviour unchanged for every default deployment); a
    non-zero `postgresql.conf` override now actually sizes the live pool instead
    of being silently dropped. Regression coverage:
    `cmd/goopg/main_test.go:TestTransactionBuffersFromGUC` (+ nil-registry) pins
    the GUC read; `clog_bufferpool_live_test.go:TestSetCLOGBuffersSizesPool` pins
    the `SetCLOGBuffers` → `pool.nslots = EffectiveCLOGBuffers(n,0)` end of the
    wire (auto floor, explicit 128, below-floor clamp to 16).

Regression coverage: `clog_bufferpool_live_test.go`
(`TestCLOGPoolIsLiveStore` — read/write/HighestKnownXID across pages+segments +
recovery reopen; `TestCLOGPoolMarkUnknownAsAbortedThroughPool` — the recovery
sweep preserves a committed lane; `TestCLOGPoolTruncateInvalidates` — segment
removal + pool page invalidation).

**Gates run (Part B):** `go build ./...` clean; `go test -race ./internal/mvcc/...`
+ `./internal/wal/...` (incl. `xlog_replay`) PASS; `internal/initdb` + `internal/server`
full suites PASS; heterogeneous PG-standby E2E (`TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart`,
`TestE2E_ChecksumStreamingGoopgToPG`) PASS — a real PG 18.3 standby reads the
`pg_xact/` bytes the pool writes; **TPC-H Q12=2 / Q13=33 spot-check PASS** on the
populated 6M-row data dir (visibility checks served by the pool); pgbench smoke
on commit. `gofmt -l` / `go vet ./internal/mvcc/` clean.

### Part C — drop the resident banks + flat file (PLAN, this loop)

Remove `clogBank`/`banks`/`banksMu` (renamed to `slruDirMu`, its one remaining
job) and every legacy-store method, completing the 2-bit collapse (16× memory
reduction: 1 byte/XID resident → the pool's bounded page budget, independent of
`NextXID`).

**Key finding that simplified this beyond the Part B blueprint's own estimate:**
every production caller (`GetStatus`/`setStatus`/`InitializeAsCommitted`/
`MarkUnknownAsAborted`/`HighestKnownXID`/`TruncateCLOG`) already had a complete,
self-sufficient `if p := c.pool.Load(); p != nil { ... }` branch from Part B —
none of them read `banks` inside that branch. The **only** thing `banks` still
did on the production path was serve as a **transient staging buffer** inside
`EnablePGSLRUMirror`: `loadFromSLRU` copied the SLRU segment bytes into `banks`,
then `mirrorTerminalRangeBatchedUnlocked` immediately read those same bytes back
out via `GetStatus` (banks branch, since `pool` didn't exist yet) and wrote them
right back into the same SLRU segments — a no-op round-trip for any data dir
that has only ever been touched by a Part-B-or-later binary (no production code
path writes the legacy flat file once Part B lands — `bootstrapCLog`'s
`SetCommitted(1)/(2)` are XIDs `< FirstNormalTransactionID` and no-op in the
pool branch before ever touching a bank or the flat file). So Part C does not
need a "re-sequence the bootstrap" redesign for that case: it simply
**deletes** the `loadFromSLRU` call and the backfill loop from
`EnablePGSLRUMirror` and creates the pool directly. The pool's own lazy
page-fault-on-read (already exercised by `TestCLOGBufferPoolLRUEviction`/
`TestCLOGBufferPoolRoundTripAllLanes`) is the sole remaining "load" mechanism.
**Accepted compatibility cut (confirmed via review, not glossed over):** a data
dir last touched by a **pre-Part-B** binary could in principle still carry a
legacy flat file with committed/aborted bytes that were never projected into
the SLRU (e.g. a crash between the old `flush()` write and the old
`mirrorGroupToSLRULocked` SLRU write). Under Part C, `OpenCLog` never reads
`path` at all, so those bytes are silently never recovered — this is a real,
deliberate cutoff (not merely an on-disk format non-issue as an earlier draft
of this plan implied), justified only because Part B already stopped writing
and trusting the flat file, and every gate in this project re-inits the data
dir across a milestone like this one (Hard-won Rule #3). Any data dir that
needs to survive this transition must be re-initialized, exactly as Part B's
own landing already required.

**What is being removed** (`internal/mvcc/clog.go`, `internal/mvcc/clog_groupcommit.go`):
- Struct: `clogBank` type; `CLog.banks`, `CLog.path`, `CLog.dirtyMu`,
  `CLog.dirtyPages` fields. `CLog.banksMu` **renamed** to `CLog.slruDirMu` (its
  one surviving job — guarding the `slruDir` field read by
  `mirrorToSLRUUnlocked`/`firstRetainedSLRUXID`/`highestSLRUXID`/`SLRUDir`).
- Methods, entirely: `getOrCreateBank`, `getBank`, `distributeToBanks`,
  `markFlatDirty`, `flushDirtyPagesLocked`, `flush`, `flushLocked`,
  `mirrorTerminalRangeBatchedUnlocked` (dead once the `EnablePGSLRUMirror`
  backfill loop and `MarkUnknownAsAborted`'s legacy branch are both gone — its
  only two callers), `mirrorGroupToSLRULocked` + `applySegmentLanesLocked`
  (`clog_groupcommit.go`; dead once `applyGroupBatchLocked`'s legacy branch is
  gone — its only caller), `loadFromSLRU`.
- Methods, legacy branch only (kept, pool branch becomes unconditional):
  `GetStatus`, `setStatus`, `InitializeAsCommitted`, `MarkUnknownAsAborted`,
  `HighestKnownXID`, `TruncateCLOG` step (5a)/(5b) bank-drop and
  flat-file-rewrite-on-no-pool.
- `OpenCLog(path string)` no longer reads the flat file into banks — it is now
  `return &CLog{}, nil` for a missing/present file alike (the `path` parameter
  is kept, unused, to avoid a signature change rippling through ~13 call sites
  across `internal/mvcc` + `internal/initdb`; each callsite comment now notes
  the legacy flat file this path used to name is never read or written).
- `IsEmpty()`: needs a rewrite, and **not** with a process-local flag — an
  earlier draft of this plan proposed a `CLog.everWritten atomic.Bool` set by
  `setStatus`, but an independent review agent caught that this is a live
  correctness bug: `open.go:846`'s `if clog.IsEmpty() { clog.InitializeAsCommitted(...) }`
  upgrade-detection check runs immediately after `EnablePGSLRUMirror`, **before**
  any in-process `setStatus` call — a process-local flag would read `false`
  (i.e. "empty") on *every* ordinary restart of a populated cluster, regardless
  of how much real committed/aborted status already sits durably in `pg_xact/`,
  wrongly routing every restart into `InitializeAsCommitted` (stamps every
  Unknown XID Committed) instead of the correct crash-recovery
  `MarkUnknownAsAborted` sweep in the `else` branch — silently resurrecting
  crashed/in-progress transactions as visible-and-committed after every
  restart. `IsEmpty()` must instead answer from **durable, on-disk** truth.
  Fix: reuse the already-existing `highestSLRUXID()` (scans the on-disk SLRU
  segments for the highest XID carrying any non-Unknown 2-bit lane, returning 0
  if none exists) — `IsEmpty()` becomes `return c.highestSLRUXID() == 0` when
  the pool is live. This needs no new field, reuses machinery already proven by
  `HighestKnownXID`'s Part B pool branch, and is disk-truth by construction (a
  restart with real prior status on disk correctly reads non-empty; a truly
  virgin `pg_xact/` — fresh bootstrap or genuine pre-M0030-0007 upgrade —
  correctly reads empty). `internal/initdb/open.go`'s upgrade-detection caller
  is unaffected in behavior — same semantics, disk-truth mechanism instead of
  a process-local one.

**Test migration (required for this to be safe).** An independent review agent
exhaustively grepped every `&CLog{}`/`OpenCLog(` call site and found the initial
~20-function estimate incomplete; the corrected, verified scope:

- **Plain migration** (add `EnablePGSLRUMirror(t.TempDir())` before exercising
  the CLog, same inputs/outputs expected): the ~20 functions across
  `internal/mvcc/clog_test.go`, `clog_slru_recovery_test.go`, `manager_test.go`
  (`TestClassifyXID_ClogAbortedFallback`), `snapshot_clog_fallback_test.go`, and
  **`internal/initdb/xact_recovery_test.go`'s all four tests**
  (`TestReplayCLogFromWAL_NativeCommit`/`_NativeAbort`/`_CommitInvalAlsoStamps`/
  `_MissingWalDir`) — verified these do **not** already route through
  `initdb.Open`/`bootstrapCLog` (an earlier draft of this plan wrongly assumed
  they did); they construct a bare `OpenCLog` directly and need the same
  mirror-enablement migration as the `internal/mvcc` tests.
  `internal/initdb/pg_xact_slru_test.go`/`pg_catalog_physical_load_test.go` also
  in this bucket (confirmed by the review).
- **Reopen-and-compare assertions that need the mirror on BOTH sides**
  (silent-regression risk, not a compile error — flagged explicitly per the
  review): `clog_test.go`'s `TestCLogPersistence` and the reopen half of
  `TestCLogMarkUnknownAsAborted` each open a `CLog`, write status, then open a
  **second, independent `CLog`** on the same path and assert the status
  reloads. Today this works because `OpenCLog` reads the legacy flat file into
  `banks`; once `OpenCLog` stops reading `path` entirely, both the writer and
  the reopened reader must call `EnablePGSLRUMirror(dir)` against the *same*
  `dir` so the reload goes through the shared SLRU directory instead — without
  this, the reopened reader would silently read back `TxnStatusUnknown` and
  the test would report a wrong (not a compile-time) failure.
- **Callers of the deleted `loadFromSLRU` directly — need bespoke rewrites, not
  mechanical migration** (compile-breaking, found by the review, absent from
  the original draft): a shared helper `freshFromSLRU` (`clog_dual_store_consistency_test.go:51-58`)
  builds a bare `&CLog{}` and calls `.loadFromSLRU(slruDir)` directly to
  independently re-decode the on-disk SLRU bytes as a sibling-path equivalence
  check, used by `TestCLogDualStoreConsistency`, `TestCLogEnableMirrorBackfillBatched`,
  `TestCLogSubCommittedResolvesViaParent` (`clog_dual_store_consistency_test.go`)
  and `TestGroupCommitConcurrent` (`clog_groupcommit_test.go:110`); plus
  `TestCLogMarkUnknownAsAbortedBatchedSLRU` (`clog_test.go:231-295`), which
  does the same thing inline. **Simply calling `EnablePGSLRUMirror` on the
  `fresh` CLog does not preserve these tests' intent** — their entire point is
  an independent decode path that does NOT go through the pool (so pool bugs
  can't mask themselves). Rewrite each to decode the SLRU segment bytes
  directly (e.g. a small test-local helper that reads a segment file and
  unpacks 2-bit lanes, mirroring what `loadFromSLRU` did) rather than routing
  through `CLog`/the pool at all — keeping these as genuine sibling-path
  checks instead of deleting the independence the tests were designed to
  provide.
- `TestLoadFromSLRU_SegFileNotMultipleOfBlockSize` (misaligned-segment-length
  case) needs replacing with an equivalent pool-level fault-in assertion (the
  pool already handles a short file by zero-filling the missing tail, per
  `clogBufferPool.readPage`/`clog_bufferpool_test.go`'s existing eviction/
  round-trip coverage — the migrated test should pin the misaligned-length
  case specifically, the original test's unique contribution).
- `clog_bufferpool_live_test.go` — audit only; it already exercises the pool
  directly and is expected to need no change, confirm during implementation.

**On-disk format.** Confirmed no on-disk format is tied to `banks` — it is
purely an in-memory struct; the durable stores (segment files under `pg_xact/`)
are untouched byte-for-byte. The fix_plan's "re-init data dir on the
memory-model change" caution is about safety margin during rollout (a data dir
touched by a pre-Part-C binary could in principle still have a stale
`global/pg_xact` flat file lying around from before Part B — now permanently
ignored), not an actual format break; every gate below runs against a freshly
initdb'd dir per Hard-won Rule #3 regardless.

**Mandatory gates for this change (highest-blast-radius subsystem — run before
declaring done, not deferred):** `go build ./...` clean; `go vet ./...` clean;
`go test -race ./internal/mvcc/... ./internal/wal/...`; `internal/initdb` +
`internal/server` full suites; heterogeneous PG-standby E2E
(`TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart`,
`TestE2E_ChecksumStreamingGoopgToPG`); crash-recovery replay
(`internal/initdb/xact_recovery_test.go`, `clog_crash_test.go`); TPC-H
Q12=2/Q13=33 spot-check on a fresh populated data dir; pgbench smoke at commit
(pre-commit hook); `gofmt -l` (no new drift beyond the pre-existing go1.25/1.26
baseline).

**Results (loop #47, 2026-07-01): all PASS.** `go build ./...`/`go vet ./...`
clean. `go test -race ./internal/mvcc/... ./internal/wal/...` PASS (one
`internal/wal` timing flake unrelated to this change,
`TestReserveEmittedAndPublishConcurrentChainAndStripePublishConsistent`, reran
green in isolation — that package has zero diff here). `internal/initdb`
(169s) and `internal/server` full suites PASS. `TestE2E_
StandbyAttachRetainsUpstreamRowsAfterRestart` and
`TestE2E_ChecksumStreamingGoopgToPG` PASS (the standby test's own liveness
note about post-restart walreceiver reconnection is a pre-existing, unrelated
caveat — the CLOG standby-attach invariant itself is verified). Crash-recovery
replay (`internal/initdb/xact_recovery_test.go`, `clog_crash_test.go`) PASS as
part of the `internal/initdb` suite run. TPC-H spotcheck on the persistent
`postgres@postgres` data target: Q12=2 (28.32s), Q13=33 (90.45s), RESULT=PASS.
`gofmt -l` on every touched file: clean. pgbench smoke runs at commit time via
the pre-commit hook. A post-implementation review pass also found and fixed
three stale doc comments left behind by the plan's own edits (two "legacy
flat file" references in `internal/initdb/{open,initdb}.go`, and the
`TruncateCLOG`/`SetSubCommitted` doc comments in `clog.go` still describing
the now-deleted `banks`/flat-file mechanics) plus one now-unused
`applyGroupBatchLocked(batch)` parameter (the pool flush no longer needs
per-member data) — all confirmed via `go build`/`go vet`/full re-test after
the cleanup.

## Part B implementation blueprint (for the dedicated full-gate session)

This section is the code-grounded execution plan derived for the dedicated
session so it need not re-map the entanglement. It supersedes the bare
"open questions" above with concrete resolutions.

### Current entanglement (what Part B replaces)

- **In-memory store** = `banks` (`clog.go`); `GetStatus`/`setStatus` read/write
  `b.data[byteIdx(xid)]` (one byte/XID).
- **Durability** is driven by the M0117-0005 group-commit leader:
  `setStatus` → `markFlatDirty` + `groupUpdate` → `runLeader` →
  `applyGroupBatchLocked`, which does **(1)** `flushDirtyPagesLocked` (flat file
  `global/pg_xact`) and **(2)** `mirrorGroupToSLRULocked` (the `pg_xact/` SLRU
  segments, batched one-fsync-per-segment, **reading the lane bytes from
  `banks`**).
- **OR semantics**: `mirrorGroupToSLRULocked`/`mirrorToSLRUUnlocked` OR the lane
  in (never clear) to preserve a durable committed bit against a stale in-memory
  abort (M0117-0004). The pool primitive is **clear-then-set** (PG-faithful).
- **Reads** = `GetStatus(banks)`. **Bulk callers** =
  `InitializeAsCommitted` / `MarkUnknownAsAborted` / `HighestKnownXID` /
  `loadFromSLRU` / `distributeToBanks` / `TruncateCLOG`, all over `banks`.

### Resolutions

1. **Dual-path keyed on `slruDir` (resolves "mirror-disabled fallback").**
   Production **always** sets `slruDir` (both `initdb.Open` and `initdb` call
   `EnablePGSLRUMirror` right after `OpenCLog`), so create the pool in
   `EnablePGSLRUMirror` (`c.pool = newCLOGBufferPool(dir, EffectiveCLOGBuffers(...))`)
   and gate the live store on `c.pool != nil`. The no-`slruDir` path (only the
   `&CLog{}` unit tests) keeps `banks`. This makes the pool the in-memory store
   for the production path (Part B's deliverable) without a flat-file-backed pool
   variant; banks are removed only in Part C, after the no-mirror tests are
   migrated or dropped.
2. **Writes.** When `c.pool != nil`, `setStatus` writes the lane via
   `pool.setStatusWithLSN(xid, status, lsn)` and routes durability through
   `pool.flushDirty()` called from the group-commit leader, **replacing both**
   `flushDirtyPagesLocked` and `mirrorGroupToSLRULocked` in
   `applyGroupBatchLocked` (the pool already does batched per-segment fsync + the
   WAL barrier). Wire `pool.flushWAL = wal.Writer.FlushUpTo` here — this is the
   **join point with M0117-0007 Part B** (async commit); until then inject a
   barrier that flushes unconditionally (synchronous-commit semantics).
3. **OR-vs-clear-then-set (the load-bearing correctness point).** The M0117-0004
   hazard existed *because* two stores (banks + the separately-flushed mirror)
   could disagree and a whole-file flush could clobber a durable commit with a
   stale abort. With the pool as the **single** store there is no second store to
   disagree, so clear-then-set is correct and PG-faithful. **Must verify** the
   recovery sequence still holds: `loadFromSLRU` populates the pool, and
   `MarkUnknownAsAborted` keeps its "only `Unknown`→`Aborted`" guard (read via
   `pool.getStatus`) so it never clears a committed lane `loadFromSLRU` set.
4. **Reads.** `GetStatus` → `pool.getStatus` when `c.pool != nil`, *after* the
   CLog-layer short-circuits (`xid < FirstNormalTransactionID`, `OldestClogXid()`
   truncation floor) that the pool deliberately does not do.
5. **Bulk callers.** Re-point `InitializeAsCommitted` / `MarkUnknownAsAborted` /
   `HighestKnownXID` / `loadFromSLRU` onto the pool; prefer the SLRU as the
   authoritative load (`loadFromSLRU` already merges segments) and skip the
   flat-file `distributeToBanks` load when the pool is live.
6. **Truncation (resolves "truncation").** `TruncateCLOG` keeps
   `truncateSLRUSegments(cutoffPage)` but replaces the bank-drop with **pool page
   invalidation**: drop every `pool.slots`/`pageMap` entry whose `pageNo <
   cutoffPage` (faulted back in as all-zero/in-progress if ever re-read, which the
   `OldestClogXid` floor prevents).
7. **Flat file.** Part B may keep `global/pg_xact` written (redundant) or stop the
   flat-file replay; Part C removes `global/pg_xact` + `flushDirtyPagesLocked` +
   `markFlatDirty` + `distributeToBanks`.

### Mandatory gates (why this is a dedicated session, not an autonomous loop)

A store swap in CLOG is the project's highest-blast-radius change (silent
visibility/durability regression — Hard-won Rule #1). Validation **requires**:

- `go test -race ./internal/mvcc/... ./internal/wal/...` — the equivalence test
  (`TestCLOGBufferPoolEncodingMatchesSLRUMirror`) covers *encoding* only; it does
  **not** cover the durability ordering or the OR→clear-then-set reconciliation.
- crash-recovery replay (`internal/wal/xlog_replay_test.go`) — the
  `loadFromSLRU`/`MarkUnknownAsAborted` repair path.
- **heterogeneous PG-standby E2E** — a real PG standby reads the `pg_xact/` bytes
  the pool writes via `SimpleLruReadPage_ReadOnly`; this is the only check that
  the live SLRU byte stream + fsync ordering is standby-correct.
- fresh-server **TPC-H Q12/Q13 spot-check on a populated data dir** (SKIPs without
  data — not reliably available in the autonomous WSL2 loop) + pgbench smoke.
- re-init the data dir for the Part C on-disk-model change.

## Why land Part A alone

- The pack/unpack lane arithmetic, segment-file page I/O, and LRU eviction are
  the load-bearing, error-prone pieces; getting them right and unit-tested in
  isolation de-risks the swap.
- No live CLOG behaviour changes this slice (the pool has no caller yet), so the
  blast radius is nil — the change is purely additive and cannot regress
  visibility.
- The high-risk swap (Part B/C) can then land in a dedicated session that can run
  the full TPC-H Q12/Q13 spot-check and standby-visibility E2E, which SKIP under
  the worktree isolation this milestone uses to avoid the foreign M0100-0010 WIP.

## Tests (`internal/mvcc/clog_bufferpool_test.go`)

- `TestEffectiveCLOGBuffers` — GUC→budget resolution vs `CLOGShmemBuffers`
  (auto floor/cap/bank-rounding; explicit floor 16 / cap).
- `TestCLOGBufferPoolRoundTripAllLanes` — all four lanes round-trip; four XIDs
  packed in one byte do not clobber each other; durable after `flushDirty` +
  fresh-pool reopen.
- `TestCLOGBufferPoolIdempotentSet` — repeat set is a no-op; a different terminal
  status overwrites the lane (clear-then-set).
- `TestCLOGBufferPoolLRUEviction` — resident set never exceeds the budget; an
  evicted dirty page is written back and reloads correctly.
- `TestCLOGBufferPoolEncodingMatchesSLRUMirror` — **sibling-path equivalence**:
  the segment bytes the pool writes are byte-identical to `mirrorToSLRUUnlocked`
  for the same `(xid, status)` set.

## Gates run (Part A)

- `go build ./...` — PASS.
- `go test -race ./internal/mvcc/...` — PASS.
- `go test ./internal/config/...` — PASS (GUC + sample-config coverage).
- `go test ./internal/initdb/... ./internal/server/...` — PASS (GUC registry
  consumers).
- `gofmt -l` / `go vet` — clean.
- TPC-H spot-check SKIPs under worktree isolation (data dir is in the main tree);
  no-op here since no live CLOG path changed.

## Follow-ups

- M0117-0006 Part B/C (above) — recorded in `.ralph/fix_plan.md` (M0117-0006 stays
  unchecked) and `.ralph/deferral_ledger.md`.
- PENDING HUMAN MERGE of the stacked chain m0117-0001 → -0002 → -0003 → -0004 →
  -0005 → -0006, after reconciling the foreign M0100-0010 catalog WIP.
