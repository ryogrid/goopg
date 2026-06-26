# 0118-0081 — `DROP TABLE` deferred to COMMIT + inheritance-child skip-if-vanished (M0118-0008 `alter-table-4` perm 3)

**Status:** accepted
**Milestone:** M0118-0008 (upstream isolation spec suite pass-through)
**Spec:** `postgres/src/test/isolation/specs/alter-table-4.spec` — *Add and remove inheritance with concurrent reads*
**Scope:** Enabler — **NOT a promotion.** Drives permutation 3 (`DROP TABLE c1`) to byte-for-byte vs PG 18.3 on top of 0118-0080 (perms 1 & 2). Perm 4 (`ALTER COLUMN a TYPE float`) remains deferred.

## The spec (perm 3)

```
permutation s1b s1dropc1 s2sel s1c s2sel
```

- `s1b` — `BEGIN`
- `s1dropc1` — `DROP TABLE c1` (c1 `INHERITS (p)`, holds one row a=10)
- `s2sel` — `SELECT SUM(a) FROM p` → **`<waiting ...>`**
- `s1c` — `COMMIT`
- `s2sel` — `<... completed>` → **sum = 1** (p's own row; c1 is gone)
- `s2sel` — `SELECT SUM(a) FROM p` → sum = 1

Spec comment: *"but we do cope with DROP on a child table."*

The load-bearing PG semantics, identical in spirit to perms 1 & 2 (0118-0080):

1. `DROP TABLE c1` takes `AccessExclusiveLock` on `c1` and the catalog removal is
   **invisible to other sessions until commit** (transactional DDL —
   `RangeVarCallbackForDropRelation` → `performDeletion` at commit).
2. `s2sel` expands `p`'s child set from its own snapshot — which **still includes
   `c1`** because the drop has not committed — *then* locks each child
   `AccessShare`, so it blocks on `s1`'s `AccessExclusiveLock` on `c1`
   (`<waiting>`).
3. After `s1` commits, `s2sel` resumes, acquires the lock, finds `c1` **gone**
   (`try_table_open` → `NULL` during inheritance expansion), and **skips** it —
   the sum excludes c1's row → **1**, not 11.

## What goopg did wrong

goopg serves inheritance membership from a single shared in-memory catalog and
`dropTableByRef` removed `c1` from the catalog **synchronously at statement time**
and took **no lock on the table being dropped**. So:

- `s2sel` planned `{p}` (c1 already gone) and returned **immediately** — no
  `<waiting ...>` marker, the first divergence after the 0118-0080 enabler
  advanced the first divergence to perm 3's `<waiting ...>` line (L43).

## The fix — two pieces, mirroring the deferred-DDL pattern

### (1) Defer `DROP TABLE` to COMMIT + `AccessExclusiveLock` (DDL side)

`dropTableByRef` (renamed body → `dropTableByRefImmediate`) now:

- Takes a **transaction-scoped `AccessExclusiveLock`** on the relation via
  `acquireDDLLockTxn` (PG `RangeVarCallbackForDropRelation`). A **no-op in
  autocommit** (`TxnLockBackendID == 0`, historical non-blocking behaviour);
  held to COMMIT/ROLLBACK inside an explicit transaction.
- When the caller passes `allowDefer` **and** the table is a **simple leaf**
  inside an explicit transaction — no partition children, not a partition child
  (`PartitionParentOID == 0`), no inheritance children, no pending-detach state
  (`DetachPendingEpoch == 0`), and no temp shadow — the catalog/relfile/WAL
  removal is **recorded** in `BasicSession.pendingTableDrops`
  (`PendingTableDrop{Name, Table, SavepointDepth}` + `Add`/`Take`/
  `CancelPendingTableDropsToDepth`) and **deferred to commit**.
- Otherwise (`allowDefer == false`, or a non-simple table) the removal stays
  **immediate** via `dropTableByRefImmediate`.

`allowDefer` is `true` only for the **top-level, non-CASCADE** drop
(`execDropTable`: `s.Behavior != parser.DropCascade`). Cascade/partition
recursion and DROP FOREIGN TABLE pass `false`, so dependent removal keeps its
immediate, ordered behaviour — minimising blast radius.

`ApplyPendingTableDrops(ctx, sess)` replays each deferred drop via
`dropTableByRefImmediate` at commit, invoked **BEFORE `TxnMgr.Commit`** on
**both** commit paths (executor `execCommit` *and* server simple-query dispatch
`TxCommit`, beside `ApplyPendingIndexDrops`/`ApplyPendingInheritanceChanges`). A
ROLLBACK discards the list via `EndExplicitTransaction`;
`rollbackToSavepointOp` cancels to depth via `CancelPendingTableDropsToDepth`.

### (2) Inheritance-child skip-if-vanished (SELECT side)

A `SeqScan` produced by expanding an inheritance parent into its children now
carries `SkipIfVanished = true` (planner `buildScan` inheritance branch). At
`seqScanOp.Open`, **after** the child's `AccessShare`/scan-read lock is acquired
(the point where `s2sel`'s wait completes), if `skipIfVanished` is set and the
child's OID is no longer in the catalog (`LookupTableByOID` → `!exists`), the
scan sets `nBlocks = 0` and returns — **zero rows**, mirroring PG's
`try_table_open` → `NULL`. Without this the post-commit scan would call
`NBlocks` on the dropped relfile and **O_CREATE-recreate it empty** (see the
smgr O_CREATE hazard), masking the drop. A **directly-scanned** relation never
sets `SkipIfVanished`, so a plain `SELECT` on a dropped table still errors
"does not exist".

Together: `s2sel` plans `{p, c1}` (c1 deferred-present), the c1 scan blocks on
`s1`'s lock (`<waiting>`), and after commit the c1 scan finds c1 gone and skips
it → `SUM = p(1) + c1(skipped) = 1`. The second `s2sel` either re-plans to `{p}`
or reuses a cached `{p, c1}` plan whose c1 scan skips — **either way sum = 1**.

## Known limitation (precedent: DROP INDEX 0118-0074)

goopg's shared catalog has **no per-session MVCC visibility**, so a deferred
`DROP TABLE` is invisible to the **dropping session itself** until commit, too —
`BEGIN; DROP TABLE t; SELECT * FROM t;` would still see `t` inside the same
transaction (PG would error). This matches the accepted DROP INDEX deferral
limitation; the spec never exercises it (s1 does not reference c1 after dropping
it). The full per-session MVCC catalog is the milestone-sized work that perm 4
and the partition-concurrent specs still need.

## Blast radius

Confined to **explicit-transaction, simple-leaf** `DROP TABLE`. Autocommit drops
(the overwhelmingly common case) are unchanged (lock is a no-op, removal
immediate). Cascade/partition/foreign-table drops are unchanged
(`allowDefer = false`). The new `SkipIfVanished` post-lock check fires only for
inheritance-child scans whose relation genuinely vanished — a no-op otherwise.

## Result

- Perm 3 now matches PG 18.3 byte-for-byte: `<waiting>` → 1 → 1. The first
  divergence advanced to **perm 4** (`ERROR: attribute "a" of relation "c1" does
  not match parent's type`).
- **Spec stays `defer`:** perm 4 needs the inheritance child scan to re-validate
  the child's column **types** against the parent **after** acquiring the
  post-commit lock (PG `make_inh_translation_list`) — a deferred/visible
  `ALTER COLUMN … TYPE` plus a new post-lock type-match check that raises
  `attribute … does not match parent's type`. Tracked in the deferral ledger.

## Gates

- Probe (`RunAndCompare` on alter-table-4): perm 3 byte-matches; divergence
  advanced from L43 (perm 3 `<waiting>`) to L65 (perm 4 ERROR).
- New unit `executor.TestPendingTableDropSession` (Add/Take/cancel-to-depth).
- No regression: `TestPort_IsolationAlterTable1`/`AlterTable3`/`InheritTemp`/
  `PartitionDropIndexLocking`/`DetachPartitionConcurrently1`/
  `PartitionConcurrentAttach`/`TruncateConflict`/`CreateTrigger` all PASS.
- `go test ./internal/{executor,catalog,planner,server}/` PASS; `-race` on
  DDL/tx/savepoint/commit/rollback PASS.
- `go build ./...` + gofmt clean (only pre-existing go1.25/1.26 alignment noise
  in untouched lines); pgbench smoke = pre-commit hook.

## Oracle

- `postgres/src/backend/commands/tablecmds.c` — `RangeVarCallbackForDropRelation`
  (AccessExclusiveLock on the dropped relation), `ExecDropStmt` → `performDeletion`.
- `postgres/src/backend/optimizer/util/inherit.c` — `expand_single_inheritance_child`
  / `try_table_open` returning NULL for a concurrently-dropped child.
