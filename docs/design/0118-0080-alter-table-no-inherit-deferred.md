# 0118-0080 — `ALTER TABLE … {NO} INHERIT` deferred to COMMIT + child lock (M0118-0008 `alter-table-4` enabler)

**Status:** accepted
**Milestone:** M0118-0008 (upstream isolation spec suite pass-through)
**Spec:** `postgres/src/test/isolation/specs/alter-table-4.spec` — *Add and remove inheritance with concurrent reads*
**Scope:** Enabler — **NOT a promotion.** Drives permutations 1 & 2 (NO INHERIT, NO INHERIT + INHERIT) to byte-for-byte vs PG 18.3; perms 3 (DROP child) & 4 (ALTER COLUMN TYPE) remain deferred.

## The spec

`alter-table-4` is the last DDL-concurrency spec of the M0118-0008 tail. Setup:
`p(a int)`, child `c1 INHERITS (p)`, standalone `c2(a int)`; one writer session
`s1` runs inheritance DDL inside an explicit transaction, one reader `s2` runs
`SELECT SUM(a) FROM p`. The four permutations:

| # | s1 steps (in a txn) | expected `s2sel` (1st) | expected `s2sel` (2nd, after commit) |
|---|---------------------|------------------------|--------------------------------------|
| 1 | `c1 NO INHERIT p` | `<waiting>` → **11** | 1 |
| 2 | `c1 NO INHERIT p`; `c2 INHERIT p` | `<waiting>` → **11** | 101 |
| 3 | `DROP TABLE c1` | `<waiting>` → **1** | 1 |
| 4 | `c1 NO INHERIT p`; `c1 ALTER COLUMN a TYPE float` | `<waiting>` → **ERROR** | 1 |

The load-bearing PG semantics (spec comment: *"NO INHERIT will not be visible to
concurrent select, since we identify children before locking them"*):

1. The inheritance DDL takes `AccessExclusiveLock` on the child and the catalog
   change is **invisible to other sessions until commit** (transactional DDL).
2. `s2sel` expands `p`'s child set from its **own** snapshot (still includes
   `c1`), **then** locks each child `AccessShare` — so it blocks on `s1`'s
   `AccessExclusiveLock` on `c1` (`<waiting>`).
3. After commit `s2sel` completes scanning the child set it already chose
   (perm 1/2: still scans `c1` → SUM includes `c1` = 11); the **next** `s2sel`
   re-plans against the committed catalog (`c1` gone → 1; `c2` now in → 101).

## What goopg had

`AlterTableInherit`/`AlterTableNoInherit` mutated the **shared** in-memory catalog
(`RegisterInheritanceChild`/`UnregisterInheritanceChild`) **synchronously at
statement time** and took **no lock**. So `s1delc1` immediately removed `c1` from
`p`'s child set; `s2sel` then planned `{p}` only → returned 1 with **no wait**
(probe: first divergence at the `<waiting ...>` marker, every permutation).

The SELECT side was already correct: the planner expands an inherited parent into a
`UNION ALL` of per-child `SeqScan`s (`collectInheritanceDescendants`), and each
`SeqScan.Open` already takes a transaction-scoped `AccessShare` via
`acquireScanReadLockTxn`. The only missing pieces were on the **DDL side**:
deferral + the child lock + plan-cache bypass.

## The fix (mirrors the deferred-DDL-until-commit pattern: DROP INDEX 0118-0074, ATTACH 0118-0077)

1. **Child lock.** Both `AlterTableInherit` and `AlterTableNoInherit` now take a
   transaction-scoped `AccessExclusiveLock` on the altered child via the existing
   `acquireDDLLockTxn` (mirrors PG `ATExecAddInherit`/`ATExecDropInherit`). No-op
   in autocommit (`TxnLockBackendID==0`) and for system catalogs.

2. **Deferred catalog mutation.** Inside an explicit transaction (gated by new
   `(*ddlOp).inheritDeferSession()`: explicit txn + `*catalog.InMemory`), the
   register/unregister (and the INHERIT column copy) is **recorded** rather than
   applied: `BasicSession.pendingInheritChng` holds
   `PendingInheritanceChange{ParentOID, ChildOID, NoInherit, SavepointDepth}`
   (with `Add`/`Take`/`CancelPendingInheritanceChangesToDepth`). The INHERIT
   validation (self / circular / duplicate) still runs immediately, as in PG.
   `ApplyPendingInheritanceChanges(ctx, sess)` performs the real mutation at
   commit — invoked **before** `TxnMgr.Commit` on **both** commit paths (executor
   `execCommit` *and* server dispatch `TxCommit`, beside `ApplyPendingIndexDrops`
   / `ApplyPendingPartitionAttaches`). `ProcessRollbackUndos` →
   `DiscardPendingInheritanceChanges` drops them on any ROLLBACK;
   `rollbackToSavepointOp` cancels entries recorded at the rolled-back depth.
   Autocommit applies immediately (unchanged).

3. **Plan-cache bypass.** A new `catalog.InMemory.pendingInheritanceChangeCount`
   (O(1) `MarkInheritanceChangePending`/`UnmarkInheritanceChangePending`/
   `HasPendingInheritanceChange`) is incremented when a change is deferred and
   decremented at apply/discard/savepoint-cancel. The server bypasses the
   cross-session plan cache while it is non-zero (new `inheritanceChangePending`
   helper wired into both the simple-query (`dispatch.go`) and extended
   (`dispatch_extended.go`) cache guards, beside `partitionDetachPending`). This
   stops `s2sel`'s 1st plan `{p, c1}` (built during the pending window) from being
   reused by the 2nd `s2sel` after commit — which must re-plan to `{p}` → 1.

### Why this yields perms 1 & 2

`s1delc1` records the deferred unregister + holds `AccessExclusiveLock` on `c1`.
`s2sel` plans `{p, c1}` (catalog still shows `c1` as a child), the `c1` `SeqScan`
blocks on `AccessShare` vs `s1`'s `AccessExclusiveLock` (`<waiting>`). After
`s1c` commits (applies the unregister, releases the lock) `s2sel` completes
scanning `{p, c1}` → 11. The 2nd `s2sel` re-plans (cache bypassed) against the
committed catalog → `{p}` = 1 (perm 1) / `{p, c2}` = 101 (perm 2, `c2 INHERIT`
also deferred so it appears only after commit).

## Blast radius

- The deferral and the child lock fire **only inside an explicit transaction over
  an InMemory catalog**. Autocommit `ALTER TABLE … {NO} INHERIT` (regress, normal
  usage, pg_dump/restore) is byte-identical to before.
- **Partitioning is untouched:** ATTACH/DETACH PARTITION use
  `RegisterPartitionChild`, a different catalog map and a different executor case;
  only table-inheritance `Register/UnregisterInheritanceChild` is deferred here.
- Known compromise (shared catalog, same as DROP INDEX 0118-0074): the altering
  session sees the OLD inheritance state for the rest of its own transaction. The
  spec never reads `p` from `s1` mid-transaction, so output is unaffected; a
  full per-session MVCC catalog (the milestone-sized work) would remove the
  compromise.

## Still deferred (perms 3 & 4) — ledger

Both need their own deferred-DDL + post-lock re-check, beyond this enabler:

- **Perm 3 (`DROP TABLE c1`):** DROP inside a txn must defer the catalog removal to
  commit + `AccessExclusiveLock` on `c1`, and after `s2sel` acquires the child lock
  it must detect `c1` was dropped and **skip** it (`try_relation_open` → NULL) so
  the SUM excludes it (→ 1). goopg removes the table from the catalog immediately
  and the inheritance scan would error on a vanished child rather than skip it.
- **Perm 4 (`ALTER COLUMN a TYPE float`):** defer the column-type change + after
  `s2sel` locks `c1`, re-validate the child column against the parent and raise
  `ERROR: attribute "a" of relation "c1" does not match parent's type`.

## Gates

- Probe (throwaway `RunAndCompare`): perms 1 & 2 now byte-match PG 18.3
  (`<waiting ...>` / `<... completed>` / SUM 11 then 1 / 101); first divergence
  advanced to perm 3.
- New units: `catalog.TestInheritanceChangePendingCounter` (counter Mark/Unmark +
  zero clamp); `executor.TestPendingInheritanceChangeSession` (Add/Take +
  savepoint-depth cancel count).
- No regression: `TestPort_Isolation{InheritTemp, DetachPartitionConcurrently1..4,
  PartitionConcurrentAttach, AlterTable1, AlterTable3, CreateTrigger,
  TruncateConflict}` all PASS; `go test ./internal/{catalog,executor,server}/`
  PASS; `-race` catalog + executor DDL/tx/savepoint/partition paths PASS.
- `go build ./...` + `go vet` clean; pgbench smoke = pre-commit hook.
