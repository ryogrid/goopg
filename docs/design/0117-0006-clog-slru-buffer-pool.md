# 0117-0006 — CLOG SLRU buffer pool / 2-bit collapse (gap G6)

Status: **accepted (Part A landed; Part B/C deferred)**
Milestone: M0117-0006
Branch: `m0117-0006-clog-slru-buffer-pool` (off the M0117-0005 tip `5fcdb27b`)

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

### Part C — drop the resident banks + flat file (deferred)

Remove `clogBank`/`banks`/`flush`/`flushDirtyPagesLocked` once Part B routes all
access through the pool, completing the 2-bit collapse (16× memory reduction).

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
