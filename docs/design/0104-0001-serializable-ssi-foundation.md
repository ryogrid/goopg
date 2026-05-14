# 0104-0001 — Serializable (SSI) Foundation and GUC Parity

**Status:** in-progress (M0104-0001 landed 2026-05-14)
**Date:** 2026-05-14
**Milestone:** M0104
**Tracks:** `.ralph/fix_plan.md` M0104-0001..M0104-0006

## Problem

goopg currently accepts `serializable` in SQL/GUC surfaces but maps it to the
same runtime behavior as REPEATABLE READ. This blocks PostgreSQL-compatible
Serializable Snapshot Isolation (SSI) and allows anomaly classes that
SERIALIZABLE must reject.

The implementation target is PostgreSQL-style SSI: snapshot isolation with
predicate locks and rw-conflict graph checks, rejecting dangerous structures
with SQLSTATE `40001`.

## Goals

1. Keep PostgreSQL-compatible user-facing GUC names and enum values for
   transaction isolation controls.
2. Ensure SERIALIZABLE transactions use an SSI path, not RR aliasing.
3. Add predicate-lock and conflict-tracking infrastructure needed to prevent
   serialization anomalies.
4. Define pre-commit failure policy and transactional cleanup contracts.

## Non-Goals (First Slice)

- Full parity for every upstream predicate-lock target specialization in one
  step.
- Read-only deferrable optimization in the initial delivery.
- Distributed serializable protocols.

## PostgreSQL Baseline to Follow

### GUC and isolation surface

- `postgres/src/backend/utils/misc/guc_tables.c`
  - `default_transaction_isolation`
  - `transaction_isolation`
  - `max_predicate_locks_per_xact`
  - `max_predicate_locks_per_relation`
  - `max_predicate_locks_per_page`
- `postgres/src/backend/access/transam/xact.c`
  - `DefaultXactIsoLevel`
  - `XactIsoLevel`

### SSI core

- `postgres/src/backend/storage/lmgr/predicate.c`
  - `GetSerializableTransactionSnapshot`
  - `CheckForSerializableConflictIn`
  - `CheckForSerializableConflictOut`
  - `FlagRWConflict`
  - `PreCommit_CheckForSerializationFailure`
- `postgres/src/include/storage/predicate.h`
- `postgres/src/include/storage/predicate_internals.h`
- `postgres/doc/src/sgml/mvcc.sgml` (`xact-serializable` section)

## Proposed goopg Architecture

### 1. Isolation-level and GUC parity

- Preserve PostgreSQL GUC names and accepted textual values.
- Stop mapping `serializable` to `IsolationRepeatableRead` in mvcc parsing.
- Route SERIALIZABLE transactions through SSI registration/cleanup hooks.

**M0104-0001 status (landed 2026-05-14):**

- `mvcc.IsolationSerializable` added as a distinct enum constant; the
  `(IsolationLevel).String()` round-trip now emits `"serializable"` rather
  than `"repeatable read"` for SERIALIZABLE transactions.
- `mvcc.ParseIsolationLevel("serializable")` returns
  `IsolationSerializable` (no longer aliased to `IsolationRepeatableRead`).
  Upstream weakening of READ UNCOMMITTED → READ COMMITTED is preserved.
- `mvcc.Manager.Begin` accepts the new enum and stamps it onto the
  returned `Transaction.Isolation`, so the SSI-aware code paths (M0104-0002+)
  can branch on a single source of truth.
- `mvcc.Manager.SnapshotFor` reuses the REPEATABLE READ pinned-snapshot
  branch for `IsolationSerializable` deliberately: this is the SI
  half of SSI; the predicate-lock / rw-edge overlay will be added on
  top in M0104-0003..0005 without changing the snapshot acquisition
  contract.
- `executor.BasicSession.SetIsolationLevel` accepts
  `IsolationSerializable`, so the BEGIN ISOLATION LEVEL / SET TRANSACTION
  ISOLATION LEVEL plan paths round-trip the new tag onto the open
  transaction.
- `default_transaction_isolation` and `transaction_isolation` GUCs
  already accepted `"serializable"` in `internal/config/defaults.go`
  (PG enum parity, EnumOptions); no GUC-layer change was required.

Regression coverage:

- `internal/mvcc/manager_test.go::TestParseIsolationLevel` asserts the new
  parse result.
- `internal/mvcc/manager_test.go::TestSerializableDistinctFromRepeatableRead`
  pins enum distinctness, `String()` parity, `Begin` acceptance, and the
  RR-style pinned snapshot semantics for the first slice.
- `internal/executor/transaction_test.go::TestTransactionBeginSerializableSession`
  pins the executor BEGIN path tagging the active transaction with
  `IsolationSerializable`.

### 2. Serializable transaction state

Introduce per-transaction SSI state, conceptually analogous to upstream
`SERIALIZABLEXACT`:

- transaction identity and lifecycle state
- rw-conflict incoming/outgoing edges
- predicate-lock ownership references
- commit sequence metadata for conflict decisions

State is created when a serializable snapshot is acquired and cleaned up on
transaction finish.

### 3. Predicate lock manager (SIREAD)

Add a predicate-lock subsystem with target granularity:

- relation
- page
- tuple
- range abstraction for index-backed predicate protection

Include coarsening/escalation rules under memory pressure and expose limits via
PostgreSQL-compatible GUC names (`max_predicate_locks_per_*`).

### 4. Conflict tracking hooks

- **Read path (`conflict-in`)**: when a SERIALIZABLE reader touches a protected
  target that a concurrent writer modified.
- **Write path (`conflict-out`)**: when a SERIALIZABLE writer modifies a target
  with active SIREAD coverage by concurrent serializable transactions.

Both paths add explicit rw edges used by pre-commit dangerous-structure checks.

### 5. Pre-commit serialization failure check

Before commit of a SERIALIZABLE transaction, evaluate dangerous-structure
criteria on current conflict graph and abort with SQLSTATE `40001` when needed.

Policy goal: match PostgreSQL user-visible behavior (serialization failure and
retry expectation), while allowing staged internal implementation details.

### 6. Cleanup contracts

On commit/abort:

- release predicate locks owned by the transaction
- retire conflict-graph participation safely
- keep bookkeeping bounded for long-lived systems

## Error Semantics

- Serialization anomaly prevention failures must surface as SQLSTATE `40001`
  (`serialization_failure`).
- Existing deadlock path (`40P01`) remains distinct.

## Verification Plan

1. Unit tests for GUC/isolation mapping to ensure SERIALIZABLE is distinct from
   RR in execution path selection.
2. Targeted anomaly test cases (write skew / rw-cycle shapes) that must raise
   `40001`.
3. Conflict-graph and predicate-lock lifecycle tests (no leakage, deterministic
   cleanup).
4. Promotion of applicable deferred isolation specs from D-002 once blockers are
   removed.

## Rollout Notes

Implementation is intentionally staged in `M0104-0001..0008` so correctness
lands before broad optimization. The first acceptable release is correctness-
first: detect and reject anomalies; optimize lock granularity and contention
paths in follow-up slices without changing SQL-visible semantics.
