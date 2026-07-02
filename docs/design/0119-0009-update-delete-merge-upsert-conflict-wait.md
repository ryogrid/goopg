# 0119-0009 — UPDATE/DELETE/MERGE/upsert conflict-wait sibling-path parity

Status: accepted
Date: 2026-07-01 (loop #46)

## Problem

The M0118-0004 closure note (ledger row, loop #44) promoted "UPDATE/DELETE
conflict-wait on a conflicting lock-only locker" to its own open item,
M0119-0009, describing the gap as: `stampUpdaterXmaxPreservingLockers`
(`internal/executor/operators_storage.go`) only *preserves* a pre-existing
**non-conflicting** lock-only locker into a `{updater + survivors}`
MultiXactId; a **conflicting** locker is silently dropped by the stamp
instead of being waited for, unlike upstream `heap_update`/`heap_delete`
(`heapam.c`), which call `MultiXactIdWait` on a conflicting lock-only xmax
before proceeding.

Investigation (this loop) found the picture is more nuanced than the ledger
row implied:

1. **`waitForConflictingRowLock`/`conflictingRowLockHolders` already exist**
   (`operators_storage.go`, added by M0118-0003, commit `815c42d0`,
   2026-06-21 — *before* the M0118-0004 ledger row was written) and correctly
   implement the conflict-aware wait: they resolve a lock-only xmax (single
   or MultiXact) to its still-active members, filter by
   `multixact.StatusesConflict`, and `epqWait` each conflicting holder
   (registers a wait-for-graph edge so a cycle surfaces as `40001`, not a
   hang) before returning.
2. This wait **is** wired at the three canonical write sites:
   `updateViaIndex` (index-driven UPDATE), `updateOp.Next`'s seqscan branch,
   and `deleteOp.Next` — each calls it once per pending row, before the
   HOT/non-HOT stamp attempt.
3. It was **never wired** at five sibling sites that call the same
   `stampUpdaterXmaxNonHOT`/`stampUpdaterXmaxPreservingLockers` producer:
   - `updateWithFrom` (`UPDATE ... FROM`)
   - `deleteWithUsing` (`DELETE ... USING`)
   - `mergeApplyUpdate` / `mergeApplyDelete` (`MERGE`, `operators_merge.go`)
   - `upsertOp.applyUpdate` (`INSERT ... ON CONFLICT DO UPDATE`,
     `operators_upsert.go`)

   These five sites went straight from "no concurrent updater" (an
   `isConcurrentlyUpdated`/EPQ check that only detects a genuine concurrent
   *updater*, not a lock-only *locker*) to the stamp, so a still-active
   conflicting lock-only holder was silently dropped by
   `stampUpdaterXmaxPreservingLockers`'s conflict filter exactly as the
   ledger row described — just only for these five, not universally.

4. **A separate, older, coarser mechanism already exists for the scan-based
   sites.** `scanMatching` (the shared row-scan helper used by
   `updateOp.Next`'s seqscan branch, `deleteOp.Next`, `updateWithFrom`, and
   `deleteWithUsing`) has its own M0021-era block: any row whose xmax is a
   **foreign lock-only holder of any strength** (`lockedByForeign`, not
   conflict-aware) makes the scan call `ctx.acquireTupleLock` — a genuine
   blocking lockmgr `Acquire` on the tuple tag — before the row is even
   collected into `pending`/`victims`. This means for the four
   `scanMatching`-based sites (including the two sibling gaps closed here,
   `updateWithFrom`/`deleteWithUsing`), a same-row conflict is normally
   caught during the scan already; `waitForConflictingRowLock` at the stamp
   is the narrower second gate that additionally protects the
   scan-completed-then-stamp race window (Step 2 collects all matching rows
   across possibly many blocks before Step 3 applies them; a fresh
   conflicting lock acquired in that window would slip past the scan-time
   check) and is MultiXact-precise where the scan-time check is not
   (`lockedByForeign` doesn't distinguish lock strength at all). `MERGE`
   and upsert's `applyUpdate` do **not** use `scanMatching` (they resolve a
   single target row directly via join-match / arbiter-probe), so for them
   `waitForConflictingRowLock` is the *only* gate — confirmed empirically
   (see Verification).

## Change

Wire `waitForConflictingRowLock` at all five gap sites, immediately before
the point each already computes/uses the `stampUpdaterXmaxNonHOT` keysUpdated
boolean (so the `reqStatus` classification matches exactly what the stamp
call already uses — no new classification logic):

- `updateWithFrom` (`operators_storage.go`): new `updReqStatusFrom` computed
  once from `hotEligible` (mirrors `updateViaIndex`'s `updReqStatus`), wait
  call inserted right after `seen[key]=true` and before firing the BEFORE
  UPDATE trigger — before any buffer is pinned, avoiding the pre-existing
  pin/lock reuse further down in the function.
- `deleteWithUsing` (`operators_storage.go`): `multixact.StatusUpdate`
  (DELETE always conflicts with every lock strength), inserted before firing
  the BEFORE DELETE trigger.
- `mergeApplyUpdate`/`mergeApplyDelete` (`operators_merge.go`):
  `multixact.StatusUpdate` (MERGE's producer call already hardcodes
  `keysUpdated=true`, i.e. it doesn't yet distinguish key vs non-key SET
  columns — a separate, unrelated, pre-existing simplification), inserted
  right after the "no concurrent update, safe to write" unlock/unpin and
  before firing the BEFORE trigger.
- `upsertOp.applyUpdate` (`operators_upsert.go`): reqStatus mirrors the
  existing `o.onConflictUpdateTouchesKeyColumn()` boolean already passed to
  the stamp call, inserted at the top of the function before
  `MaterializeWriterXID`/pin.

No new primitive was needed — this is purely a wiring change reusing the
M0118-0003 helper, matching design 0118-0011's own "Wiring" section, which
already enumerated all five of these as sibling sites for
`stampUpdaterXmaxPreservingLockers` but only wired the producer there, not
the pre-stamp wait.

## Verification

New tests (`internal/executor/merge_upsert_conflict_wait_test.go`,
`internal/executor/update_from_delete_using_conflict_wait_test.go`), each a
two-session harness mirroring `TestUpdateBlocksOnForeignTupleLock`: session 1
holds a conflicting `SELECT ... FOR {UPDATE|SHARE}` on a row, session 2
attempts the write in a goroutine, the test asserts session 2 is still
blocked after 300ms and unblocks within 2s of session 1 releasing.

- `TestMergeApplyUpdateWaitsOnForeignConflictingLock`,
  `TestMergeApplyDeleteWaitsOnForeignConflictingLock`: call
  `mergeApplyUpdate`/`mergeApplyDelete` directly (bypassing MERGE's SQL
  grammar/join logic, which is not under test). **Confirmed RED→GREEN**:
  reverting the production fix (`git stash` on `operators_merge.go` alone)
  makes both fail immediately (`mergeApplyUpdate returned early`) — proving
  these two sites had no other blocking mechanism and the fix is load-bearing.
- `TestUpsertOnConflictDoUpdateWaitsOnForeignConflictingLock`: full SQL path
  (`INSERT ... ON CONFLICT (id) DO UPDATE`). **Passes with or without the
  fix** — upsert's own arbiter-conflict-detection scan
  (`detectArbiterConflict`'s "Case 3" logic, pre-existing, not touched this
  loop) already waits on a conflicting lock-only holder *before* the arbiter
  even resolves to a `conflictPtr`, so by the time `applyUpdate` runs no
  conflicting locker remains on the *normal* key-probe arbiter path. The new
  wait in `applyUpdate` is still real protection for the **NULLS NOT
  DISTINCT arbiter path** (`probeArbiterNND`/`checkNullsNotDistinctViaHeapScan`,
  M0119-0004 slice 415-ish work), which does a plain heap scan with **no**
  wait logic at all — not covered by a dedicated test this loop (ledgered
  below).
- `TestUpdateFromBlocksOnForeignConflictingLock`,
  `TestDeleteUsingBlocksOnForeignConflictingLock`: full SQL path. **Pass
  with or without the fix** — `scanMatching`'s scan-time lockmgr block (see
  Problem §4) already blocks the single-row two-session scenario these tests
  construct; they are valid regression/smoke coverage of the SQL surface
  (and needed `lm.ReleaseAll(1)`, not just `ctx.TxnMgr.Rollback`, to unblock
  — the scan-time gate is a lockmgr Acquire, not an xact wait) but do not
  discriminate the specific Step-2/Step-3 race window this loop's fix
  additionally closes for these two sites.

Gates: `go build ./...`/`go vet ./...` clean; race batch
(`-race ./internal/executor/... -run 'Multixact|Tuplelock|LockUpdate|
UpdateLocked|PropagateLock|LockCommitted|EvalPlanQual|SkipLocked|Nowait|
Merge|StampUpdater|HOTUpdate|Upsert|ConflictingRowLock|BlocksOnForeign|
ConflictWait'`) PASS; full `internal/executor` suite PASS; `-race
internal/mvcc`+`internal/multixact`+`internal/wal` PASS; `internal/catalog`+
`internal/planner`+`internal/server` PASS; `TestPort_PgDumpConnectionSetup`
PASS (unaffected — no pg_dump-visible behaviour changed); TPC-H spotcheck
Q12/Q13 PASS; pgbench smoke = pre-commit hook.

## Deferred / not this loop

- The narrow Step-2/Step-3 race window for `updateWithFrom`/
  `deleteWithUsing` (a fresh conflicting lock acquired after `scanMatching`'s
  block clears but before Step 3's stamp) is now closed by this loop's fix
  but has no dedicated regression test — proving it requires a third
  session with very precise timing between Step 2 completing and Step 3
  starting; not attempted this loop (high effort, narrow payoff, and the
  fix itself is the same reviewed, tested pattern as the other four sites).
- `upsertOp.applyUpdate`'s NULLS NOT DISTINCT arbiter path
  (`probeArbiterNND`) has no conflicting-lock wait of its own before
  resolving `conflictPtr` (unlike the normal key-probe arbiter's Case 3
  logic) — this loop's `applyUpdate`-level wait is a correct but coarser
  fix (waits at the stamp, not during the NND scan itself, so a concurrent
  NND-arbiter probe could still observe a stale row during the scan) and
  has no dedicated test. Low priority: no isolation spec or fixture in
  scope exercises a locked row under an NND arbiter today.
- `scanMatching`'s M0021-era `lockedByForeign` block (Problem §4) is
  **not conflict-aware** — it blocks a no-key UPDATE on a non-conflicting
  `FOR KEY SHARE` locker exactly as hard as a key-changing UPDATE, which is
  more conservative than upstream `heap_update`'s `HEAP_XMAX_IS_LOCKED_ONLY`
  fast path and than this project's own `stampUpdaterXmaxPreservingLockers`
  design intent (0118-0011). No spec in the current inventory exercises a
  no-key UPDATE via a scan path (the only passing perms for this scenario,
  `tuplelock-upgrade-no-deadlock` perms 2/3, route through `updateViaIndex`,
  which does not call `scanMatching`), so this is not a regression, but it
  is a real latent over-blocking gap for a future seqscan-driven variant of
  that scenario. Resume: make `scanMatching`'s row-visibility filter call
  `multixact.StatusesConflict` (needs the write's `reqStatus` threaded into
  `scanMatching`'s signature, which today only takes `pred`) instead of the
  unconditional `lockedByForeign`.

## Oracle

`postgres/src/backend/access/heap/heapam.c`: `heap_update`/`heap_delete`'s
`HEAP_XMAX_IS_LOCKED_ONLY` branch calls `MultiXactIdWait`/
`XactLockTableWait` before proceeding; `DoesMultiXactIdConflict`/
`get_mxact_status_for_lock` drive the per-member conflict filter mirrored by
`multixact.StatusesConflict`.
