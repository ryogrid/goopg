# 0118-0042 — Harden the `FOR UPDATE/SHARE SKIP LOCKED` row-lock isolation family to pass-required

**Status:** accepted
**Milestone:** M0118-0003 (Row locking) — regression-protection hardening
**Date:** 2026-06-23

## Summary

Promotes the four `skip-locked` isolation specs — `skip-locked`,
`skip-locked-2`, `skip-locked-3`, `skip-locked-4` — from observability
(`runIsoSpec`, which `t.Skip()`s a non-matching `defer` result) to
**pass-required** (`runIsoSpecStrict`, where a non-match is a hard red test).
All four already match PG 18.3 byte-for-byte; **no engine change** is involved.

This closes a regression-protection gap. M0118-0003 (Row locking) is marked
**COMPLETE** in `.ralph/fix_plan.md` and asserts "All 20 specs PASS vs PG 18.3 …
CSV rows already `pass`", yet the dedicated test functions for the `SKIP LOCKED`
family still ran through the non-strict `runIsoSpec` helper. A future regression
in the multixact-aware / tuple-lock / update-chain `SKIP LOCKED` paths would have
turned those tests into a silent `t.Skip` rather than a failure. Hardening them to
`runIsoSpecStrict` makes the completeness claim actually enforced.

## What each spec proves

All four exercise the **no-wait row-lock mode** `SELECT … FOR UPDATE/SHARE SKIP
LOCKED`, where a session must *skip* (not block on) a row already row-locked by
another session and claim the next available row. They cover the four distinct
ways a row can be "locked" in goopg's row-lock engine:

| spec | lock structure skipped | upstream comment |
|------|------------------------|------------------|
| `skip-locked`   | a plain `FOR UPDATE` row lock (single locker) | "regular row locks can't be acquired" |
| `skip-locked-2` | a **multixact** with a still-held `FOR SHARE` member (the `FOR UPDATE` upgrade conflicts) | "SKIP LOCKED with multixact locks" |
| `skip-locked-3` | a **tuple lock** (heavyweight tuple-lock wait queue) | "SKIP LOCKED with tuple locks" |
| `skip-locked-4` | a row reached by following an **updated ctid chain** to its live successor | "SKIP LOCKED with an updated tuple chain" |

Each is a genuine concurrency assertion: e.g. in `skip-locked`, when `s1` holds
row 1 and `s2` runs `… FOR UPDATE SKIP LOCKED LIMIT 1`, `s2` returns row **2**
(skipping the locked row), and the two sessions claim disjoint rows across all 21
permutations without ever blocking. These pass because the cumulative
multixact-aware lock producer + subxact-scoped row-lock infrastructure landed in
the M0118-0003/0004 slices already drives every permutation correctly — the
`SKIP LOCKED`/`NOWAIT` conflict gate (`stampLockInner` conflict-before-wait, the
multixact member conflict check, and the ctid-chain follow on a no-key update)
was the load-bearing work; this change only hard-asserts it.

## Change

`internal/testport/isolation_port_test.go`: `runIsoSpec` → `runIsoSpecStrict` in
`TestPort_IsolationSkipLocked`, `TestPort_IsolationSkipLocked2`,
`TestPort_IsolationSkipLocked3`, `TestPort_IsolationSkipLocked4`; enriched the
terse `-3`/`-4` docstrings to describe the lock structure each one skips.

No production code changes. No catalog/parser/executor/lockmgr changes.

## Oracle

`postgres/src/test/isolation/specs/skip-locked{,-2,-3,-4}.spec` and their
`expected/*.out`. Compared byte-for-byte (after isolationtester normalization)
against the local PG 18.3 install via `IsolationRunner.RunAndCompare`.

## Verification

- `TestPort_IsolationSkipLocked{,2,3,4}` — strict PASS (21/many permutations each).
- Probe (`RunAndCompare` over all four) returned `status=pass`, empty diff, before
  the helper switch — confirming the promotion is free.
- Build + `go vet` clean; pgbench TPC-B smoke is the machine-enforced pre-commit hook.

## Scope / deferral

This hardens only the `SKIP LOCKED` family. The sibling no-wait-mode `nowait`
family (`nowait{,-2,-3,-4,-5}`, `lock-nowait`) and the remaining M0118-0003
row-lock specs still run through `runIsoSpec`; promoting those to strict is a
follow-up of the same shape (verify byte-match, flip the helper) and is left for a
later loop to keep this change focused and individually verifiable. The genuinely
**deferred** M0118-0008 DDL/maintenance tail (`alter-table-{1,2,4}`,
`detach-partition-concurrently-*`, `partition-concurrent-attach`,
`partition-drop-index-locking`, `reindex-concurrently-toast`,
`vacuum-no-cleanup-lock`, `plpgsql-toast`) all still diverge and need real engine
work (concurrent-detach protocol, dollar-quote parsing, ADD/VALIDATE CONSTRAINT
lock semantics, cursor-pin VACUUM) — out of scope here.
