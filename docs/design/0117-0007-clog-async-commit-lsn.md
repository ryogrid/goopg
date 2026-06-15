# 0117-0007 — CLOG async-commit LSN tracking (gap G8)

Status: **accepted (Part A landed; Part B deferred)**
Milestone: M0117-0007
Branch: `m0117-0007-clog-async-commit-lsn` (off the M0117-0006 tip `318f38c8`)

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

### Part B — live `synchronous_commit=off` activation (DEFERRED)

Wire `flushWAL` to the live WAL writer, thread the commit-record LSN from the
commit path into `setStatusWithLSN`, and add a real async-commit path that skips
the inline per-commit WAL fsync (relying on the page-write barrier instead).
This changes the live durability path and must land with the full TPC-H Q12/Q13
spot-check + crash-recovery / standby E2E — unavailable under the worktree
isolation this milestone uses to dodge the foreign M0100-0010 WIP. Deferred with
M0117-0006 Part B (the live pool swap), since the barrier only fires once the
pool is the live store. Resume point: set the hook in the CLog constructor that
owns the pool, pass the commit LSN through `groupUpdate`/`setStatus`, and gate
the inline fsync on `synchronous_commit`.

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

Gate: `go build ./...`; `go test -race ./internal/mvcc/...`; `go test
./internal/config/... ./internal/initdb/... ./internal/server/...`;
`TestE2E_PhysicalReplication{,Sync}`; gofmt/vet. TPC-H spot-check SKIPs under
worktree isolation (no live CLOG path changed — the pool and its LSN tracking
have no live caller yet).

## Status / merge

Stacked off `m0117-0006` (`318f38c8`); PENDING HUMAN MERGE of the chain
`m0117-0001 → -0002 → -0003 → -0004 → -0005 → -0006 → -0007`. Part B activation
lands in the dedicated full-gate session that wires M0117-0006 Part B.
