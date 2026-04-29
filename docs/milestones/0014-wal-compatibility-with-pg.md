# Milestone 0014 — PostgreSQL-Compatible WAL On-Disk Format

**Status:** planned
**Depends on:** Milestone 0001 (foundational WAL writer/recovery seam), Milestone 0007 (segment preallocation and durability semantics), Milestone 0010 (WAL direct I/O and walsender handoff), Milestone 0013 (wal_buffers and WAL-before-data guardrails).
**Drives:** Binary WAL-format compatibility with PostgreSQL tooling and operational interoperability, while preserving goopg durability and recovery guarantees.

## Context

goopg currently uses a simplified WAL record frame and custom payload-kind encoding.  
That implementation is sufficient for internal durability/recovery, but it is not byte-compatible with PostgreSQL WAL internals.

This milestone introduces PostgreSQL-compatible WAL on-disk framing as a first-class contract:

- XLOG page/header semantics compatible with a defined upstream major target.
- XLogRecord-compatible record headers, chaining, and CRC coverage.
- Segment identity and naming conventions compatible with PostgreSQL expectations.
- Validation with upstream WAL tooling, especially pg_waldump, as a hard acceptance gate.

Correctness remains first priority: WAL-before-data durability, crash-recovery safety, and streaming order must not regress.

## In Scope

### PostgreSQL-Compatible XLOG Page and Segment Layout

- Adopt PostgreSQL-compatible WAL segment identity and filename layout (timeline/log/segment encoding).
- Emit page headers with upstream-compatible magic/flags semantics for the selected target major.
- Support long/short page-header rules and continuation behavior at page boundaries.

### PostgreSQL-Compatible Record Header and Chaining

- Replace the v0 length+crc frame with XLogRecord-compatible header fields and chaining rules.
- Preserve xl_prev linkage invariants across records and segment boundaries.
- Match upstream CRC algorithm and coverage boundaries.

### Resource-Manager Mapping for Existing goopg WAL Operations

- Map goopg’s current durable operations (checkpoint, heap insert/delete/vacuum, btree insert/split, page-image style fallback where required) to a PostgreSQL-compatible record classification strategy.
- Define explicit behavior for operations not yet representable in this milestone (clear, deterministic errors instead of silent format drift).

### Reader, Recovery, and Streaming Integration

- Update WAL reader/iterator/replay paths to decode and apply the new format.
- Maintain crash-recovery idempotency and replay ordering guarantees.
- Preserve existing walsender and walreceiver ordering semantics while using the new on-disk bytes.

### Bootstrap, Compatibility Guardrails, and Rollout

- New clusters created after this milestone use the compatible format by default.
- Existing clusters with legacy goopg WAL format fail fast with actionable diagnostics unless an explicit migration path is applied.
- Provide operator-facing rollout notes and downgrade/rollback constraints.

### Validation and Observability

- Add compatibility tests that verify pg_waldump can parse emitted WAL on the target upstream major.
- Extend WAL observability surfaces to report WAL format mode/version and compatibility status at runtime.
- Keep current durability and replication regression suites green.

## Out of Scope

- Full pg_upgrade compatibility for pre-milestone clusters.
- Cross-major PostgreSQL WAL binary compatibility in one pass.
- New logical-replication protocol features unrelated to physical WAL framing.
- WAL compression/encryption redesign.
- Replacing existing durability policy (fsync/fdatasync direct-I/O policy remains semantically unchanged).

## Required Design Docs

Place under docs/design with sequential numbering at creation time:

- 0014-0001-xlog-page-and-segment-layout-compat.md
- 0014-0002-xlogrecord-header-and-rmgr-mapping.md
- 0014-0003-recovery-streaming-and-compat-validation.md
- 0014-0004-rollout-guardrails-and-operator-playbook.md

These design docs should cross-link to:

- docs/design/root-0008-wal-and-recovery.md
- docs/design/0007-0001-wal-segment-preallocation.md
- docs/design/0007-0002-fdatasync-commit-path.md
- docs/design/0010-0001-wal-direct-io-write-path.md
- docs/design/0013-0001-wal-buffers-architecture.md

## Reference

Upstream sources to consult:

- postgres/src/include/access/xlogrecord.h
- postgres/src/include/access/xlog_internal.h
- postgres/src/backend/access/transam/xlog.c
- postgres/src/backend/access/transam/xlogreader.c
- postgres/src/bin/pg_waldump/

## Definition of Done

1. WAL segment filenames and page headers are emitted in PostgreSQL-compatible form for the selected target major.
2. WAL records use XLogRecord-compatible header/chaining/CRC semantics, including boundary continuation behavior.
3. goopg WAL can be parsed by upstream pg_waldump for representative workloads in CI.
4. Recovery from cold start and crash-restart remains correct under the new format (including multi-segment and boundary-spanning records).
5. Existing WAL-producing paths (heap, btree, checkpoint, and replication-adjacent paths that read physical WAL bytes) remain functionally correct.
6. WAL-before-data durability invariants remain enforced under normal flush, checkpoint flush, and eviction-triggered flush.
7. Direct-I/O and wal_buffers paths remain compatible with the new WAL record/page framing and show no correctness regression.
8. Legacy WAL-format detection and guardrail messages are implemented and documented.
9. Runtime observability exposes WAL format mode/version and compatibility diagnostics.
10. All required design docs (0014-0001 through 0014-0004) are merged with status accepted.