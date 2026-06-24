# 0118-0006 — Soft-deadlock wait-queue reordering (M0118-0004 slice: deadlock-soft, deadlock-soft-2)

Status: accepted

## Problem

Two isolation specs describe deadlock *cycles that PostgreSQL resolves without
rolling anyone back*, by rearranging a lock's wait queue:

- **`deadlock-soft`** — four sessions form a 4-cycle with **two hard edges** and
  **two soft edges**:
  - `d1` holds `a1` (ACCESS SHARE) and waits on `a2` (ACCESS SHARE); `d2` holds
    `a2` (ACCESS SHARE) and waits on `a1` (ACCESS SHARE).
  - `e1` waits on `a1` (ACCESS EXCLUSIVE, behind holder `d1`); `e2` waits on `a2`
    (ACCESS EXCLUSIVE, behind holder `d2`).
  - The graph: `d2 → e1` (soft: `d2`'s ACCESS SHARE is queued behind `e1`'s
    conflicting ACCESS EXCLUSIVE request on `a1`), `e1 → d1` (hard: `e1` waits for
    holder `d1`), `d1 → e2` (soft, on `a2`), `e2 → d2` (hard). The detector
    **reverses the `d1 → e2` soft edge** (moves `d1` ahead of `e2` in `a2`'s
    queue). `d1`'s ACCESS SHARE is compatible with holder `d2`'s ACCESS SHARE, so
    `d1` is granted immediately, unblocking the whole chain. **Nobody errors.**

- **`deadlock-soft-2`** — `s1` (`deadlock_timeout = '10ms'`) blocks for SHARE
  UPDATE EXCLUSIVE on `a2` behind **two** ACCESS EXCLUSIVE waiters `s3` and `s4`
  that are themselves hard-blocked on `s2`'s ACCESS SHARE holder. `s1`'s request
  does **not** conflict with the holder, so `s1` must *jump over both* `s3` and
  `s4` — a topological reorder of a multi-waiter queue, not a single-edge swap.

goopg's detector ([0012-0002](0012-0002-deadlock-detection-algorithm.md),
generalised in [0118-0005](0118-0005-general-deadlock-timeout-detection.md)) only
ever built **hard** edges (waiter → conflicting *holder*) and only ever resolved a
cycle by **cancelling a victim**. It had no notion of a soft (queue-order) edge and
no queue-rearrangement path. For both specs the hard-edge-only graph is acyclic
(`e1→d1`, `e2→d2` for `deadlock-soft`; `s3→s2`, `s4→s2`, `s2→s1` for
`deadlock-soft-2`), so the detector found nothing, both blocked waiters parked
forever, and the runner reported `d1a2` / `s1b` as `<... timed out waiting>`.

## Change

Mirror upstream's `src/backend/storage/lmgr/deadlock.c` soft-edge machinery in
`internal/lockmgr/deadlock.go` (lock groups omitted — goopg has no parallel-query
lock groups). All of it runs under the existing coarse `lm.mu`, on the
**timer-driven path only** (`runDeadlockCheckFor`, the firing backend `prefer != 0`);
the synchronous `CheckDeadlocksNow` path (`prefer == 0`, unit tests) keeps the
legacy hard-edge-only youngest-victim search, now factored into
`checkDeadlockHardOnlyLocked`.

New pieces (each named after its deadlock.c analogue):

- **`findLockCycle(start, waitOrders)`** (`FindLockCycle` / `FindLockCycleRecurse`)
  — DFS outward from `start`. For the proc being visited it first follows **hard**
  edges (holders whose mask conflicts with the proc's requested mode), then **soft**
  edges (procs *ahead of it* in the lock's wait queue whose pending request
  conflicts). A revisit of index 0 closes a cycle *through `start`*; any other
  revisit is a cycle not involving the start and is ignored. It returns the soft
  edges contained in the cycle (empty ⇒ pure hard cycle). It honours the
  hypothetical per-lock orderings in `waitOrders` in preference to the true FIFO
  order.
- **`testConfiguration(start, cur)`** (`TestConfiguration`) — expands the current
  constraint set into wait orderings, then checks for cycles from each constraint
  endpoint and from `start`. Returns `-1` (hard deadlock / inconsistent), `0`
  (deadlock-free), or `>0` (soft cycles remain; the soft edges are candidates to
  reverse next).
- **`deadLockCheck(start)`** (`DeadLockCheckRecurse`) — depth-first search over
  constraint sets: try reversing each available soft edge as an added constraint
  until a deadlock-free configuration is found. Returns `(resolved, solution)`:
  `resolved=false` ⇒ hard deadlock (roll back `start`); `resolved=true` with an
  empty solution ⇒ no cycle involving `start`; a non-empty solution ⇒ the set of
  soft edges to reverse.
- **`expandConstraints` / `topoSort`** (`ExpandConstraints` / `TopoSort`) — for each
  lock named by the solution, rebuild its wait-queue order so every constraint's
  *waiter precedes its blocker*, minimising disturbance to the existing FIFO order
  (emit from the back, repeatedly choosing the highest-index proc with no remaining
  before-constraints). Returns `false` on contradictory constraints.
- **`applyWaitOrders(orders)`** — rewrite the affected `lockState.waiters` slices to
  the solved order (preserving the existing `*Waiter` pointers so parked goroutines
  still receive their `signal`), then run `wakePassLocked` on each so any waiter the
  new order makes grantable is promoted. **No victim is cancelled.**

`checkDeadlockLockedFor(prefer)` ties it together: for `prefer != 0` it runs
`deadLockCheck(prefer)`; a hard cycle cancels `prefer` (unchanged from
[0118-0005](0118-0005-general-deadlock-timeout-detection.md), matching PG's
"roll back the session that ran the check"); a soft cycle calls
`expandConstraints` + `applyWaitOrders`; no cycle is a no-op.

### Worked example (`deadlock-soft`)

`d2`'s 10 ms timer fires first. `findLockCycle(d2)` discovers the cycle
`d2 → e1 → d1 → e2 → d2` with soft edges `{d1→e2 on a2}` and `{d2→e1 on a1}`.
`deadLockCheck` adds the constraint "`d1` before `e2` on `a2`"; `topoSort` reorders
`a2`'s queue `[e2, d1]` → `[d1, e2]`; `testConfiguration` now finds **no** cycle, so
the solution is that single constraint. `applyWaitOrders` rewrites `a2`'s queue and
`wakePassLocked` grants `d1` (its ACCESS SHARE is compatible with holder `d2`).
`d1a2` completes; on `d1c` the releases cascade (`e1` then `d2a1` then `e2`),
reproducing the expected output exactly. `deadlock-soft-2` is the same machinery
with a 3-element queue (`s1` topologically sorted ahead of both `s3` and `s4`).

## Scope / non-goals

- Hard multi-object cycles still roll back the firing backend
  ([0118-0005](0118-0005-general-deadlock-timeout-detection.md)); `deadlock-hard`
  remains green byte-for-byte.
- The row-lock `xmax` / `WaitForXID` wait graph
  (`tuplelock-upgrade-no-deadlock`, `multixact-no-deadlock`) is invisible to
  `lockmgr` and remains a future slice, as does `deadlock-parallel` (lock groups).
- The soft search is exercised only on the timer path; `CheckDeadlocksNow` is
  deliberately unchanged so the existing youngest-victim unit tests still pin the
  legacy behaviour.

## Oracle

`postgres/src/backend/storage/lmgr/deadlock.c` — `DeadLockCheck`,
`DeadLockCheckRecurse`, `TestConfiguration`, `FindLockCycle`,
`FindLockCycleRecurse(Member)`, `ExpandConstraints`, `TopoSort`. Behaviour compared
against `./postgres/local_install` PG 18.3 via the ported specs.

## Verification

- `TestPort_IsolationDeadlockSoft` and `TestPort_IsolationDeadlockSoft2` PASS
  byte-for-byte vs PG 18.3.
- Regression: `TestPort_IsolationDeadlockHard`, `DeadlockSimple`, `LockNowait`,
  `TuplelockUpdate` still PASS.
- `go test -race ./internal/lockmgr/...` green (all hard-cycle / linear-chain /
  youngest-victim unit tests unchanged).
- `go build ./...`, `gofmt`, `go vet ./internal/lockmgr/` clean.
- CSV rows `deadlock-soft{,-2}` `failed`→`pass`; coverage + inventory regenerated
  (isolation pass 54→56).
