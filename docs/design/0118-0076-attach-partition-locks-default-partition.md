# 0118-0076 — `ALTER TABLE … ATTACH PARTITION` locks the DEFAULT partition (M0118-0008 `partition-concurrent-attach` enabler)

- **Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
- **Spec:** `postgres/src/test/isolation/specs/partition-concurrent-attach.spec`
- **Status:** accepted — **enabler, NOT a promotion** (spec stays `defer`)
- **Builds on:** 0118-0075 (ATTACH default-conflict check)

## The spec & the three interlocking blockers

`partition-concurrent-attach` interleaves an in-transaction `ALTER TABLE tpart
ATTACH PARTITION tpart_2 FOR VALUES FROM (100) TO (200)` (session `s1`) with a
concurrent `INSERT INTO tpart VALUES (110,…),(120,…),(150,…)` (session `s2`).
In PG the insert renders `<waiting ...>` and, once `s1` commits, fails with
`violates partition constraint`. Reaching that behaviour needs three interlocking
pieces:

- **(a)** `s2` must NOT see the uncommitted `tpart_2`, so its insert routes to
  the existing DEFAULT partition (`tpart_default`) rather than to `tpart_2`.
- **(b)** the ATTACH must hold an `AccessExclusiveLock` on the DEFAULT partition,
  so `s2`'s insert (which routes to the default and takes a `RowExclusiveLock`)
  **waits** until the attach commits.
- **(c)** after the wait, re-validating the default's now-narrowed constraint
  sees the routed rows fall in `tpart_2`'s range and rejects them.

(a) and (c) are the milestone-sized per-session MVCC-catalog work shared with
`alter-table-4`. **This slice lands (b)** — the standalone, independently-correct
locking half — which goopg was missing entirely.

## Change — lock the default partition during ATTACH

PostgreSQL's `ATExecAttachPartition` (`src/backend/commands/tablecmds.c`) locks
the existing default partition **first**, before opening the table being
attached, because attaching a new partition narrows the default's implicit
constraint:

```c
defaultPartOid =
    get_default_oid_from_partdesc(RelationGetPartitionDesc(rel, true));
if (OidIsValid(defaultPartOid))
    LockRelationOid(defaultPartOid, AccessExclusiveLock);
```

goopg's `AlterTableAttachPartition` executor case took no lock on the default, so
a concurrent insert routing to the default never blocked behind an open attach.

### Mechanism

- **`executor/operators_ddl.go`** — new `(*ddlOp).lockDefaultPartitionForAttach(parent)`:
  finds the parent's immediate DEFAULT child (scan `InMemory.PartitionChildren`
  for a `PartitionBound.IsDefault`) and takes an `AccessExclusiveLock` on it via
  the existing transaction-scoped `acquireDDLLockTxn`. Called in the
  `AlterTableAttachPartition` case **before** the default-conflict check (matching
  PG's ordering), for every non-default attach.
- **Transaction-scoped:** `acquireDDLLockTxn` is a no-op when
  `TxnLockBackendID == 0` (autocommit) or for system relations, so the lock is
  taken (and held to commit) only inside an explicit transaction — exactly the
  spec's `s1`. Plain `CREATE TABLE … PARTITION OF`, autocommit `ATTACH`, and the
  query hot path are unaffected (zero blast radius).

The lock rides the same `tableLockMgr` as `LOCK TABLE`/`DROP INDEX`, so it both
appears in the `pg_locks` bridge and conflicts with the `RowExclusiveLock` a
concurrent insert into the default would take — the wait edge the spec needs once
(a) routes that insert to the default.

## Scope / what this does NOT do

The spec is **not** promoted: without (a), goopg's shared catalog still makes the
uncommitted `tpart_2` visible to `s2`, so `s2`'s insert routes to `tpart_2` (not
the default) and the new default lock is never contended along the spec's path.
(c)'s post-wait re-validation is likewise unbuilt. Those remain the per-session
MVCC-catalog milestone (ledger). This change is independently correct and
PG-faithful regardless: an in-transaction ATTACH now exclusively locks the
default partition exactly as upstream does.

## Verification

- New `attach_default_lock_test.go`:
  `TestAttachPartitionLocksDefaultPartition` (the default partition is
  AccessExclusive-locked by the attaching backend; an already-attached non-default
  sibling is not) and `TestAttachPartitionNoDefaultNoLock` (no default ⇒ no
  sibling lock).
- No regression: `TestAttachPartitionRejectsDefaultConflict` /
  `…NoConflictSucceeds` / `…NestedDefaultNamesLeaf` (0118-0075);
  `go test ./internal/executor/` (full package) PASS;
  `TestPort_IsolationDetachPartitionConcurrently1` strict PASS (shares the attach
  setup).
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Oracle

`src/backend/commands/tablecmds.c` `ATExecAttachPartition` —
`get_default_oid_from_partdesc` + `LockRelationOid(defaultPartOid,
AccessExclusiveLock)`. Compared against `./postgres/local_install` PG 18.3.
