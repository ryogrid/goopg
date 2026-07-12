# C2 — Remove the commit-path CLOG (pg_xact) fsync

status: design · date: 2026-07-13 · base: goopg `e453e3f2` · depends on:
nothing (recommended first on multi-ms-fsync hosts; see README X1) · gates:
see [README](README.md#common-gates-referenced-as-g--by-every-slice-table)

## 1. Problem and numbers

During a 60 s pgbench `-N` run, goopg issued **6,734 plain `fsync` calls on
pg_xact segments — ~1 per 11 commits (≈ every other WAL group) — averaging
6.29 ms (42.4 s cumulative)**, competing with WAL `fdatasync` on the same
device queue (`../01-results.md` AUX). The block profile attributes **32.8 %
of all block delay** to the lock held across that fsync. PostgreSQL performs
**zero** SLRU I/O on the commit path.

**Storage dependence** (`../02` M2): this win is proportional to the device's
fsync floor. On this WSL2 host the floor is multi-ms; on power-loss-protected
NVMe it approaches zero. Verify the floor on the target host before
sequencing C2 first.

## 2. Current-code map (verified at `e453e3f2`)

**Sync-commit flow**: `open.go:993` `clog.SetCommitted(xid)` →
`setStatus` (clog.go:485) → `setStatusWithLSN(xid, status, **0**)` (clog.go:499)
→ pool write marks the page dirty (`clogBufferPool.setStatusWithLSN`,
clog_bufferpool.go:328) → since `lsn==0` and the lane changed:
`groupUpdate` (clog_groupcommit.go:42, Treiber-stack enqueue) → leader
`runLeader` (:70) holding `flushMu` → `applyGroupBatchLocked` (:106) →
**`clogBufferPool.flushDirty` (clog_bufferpool.go:420-467)**:

1. under **`clogBufferPool.mu`** (held for the whole body): bucket dirty pages
   by segment; compute max group LSN;
2. `flushWALBeforeWriteLocked(maxLSN)` (:439) — **no-op for sync commits**
   because they recorded `lsn=0`;
3. per segment: open, `WriteAt` each dirty page, **`f.Sync()`** (:455) — the
   measured 6.3 ms — close, clear dirty bits.

Followers park on a buffered channel, woken after the leader releases
(:92-96). **The contended lock is `clogBufferPool.mu`** (every `getStatus` /
`setStatusWithLSN` serializes behind the in-flight fsync), not `flushMu`.

**Async path (the template)**: `SetCommittedWithLSN` (clog.go:195) →
`setStatusWithLSN(lsn≠0)` records the commit-record LSN in the page's
`groupLSN` array (clog_bufferpool.go:348-354) and **returns without flushing**
(clog.go:516-534 early-return with the "skip the eager durable write-back"
comment). Durability then rides:

- **eviction**: `pinPageLocked` (:227-237) → barrier
  `flushWALBeforeWriteLocked(maxGroupLSNLocked(idx))` (:231) →
  `writePageToDisk` (:273-288, write + fsync);
- **checkpoint**: `FlushCLOGFn` (`internal/wal/checkpointer.go:481-492`,
  wired to `clog.FlushAll` at open.go:1662) — runs in the dirty-flush phase
  **before the redo LSN is sampled** (:508-523), and an error **fails the
  whole checkpoint** — the invariant "pg_xact on disk covers everything before
  redo";
- **recovery**: `replayCLogFromWAL` (`internal/initdb/xact_recovery.go`) runs
  on **every startup** (open.go:1466-1475), replaying BOTH native
  (`RecordKindXactCommit/Abort/CommitInval`) and canonical (`RmgrXact`)
  records from the last checkpoint's `replayStart` (recovery.go:10261),
  re-stamping CLOG (`SetCommitted`/`SetAborted`) and advancing nextXID.
  **`MarkUnknownAsAborted` runs before it** (open.go:1057 vs :1467) — unknown
  lanes go aborted first, then WAL commit records override to committed. This
  ordering is load-bearing.

**The direct exemplar test**:
`TestReplayCLogFromWAL_RecoversUnflushedAsyncCommit`
(`internal/initdb/xact_recovery_test.go:133-195`) — a commit never flushed to
pg_xact is reconstructed purely from its WAL record on restart. C2 = make
sync commits take exactly this path.

**pg_subtrans**: a separate SLRU whose `SetParent` fsyncs **on every call**
(`internal/mvcc/subxact_slru.go:124`) at subxact registration — an independent
per-write fsync cost, **out of C2 scope** (deferral-ledger row in S4).

Misleading comment to not be fooled by: `internal/wal/recovery.go:68`
("crash-recovery is a no-op for [XactCommit]") describes the *first* replay
pass only; the CLOG re-stamp is the second pass above.

## 3. PostgreSQL reference

- `access/transam/clog.c` `TransactionIdSetPageStatus` /
  `TransactionIdSetStatusBit`: sets two bits in the SLRU shared buffer under
  the bank lock — **no I/O**.
- `access/transam/slru.c` `SlruPhysicalWritePage`: called at checkpoint
  (`CheckPointCLOG` → `SimpleLruWriteAll`) or page eviction; performs
  `XLogFlush(max_lsn)` **before** writing the page — the barrier goopg's
  `flushWALBeforeWriteLocked` mirrors.
- `access/transam/xact.c` `RecordTransactionCommit`: commit durability is the
  WAL flush of the commit record; CLOG bits are set after, in memory. Recovery
  (`xact_redo_commit`) re-stamps CLOG from the WAL record.

## 4. Target design

Synchronous commits stop performing the eager durable write-back. The CLOG
page is updated in the buffer pool, tagged with the **commit record's LSN**,
and left dirty — identical to today's async path. Durability moves entirely to
the three existing mechanisms (eviction / checkpoint / WAL replay).

Concretely:

1. The sync path routes through the `SetCommittedWithLSN` plumbing with the
   real commit-record end-LSN instead of `SetCommitted`'s `lsn=0`
   (`open.go:972-996` already has `endLSN` in hand for the WAL flush — pass it
   through in the `waitLocalFlush` branch too).
2. `applyGroupBatchLocked` no longer calls `flushDirty` (the group machinery
   may shrink or disappear — see D1); pages stay dirty.
3. Eviction and checkpoint paths are **unchanged** — their barrier + fsync now
   simply cover sync-commit pages too, because those pages carry LSNs.

### The `ErrLSNNotWritten` swallow must become fatal (adversarial F3)

`open.go:982-985` treats `ErrLSNNotWritten` from the commit-record
`FlushUpTo` as **non-fatal** — the txn is acked and `SetCommitted*` runs — on
the rationale that the record "will be persisted by the next checkpoint or
explicit flush." Today that rationale is underwritten by the very fsync C2
deletes: the eager pg_xact write-back makes the commit status durable even
when the WAL flush was skipped. After S3, durability rides entirely on a WAL
record whose flush was just swallowed → crash before the next checkpoint loses
an **acked** commit, and `replayCLogFromWAL` cannot reconstruct it
(`MarkUnknownAsAborted` leaves it aborted; no durable record to override).
**S3 therefore makes `ErrLSNNotWritten` on the sync-commit path fatal (abort
the txn / do not ack) or forces a real flush**, with a fault-injection test:
sync commit whose flush returns `ErrLSNNotWritten` → SIGKILL before checkpoint
→ restart → the row must NOT be visible (or the ack must never have been
sent).

### Decision log

| # | decision | rationale |
|---|---|---|
| D1 | **Skip the write-back entirely** at commit (not "write without fsync"). | One dirty-tracking discipline (pool dirty bit + checkpoint/eviction), no half-durable middle state to reason about; matches PG exactly. |
| D2 | **Sync-commit LSN association is a REQUIRED companion, not an optimization.** | Without it, a sync page written back later (eviction/checkpoint) skips the `XLogFlush`-before-SLRU-write barrier (`maxGroupLSNLocked`=0) — pg_xact on disk could then advertise a commit whose WAL record is not durable, breaking PG's ordering rule. For sync commits the WAL record is already durable at ack time, so the barrier will fast-exit via `FlushUpTo`'s `flushedLSNAtomic` check — cheap, but it must be armed. (Exact durable-point audit: O-C2-1.) |
| D3 | **pg_subtrans per-write fsync is out of scope** — deferral-ledger row. | Different SLRU, different call site (registration, not commit); bundling would widen blast radius. |
| D4 | Keep `flushMu`/group machinery only if something still needs cross-commit batching after the cut; otherwise delete in S4. | The group existed to amortize the fsync; with no commit-path I/O the leader has nothing durable to batch. Status-bit writes are already individually cheap under `clogBufferPool.mu`. |

## 5. Invariants and failure modes

- **I1 (commit ack order)**: WAL commit record durable **before** client ack —
  unchanged (the WAL `FlushUpTo` in the xact-marker logger precedes
  `SetCommitted*`); C2 removes a *later* redundant durability, not this one.
- **I2 (SLRU write barrier)**: no CLOG page reaches disk before the WAL up to
  its max group LSN is flushed — preserved by D2 + the untouched
  eviction/checkpoint barrier calls.
- **I3 (checkpoint ordering)**: README X6 rev 2 — today CLOG flushes before
  the redo sample; after C1-S3 moves to redo-published-first, the flush covers
  everything ≤ the published redo a fortiori. Either order satisfies C2's
  "pg_xact on disk covers pre-redo"; what C2 must never weaken is
  `FlushCLOGFn`'s error-fails-checkpoint contract.
- **I4 (recovery ordering)**: `MarkUnknownAsAborted` before
  `replayCLogFromWAL` (unknown→aborted, then WAL overrides to committed).
  Add a regression test pinning the order (S3).
- **F1 (crash with unflushed sync commits)**: bits lost from the pool are
  reconstructed by `replayCLogFromWAL` from post-redo WAL commit records —
  the async exemplar test proves the mechanism; S3 adds the sync sibling.
- **F2 (crash during checkpoint)**: checkpoint fails before advancing redo if
  the CLOG flush fails; the previous checkpoint's redo still covers
  reconstruction. Unchanged from today.
- **F3 (dirty accumulation)**: with `checkpoint_timeout=24h` bench configs,
  dirty CLOG pages accumulate between checkpoints (~1 page / 32k XIDs ⇒ at
  15 k TPS ≈ one new dirty page every ~2 s; pool eviction bounds residency —
  audit pool sizing, O-C2-2) and the checkpoint flush becomes a burst —
  measure in S4.
- **F4 (readers)**: live readers are unaffected (pool hit sees memory; a
  refault after eviction reads post-write-back bytes). Offline byte-readers
  (basebackup pg_xact copy, pg_resetwal slru bounds) see checkpoint-fresh
  bytes — same as PG; the clean-shutdown checkpoint keeps
  `pgresetwal_port_test.go` green.

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| **S1** | Test refit (behavior-preserving) | Enumerate + refit the eager-disk-bytes tests (§7) to call `clog.FlushAll()` before disk-read assertions (keeps their byte-layout oracle value; add the accessor if unexported where needed). Zero production diff. | G-unit green with production untouched |
| **S2** | Sync-commit LSN association (behavior-preserving) | `open.go` xact-marker logger passes `endLSN` on the sync branch too (via the `SetCommittedWithLSN` plumbing); eager flush **still on**. `TestCLogSetCommittedNoLSNNeverFiresBarrier` renamed/inverted (the barrier is now armed but fast-exits on already-durable LSNs — assert that). New test: barrier fast-exit for durable sync LSNs. | G-race, G-crash, refitted clog suites |
| **S3** | **The cut** | `applyGroupBatchLocked` stops calling `flushDirty` (or the sync path stops entering the group entirely, per D4 findings); **`ErrLSNNotWritten` on the sync path becomes fatal** (see §4 above) with its fault-injection kill-9 test. New kill-9 test cloned from `TestReplayCLogFromWAL_RecoversUnflushedAsyncCommit`: sync commit → ack → SIGKILL before any checkpoint → restart → row visible. New ordering test for I4. | G-crash (incl. new tests), G-race, G-unit, smoke; **G-perf** (aux2: commit-path pg_xact fsyncs → ~0; END latency) |
| **S4** | Cleanup + pressure check | Delete dead eager-flush plumbing (+`flushMu`/group if D4 says so); the pg_subtrans deferral-ledger row (drafted below); measure dirty-CLOG accumulation and the checkpoint fsync burst under a long run; document pool-eviction bounds (O-C2-2); **note**: post-C2 the eviction barrier fires for sync pages under `clogBufferPool.mu` — a WAL `FlushUpTo` inside the pool mutex serializes `getStatus` behind it (no deadlock — verified lock ordering — but a contention source to watch). | full gate set + G-perf re-measure (repeat after C1-S6a lands — README X2) |

Draft ledger row for S4 (copy into `.ralph/deferral_ledger.md`, matching its
7-column format):

> `| - | <date> | C2 clog-commit-fsync | Commit-path pg_xact fsync removed
> (applyGroupBatchLocked no longer calls flushDirty); durability =
> checkpoint FlushCLOGFn + eviction writePageToDisk + replayCLogFromWAL. |
> pg_subtrans SetParent still fsyncs on EVERY call
> (internal/mvcc/subxact_slru.go:124) — independent per-write fsync on the
> subxact registration path. | subxact_slru.go:124; apply the same
> defer-to-checkpoint/eviction discipline with an LSN barrier. | Separate
> SLRU + call site; bundling would have widened C2's blast radius. |`

## 7. Test-impact matrix

| test | impact |
|---|---|
| `pg_xact_slru_test.go` (**lives in `internal/initdb/`**, not `internal/mvcc/`): `TestCLog_SLRUMirror_StatusBitLayout` (:104), `_ExtendsSegmentFile` (:155), `_SegmentRollover` (:186) | S1: add `FlushAll()` before `os.ReadFile` assertions |
| `clog_dual_store_consistency_test.go`: `TestCLogDualStoreConsistency` (:140), `TestCLogSubCommittedResolvesViaParent` (:224), `TestCLogTruncateKeepsStoresConsistent` (:315) | S1: `FlushAll()` before `freshFromSLRU` disk reads |
| `clog_asynccommit_test.go`: `TestCLogSetCommittedWithLSNDefersFlush` (:31) | unchanged (C2 generalizes exactly this) |
| `clog_asynccommit_test.go`: `TestCLogSetCommittedNoLSNNeverFiresBarrier` (:99) | S2: rename/invert — sync now carries an LSN |
| `xact_recovery_test.go` (all `TestReplayCLogFromWAL_*`) | unchanged; S3 adds the sync-commit sibling |
| `checkpointer_test.go`: `TestCheckpointerCallsFlushCLOGFn` (:911), `…ErrorFailsCheckpoint` (:948) | unchanged — they pin the contract C2 depends on |
| `clog_bufferpool_lsn_test.go` (`TestFlushWALBarrierFiresBeforeWrite` :136), `clog_slru_recovery_test.go` | S2/S3: extend for sync-LSN pages |
| `clog_test.go:296` `TestCLogMarkUnknownAsAbortedBatchedSLRU` (SetCommitted then disk read) | S1 refit: `FlushAll()` before the disk read |
| `clog_groupcommit_test.go:109` `TestGroupCommitConcurrent` "View 3: SLRU-only reconstruction" (disk read, **no FlushAll today**) | S1 refit + S3: group tests shrink with D4 |
| `clog_bufferpool_test.go:174` equivalence oracle (`mirrorToSLRUUnlocked`) | unchanged — constrains byte layout, not flush timing |
| kill-9 matrices (`clog_crash_test.go`, `TestKillKillRecovery`) | must stay green throughout; S3's new test joins them |

## 8. Performance verification

- aux2 strace signature: plain `fsync` count during `-N` drops from ~6,700/min
  to ~0 on the commit path (eviction/checkpoint fsyncs remain, rare).
- `run_rw50.sh`: END latency reduction (this host: removes an amortized
  ~3.5 ms/group serialized wait + unloads the shared disk queue, so WAL
  `fdatasync` average should also drop from 3.8 ms toward the device floor);
  record before/after with commit hashes.
- Checkpoint burst: time `FlushCLOGFn` during a checkpoint after a long run
  (S4); confirm no latency cliff.

## 9. Open questions (flagged, not resolved)

- **O-C2-1**: pin down the exact statement in the sync path where the commit
  record is durable relative to `SetCommitted*` (audit `open.go:962-1001`
  ordering) — needed for the D2 fast-exit claim and the I1 statement.
- **O-C2-2**: clogBufferPool sizing/eviction policy — if residency is
  effectively unbounded for bench-sized XID ranges, checkpoint is the sole
  flush point; bound the burst and document pool limits.
- **O-C2-3**: two-phase commit / prepared transactions
  (`twophase_commit_test.go`): does PREPARE/COMMIT PREPARED take a separate
  eager CLOG flush that also needs the treatment?
- **O-C2-4**: exact async-vs-sync divergence inside `applyGroupBatchLocked`
  today (does any async commit ever reach the leader's flush?) — determines
  whether S3 removes the call or the enqueue.
