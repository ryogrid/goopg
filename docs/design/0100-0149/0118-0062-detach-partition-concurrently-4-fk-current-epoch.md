# 0118-0062 — `detach-partition-concurrently-4` (partial): FK existence checks observe the CURRENT detach epoch, not the txn snapshot

**Milestone:** M0118-0008 (Upstream Isolation Spec Suite Pass-Through)
**Status:** accepted (partial — FK behaviour landed; cursor behaviour deferred)
**Spec:** `postgres/src/test/isolation/specs/detach-partition-concurrently-4.spec` (20 permutations)
**Test:** probed via `RunAndCompare` (not yet `runIsoSpecStrict` — see *Deferred*)
**Oracle:** PostgreSQL 18.3 `src/backend/utils/adt/ri_triggers.c`
(`RI_FKey_check` runs its existence query under the latest snapshot),
`src/backend/commands/tablecmds.c` (`MarkInheritDetached` /
`ATExecDetachPartition`).

## Problem

`detach-partition-concurrently-4` exercises foreign keys in the face of a
concurrent `DETACH PARTITION … CONCURRENTLY` of a partition on the **referenced**
side. The spec's first family of permutations asserts:

> Trying to insert into a partially detached partition is rejected … **even under
> REPEATABLE READ mode.**

Concretely, `d4_primary` is partitioned by list on `a` with `d4_primary1` holding
value `1`; `d4_fk(a)` references `d4_primary`. While `d4_primary1` is in the
*detach-pending* state (a `DETACH … CONCURRENTLY` that was marked then cancelled,
or that is still waiting), inserting `1` into `d4_fk` must fail its FK check —
the value lives only in the detaching partition, which is invisible to the RI
existence query.

PostgreSQL fails this **regardless of the enforcing transaction's isolation
level**: the RI trigger query runs under a *current* snapshot, so the moment the
detach is marked, the partition is omitted from the FK existence scan. This is
deliberately decoupled from the writing transaction's own MVCC snapshot — under
REPEATABLE READ a plain `SELECT * FROM d4_primary` (or a cursor opened before the
detach) **still sees** the row `1`, yet the FK check does not. The spec comment
("you can see a row that the RI query does not see") is exactly this asymmetry.

### goopg's bug

Since design 0118-0060 (detach-partition-concurrently-2) goopg filtered
detach-pending partitions out of the FK existence scan (`allDescendants`) using
the **enforcing statement's snapshot epoch** (`ctx.Snap.PartitionDetachEpoch`).
For READ COMMITTED that happens to be correct, because each statement refreshes
its snapshot to the current epoch. But under REPEATABLE READ the transaction's
snapshot predates the detach, so `snapEpoch < DetachPendingEpoch`, the partition
was **not** filtered, the FK scan found value `1`, and the insert wrongly
succeeded — diverging from PG at the first RR permutation.

## Change

`internal/executor/operators_fk.go` — `snapDetachEpoch` now returns the **global
current** partition-detach epoch (`mvcc.CurrentPartitionDetachEpoch()`) instead of
the per-statement snapshot epoch. It is consumed only by the two FK existence
scans (`scanTableForMatch` and `scanTableForMatchFKWait`, both via
`allDescendants`), so the change is scoped precisely to referential-integrity
checks. Ordinary query / cursor partition expansion is untouched: the planner
still expands `collectAllPartitionLeaves` against the statement's snapshot epoch
(`SearchPathCatalog.CurrentPartitionDetachEpoch`), preserving the RR-visible-row
asymmetry above.

This mirrors PostgreSQL: a partition that became detach-pending at or before
*now* is invisible to every FK existence check, while a row that is still
MVCC-visible to an old snapshot remains readable by ordinary scans.

### Why this is safe for the already-passing siblings

- **detach-partition-concurrently-2** — its FK inserts are all issued by
  autocommit / READ COMMITTED sessions whose statement snapshot epoch already
  equals the current epoch, so the value returned by `snapDetachEpoch` is
  unchanged. `TestPort_IsolationDetachPartitionConcurrently2` still PASS.
- **detach-1/3, fk-snapshot, fk-contention, partition-key-update-1..4** — all
  PASS unchanged.

The success case is preserved too: in the permutation
`… s1updcur s2detach …` the `UPDATE … SET a = 1` runs *before* the detach is
marked, so the current epoch shows no pending detach, value `1` is found, and the
update succeeds — after which the `DETACH` itself fails with
`removing partition … violates foreign key constraint d4_fk_a_fkey_1`
(`detachPartitionFKRefCheck`, already implemented in 0118-0060).

## Deferred (cursor behaviour — not in this loop)

detach-4 is **not yet promoted** to `runIsoSpecStrict`. The remaining
divergences are all about cursors over the referenced partitioned table, and
each needs a distinct cursor-snapshot-lifetime capability goopg does not yet
have:

1. **Cursor snapshot pinning at DECLARE.** goopg materialises a cursor lazily on
   first `FETCH` (`cursorEntry`, `internal/server/conn_tx.go`), so a cursor
   declared *before* the detach but fetched *after* it sees the post-detach
   partition set (1 row) instead of its declaration-time set (2 rows). A cursor
   must capture its snapshot — including the partition-detach epoch — at DECLARE
   and expand partitions against that frozen epoch at FETCH.
2. **Detacher waits on an open cursor.** A `DECLARE CURSOR` over the partitioned
   parent must register a pinned snapshot / relation lock so the concurrent
   `DETACH … CONCURRENTLY` blocks (renders `<waiting …>`) and is cancellable.
   Today the cursor holds neither, so the detacher completes immediately and
   there is nothing for `s1cancel` to cancel.

Both are tracked in `.ralph/deferral_ledger.md`. They are the same
cursor-pinned-snapshot machinery and should land together as the detach-4
promotion loop.

## Tests

- Probe (`RunAndCompare`): the FK-only permutations (READ COMMITTED **and**
  REPEATABLE READ inserts/updates of value `1`) now match PG byte-for-byte; the
  residual diff is confined to the eight cursor permutations.
- `TestPort_IsolationDetachPartitionConcurrently1/2/3` — PASS (no regression).
- `TestPort_IsolationFkSnapshot`, `TestPort_IsolationFkContention`,
  `TestPort_IsolationPartitionKeyUpdate1..4` — PASS.
- `go test ./internal/executor/ ./internal/catalog/` — PASS.
- pgbench CI-parity smoke — 0 failed.
