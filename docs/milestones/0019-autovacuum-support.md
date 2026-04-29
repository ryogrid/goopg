# Milestone 0019 - Autovacuum Support

**Status:** planned
**Depends on:** Milestone 0001 (core runtime and storage), Milestone 0006 (planner statistics plumbing), Milestone 0012 (lock manager and deadlock behavior), Milestone 0016 in docs/design (VACUUM and ANALYZE baseline semantics).
**Drives:** Automatic dead-tuple cleanup and statistics refresh in steady-state operation, without requiring manual VACUUM/ANALYZE workflows.

## Context

goopg currently has manual VACUUM and ANALYZE pathways, but no background autovacuum scheduler. In practical PostgreSQL-style operation this creates two problems:

- dead tuples accumulate between manual maintenance runs,
- table statistics drift, causing planner quality to degrade over time.

This milestone adds an autovacuum launcher and workers with PostgreSQL-compatible operational behavior for the supported subset, including anti-wraparound safety.

## In Scope

### Launcher and Worker Architecture

- Add a long-lived autovacuum launcher goroutine that periodically evaluates candidate relations.
- Add autovacuum worker goroutines with bounded concurrency.
- Implement launcher-to-worker handoff with deterministic worker selection and cancellation-safe lifecycle handling.

### Vacuum/Analyze Trigger Policy

- Implement autovacuum trigger formulas in the supported subset:
  - vacuum threshold + scale factor,
  - analyze threshold + scale factor,
  - insert-heavy table vacuum trigger where supported.
- Support global GUC-driven defaults and per-table overrides (reloptions subset).
- Respect table-level disable switches for autovacuum/analyze where configured.

### Anti-Wraparound Safety

- Implement freeze-age driven anti-wraparound autovacuum in the supported subset.
- Ensure anti-wraparound workers run even when regular autovacuum is disabled for a relation.
- Emit clear warnings/errors as xid age approaches configured safety boundaries.

### Locking, Concurrency, and Cancellation

- Integrate worker lock acquisition with existing lock manager semantics.
- Ensure workers back off or cancel deterministically on conflicting DDL/lock pressure according to the supported policy.
- Guarantee worker termination on shutdown and no orphan background work after postmaster-equivalent stop.

### Cost and Throughput Controls

- Add supported autovacuum cost controls:
  - cost delay,
  - cost limit,
  - worker count cap,
  - launcher nap interval.
- Ensure settings are enforceable at runtime and applied per worker.

### Observability and Operator Surface

- Expose autovacuum progress/status for active workers (relation, phase, tuples scanned/removed where available).
- Expose per-table last autovacuum/autoanalyze timestamps in supported stats views.
- Emit structured event logs for launcher wakeups, worker start/stop, cancellation, and anti-wraparound invocations.

### Regression and Integration Testing

- Add tests for scheduling thresholds, worker dispatch, and per-table option overrides.
- Add concurrency tests for lock conflicts and worker cancellation.
- Add anti-wraparound tests that force age-based trigger paths in deterministic harnesses.

## Out of Scope

- Full PostgreSQL autovacuum feature parity in one pass.
- Parallel vacuum internals beyond bounded multi-worker table selection.
- Full visibility map/freeze map parity where not yet implemented in goopg storage internals.
- Every autovacuum-related stats view field from upstream.
- Fine-grained I/O throttling parity beyond listed cost controls.

## Required Design Docs

Place under docs/design with sequential numbering at creation time:

- `0019-0001-autovacuum-launcher-worker-architecture.md`
- `0019-0002-trigger-formulas-and-reloptions.md`
- `0019-0003-anti-wraparound-and-freeze-policy.md`
- `0019-0004-autovacuum-observability-and-test-matrix.md`

These design docs should cross-link to:

- `docs/design/root-0016-vacuum-and-analyze.md`
- `docs/design/root-0004-configuration-and-guc.md`
- `docs/design/0012-0001-lock-manager-architecture.md`
- `docs/design/0012-0003-lock-wait-integration-and-test-matrix.md`

## Reference

Upstream sources to consult:

- `postgres/src/backend/postmaster/autovacuum.c`
- `postgres/src/backend/commands/vacuum.c`
- `postgres/src/backend/access/heap/vacuumlazy.c`
- `postgres/src/include/postmaster/autovacuum.h`
- `postgres/src/include/commands/vacuum.h`

## Definition of Done

1. Autovacuum launcher runs continuously and dispatches workers according to configured scheduling intervals.
2. Automatic VACUUM and ANALYZE triggers execute using configured thresholds and scale factors for the supported subset.
3. Per-table autovacuum reloptions override global defaults where supported.
4. Anti-wraparound autovacuum triggers at configured xid-age boundaries and is not skipped by regular disable flags.
5. Worker lock-conflict and cancellation behavior is deterministic and covered by regression tests.
6. Runtime knobs for worker count, nap interval, and vacuum cost controls are effective and test-covered.
7. Supported stats/log surfaces expose active worker status and per-table maintenance recency.
8. Shutdown path cleanly stops launcher and workers with no background task leaks.
9. Required design docs `0019-0001` through `0019-0004` are merged with status `accepted`.
