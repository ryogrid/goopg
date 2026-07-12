# wal-backend-flush — PG-parity backend-driven WAL write/flush (Design Bundle)

**Status: draft (design only — implementation not started; milestone TBD)**
**Date: 2026-07-12**

Restructures goopg's WAL write path to match PostgreSQL's: the **committing
backend goroutine itself performs the segment `pwrite` + `fdatasync`** under a
`WALWriteLock`-analog (leader/follower, emergent group commit), and the
**background walwriter becomes PG-equivalent** — it pre-*writes* completed
pages to the OS cache on its cadence and fsyncs only per
`wal_writer_flush_after` thresholds, delay-elapsed, or segment completion.
Today's model — one dedicated writer goroutine owning all WAL I/O, backends
parking on a per-commit channel hand-off, and a `commit_delay` sleep executing
on that shared goroutine where it stalls every queued flusher — is retired.

Motivated by `analysis/perf-optimize2/`: after fix-01 removed the 57 %-CPU
`runtime.Stack` storm, c=50 simple-update is commit-flush-bound
(1,145 TPS sync-on vs PG 15,556; block profile 73 % commit-wait; sync-off
9,820 proves the CPU headroom exists). The remaining gap is the commit-flush
architecture this bundle redesigns.

## Chapters

| doc | content |
|---|---|
| [00-overview.md](00-overview.md) | problem, measured evidence, goals/non-goals, decision log |
| [01-current-architecture.md](01-current-architecture.md) | verified map of today's writer-goroutine model, its latency pathologies, and the unsynchronized-state hazard |
| [02-postgres-reference.md](02-postgres-reference.md) | PG 18.3 oracle: `XLogFlush`/`XLogWrite`/`LWLockAcquireOrWait`/walwriter policy, with `postgres/src` citations |
| [03-target-design.md](03-target-design.md) | the redesign: `walWriteLock`, `xlogWrite(writeRqst)`, the new `FlushUpTo`, walwriter `BackgroundWrite`, deletion of `state.loop` |
| [04-concurrency-and-invariants.md](04-concurrency-and-invariants.md) | ownership migration, lock ordering, LSN invariants and publication order, drain-vs-append staging, crash safety |
| [05-migration-and-verification.md](05-migration-and-verification.md) | 7-slice staged migration with per-slice gates and the rollback story |
| [06-performance-model-and-observability.md](06-performance-model-and-observability.md) | honest performance expectations, new GUCs (PG defaults), wait-event attribution |

## Relationship to existing designs

- **Supersedes (on implementation):** `0098-0002-wal-group-commit.md` (the
  queue+writer-goroutine group commit), `0099-0002-wal-group-commit-batching-policy.md`
  (the hardcoded 1000 µs/5 batching sleep), `wal_fsync_flow_primary.md` §§ describing
  the loop-owned flush, and `0107-0007ai`'s single-drainer assumption (superseded to
  "the single drainer is the `writeMu` holder"). Status flips on those docs happen in
  the implementation slices, not in this docs-only change.
- **Builds on:** `perf-optimize/07-wal-fsm-insert.md` + the `0107-0007*` slice-B set
  (stripe insert locks, `insertPosTracker`, `walBuffer` atomic head/base,
  `stripeWriterCore`/`PublishUpTo`, `appendMu` RLock protocol `0107-0007ah`),
  `0013-0001` (WAL buffers), `0010-0002` (walsender memRing fed-on-append),
  `0007-0002` (fdatasync commit path).
- **Constraints:** on-disk WAL format unchanged (PG-compat invariants
  `0107-0001-m0106-pg-compat-invariants.md`).
- **Evidence base:** `analysis/perf-optimize2/` (runs 20260712_114859 and the
  before/after in `05-improvement-results.md`).

## Review log

Three agent reviews ran against the drafts; every finding was applied (none
rejected).

| date | reviewer lens | outcome |
|---|---|---|
| 2026-07-12 | goopg-code accuracy (01/03/04/05/06 vs internal/wal @ HEAD) | 2 MAJOR + 1 MINOR applied: (1) `wal_writer_flush_after` is NOT a new GUC — it is already registered (defaults.go, BootVal 1 MiB) and in the sample file, merely inert; reworded 00 D6/03 §3.4/05 slice-4/06 §6.3 to "wire the existing GUC". (2) commit-path order corrected: clog mark happens INSIDE the xactMarker hook, BEFORE the SyncRep wait (01 §1.2 diagram + §1.4). (3) walwriter-tick wording qualified with the fix-03 fast-exit no-op (01 §1.3). All other claims verified accurate. |
| 2026-07-12 | PG fidelity (02 + embedded PG claims vs postgres/src 18.3) | **Zero corrections** — every function name, line citation, mechanism, and (critically) GUC default (commit_delay 0, commit_siblings 5, wal_writer_flush_after 1 MB, wal_writer_delay 200 ms) verified correct against guc_tables.c and xlog.c/walwriter.c/lwlock.c/xact.c. |
| 2026-07-12 | concurrency correctness (adversarial) | 4 MAJOR + 8 MINOR applied. MAJOR: (M1) slice-3 lock-order self-contradiction (03 vs 04/05/00 — one reading killed emergent group commit, the other was an ABBA deadlock; also the "identical to today" justification was false since HEAD's drain already runs without appendMu) → resolved: flusher/walwriter `writeMu`-only from slice 3; slow paths nest `writeMu` inside `appendMu.Lock` covering the whole drain-coupled tail; slice 7 re-scoped to audit+stress. (M2) `publishTail` Load-then-Store corrupts the ring under multi-caller `PublishUpTo` → CAS-max conversion as a slice-1 hard precondition. (M3) legacy mode would spin forever in `waitInsertionsToFinish` → legacy frontier branch. (M4) a holder panic (recovered per-connection) would leak `writeMu` forever → mandatory panic-safe holder scope (deferred release + re-panic). MINOR m5–m12: sticky error epoch for loser termination; coverage-argument qualifiers (widen-under-lock is load-bearing; walwriter holds are non-covering generations); TryLock wording (starvation-mode handoff); Insert frontier = published tail, not writeLSNAtomic (+ xlogWrite target validation); Close/late-flusher recheck + BackgroundWrite has no done-escape (ticker-stop ordering is a hard invariant); const gating rules; spin-cost note on core.Load(); dropped the wrong AppendRaw "no concurrent flushers" parenthetical. **Verified holding under attack**: the missed-wakeup proof, waitInsertionsToFinish deadlock-freedom, clog/bufpool hook cycle-freedom, error-path idempotency, LSN publication order, eagerWG-under-Close. Verdict after fixes: sound to commit as draft. |
