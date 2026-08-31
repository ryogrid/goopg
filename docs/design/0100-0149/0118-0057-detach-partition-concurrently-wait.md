# 0118-0057 — `DETACH PARTITION … CONCURRENTLY` waits for concurrent table users (M0118-0008 enabler)

**Status:** accepted
**Milestone:** M0118-0008 (upstream isolation spec suite — DDL/VACUUM/maintenance concurrency)
**Kind:** enabler, NOT a spec promotion

## Problem

After the parser fix (0118-0048) and the `pg_backend_pid`/`pg_cancel_backend`
enablers (0118-0055/0056), the `detach-partition-concurrently-{1,2,3,4}` specs
parse and run, but `ALTER TABLE parent DETACH PARTITION child CONCURRENTLY`
completes **synchronously and immediately** in goopg's executor
(`AlterTableDetachPartition` → `UnregisterPartitionChild` + clear bounds). The
isolation runner therefore never renders the `<waiting ...>` marker that the
expected output requires.

Probing `detach-partition-concurrently-1` confirmed this is the spec's first
divergence: the expected `s2detach` step parks (`<waiting ...>`) while session
`s1` (which opened the partitioned table in a transaction) is still live, and
only completes (`<... completed>`) after `s1` commits. goopg ran `s2detach`
straight through.

## Upstream behaviour

Upstream's two-phase detach (`tablecmds.c` `ATExecDetachPartition` →
`DetachPartitionFinalize`): phase 1 marks the partition **detach-pending**
(`pg_inherits.inhdetachpending = true`), commits, makes the partition invisible
to new snapshots, then **waits** for every transaction whose snapshot predates
that change to terminate before phase 2 clears `relpartbound`. The waiting is
the visible behaviour the spec asserts; `DETACH … CONCURRENTLY` cannot run
inside a transaction block precisely because it must commit phase 1 and wait.

## Change

In the `AlterTableDetachPartition` executor case
(`internal/executor/operators_ddl.go`), when `act.DetachConcurrently` is set
(the flag recorded by the parser in 0118-0048): after unregistering the child
from the parent's partition tree (the membership change is globally visible the
instant we mutate goopg's single shared in-memory catalog — matching the READ
COMMITTED case where the partition disappears from concurrent snapshots
immediately), **block until no other backend holds a transaction-scoped lock**
on the parent or on the partition being detached, via the existing
`(*Context).waitForRelationLockers` primitive (the `WaitForLockers` analog
introduced for `reindex-concurrently`, 0118-0029).

`waitForRelationLockers` takes no lock of its own, so concurrent readers
(`AccessShare`) and writers (`RowExclusive`) proceed unimpeded — the
`CONCURRENTLY` contract. A bare `SELECT * FROM parent` expands to per-partition
scans, each taking a transaction-scoped `AccessShare` on the child it reads
(`acquireScanReadLockTxn`), so a reader that touched the detached partition
holds that lock until commit and the detacher parks behind it. The runner's
300 ms timing threshold renders the park as `<waiting ...>` and completion the
instant the lockers drain. Both the parent **and** the detached child are
waited on: a partitioned-parent scan locks the children (not the parent), so
the child wait is what catches a concurrent reader; the parent wait covers any
parent-lock path and is harmless otherwise.

Context cancellation (`statement_timeout`, `lock_timeout`, or a peer
`pg_cancel_backend` — the path detach-3/4 exercise via `s1cancel(s2detach)`)
aborts the wait with the matching SQLSTATE-57014 error, inherited for free from
`waitForRelationLockers`.

Gated entirely on `DetachConcurrently`: plain `DETACH PARTITION` and `DETACH …
FINALIZE` remain synchronous and unchanged, and the only specs that use the
CONCURRENTLY form are the four `detach-partition-concurrently-*` specs (all
deferred). Blast radius outside those specs is nil.

## Result

`detach-partition-concurrently-1`'s first divergence advances from the missing
`<waiting ...>`/`<... completed>` markers (now byte-correct, verified by probe)
to perm 1's row content: goopg's *repeated* `SELECT * FROM d_listp` in the same
session still observes the detached partition. The READ COMMITTED permutations'
correct post-detach row sets, plus the wait markers, now match PG.

## Still deferred (full promotion — resume points)

1. **Cross-session plan-cache invalidation for partition DDL.** A repeated
   `SELECT` over the partitioned parent in the same session is served the
   cached plan (Append over the original partition set) and does not see the
   concurrently-detached partition disappear, even in READ COMMITTED — the same
   class of issue `inherit-temp` (0118-0037) solved by bypassing the shared plan
   cache when temp inheritance children exist. A partition-membership-aware
   bypass/invalidation is the next bounded slice.
2. **REPEATABLE READ snapshot-stable partition visibility.** The `s1brr`
   permutations require the detached partition to remain visible to a snapshot
   taken before the detach committed. goopg's single shared catalog makes the
   membership change globally visible immediately; faithful behaviour needs
   transactional (MVCC) catalog visibility of partition membership — the
   milestone-sized subsystem shared with `alter-table-4` and
   `partition-concurrent-attach`.
3. **Two-phase pending-detach state** (detach-3/4): `pg_inherits.inhdetachpending`,
   `pg_partition_tree`, `DETACH … FINALIZE` of a partially-detached partition,
   "cannot drop another partition while one is pending detach", cancel-then-resume.

## Gates

- Probe (throwaway `zz_probe_test.go`): `detach-partition-concurrently-1`
  `<waiting ...>`/`<... completed>` markers now byte-match PG; first divergence
  advanced to the plan-cache row-visibility gap.
- `detach-partition-concurrently-{2,3,4}` still `defer` cleanly — no hang/panic;
  the cancel-during-wait path (`s1cancel(s2detach)`) is handled by
  `waitForRelationLockers`' context-cancellation support.
- `go build ./...` + `go vet ./internal/executor/` clean; `internal/executor`
  package tests PASS.
- pgbench smoke = pre-commit hook (`.githooks/pre-commit`).
