# Milestone 0005 — Streaming Replication Support

**Status:** planned
**Depends on:** Milestone 0001 (foundational server), Milestone 0002 (WAL durability and checkpointing foundations).
**Blocks:** High-availability and failover-focused milestones.

## Context

To align goopg more closely with PostgreSQL operational expectations, the project needs physical streaming replication support. This milestone introduces a primary/standby architecture where WAL generated on the primary is continuously streamed and replayed on one or more standbys. The objective is correctness and operability first: replication should be reliable, observable, and restart-safe before performance tuning.

## In Scope

### WAL Sender / Receiver Pipeline

- Primary-side WAL sender process that streams WAL records to standby nodes over the PostgreSQL wire protocol.
- Standby-side WAL receiver process that persists incoming WAL and coordinates replay.
- Proper backpressure behavior when standbys lag or disconnect.

### Standby Recovery and Replay

- Startup in standby mode with recovery configuration equivalent to PostgreSQL's current behavior.
- Continuous WAL replay with consistent recovery state tracking.
- Reconnect and catch-up behavior after transient network failures.
- Crash-safe restart of standby replay from durable positions.

### Replication Slots and Retention Safety

- Physical replication slot support sufficient to prevent WAL removal required by connected standbys.
- WAL retention behavior that is safe for replication lag and bounded by configuration.
- Visibility into retained WAL caused by replication slots.

### Observability and Operations

- System views and status fields required to inspect replication health (for example, sender/receiver/replay status and lag indicators).
- Replication-related logging with actionable context for disconnections, replay pauses, and WAL retention pressure.
- Basic operational controls for starting, stopping, and validating replication in test environments.

## Out of Scope

- Logical replication and publication/subscription flows.
- Automatic failover, leader election, or cluster management tooling.
- Synchronous replication guarantees and quorum commit policies (deferred to a later milestone unless minimally required for compatibility tests).
- Multi-primary or bidirectional replication.

## Required Design Docs

- `0005-0001-streaming-replication-architecture.md` — process model, protocol flow, and state transitions.
- `0005-0002-standby-recovery-and-replay.md` — durable replay, restart semantics, and consistency model.
- `0005-0003-replication-observability.md` — system views, metrics, and operational diagnostics.

## Definition of Done

1. A primary and a standby can be started from clean state, establish replication, and keep streaming under sustained write workload.
2. Standby replay state remains consistent across stop/start and crash-restart scenarios.
3. WAL retention is safe for lagging standbys via replication-slot-aware behavior, with configuration documented and test-covered.
4. At least one end-to-end replication test verifies primary writes become visible on standby in order and without data loss after reconnect.
5. Replication health is inspectable through documented status surfaces (views and/or commands) that provide sender, receiver, and replay progress.
6. All required design docs are merged with status `accepted`.
