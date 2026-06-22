# 0118-0033 — `vacuum-skip-locked` isolation spec: conditional maintenance lock + severity-aware runner (M0118-0008)

**Status:** accepted
**Date:** 2026-06-22
**Spec:** `postgres/src/test/isolation/specs/vacuum-skip-locked.spec`
**Test:** `TestPort_IsolationVacuumSkipLocked` (`internal/testport/isolation_port_test.go`)

## Summary

Promotes the `vacuum-skip-locked` isolation spec to **pass-required**
(byte-identical to PG 18.3 across all 16 permutations) — the 7th M0118-0008
promotion. The spec runs `VACUUM`/`ANALYZE` with the `SKIP_LOCKED` option
against a partitioned table while another session holds `part1` in `SHARE` or
`ACCESS EXCLUSIVE` mode, observing what is skipped, what warns, and what waits:

```
session s1: BEGIN; LOCK part1 IN { SHARE | ACCESS EXCLUSIVE } MODE;   -- holds a conflicting lock
session s2: VACUUM (SKIP_LOCKED) part1, part2;        -- explicit list  → WARNING + skip part1
            VACUUM (SKIP_LOCKED) parted;              -- partitioned    → silent skip part1
            ANALYZE (SKIP_LOCKED) parted;             -- inheritance read waits under ACCESS EXCLUSIVE
session s1: COMMIT;
```

Behaviour reproduced (PG `vacuum.c` / `analyze.c`):

1. **Conditional per-relation lock.** `VACUUM`/`ANALYZE (SKIP_LOCKED)` take the
   relation lock conditionally (PG `ConditionalLockRelationOid`):
   `ShareUpdateExclusiveLock`, or `AccessExclusiveLock` for `VACUUM FULL`. A
   relation a conflicting lock is already held on is **skipped** rather than
   waited on. Both `SHARE` and `ACCESS EXCLUSIVE` conflict with
   `ShareUpdateExclusive`/`AccessExclusive`, so `part1` is skipped under either.
2. **Explicit ⇒ WARNING, expanded ⇒ silent.** A relation the user named
   explicitly emits `WARNING: skipping vacuum of "part1" --- lock not available`
   (`analyze` for ANALYZE); a partition reached by *expanding* a partitioned
   table is skipped **silently** (PG suppresses the log line for relations not
   in the original list).
3. **ANALYZE inheritance read waits.** `ANALYZE` of a partitioned parent gathers
   inheritance-tree statistics by reading every leaf partition under a
   **blocking** `AccessShareLock` — `SKIP_LOCKED` does *not* cover this scan
   (`acquire_inherited_sample_rows`). So `ANALYZE parted` / `VACUUM (ANALYZE)
   parted` **waits** on a child held in `ACCESS EXCLUSIVE` (conflicts with
   `AccessShare`) but **not** in `SHARE` (compatible). Plain `VACUUM` does no
   such scan and never waits.

## Fixes

### 1. Conditional maintenance lock — `tryAcquireMaintenanceLock`

`internal/executor/context.go` gains `(*Context).tryAcquireMaintenanceLock(rel,
mode) bool`, the non-blocking sibling of the existing lock helpers and a mirror
of PG's `ConditionalLockRelationOid`: it `TryAcquire`s `mode` under the active
backend identity (transaction backend inside an explicit block, else the
per-statement backend) and **releases immediately** on success — goopg's
`VACUUM`/`ANALYZE` is a self-contained statement that needs the lock only to
*detect* contention, not to hold off concurrent access for its duration. It
returns `false` the instant a conflicting lock is held by another backend, and
treats system catalogs (OID < `firstNormalObjectOID`) and a zero backend as
always-available.

### 2. VACUUM / ANALYZE target expansion + skip semantics

`operators_vacuum.go` and `operators_analyze.go` now expand the target list into
concrete heap relations, tagging each as **explicit** (user-named ⇒ a skip
warns) or **expanded** (a partition child ⇒ a skip is silent), and recording the
partitioned parents encountered (`expandVacuumTargets` / `expandAnalyzeTargets`,
sharing the `vacuumTarget` struct). For each target, when `SKIP_LOCKED` is set
and `tryAcquireMaintenanceLock` reports contention, the relation is skipped —
with the `skipping vacuum/analyze of "X" --- lock not available` WARNING
(`Context.AddWarning`) only when explicit.

After the per-relation pass, `ANALYZE` (and `VACUUM (ANALYZE)`) of a partitioned
parent calls `analyzeInheritanceWait`, which acquires + releases a **blocking**
`AccessShareLock` on each leaf partition (`acquireRelLockMaybeTransient`),
reproducing the inheritance-stats read that makes ANALYZE wait under `ACCESS
EXCLUSIVE`. goopg does not yet compute inherited statistics, but the lock
interaction is what the spec observes. (`VACUUM FULL` uses `AccessExclusiveLock`
for the conditional probe, matching `vacuum_open_relation`; for this spec the
skip outcome is identical to `ShareUpdateExclusive`.)

### 3. Isolation runner echoes real message severity

The spec's expected output distinguishes `s2: WARNING:  …` from `s2: NOTICE:
…`, but `internal/testport/framework/isolation_runner.go` previously hard-coded
`NOTICE:` for every captured server message. The notice handler now records the
protocol severity from `pq.Error.Severity` (`q.push(n.Severity, n.Message)`) and
the four notice-emit sites print it verbatim (`%s: %s` over the
already-severity-prefixed string). An empty severity falls back to `NOTICE`.
This is strictly more faithful — all previously-passing specs emit
`NOTICE`-severity messages and render identically; only WARNING-severity output
(new here) changes — and the full `TestPort_Isolation*` suite still passes.

## Why a probe-first pick

Per the M0118-0008 methodology, a throwaway `zz_probe_test.go` ran the remaining
tail (`alter-table-4`, `inherit-temp`, `plpgsql-toast`, the `vacuum-*` family,
`detach-partition-concurrently-1`) through `RunAndCompare` and inspected the
first divergence. `vacuum-skip-locked` diverged at the first permutation's
missing `WARNING` line — a single, well-understood behaviour (conditional lock +
severity) rather than a deep feature gap, and self-contained to the
VACUUM/ANALYZE path plus a one-line runner faithfulness fix.

## Gates

- `TestPort_IsolationVacuumSkipLocked` strict **PASS** (16 permutations).
- Full `TestPort_Isolation*` suite **PASS** (runner severity change has no
  regression — every NOTICE-emitting spec unchanged).
- `internal/testport/framework` unit tests PASS; executor vacuum/analyze/freeze
  units PASS; `go test ./internal/executor/` PASS; `-race ./internal/lockmgr/`
  PASS.
- `go build ./...`; pgbench CI-parity smoke (pre-commit hook).

## Remaining M0118-0008 tail (still deferred)

`alter-table-{1,2,4}` (ADD/VALIDATE CONSTRAINT + INHERIT lock semantics); the
`*-conflict` family (`truncate-conflict`, `vacuum-conflict`,
`cluster-conflict{,-partition}`) needs CREATE ROLE / SET ROLE / OWNER ACL
infrastructure; `vacuum-{concurrent-drop,no-cleanup-lock}` need
GUC/reltuples accounting; `detach-partition-concurrently-{1..4}` and
`partition-*` need DETACH PARTITION CONCURRENTLY semantics; `reindex-toast`,
`inherit-temp`, `plpgsql-toast`.
