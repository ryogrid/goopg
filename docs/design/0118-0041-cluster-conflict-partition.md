# 0118-0041 — `cluster-conflict-partition` isolation spec promotion (M0118-0008)

**Status:** accepted
**Date:** 2026-06-23
**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
**Spec:** `postgres/src/test/isolation/specs/cluster-conflict-partition.spec`
**Test:** `TestPort_IsolationClusterConflictPartition` (`internal/testport/isolation_port_test.go`)

## Summary

Twelfth M0118-0008 promotion; the partitioned-table sibling of `cluster-conflict`
(design 0118-0040). All **4 permutations** of `cluster-conflict-partition` now
match PG 18.3 byte-for-byte **with no engine change** — the behaviour rides the
`cluster-conflict` AccessExclusiveLock landed last loop plus goopg's existing
partition catalog.

## What the spec exercises

```sql
CREATE ROLE regress_cluster_part;
CREATE TABLE cluster_part_tab (a int) PARTITION BY LIST (a);
CREATE TABLE cluster_part_tab1 PARTITION OF cluster_part_tab FOR VALUES IN (1);
CREATE TABLE cluster_part_tab2 PARTITION OF cluster_part_tab FOR VALUES IN (2);
CREATE INDEX cluster_part_ind ON cluster_part_tab(a);
ALTER TABLE cluster_part_tab OWNER TO regress_cluster_part;
```

`s2` runs `SET ROLE regress_cluster_part; SET client_min_messages = ERROR;`
then `CLUSTER cluster_part_tab USING cluster_part_ind;` while `s1` holds either
the **parent** or a **leaf** in `SHARE UPDATE EXCLUSIVE MODE`.

Expected output:

| perm | s1 holds | CLUSTER on parent |
|------|----------|-------------------|
| 1, 2 | parent `cluster_part_tab` | **waits**, then completes after `s1` commits |
| 3, 4 | leaf `cluster_part_tab1`   | **completes immediately** (no wait) |

## Why PG behaves this way (the load-bearing detail)

`ALTER TABLE … OWNER TO` on a partitioned table **does not recurse to the
partition children** — `tablecmds.c` `ATPrepCmd`/`ATExecCmd` mark
`AT_ChangeOwner` as *"This command never recurses"*. So after setup only the
**parent** is owned by `regress_cluster_part`; the two leaves remain owned by the
bootstrap superuser that ran setup.

Upstream `cluster()` (`commands/cluster.c`) for a partitioned target:

1. `RangeVarGetRelidExtended(..., AccessExclusiveLock, ...)` takes an
   **AccessExclusiveLock on the parent**. This conflicts with `SHARE UPDATE
   EXCLUSIVE`, so perms 1/2 (parent locked) **wait** here.
2. `get_tables_to_cluster_partitioned()` enumerates leaves **without locking
   them** (`find_all_inheritors(indexOid, NoLock, NULL)`), and for each leaf
   calls `cluster_is_permitted_for_relation(relid, GetUserId())`. The role does
   **not** own the leaves → every leaf is **skipped** (the
   `permission denied to cluster "…", skipping it` WARNING is suppressed by
   `client_min_messages = ERROR`).
3. The parent lock is released; `cluster_multiple_rels()` processes the (empty)
   leaf list → nothing.

So in perms 3/4 (a *leaf* held in `SHARE UPDATE EXCLUSIVE`) CLUSTER **never
attempts to lock the leaf** — the parent lock succeeds immediately and the leaf
is skipped on ownership grounds — hence no wait.

## Why goopg already matches

goopg's `clusterOp.Next` (operators_cluster.go, design 0118-0040) is a no-op
index-order rewrite that:

- looks up the **named parent** relation, and
- takes a blocking `AccessExclusiveLock` on it via
  `acquireRelLockMaybeTransient`.

It does **not** enumerate or lock partition leaves. The observable result is
identical to PG by two different routes:

- **Parent locked** (perms 1/2): goopg's AccessExclusiveLock on the parent
  conflicts with `s1`'s `SHARE UPDATE EXCLUSIVE`, so CLUSTER waits and completes
  on commit — same as PG step 1.
- **Leaf locked** (perms 3/4): goopg locks only the (unlocked) parent and never
  touches the leaf, so CLUSTER returns immediately — same as PG (which skips the
  unowned leaf). goopg reaches the no-wait outcome because it does not process
  leaves at all; PG reaches it because the role does not own them. The captured
  output is byte-identical either way.

The runner decides `<waiting ...>` purely by a 300 ms timing threshold (no
`pg_locks` probe), so the immediate-completion perms render without a waiting
marker exactly as the expected file requires.

## Scope / non-goals

This promotion does **not** add per-leaf CLUSTER processing or a faithful
non-owner `must be owner of table "<leaf>"` error — no `port` spec exercises a
role that *owns* a leaf and would observe per-child locking, and goopg's CLUSTER
performs no physical rewrite. Should goopg later implement real per-partition
CLUSTER, the leaf-enumeration + `maintenancePermitted` skip
(operators_vacuum.go) would need to be wired into `clusterOp` to preserve this
output; flagged as a bounded follow-up in the deferral ledger.

## Verification

- `TestPort_IsolationClusterConflictPartition` strict PASS (4 permutations).
- Sibling M0118-0008 specs (`cluster-conflict`, `vacuum-conflict`,
  `truncate-conflict`, `vacuum-skip-locked`, `vacuum-concurrent-drop`,
  `create-trigger`, `sequence-ddl`, `reindex-*`, `inherit-temp`) still PASS.
- No engine files changed → no executor/planner/codec risk; pgbench smoke
  remains the mandatory per-commit gate (0-failed).

## Oracle

`postgres/src/backend/commands/cluster.c` (`cluster`,
`get_tables_to_cluster_partitioned`, `cluster_is_permitted_for_relation`),
`postgres/src/backend/commands/tablecmds.c` (`AT_ChangeOwner` never-recurses).
