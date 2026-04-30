# Milestone 0022 - PostgreSQL-Compatible `pg_stat_activity` Support

**Status:** planned
**Depends on:** Milestone 0001 (core server/session lifecycle), Milestone 0012 (lock manager and lock-wait behavior), Milestone 0018 (EXPLAIN and instrumentation baselines), Milestone 0021 (`SELECT ... FOR UPDATE` lock-wait paths).
**Drives:** PostgreSQL-style backend activity introspection (`pg_stat_activity`) with practical wait-event visibility and stable operational diagnostics.

## Context

goopg currently lacks a PostgreSQL-compatible `pg_stat_activity` surface for backend/session introspection. This creates observability gaps for:

- identifying active or blocked sessions,
- correlating lock waits with SQL text and transaction state,
- diagnosing stall sources in executor, lock manager, networking, WAL, and background loops.

This milestone introduces `pg_stat_activity` support in staged form:

- Stage A: catalog/view shape, backend lifecycle/state tracking, and query/transaction timing fields.
- Stage B: wait-event tracking and publication (`wait_event_type`, `wait_event`) with names kept as close as practical to PostgreSQL naming.

The objective is operational parity for common troubleshooting workflows while keeping implementation boundaries explicit and testable.

## In Scope

### System Catalog and View Surface

- Add PostgreSQL-compatible `pg_catalog.pg_stat_activity` relation/view in the supported subset.
- Expose core columns used by monitoring and debugging tools in this milestone scope, including:
  - `datid`, `datname`, `pid`, `leader_pid`,
  - `usename`, `application_name`, `client_addr`, `client_hostname`, `client_port`,
  - `backend_start`, `xact_start`, `query_start`, `state_change`,
  - `wait_event_type`, `wait_event`, `state`,
  - `backend_xid`, `backend_xmin`,
  - `query`, `backend_type`.
- Document any intentionally deferred columns as explicit follow-up work.

### Backend Activity State Model

- Introduce per-backend activity slots similar to PostgreSQL backend status tracking.
- Track backend lifecycle transitions:
  - startup,
  - idle,
  - active,
  - idle in transaction,
  - idle in transaction (aborted),
  - fastpath function call (if applicable in goopg scope),
  - disabled/unknown fallback states where required.
- Track and publish timing transitions for `backend_start`, `xact_start`, `query_start`, and `state_change`.

### Query Text and Session Metadata Publishing

- Record current query text for active backends with bounded memory strategy.
- Preserve deterministic behavior for query truncation/length limits in this milestone scope.
- Publish session metadata (`application_name`, user/database identity, client endpoint) at connection startup and update points.

### Wait Event Taxonomy and Recording Hooks

- Add `wait_event_type` and `wait_event` tracking infrastructure in backend activity state.
- Keep wait-event names as PostgreSQL-compatible as practical, preferring upstream names when semantic equivalents exist.
- Define initial supported wait-event-type families for goopg:
  - `Lock`
  - `LWLock` (or documented subset equivalent)
  - `IO`
  - `Client`
  - `IPC`
  - `Timeout`
  - `Activity`.
- Implement wait-event recording at code-base integration points where waits actually occur, including at minimum:
  - lock waits in lock manager conflict paths,
  - client socket read/write blocking in server connection loops,
  - latch/channel/select waits in background service loops,
  - WAL writer/checkpointer wait loops,
  - AIO completion/blocking waits where available.
- Enforce start/end wait recording discipline so stale wait events are not leaked after wakeup.

### SQL and Permission Semantics

- Support `SELECT` from `pg_stat_activity` in standard SQL execution paths.
- Implement PostgreSQL-like visibility/redaction policy for non-superusers in scoped form (for example query text masking or restricted rows), with deterministic behavior documented in design docs.
- Preserve stable SQLSTATE behavior for permission failures or unsupported fields.

### Concurrency, Performance, and Safety

- Ensure activity sampling/read paths avoid global stop-the-world behavior under high session counts.
- Use lock ordering and memory ownership patterns that avoid deadlocks and races in activity snapshot reads.
- Define bounded overhead targets for activity updates in hot paths.

### Testing and Operability

- Add parser/catalog/executor tests validating `pg_stat_activity` presence and column contract.
- Add multi-session tests for state transitions and timing-field updates.
- Add lock-wait and non-lock wait tests validating `wait_event_type`/`wait_event` transitions and cleanup.
- Add compatibility tests comparing representative PostgreSQL query patterns and expected row semantics.

## Out of Scope

- Full parity with every PostgreSQL activity/statistics column in one pass.
- Cross-node/global activity views for distributed or sharded topologies.
- Historical activity retention beyond current-session snapshot semantics.
- Full statistics collector redesign beyond activity tracking needed for this milestone.

## Required Design Docs

Place under docs/design with sequential numbering at creation time:

- `0022-0001-pg-stat-activity-catalog-and-column-contract.md`
- `0022-0002-backend-status-lifecycle-and-snapshot-model.md`
- `0022-0003-wait-event-taxonomy-and-recording-hooks.md`
- `0022-0004-visibility-permissions-and-regression-matrix.md`

These design docs should cross-link to:

- `docs/design/root-0010-parser.md`
- `docs/design/root-0011-planner.md`
- `docs/design/root-0012-executor.md`
- `docs/design/0012-0001-lock-manager-architecture.md`
- `docs/design/0012-0003-lock-wait-integration-and-test-matrix.md`
- `docs/design/0018-0003-explain-analyze-instrumentation.md`
- `docs/design/0009-0004-aio-observability.md`

## Reference

Upstream sources to consult:

- `postgres/src/backend/utils/activity/backend_status.c`
- `postgres/src/backend/utils/adt/pgstatfuncs.c`
- `postgres/src/backend/utils/activity/wait_event.c`
- `postgres/src/include/utils/wait_event.h`
- `postgres/src/include/utils/wait_event_names.txt`
- `postgres/src/backend/storage/lmgr/lock.c`
- `postgres/src/backend/storage/lmgr/proc.c`
- `postgres/src/backend/storage/ipc/procsignal.c`
- `postgres/src/backend/postmaster/checkpointer.c`

## Definition of Done

### Stage A Gate (Initial Release)

1. `pg_catalog.pg_stat_activity` exists and is queryable in the supported SQL path.
2. Core identity/session/state columns are populated for active backends with deterministic nullability rules.
3. Backend lifecycle transitions (`idle`, `active`, transaction-idle variants) are reflected correctly.
4. `backend_start`, `xact_start`, `query_start`, and `state_change` are updated consistently for supported execution flows.
5. Query text publication follows documented truncation and visibility behavior.
6. Required design docs `0022-0001` and `0022-0002` are merged with status `accepted`.

### Stage B Gate (Milestone Accepted)

7. `wait_event_type` and `wait_event` are published with PostgreSQL-compatible naming wherever semantic equivalents exist.
8. Wait events are recorded at lock manager, client I/O, latch/select/background-loop, WAL, and AIO wait points in scoped code paths.
9. Wait-event start/end hygiene prevents stale wait events after wakeup or operation completion.
10. Permission/visibility semantics for `pg_stat_activity` are enforced as documented for superuser and non-superuser sessions.
11. Regression and multi-session compatibility suites for state and wait-event behavior are green.
12. Required design docs `0022-0003` and `0022-0004` are merged with status `accepted`.
