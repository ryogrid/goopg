# 0118-0077 — `ALTER TABLE … ATTACH PARTITION` defers catalog registration until COMMIT (M0118-0008 `partition-concurrent-attach` enabler)

- **Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
- **Spec:** `postgres/src/test/isolation/specs/partition-concurrent-attach.spec`
- **Status:** accepted — **enabler, NOT a promotion** (spec stays `defer`). Piece (a) of the `partition-concurrent-attach` interlock.
- **Builds on:** 0118-0075 (ATTACH default-conflict check) and 0118-0076 (ATTACH locks the DEFAULT partition).

## Problem — the uncommitted attach was visible too early

The spec's interlock relies on PostgreSQL transactional-DDL visibility: while `s1`
holds an open transaction that has run `ALTER TABLE tpart ATTACH PARTITION tpart_2
FOR VALUES FROM (100) TO (200)` but has **not** committed, a concurrent session
`s2` must not see `tpart_2`. So `s2`'s `INSERT INTO tpart VALUES (110, …)` routes
to the parent's DEFAULT partition (`tpart_default`) — where it must block on the
`AccessExclusiveLock` that the open attach took on the default (piece (b),
0118-0076) until `s1` commits.

goopg serves partition membership from a **single shared in-memory catalog**: the
`AlterTableAttachPartition` executor case called `RegisterPartitionChild` and set
the child's `PartitionBounds` **synchronously at statement time**, regardless of
whether the attach ran inside an explicit transaction. So the uncommitted
`tpart_2` was immediately visible to every session, `s2`'s INSERT routed straight
to `tpart_2`, and the default-partition lock taken by piece (b) was never
contended along the spec path. The spec's `<waiting ...>` never appeared.

This is the cross-session catalog-visibility piece — the blocker shared with
`alter-table-4`. A full per-session MVCC catalog is milestone-sized, but the
specific behaviour this spec needs is bounded and one-directional: **an
uncommitted ATTACH must be invisible to everyone until the attaching transaction
commits.** Unlike `DROP INDEX` deferral (0118-0074), which keeps a row *visible*
until commit, ATTACH must keep the new partition *invisible* until commit — and
because the change is uncommitted (never committed-but-stale), simply **not
registering** the child until commit achieves exactly that on a shared catalog.

## Change — defer the registration to COMMIT

`ALTER TABLE … ATTACH PARTITION` (non-default) issued **inside an explicit
transaction** now defers its catalog registration — setting the child's
`PartitionBounds` and the `RegisterPartitionChild` parent→child link — to COMMIT.
Until the attaching transaction commits, the child is not in the parent's
`partitionChildren` map, so routing/SELECT-expansion (which enumerate via
`PartitionChildren`) do not find it and an INSERT through the parent falls to the
DEFAULT partition. In **autocommit** (`!InExplicitTransaction`) the registration
stays immediate — ordinary ATTACH is unchanged.

The default-partition `AccessExclusiveLock` (0118-0076) and the
`checkDefaultPartitionDataConflict` data-conflict check (0118-0075) continue to
run at **statement time**, exactly as in PG's `ATExecAttachPartition`.

### Mechanism (mirrors the 0118-0074 DROP INDEX deferral)

- **`executor/session.go`** — new `PendingPartitionAttach` record (`ParentOID`,
  `ChildOID`, `Bounds`, `SavepointDepth`) and a `BasicSession.pendingPartAttaches`
  slice with `AddPendingPartitionAttach` / `TakePendingPartitionAttaches` /
  `CancelPendingPartitionAttachesToDepth`. `EndExplicitTransaction` nils the
  slice — the safety net that discards deferred attaches on **any** rollback path
  (executor `execRollback`, server dispatch `TxRollback`, SSI/FK pre-commit
  aborts), all of which funnel through `EndExplicitTransaction`.
- **`executor/operators_ddl.go`** — the `AlterTableAttachPartition` case computes
  the bounds into a local `boundsToSet`, then, when `o.ctx.Session` is a
  `*BasicSession` in an explicit transaction over an `*InMemory` catalog, records
  a `PendingPartitionAttach` instead of assigning `PartitionBounds` /
  `RegisterPartitionChild`. The new `ApplyPendingPartitionAttaches(ctx, sess)`
  performs the real registration at commit (`LookupTableByOID` → set
  `PartitionBounds` → `RegisterPartitionChild`). The child's own propagated
  unique/PK indexes are still created immediately (harmless: the child is not
  routable until commit), keeping that block untouched.
- **Commit paths (both, sibling paths kept in sync):**
  `ApplyPendingPartitionAttaches` is invoked **before** `TxnMgr.Commit` in (a) the
  executor `transactionOp.execCommit` and (b) the server simple-query dispatch
  `TxCommit` branch (which bypasses `execCommit`) — placed immediately after the
  existing `ApplyPendingIndexDrops` call. The isolation runner sends simple
  queries, so this spec exercises path (b).
- **Savepoints:** `rollbackToSavepointOp` calls
  `CancelPendingPartitionAttachesToDepth(newDepth)` (next to the existing
  `CancelPendingIndexDropsToDepth`) so an ATTACH issued inside a rolled-back
  savepoint is not still applied at the outer COMMIT.

## Effect on the spec (probed, not yet promoted)

With piece (a) in place, permutation 2 (`s1b s1a s2b s2i2 s1c s2c s2s`) now shows
`s2i2 … <waiting ...>` then `<... completed>` after `s1c` — the deferral makes
`s2` not see `tpart_2`, so the `INSERT INTO tpart_default` takes
`RowExclusiveLock` on `tpart_default` and blocks behind the open attach's
`AccessExclusiveLock` (piece (b)), exactly as PG. The remaining diffs are the
next pieces, deliberately out of scope here:

- **Permutation 1** (`INSERT INTO tpart …`, routing *through* the default
  subtree): goopg locks only the routed leaf, not the intermediate
  `tpart_default`, so `s2i` does not yet wait. Needs INSERT routing to take a
  `RowExclusiveLock` on each partition along the routing path.
- **Piece (c):** after the wait, `s2`'s buffered INSERT into `tpart_default` must
  re-validate the default's now-narrowed partition constraint (it changed because
  `tpart_2` committed) and fail with `new row for relation "tpart_default"
  violates partition constraint`. goopg does not re-validate, so the row lands and
  the final SELECT shows 6 rows instead of 3.
- **Permutation 3** (reverse): the open attach must wait for `s2`'s prior INSERT
  and then re-scan the default leaf for violating rows (`updated partition
  constraint for default partition "tpart_default_default" would be violated`).

These are the milestone-sized per-session MVCC-catalog + routing-lock work shared
with `alter-table-4`.

## Scope / known limitation

The deferral keeps the new partition invisible to the **attaching** session too
(shared catalog, not yet per-session MVCC-filtered): inside the same explicit
transaction, an ATTACH followed by a SELECT/INSERT would not yet see the new
partition. This spec never routes through the parent from `s1` before commit, so
output is unaffected; full same-session visibility remains the MVCC-catalog
milestone. The change is correctness-positive regardless: deferring to commit also
makes an ATTACH + ROLLBACK naturally leave the partition unattached (no undo
needed).

## Verification

- `go test ./internal/executor/` (full package) — PASS, incl. new
  `attach_default_lock_test.go` tests `TestAttachPartitionDeferredUntilCommit`
  (deferred inside a txn; `ApplyPendingPartitionAttaches` registers at commit and
  drains the pending list) and `TestAttachPartitionImmediateInAutocommit`
  (control: autocommit registers immediately).
- `go test ./internal/server/` — PASS (the commit-path wiring lives in dispatch).
- No regression: `TestPort_IsolationDetachPartitionConcurrently1` and
  `TestPort_IsolationPartitionDropIndexLocking` (strict) PASS.
- Probe: `partition-concurrent-attach` now matches PG on permutation 2's
  `<waiting ...>`/`<... completed>` interlock; spec stays `defer` pending pieces
  (c) + routing locks.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Oracle

Mirrors PostgreSQL's transactional-DDL visibility: `ATExecAttachPartition`
(`src/backend/commands/tablecmds.c`) inserts the `pg_inherits` / updates the
`pg_class.relpartbound` tuples within the attaching transaction's command, but the
change is MVCC and only becomes visible to other snapshots at commit, while the
`AccessExclusiveLock` on the existing default partition is held to
end-of-transaction. goopg, lacking a per-session MVCC catalog, reproduces the
other-session-invisible-until-commit half by deferring the shared-catalog
registration to commit.
