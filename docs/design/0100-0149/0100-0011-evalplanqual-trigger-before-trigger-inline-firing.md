# M0100-0011: BEFORE Trigger Inline Firing During UPDATE Scan

## Problem

PostgreSQL fires BEFORE ROW triggers per-row during the UPDATE scan: after each row's
WHERE condition passes, the BEFORE trigger fires before moving on to the next row.
AFTER triggers are deferred until the entire scan and write phase completes.

goopg's `updateOp` used a two-phase model:

- **Phase 1**: scan all matching rows, evaluate WHERE + SET, collect to `pending[]`
- **Phase 2**: fire BEFORE triggers for all collected rows, write, fire AFTER triggers

This caused BEFORE trigger NOTICEs (e.g. from `trig_report()`) to appear AFTER all
WHERE-evaluation NOTICEs, diverging from PG's interleaving order.

### Observed vs expected NOTICE ordering

Given two rows (key-a, key-b) where key-a matches WHERE and key-b does not:

```
-- PG (correct):
s1: NOTICE: upd: key-a = key-a: t      -- key-a WHERE pass
s1: NOTICE: upk: val-a <> mismatch: t  -- key-a WHERE pass (2nd condition)
s1: NOTICE: trigger: rep_b_u BEFORE    -- BEFORE trigger for key-a
s1: NOTICE: upd: key-b = key-a: f      -- key-b WHERE fail
s1: NOTICE: trigger: rep_a_u AFTER     -- AFTER trigger (deferred)

-- goopg before fix (wrong):
s1: NOTICE: upd: key-a = key-a: t
s1: NOTICE: upk: val-a <> mismatch: t
s1: NOTICE: upd: key-b = key-a: f      -- key-b scanned before BEFORE trigger fires!
s1: NOTICE: trigger: rep_a_u AFTER     -- BEFORE trigger missing entirely
```

## Root Cause

The two-phase scan deferring BEFORE triggers to Phase 2 means all WHERE-condition
side effects (RAISE NOTICE via `noisy_oper`) for all rows complete before any BEFORE
triggers fire.

## Fix

For RC non-inheritance rows, Phase 1's scan callback now:

1. **Resolves concurrent updates (EPQ)** inline: detects concurrent xmax, waits,
   re-fetches the latest row via `epqFollowHOT`/`epqFollowChain`, and re-evaluates
   SET expressions against the post-EPQ row. This also fires WHERE-condition NOTICEs
   for the re-evaluated row (via `epqFollowHOT`'s predicate check), matching PG's
   behavior where noisy WHERE re-evaluation appears after the wait.

2. **Fires BEFORE trigger inline** after EPQ resolution, before moving to the next row.

3. **Sets `pendingUpdate.beforeFired = true`** so Phase 2 skips BEFORE trigger firing
   for these rows (avoiding double-fire).

Phase 2 retains its EPQ retry loop as a safety net (handles RR/SSI + inheritance +
any concurrent update that arrives between Phase 1 and Phase 2). For RC non-inheritance
rows whose EPQ was already resolved in Phase 1, Phase 2's `isConcurrentlyUpdated`
returns false on the first check and the write proceeds immediately.

## Scope

- RC non-inheritance rows only; inheritance children retain Phase 2 handling.
- RR/SERIALIZABLE rows: Phase 1 EPQ is skipped; Phase 2 returns 40001 as before.
- AFTER triggers: still deferred to Phase 2 (correct PG semantics).
- The two-phase write deferral is preserved: no rows are written during Phase 1,
  avoiding the re-scan-own-writes problem.

## Files

- `internal/executor/operators_storage.go`: `pendingUpdate.beforeFired`, Phase 1
  inline EPQ + BEFORE trigger block, Phase 2 `!pu.beforeFired` guard.
