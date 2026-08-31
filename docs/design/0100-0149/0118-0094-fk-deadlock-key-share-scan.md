# 0118-0094 — `fk-deadlock.spec` PROMOTED: FK existence scan made FOR KEY SHARE-aware (M0118-0005)

**Status:** accepted
**Milestone / spec:** M0118-0005 (FK / referential-integrity concurrency) — isolation
spec `fk-deadlock`
**Date:** 2026-06-25
**Suite row:** D-002 (`docs/test-port/postgres-oracle-port-status.csv`)

## Summary

`fk-deadlock.spec` is now promoted `defer` → **`pass`** (pass-required, hard-asserted
via `runIsoSpecStrict` in `TestPort_IsolationFkDeadlock`), byte-identical to PostgreSQL
18.3 across all 14 permutations. The fix is a one-function change in the FK existence
scan plus two small helpers — no new subsystem.

## The spec

Two sessions each `INSERT` a child row that references the *same* parent row
(`parent_key = 1`), then each issues a **no-key** `UPDATE` of the parent
(`UPDATE parent SET aux = ...`; `aux` is not a key column). The 14 permutations
interleave the inserts, updates, and commits in every legal order.

Expected behaviour (PG 18.3, `expected/fk-deadlock.out`): the **child INSERTs never
wait**. The only blocking that appears is between the two parent no-key `UPDATE`s when
they run concurrently — `s2u` (or `s1u`) shows `<waiting ...>` then `<... completed>`
after the other session commits. There are **no deadlock aborts** in the expected
output despite the spec's name.

## Root cause

A child `INSERT`'s FK check is the equivalent of `SELECT … FOR KEY SHARE` on the
referenced parent row. A `FOR KEY SHARE` lock conflicts **only** with a *key-changing*
modification of that row — a key `UPDATE` or a `DELETE` (upstream
`MultiXactStatusUpdate`) — and is **compatible** with a concurrent no-key `UPDATE`
(`StatusNoKeyUpdate`) and with pure row locks.

goopg's FK existence scan `scanRelForFKMatch` (`internal/executor/operators_fk.go`)
did not model that compatibility. It treated **any** in-flight, non-self,
non-lock-only `xmax` on the matched parent row as a conflict, recorded it as `pending`,
and made the caller (`scanTableForMatchFKWait`) block on it via `WaitForXID`. So when
one session had a still-in-flight no-key `UPDATE` of the parent, the other session's
child `INSERT` blocked where PostgreSQL proceeds.

Debug trace (`FKMATCH … xmax=8 multi=false lockonly=false active=true`) confirmed the
matched parent row carried a single-xid no-key-update `xmax` from the peer session, and
the scan classified it as a wait.

The sibling row-lock path (`lockRowsOp`, `operators_lockrows.go:739`) already does the
correct thing for explicit `SELECT … FOR KEY SHARE`:
`keyConflict := lockStrength == Excl || keysUpdated`. The FK scan was simply missing
the same key-awareness — a classic sibling-paths-out-of-sync gap.

## Fix

`scanRelForFKMatch` now computes whether the matched row's in-flight `xmax` is a
*key-changing* modification, and only treats a key-changing one as `pending`:

- **Single-xid `xmax`** (`fkXmaxIsKeyChanging`): key-changing iff `HEAP_KEYS_UPDATED`
  is set (a key UPDATE) **or** the tuple is a delete, detected structurally — goopg
  does not stamp `HEAP_KEYS_UPDATED` on DELETE, so a self-pointing / invalid `t_ctid`
  is the delete signal (the same test `lockRowsOp.chainMembers` uses, M0118-0003).
- **Updater-bearing multixact `xmax`** (`multixactUpdaterIsKeyChanging`): resolve the
  updater member's `Status`; key-changing iff it is `StatusUpdate` (key UPDATE /
  DELETE) rather than `StatusNoKeyUpdate`.
- **Lock-only `xmax` / multixact with no updater member:** never a conflict (pure
  lockers do not change the key or delete the row) — unchanged.

A matched row whose in-flight modification is a no-key `UPDATE` is now a **clean
match**: the FK is satisfied immediately (the referenced key is intact), so the child
`INSERT` does not wait. A key `UPDATE` or `DELETE` still records `pending` and waits,
then re-scans after the updater settles (raising 23503 if the key is gone) — the
existing M0100-0005q wait/retry behaviour is preserved.

With FK inserts no longer over-waiting, the only blocking left in the spec is the two
parent no-key `UPDATE`s serialising against each other, which rides goopg's existing
UPDATE-conflict path (no-key UPDATE vs no-key UPDATE = `ExclusiveLock` conflict). That
matches PG 18.3 exactly.

## Why goopg does not need a persistent KEY SHARE lock here

PostgreSQL's FK check leaves a `FOR KEY SHARE` lock on the parent row that persists to
commit; the spec's UPDATE waits are, in upstream, partly against those locks. goopg's
FK enforcement uses a visibility wait rather than a persistent tuple lock, but the
observable output is identical for this spec because: (a) `FOR KEY SHARE` is compatible
with both peers' `FOR KEY SHARE` and with a concurrent no-key UPDATE, so the lock would
never cause an INSERT to wait anyway; and (b) the no-key-UPDATE-vs-no-key-UPDATE
conflict already provides the only blocking the spec exercises. (The persistent-lock
design is a separate, larger change and is not required by any current `port` spec.)

## Files

- `internal/executor/operators_fk.go` — `scanRelForFKMatch` matched-row decision made
  key-aware; new helpers `fkXmaxIsKeyChanging` (single-xid) and
  `multixactUpdaterIsKeyChanging` (multixact updater member). Added `internal/multixact`
  import.
- `internal/testport/isolation_port_test.go` — new `TestPort_IsolationFkDeadlock`
  (`runIsoSpecStrict`); updated the now-stale "fk-deadlock remains deferred" note on
  `TestPort_IsolationFkDeadlock2`.
- `docs/test-port/postgres-oracle-port-status.csv` (+ regenerated `.md`) — D-002
  rationale: fk-deadlock promoted; three FK specs remain deferred.

## Bounded blast radius

The change only affects the FK existence scan's wait decision, and only narrows it:
the sole new behaviour is *not waiting* on a no-key updater (previously a wait). Every
existing conflict — key UPDATE, DELETE, and the wait/re-scan/23503 + moved-partition
paths — is unchanged. The structural-delete and multixact-updater classification reuse
the exact logic the row-lock path already trusts.

## Gates

- `TestPort_IsolationFkDeadlock` **strict PASS** (14/14 permutations byte-identical).
- FK sibling strict specs PASS, no regression: `FkDeadlock2`, `FkContention`,
  `ReferentialIntegrity`, `FkSnapshot`, `TemporalRangeIntegrity`.
- Row-lock / deadlock / multixact isolation batch PASS: `LockCommittedUpdate`,
  `LockCommittedKeyupdate`, `DeleteAbortSavept{,2}`, `AbortedKeyrevoke`,
  `MultixactNoForget`, `Deadlock{Simple,Hard,Soft,Soft2}`, `MultixactNoDeadlock`,
  `TuplelockUpgradeNoDeadlock`.
- `-race` `./internal/executor` (FK / lockrows / multixact / concurrent-update) PASS;
  `./internal/executor ./internal/multixact ./internal/mvcc` unit suites PASS.
- `go build ./...` clean; pgbench CI-parity smoke = pre-commit hook.
