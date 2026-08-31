# 0118-0020 — VACUUM uses the HOT-chain / multixact-aware prune (M0118-0009 slice: freeze-the-dead)

Status: accepted
Milestone: M0118-0009 (upstream isolation spec suite pass-through — misc/system specs)
Spec promoted: `postgres/src/test/isolation/specs/freeze-the-dead.spec` `failed` → `pass`
Test: `TestPort_IsolationFreezeTheDead` (`internal/testport/isolation_port_test.go`)

## Problem

`freeze-the-dead.spec` exercises the interaction of tuple freezing with dead and
recently-dead tuples that carry a `FOR KEY SHARE` multixact whose oldest member is
the *updater*. The scenario:

1. `s1` updates `id=3` (non-key column `x`), then `s2` and `s3` take `FOR KEY SHARE`
   on the same row → the old tuple's `xmax` becomes a MultiXactId
   `{s1-updater, s2-keyshare, s3-keyshare}` (a no-key update is compatible with KEY
   SHARE, so the lockers join rather than wait).
2. `s1` updates again, creating a third HOT chain version.
3. `s1` and `s2` commit; `s3` stays open holding its key share.
4. `s2` runs `VACUUM FREEZE`, then `s1` does an **index-only-eligible** `SELECT id=3`.

Expected (PG 18.3): the index scan still finds the live row `3|333|2`, and after
`s3` commits + a second `VACUUM FREEZE`, `SELECT * ORDER BY name,id` returns both
rows. The spec guards against a historical upstream freezing bug that broke HOT
chains / revived dead rows.

goopg diverged: after the first `VACUUM FREEZE` the index scan returned **0 rows**,
and the final select was **missing `id=3`** entirely.

## Root cause — sibling-path divergence

goopg has two dead-tuple reclamation paths:

- **Opportunistic prune** (`storage.PagePruneOpt`, taken on the HOT-update
  page-full path) — already correct: it resolves an updater-bearing multixact
  `xmax` to its updater before the horizon compare, and converts a dead **HOT
  chain root** to an `ItemIDRedirect` pointing at the live chain tip so the
  index entry keeps resolving.
- **VACUUM** (`vacuum.vacuumCore`) — naive. Its `isDead` was literally
  `h.Xmax != Invalid && h.Xmax < horizon`, and it fed the dead set straight to
  `VacuumHeapPageBySlots`, which **physically removes** the line pointers.

Two bugs compounded in the VACUUM path:

1. **Category error on multixact xmax.** The chain root's `xmax` is a *MultiXactId*
   (a small integer in a separate number space), not a transaction id. Comparing it
   to the xid horizon spuriously marked the root "dead".
2. **No HOT-chain awareness.** Even granting the root is removable, physically
   deleting the root line pointer (which the B-tree index points at) orphans the
   rest of the chain — the live tip `v3` becomes unreachable by index scan, and the
   row disappears.

This is the recurring *sibling-paths-must-agree* failure mode: `PagePruneOpt` was
fixed for multixacts + HOT but its VACUUM twin `vacuumCore` was not.

## Fix

Unify the two paths on the already-correct kernel.

- `internal/storage/prune.go`: extract the shared reclamation kernel
  `pagePruneCore(p, oldestXmin)` (the multixact/HOT-aware dead-set build →
  redirect dead roots / mark HOT-only + standalone tuples unused → compact →
  clear `pd_prune_xid`). `PagePruneOpt` keeps its `pd_prune_xid` fast-path gate and
  delegates to the kernel; new **`PageVacuumPrune`** calls the kernel
  **unconditionally** (VACUUM must prune regardless of the opportunistic hint).
  The kernel now also returns the surviving-LP_NORMAL count for `Stats.Live`.
- `internal/vacuum/vacuum.go`: `vacuumCore` replaces its
  `CollectDeadHeapSlots` + `VacuumHeapPageBySlots` + `LogHeapVacuum` block with
  `storage.PageVacuumPrune` + the **`LogHeapPruneOpt`** WAL record
  (`RecordKindHeapPruneOpt`, which already carries `redirects`+`unused` and has a
  replay path — so crash/standby recovery reproduces the redirects bit-for-bit).
  Index-vacuum `DeadTIDs` are now collected only from the fully-removed `Unused`
  line pointers; redirected roots keep their valid index entry, and HOT-only
  removed tuples have no index entry (a no-op removal).

### Why it now matches PG

First `VACUUM FREEZE` (with `s3` still running): the root `v1`'s multi resolves to
updater `s1` (committed, `< horizon`), so the chain root is removable, but
`PageVacuumPrune` redirects `v1 → v3` (the live tip) and marks the middle `v2`
unused. The index entry on `v1` now redirects to `v3`, so `s1`'s index scan reads
`3|333|2`. The freeze pass then only sees the live `v3`. After `s3` commits and the
second `VACUUM FREEZE`, the page still holds `v3` (live) plus the `v1` redirect, so
the final seqscan returns both rows. Byte-identical to PG 18.3.

## Blast radius

VACUUM is high-blast-radius, so the change is deliberately a *unification* onto an
existing, tested kernel rather than new pruning logic. Behaviour for non-HOT,
non-multixact deletes is unchanged (they remain standalone `Unused` removals with
the same `Stats.Dead`/`Stats.Live` accounting — confirmed by the existing
`internal/vacuum` unit tests). The WAL record kind for VACUUM page-prunes changes
from `RecordKindHeapVacuum` to `RecordKindHeapPruneOpt`; both already had replay
support, and the prune-opt record is a strict superset (it can also carry
redirects). No on-disk page-format change.

## Gates

- `TestPort_IsolationFreezeTheDead` PASS (both spec halves byte-match PG 18.3).
- `go test -race ./internal/storage/... ./internal/vacuum/... ./internal/wal/... ./internal/mvcc/...` — green.
- `go test ./internal/executor/...` (prune/freeze/fsm/vacuum) — green.
- Isolation regression batch (multixact-no-forget, delete-abort-savept{,-2},
  aborted-keyrevoke, lock-committed-{update,keyupdate}, inplace-inval) — green.
- pgbench CI-parity smoke (pre-commit hook) — 0 failed.

## Follow-ups / deferred

Unchanged from the M0118-0009 ledger: lock carry-forward on the non-HOT update
paths (delete+insert / `UPDATE…FROM` / MERGE / upsert) remains a bounded follow-up,
not exercised by any current `port` spec. The remaining M0118-0009 misc specs
(async-notify, stats, horizons, intra-grant-inplace{,-db}, prepared-transactions,
temp-schema-cleanup, subxid-overflow) are untouched.
