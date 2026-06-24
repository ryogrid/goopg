# 0118-0035 — `vacuum-concurrent-drop` isolation spec pass-through

Milestone: **M0118-0008** (DDL / VACUUM / maintenance concurrency).
Status: **landed** — `vacuum-concurrent-drop` promoted to pass-required.

## Spec

`postgres/src/test/isolation/specs/vacuum-concurrent-drop.spec` verifies the log
messages VACUUM and ANALYZE emit when a *specified* relation is concurrently
DROPped, and that no message is emitted for a relation reached only by expanding
a partitioned table.

Setup: a list-partitioned `parted` with leaf partitions `part1`, `part2`.

Each of the six permutations is:

```
s1: BEGIN; LOCK part1 IN SHARE MODE;          -- holds SHARE on part1
s2: VACUUM part1, part2;   (or VACUUM parted, ANALYZE …, VACUUM ANALYZE …)
s1: DROP TABLE part2; COMMIT;                 -- drops part2, releases part1
```

Expected (PG 18.3):

- `s2` **blocks** (`<waiting ...>`) acquiring its lock on `part1` because
  ShareUpdateExclusiveLock conflicts with `s1`'s ShareLock.
- After `s1` commits, `s2` proceeds; reaching `part2` (now dropped) it logs
  `WARNING: skipping vacuum of "part2" --- relation no longer exists`
  **only when the relation was named explicitly** (`VACUUM part1, part2`,
  `ANALYZE part1, part2`, `VACUUM ANALYZE part1, part2`).
- For `VACUUM parted` / `ANALYZE parted` / `VACUUM ANALYZE parted` the dropped
  `part2` is reached by partition expansion, so the skip is **silent**.

## Root cause

goopg's VACUUM/ANALYZE only acquired a per-relation heavyweight lock on the
**SKIP_LOCKED** path (`tryAcquireMaintenanceLock`, design 0118-0033). Without
SKIP_LOCKED neither op took any heavyweight relation lock, so `s2` never waited
behind `s1`'s `LOCK … IN SHARE MODE` (no `<waiting ...>`), and there was no
post-lock re-check, so a concurrently dropped target produced no warning.

## Fix

Two changes, mirroring upstream `vacuum.c` `vacuum_open_relation`:

1. **Blocking per-relation lock (non-SKIP_LOCKED path).** The
   `vacuumOp`/`analyzeOp` per-target loop now acquires the maintenance lock
   (`ShareUpdateExclusiveLock`, or `AccessExclusiveLock` for `VACUUM FULL`) via
   the existing `(*Context).acquireRelLockMaybeTransient` when SKIP_LOCKED is
   not set. That helper waits behind a conflicting holder during acquisition and
   releases immediately in autocommit (VACUUM/ANALYZE are their own implicit
   transactions), so the `<waiting ...>` happens but no lock lingers. The wait
   lands on `part1` (held by `s1` in SHARE); ShareUpdateExclusive ⟂ Share per the
   standard conflict matrix.

2. **Post-lock existence re-check.** After taking the lock, the target is
   re-checked against the live catalog via the new `relationStillExists` helper
   (`catalog.InMemory.LookupTableByOID`, the analog of PG `try_relation_open`).
   A target dropped by the transaction that committed while we waited is skipped
   — emitting `skipping vacuum/analyze of "X" --- relation no longer exists`
   only for an **explicit** target (`vacuumTarget.explicit`), silently for an
   expanded partition child. The `explicit` flag was already computed by
   `expandVacuumTargets`/`expandAnalyzeTargets` (design 0118-0033). The existing
   severity-aware isolation runner renders the `WARNING:` line.

The warning text differs from the SKIP_LOCKED path's `--- lock not available`
(which signals contention) because here the relation genuinely no longer exists.

### Files

- `internal/executor/operators_vacuum.go` — non-SKIP_LOCKED blocking acquire +
  existence re-check in `vacuumOp.Next`; new `relationStillExists` helper.
- `internal/executor/operators_analyze.go` — same in `analyzeOp.Next`.
- `internal/testport/isolation_port_test.go` — `TestPort_IsolationVacuumConcurrentDrop`
  (strict).

## Blast radius

VACUUM/ANALYZE without SKIP_LOCKED now take a real (transient in autocommit)
ShareUpdateExclusiveLock per user relation. ShareUpdateExclusive is
self-compatible, so two concurrent VACUUMs of different relations never block;
the lock is contended only by an explicit `LOCK TABLE` / conflicting DDL, where
waiting is the correct PG behaviour. System catalogs (OID <
`firstNormalObjectOID`) are skipped by `acquireRelLockMaybeTransient`. The
post-lock existence re-check is a no-op for any relation that is not dropped
mid-statement (the overwhelmingly common case). pgbench TPC-B smoke: 0 failed,
no TPS regression.

## Gates

- `TestPort_IsolationVacuumConcurrentDrop` strict PASS (6 permutations).
- `TestPort_IsolationVacuumSkipLocked` PASS (no regression from the existence
  re-check now also running on the SKIP_LOCKED success path).
- `go test ./internal/executor -run 'Vacuum|Analyze'` PASS.
- `go test -race ./internal/lockmgr/...` PASS.
- pgbench TPC-B smoke 0-failed.

## Remaining M0118-0008 deferrals

Unchanged: `alter-table-{1,2,4}` (ADD/VALIDATE CONSTRAINT lock semantics;
INHERITS), the `*-conflict` family (privilege infra), the partition
ATTACH/DETACH specs, `vacuum-no-cleanup-lock` (reltuples accounting),
`inherit-temp`, `plpgsql-toast`, `reindex-concurrently-toast`.
