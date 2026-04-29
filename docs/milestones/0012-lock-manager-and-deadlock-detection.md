# Milestone 0012 — Lock Manager Foundation and PostgreSQL-Style Deadlock Detection

**Status:** planned
**Depends on:** Milestone 0001 (foundational server process model and
transaction shell), Milestone 0002 (concurrent storage and B-tree
correctness baseline), and Milestone 0004 (TAP-style multi-session test
infrastructure).
**Drives:** Correct SQL-level blocking semantics under concurrent sessions,
deterministic deadlock resolution behavior, and first-class lock-wait
observability for future concurrency features.

## Context

goopg currently relies on local Go synchronization primitives at subsystem
boundaries (buffer/content latches, goroutine coordination) but does not yet
expose a PostgreSQL-style SQL lock manager for transaction-visible lock waits
and lock conflict arbitration.

The codebase already defines SQLSTATE `40P01` (`deadlock_detected`) in
`internal/sqlstate/codes.go`, but no lock wait-for graph construction or
deadlock detection path currently produces this error in query execution.

This milestone introduces a lock manager baseline and makes deadlock detection
the first mandatory compatibility slice, using PostgreSQL's approach as the
reference model (wait queues, wait-for graph traversal, cycle detection, and
single-victim cancellation).

## In Scope

### Lock Manager Core (v0 Surface)

- Introduce lock table primitives keyed by lock tag (relation-level at minimum
  for this milestone), plus mode-based compatibility checks.
- Implement lock acquisition/release APIs usable by executor and catalog DDL/
  DML paths.
- Track lock ownership and waiter state per backend/session in a shape suitable
  for deadlock detection.

### Wait Queue and Wakeup Semantics

- Add blocking lock acquisition behavior for conflicting lock requests,
  including explicit waiter enqueue/dequeue lifecycle.
- Ensure wakeup behavior after lock release is deterministic and starvation-safe
  for this milestone's supported lock modes.
- Provide clear cancellation paths for interrupted waits so waiter cleanup does
  not leak queue membership.

### PostgreSQL-Style Deadlock Detection

- Build and traverse wait-for edges from current waiter/holder state when
  lock waits exceed deadlock-check trigger conditions.
- Detect cycles involving two or more backends and resolve by selecting a
  victim backend/transaction to abort from the cycle.
- Surface deadlock cancellation as SQLSTATE `40P01` to clients, with a message
  shape aligned to PostgreSQL expectations for debugging.
- Guarantee lock and waiter cleanup after victim cancellation so non-victim
  sessions can continue and complete.

### Executor Integration and Regression Coverage

- Integrate lock acquisition in representative executor/catalog paths where
  concurrent access can conflict under this milestone's lock model.
- Add deterministic multi-session tests (TAP and/or Go integration) that
  reproduce deadlocks and verify victim selection, rollback, and survivor
  progress.
- Add non-deadlock contention tests to prove waits resolve correctly without
  false-positive deadlock errors.

### Observability

- Add internal counters/log events for lock waits, deadlock checks, cycles
  detected, and victims canceled.
- Expose enough diagnostics to support test assertions and operator triage.

## Out of Scope

- Serializable isolation predicate locking (SSI) and predicate-deadlock
  handling.
- Advisory locks and cross-database lock namespaces.
- Distributed deadlock detection across multiple nodes.
- Full parity with every PostgreSQL lock tag/mode combination in one pass.
- Lock-timeout UX beyond what is strictly required for deadlock detection
  correctness in this milestone.

## Required Design Docs

Place under `docs/design/` with sequential numbering at creation time:

- `0012-0001-lock-manager-architecture.md` — lock tag model, lock-mode
  compatibility matrix (v0 subset), waiter lifecycle, and backend state
  ownership.
- `0012-0002-deadlock-detection-algorithm.md` — wait-for graph derivation,
  cycle-detection procedure, victim selection policy, SQLSTATE/reporting
  contract, and cleanup invariants.
- `0012-0003-lock-wait-integration-and-test-matrix.md` — executor integration
  points, reproducible deadlock scenarios, non-deadlock contention cases,
  and observability assertions.

These design docs should cross-link to:
`docs/design/root-0001-architecture-overview.md`,
`docs/design/0002-0002-btree-concurrency.md`, and
`docs/design/0004-0001-go-test-utility-library.md`.

## Reference

Upstream sources to consult:

- `postgres/src/backend/storage/lmgr/lock.c` — lock acquisition, grant, and
  wait queue behavior.
- `postgres/src/backend/storage/lmgr/proc.c` — backend wait-state lifecycle
  and wakeup/cancellation mechanics.
- `postgres/src/backend/storage/lmgr/deadlock.c` — wait-for graph traversal,
  cycle handling, and deadlock resolution strategy.
- `postgres/src/backend/storage/lmgr/lmgr.c` and
  `postgres/src/include/storage/lock.h` — lock manager API and core structs.

## Definition of Done

1. A lock manager API exists in goopg and is exercised by at least one
   conflict-capable executor/catalog path using relation-level lock tags.
2. Conflicting lock requests block and later wake correctly when blockers
   release, with no leaked waiter entries.
3. A deterministic two-session deadlock test reproduces a cycle and returns
   SQLSTATE `40P01` to exactly one victim, while the surviving session can
   continue.
4. A multi-edge deadlock scenario (three or more sessions) is covered by
   regression tests and resolves without cluster-wide stall.
5. Non-deadlock contention tests confirm no false-positive `40P01` under
   normal wait/unblock flows.
6. Cancellation/timeout/interruption paths cleanly remove pending wait edges
   and preserve lock table consistency.
7. Observability counters/logs for wait and deadlock events are test-assertable
   and documented.
8. All required design docs (`0012-0001` to `0012-0003`) are merged with
   status `accepted`.