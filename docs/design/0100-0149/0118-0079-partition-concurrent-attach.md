# 0118-0079 — `partition-concurrent-attach` PROMOTED (INSERT routing-path locks + fresh-snapshot ATTACH re-scan)

- **Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
- **Spec:** `postgres/src/test/isolation/specs/partition-concurrent-attach.spec`
- **Status:** accepted — **PROMOTION** (all 3 permutations byte-for-byte vs PG 18.3)
- **Builds on:** 0118-0075 (ATTACH default-conflict check), 0118-0076 (ATTACH
  locks the default partition), 0118-0077 (ATTACH defers catalog registration to
  COMMIT), 0118-0078 (INSERT-time DEFAULT partition constraint enforcement)

## The spec

`partition-concurrent-attach` interleaves an in-transaction `ALTER TABLE tpart
ATTACH PARTITION tpart_2 FOR VALUES FROM (100) TO (200)` (session `s1`) with a
concurrent `INSERT … VALUES (110,…),(120,…),(150,…)` (session `s2`). The
partition tree is:

```
tpart                      partition by range(i)
├── tpart_1                FOR VALUES FROM (0) TO (100)
├── tpart_default          DEFAULT, itself partition by list(j)
│   └── tpart_default_default   DEFAULT
└── tpart_2                FOR VALUES FROM (100) TO (200)   ← attached concurrently
```

The rows have `i ∈ {110,120,150}` — all in `tpart_2`'s range. Three permutations:

1. `s2i` = `INSERT INTO tpart …` routes (under its pre-attach snapshot) through
   `tpart_default` → `tpart_default_default`; once `s1` commits it must
   `<waiting ...>` then `ERROR: new row for relation "tpart_default" violates
   partition constraint` (23514).
2. `s2i2` = direct `INSERT INTO tpart_default (i,j) …`: same outcome, error names
   `tpart_default`.
3. reverse order — `s2i` first, then `s1a` attaches: `s1a` must `<waiting ...>`
   behind the open insert, then `ERROR: updated partition constraint for default
   partition "tpart_default_default" would be violated by some row` (23P01).

## What was already in place

- **(a)** `s2` does not see the uncommitted `tpart_2` because ATTACH defers
  catalog registration to COMMIT (0118-0077), so its insert routes to the default.
- **(b-direct)** ATTACH holds `AccessExclusiveLock` on the default partition
  (0118-0076), and a *direct* `INSERT INTO tpart_default` takes `RowExclusiveLock`
  on its named target — so **perm 2 already waited and passed**.
- **(c-insert)** `checkDefaultPartitionInsertConstraint` re-routes a routed row
  one level per default ancestor against the live catalog and raises 23514
  (0118-0078).
- the ATTACH-side default-content re-scan `checkDefaultPartitionDataConflict`
  already raises 23P01 when the default holds offending rows (0118-0075).

## The two remaining gaps this slice closes

### Gap 1 — INSERT routing-path lock (perms 1 & 3)

For an `INSERT INTO tpart` whose row routes *through* the intermediate default
`tpart_default`, goopg locked only the **named** target `tpart` (in
`insertOp.Open`); it took no lock on `tpart_default` along the routing path. So:

- **perm 1:** `s2i` did not wait behind `s1`'s `AccessExclusiveLock` on
  `tpart_default`; it routed and wrote before `s1` committed, the live catalog had
  no `tpart_2`, the re-route claimed nothing, and the row landed (6 rows).
- **perm 3:** `s1a`'s ATTACH `AccessExclusiveLock` on `tpart_default` found no
  conflicting holder (s2 held nothing on the default), so it did not wait.

PostgreSQL's `ExecInsert` opens **every partition a tuple is routed into** in
`RowExclusiveLock`. New `lockRoutingPathPartitions(ctx, named, leaf)`
(`operators_storage.go`) walks the parent chain from the routed leaf up to (but
excluding) the named INSERT target and takes a transaction-scoped
`RowExclusiveLock` on every **intermediate** partition via the existing
`acquireWriteLockTxn` — for this tree, exactly `tpart_default`. It is wired into
`insertOp.Next` right after routing resolves the leaf, before the FK / default
constraint checks. The named target is already locked in `Open`; the leaf is the
heap-write target (its locking is unchanged). A single-level partitioned table
(leaf's parent IS the named target) has no intermediates ⇒ no-op.

`RowExclusiveLock` is self-compatible and conflicts only with DDL-grade modes, so
ordinary concurrent partitioned INSERTs never block each other — only a
concurrent ATTACH/DETACH/DDL on the intermediate partition does. With this:

- **perm 1:** `s2i`'s `RowExclusiveLock` on `tpart_default` waits behind `s1`'s
  `AccessExclusiveLock`; once `s1c` commits, the lock is granted, the live catalog
  now has `tpart_2`, and `checkDefaultPartitionInsertConstraint` re-routes the row
  onto it → 23514.
- **perm 3:** `s2i` holds `RowExclusiveLock` on `tpart_default` to commit, so
  `s1a`'s `lockDefaultPartitionForAttach` `AccessExclusiveLock` acquire **waits**.

### Gap 2 — fresh snapshot for the ATTACH re-scan (perm 3)

Once Gap 1 makes `s1a` wait, it is unblocked only after `s2c` commits — but
`s1a`'s statement snapshot was taken at statement start, **before** the wait, so
`checkDefaultPartitionDataConflict`'s `SELECT 1 FROM tpart_default WHERE i >= 100
AND i < 200` did not see `s2`'s just-committed rows and found no conflict (the
attach wrongly succeeded → 6 rows). PostgreSQL's
`check_default_partition_contents` scans under the latest snapshot. Fix: the
conflict scan now refreshes `synthCtx.Snap = TxnMgr.SnapshotFor(ctx.Tx)`
(`operators_ddl_partition.go`) before opening the scan — mirroring the
detach-4 post-wait RI re-validation (0118-0064). A fresh snapshot also sees the
transaction's own uncommitted writes, so the synchronous CREATE/ATTACH paths are
unaffected. Now `s1a` finds the rows and raises 23P01 naming the leaf default
`tpart_default_default`.

## Scope / blast radius

- `lockRoutingPathPartitions` only adds locks on **intermediate** partitions of a
  routed INSERT, in `RowExclusiveLock` (DML-grade, self-compatible). Outside an
  explicit transaction the acquire is transient (acquired+released, the wait still
  happens during acquisition); system catalogs are skipped. Single-level
  partitioned tables and non-partitioned INSERTs are untouched.
- The fresh-snapshot refresh is confined to the DDL default-content conflict scan.

## Verification

- `TestPort_IsolationPartitionConcurrentAttach` strict PASS — all 3 permutations
  byte-for-byte vs PG 18.3.
- No regression across the M0118-0008 partition/DDL siblings:
  `DetachPartitionConcurrently1/2/3/4`, `AlterTable1/2/3`, `CreateTrigger`,
  `InheritTemp`, `TruncateConflict`, `ClusterConflict`,
  `ClusterConflictPartition`, `VacuumConflict`.
- `go test ./internal/executor/` (executor unit suite incl. the 0118-0075/0078
  attach/default tests) PASS; `-race ./internal/executor/`.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Oracle

- `src/backend/executor/nodeModifyTable.c` `ExecInsert` /
  `execPartition.c` `ExecFindPartition` → `table_open(partoid,
  RowExclusiveLock)` for each routed partition.
- `src/backend/commands/tablecmds.c` `ATExecAttachPartition` →
  `check_default_partition_contents` scanning under the latest snapshot.
- Compared against `./postgres/local_install` PG 18.3.
