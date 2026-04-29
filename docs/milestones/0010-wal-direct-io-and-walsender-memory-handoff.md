# Milestone 0010 — WAL Direct I/O Writes and In-Memory Walsender Handoff

**Status:** planned
**Depends on:** Milestone 0001 (foundational server and WAL writer),
Milestone 0005 (streaming replication process model with walsender /
walreceiver), Milestone 0007 (preallocated WAL segments and `fdatasync`-
based commit durability), Milestone 0009 (AIO subsystem boundaries and
I/O observability conventions).
**Drives:** Lower cache pollution on write-heavy systems, predictable WAL
write latency under sustained load, and replication streaming correctness
when WAL write paths bypass the OS page cache.

## Context

Milestone 0007 intentionally deferred `O_DIRECT` / direct-I/O WAL writes,
and Milestone 0009 kept replication paths on synchronous, page-cache-backed
I/O. This milestone closes that gap for WAL storage writes: both the
primary WAL writer and standby walreceiver WAL persistence paths gain a
Linux direct-I/O mode.

Direct-I/O changes a key assumption used by many systems: newly written WAL
bytes are no longer expected to be cheaply readable from the page cache.
If walsender depends only on disk reads from recently-written segments,
streaming behavior can regress under cache-cold or cache-bypassed
conditions. Therefore this milestone also includes a concrete in-memory WAL
data handoff path for walsender so active senders can stream recent WAL
without depending on disk-cache residency.

The objective is correctness first, then predictable performance: durability
and replication ordering semantics must remain equivalent to pre-milestone
behavior while introducing direct-I/O constraints (alignment, short-write
handling, and fallback behavior).

## In Scope

### WAL Writer Direct-I/O Path

- Add a WAL direct-I/O mode for Linux WAL segment writes on the primary.
- Ensure WAL write buffers satisfy `O_DIRECT` alignment constraints
  (address, length, and offset alignment), with explicit handling for tail
  writes that are not naturally aligned.
- Preserve existing durability semantics from Milestone 0007: commit success
  is still credited only after the required sync barrier succeeds.
- Keep directory and metadata durability steps (`fsync` where required)
  unchanged in meaning.

### Walreceiver Direct-I/O WAL Persistence

- Add the corresponding direct-I/O write path for walreceiver when storing
  streamed WAL on the standby.
- Preserve replay correctness and restart behavior across clean shutdown and
  crash-restart scenarios.
- Ensure walreceiver write-path errors and fallback events surface clearly in
  logs and status surfaces.

### In-Memory WAL Handoff to Walsender

- Implement a bounded in-memory WAL handoff mechanism so walsender can read
  recent WAL bytes from memory instead of relying exclusively on disk reads.
- Define retention and eviction rules by LSN window and active sender demand,
  including behavior when senders lag beyond the in-memory window.
- Keep a correctness-preserving disk fallback path for historical WAL ranges
  not available in memory.
- Preserve ordering and exactly-once stream framing semantics already defined
  by Milestone 0005 replication flow.

### Configuration and Compatibility

- Introduce a WAL direct-I/O configuration surface (GUC-style) with clear
  defaults and startup-time reporting.
- On platforms or filesystems where direct-I/O cannot be used safely,
  downgrade to buffered I/O with explicit operator-visible diagnostics.
- Maintain compatibility with existing segment format and recovery
  expectations.

### Observability

- Add counters and status fields for WAL direct-I/O writes, fallback events,
  and in-memory walsender handoff hits/misses.
- Add startup/runtime log lines indicating whether direct-I/O and in-memory
  handoff paths are active.

## Out of Scope

- Direct-I/O for heap/index relation files and non-WAL storage paths.
- Replication protocol redesigns unrelated to WAL transport semantics.
- Asynchronous durability-barrier redesign (`fsync` / `fdatasync` ordering
  remains semantically equivalent to existing behavior).
- Logical replication direct-I/O behavior changes.
- Windows-native direct-I/O parity in this milestone.

## Required Design Docs

Place under `docs/design/` with sequential numbering at creation time:

- `0010-0001-wal-direct-io-write-path.md` — Linux direct-I/O WAL write path
  for primary writer and walreceiver, alignment strategy, partial-write
  behavior, durability barriers, and fallback policy.
- `0010-0002-walsender-in-memory-wal-handoff.md` — in-memory WAL handoff
  architecture, retention/eviction, sender lag behavior, and disk fallback
  contract.
- `0010-0003-wal-direct-io-observability-and-operations.md` — counters,
  status views, logging, rollout defaults, and operator troubleshooting
  guidance.

These design docs should cross-link to:
`docs/design/root-0008-wal-and-recovery.md`,
`docs/design/0005-0001-streaming-replication-architecture.md`,
`docs/design/0007-0002-fdatasync-commit-path.md`, and
`docs/design/0009-0001-aio-core.md`.

## Reference

Upstream sources to consult:

- `postgres/src/backend/access/transam/xlog.c` — WAL write / sync behavior,
  segment lifecycle, and durability barriers.
- `postgres/src/backend/replication/walsender.c` — sender flow and WAL read /
  send behavior.
- `postgres/src/backend/replication/walreceiver.c` — receiver flow and WAL
  persistence path on standby.
- `postgres/src/include/access/xlog_internal.h` — WAL segment and LSN
  invariants.

## Definition of Done

1. With WAL direct-I/O mode enabled on Linux, WAL writer and walreceiver
   writes use the direct-I/O path (verified by syscall tracing and
   instrumentation), with alignment-safe behavior for all write sizes.
2. Commit and replay durability semantics remain correct: crash-restart tests
   pass without WAL corruption or lost committed transactions.
3. Walsender serves recent WAL from the in-memory handoff buffer when
   available, and streaming remains correct when page-cache residency is low
   or absent.
4. When requested WAL is outside the in-memory retention window, walsender
   falls back to disk reads without protocol or ordering regression.
5. Bounded-memory behavior is enforced under multiple concurrent standbys:
   retention limits, eviction behavior, and lagging-sender handling are
   tested and documented.
6. Direct-I/O unsupported environments fall back to buffered I/O with clear
   operator-visible diagnostics and without correctness regressions.
7. WAL direct-I/O and in-memory handoff counters / status surfaces are
   queryable and documented.
8. All required design docs (`0010-0001` to `0010-0003`) are merged with
   status `accepted`.
