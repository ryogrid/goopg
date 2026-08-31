# 0012-0002 — Deadlock Detection Algorithm

**Status:** accepted
**Milestone:** [0012 — Lock Manager and Deadlock Detection](../../milestones/0012-lock-manager-and-deadlock-detection.md)
**Spans seam:** wait-for graph derivation, cycle detection, victim
selection, SQLSTATE 40P01 reporting contract.
**Cross-links:**
[0012-0001](0012-0001-lock-manager-architecture.md) (lock manager
core surface — holders/waiters live there),
upstream `postgres/src/backend/storage/lmgr/deadlock.c`.

## Context

The lock manager from M0012-0001 already tracks holders and waiters
per `LockTag`. A waiter never becomes a holder until a Release
wake-pass demotes its blockers, so the graph "who is waiting on
whom" is fully derivable from `lm.states`. This slice adds the
periodic detector that walks that graph, finds cycles, picks a
victim, and signals the victim's `Acquire` call to abort with
SQLSTATE `40P01` (`deadlock_detected`).

## Wait-for graph

For each waiter `w` parked on tag `t` requesting mode `w.Mode`, the
detector emits an outgoing edge from `w.Backend` to every holder `h`
of any mode that conflicts with `w.Mode` on `t`:

```
edge w.Backend -> h    iff   h ∈ holders[t]
                        and  ConflictsWith(w.Mode, holders[t][h])
                        and  h != w.Backend
```

Self-edges are excluded — a backend doesn't deadlock with itself
(the lock manager's `grantedExcept(b)` check guarantees self-
compatibility).

The graph has at most O(W × H) edges where W is the number of
waiters and H the average number of holders per contended tag. For
v0's modest contention this is small; the detector walks it under
the lock manager's main mutex.

## Cycle detection

Standard iterative DFS with three colours per backend:

- **white**: unvisited
- **grey**: on the current DFS stack
- **black**: fully explored, no cycle found

A grey-on-grey edge is a back-edge → the cycle is the path from the
target grey node to the current node, plus the closing edge. The
detector returns the cycle as a slice of `BackendID` so the caller
can pick a victim.

DFS starts from each waiter in turn; backends with no outbound edges
(holders that aren't waiting on anything) are never roots. This
keeps the search confined to the actively-contending subgraph.

## Victim selection

v0 picks **the youngest backend in the cycle** — defined as the one
with the highest `BackendID` numeric value. The lock manager's
`BackendID` issuance is the executor's job (M0012-0003) and is
expected to be monotonically increasing per session, so "highest ID"
correlates with "started most recently" — the same heuristic
upstream uses (cancel the youngest xact in the cycle so older work
is preserved).

This is intentionally simpler than upstream's "soft-edge / hard-
edge" lock-strength tie-breaking — that machinery exists to make
victim selection prefer cycles caused by *upgradable* lock
escalations (RowShare → ExclusiveLock) over hard cycles. v0 doesn't
yet have upgradable locks, so the simpler "highest ID" rule is
correct for the modes we expose.

## Cancellation contract

When the detector chooses victim `v`:

1. Locate `v`'s waiter struct in `lm.states[t].waiters` for whichever
   tag it's blocked on (a backend can wait on at most one tag at a
   time — `Acquire` is the only point that parks).
2. Splice the waiter out of the queue.
3. Send a `cancellation signal` on the waiter's signal channel that
   distinguishes "you were promoted" from "you're the victim". The
   simplest encoding is a separate `cancel chan struct{}` per
   waiter, selected in the `Acquire` goroutine alongside `signal`
   and `ctx.Done()`.
4. The victim's `Acquire` returns `ErrDeadlockDetected` (a sentinel
   exported by the package); higher layers translate that to
   SQLSTATE `40P01` with a message of the form
   `"deadlock detected"` for the client.

After the cancellation send, the victim's goroutine takes `lm.mu`,
notices its waiter has already been spliced, and runs the same
"release any partial holdings" cleanup the cancellation path uses.
Net effect: no leaked waiter rows, no leaked holder rows, no
double-free even if the victim's context cancels concurrently.

The releaser-style wake-pass also runs after the victim is removed
so any waiters behind the victim get a chance to advance.

## When does the detector run?

`Acquire` schedules a check after a configurable delay
(`deadlockTimeout`, defaulting to 1 second to mirror upstream's
`deadlock_timeout` GUC). Implementation: when a goroutine parks on
its `signal` chan, it also sets a `time.AfterFunc(deadlockTimeout,
lm.runDeadlockCheck)`. Only the **first** still-parked goroutine
needs to fire — but since each fires its own timer, the detector
must be idempotent and cheap to re-run. The detector takes `lm.mu`,
walks once, and returns; concurrent runs serialise on the mutex
without harm.

For tests, the package exposes a synchronous
`CheckDeadlocksNow()` so test cases can drive the detector
deterministically without sleeping for a second.

## Algorithm pseudocode

```
runDeadlockCheck():
    lm.mu.Lock()
    defer lm.mu.Unlock()

    // 1. Build the waiter -> waiting-on adjacency.
    edges := map[BackendID][]BackendID{}
    for tag, st := range lm.states:
        for w in st.waiters:
            for h, hMask in st.holders:
                if h == w.Backend: continue
                if ConflictsWith(w.Mode, hMask):
                    edges[w.Backend].append(h)

    // 2. DFS for a cycle starting from every waiter.
    cycle := findCycle(edges)
    if cycle == nil: return

    // 3. Pick victim = max-BackendID in cycle.
    victim := max(cycle)

    // 4. Splice victim out of every waiter queue + signal.
    cancelVictim(victim)
```

## Out of scope (deferred)

- `lock_timeout` (separate from `deadlock_timeout`) — see
  M0012-0003 if needed.
- Predicate locks / SSI deadlocks — out of M0012 entirely.
- Graceful "soft-cancel" that retries the lock once the conflicting
  holder releases — v0 just aborts the victim.
- Cross-database locks (LockTag is single-DB).
- Distributed deadlock detection.

## Tests

- `TestDeadlockDetectsTwoSessionCycle`: classic A↔B cycle.
- `TestDeadlockDetectsThreeSessionCycle`: A→B→C→A multi-edge cycle.
- `TestDeadlockNoCycleNoCancel`: long but cycle-free wait chain →
  detector returns without cancelling.
- `TestDeadlockVictimGetsSentinel`: the cancelled `Acquire` returns
  `ErrDeadlockDetected`, not `context.Canceled` or nil.
- `TestDeadlockVictimReleasesPartialHoldings`: the victim's existing
  holdings on other tags are dropped so survivors can proceed.
- `TestDeadlockSurvivorMakesProgress`: after victim cancellation the
  remaining backend's `Acquire` returns successfully.
- `TestDeadlockYoungestBackendIsVictim`: pin the policy.
- `TestDeadlockTimerSchedulesCheck`: the configured 1-second timer
  fires the detector when no synchronous trigger is used.
