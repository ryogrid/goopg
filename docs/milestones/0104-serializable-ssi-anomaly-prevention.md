# Milestone 0104 — SERIALIZABLE Isolation via SSI Anomaly Prevention

**Status:** planned
**Filed:** 2026-05-14
**Depends on:** M0012 (lock manager + deadlock detection foundation), M0100-0001 (RR/Serializable snapshot semantics), M0096/M0100 isolation-test harness maturity
**Reference plan:** `.ralph/fix_plan.md` (M0104 section)

## Goal

When a session configures `default_transaction_isolation` or
`transaction_isolation` to `serializable`, goopg must prevent serialization
anomalies using Serializable Snapshot Isolation (SSI), rather than treating
SERIALIZABLE as a synonym of REPEATABLE READ.

Target behavior follows PostgreSQL's model:

- Same GUC names and user-facing enum values as upstream.
- Snapshot semantics aligned with PostgreSQL transaction-level behavior.
- Predicate-lock + rw-conflict tracking to detect dangerous structures.
- Transaction abort with SQLSTATE `40001` on serialization failure.

## In Scope

1. **GUC parity for isolation-level controls**
   (`default_transaction_isolation`, `transaction_isolation`) with
   PostgreSQL-compatible enum values and SHOW/SET behavior.
2. **Serializable transaction state management** tied to transaction lifecycle
   (registration at first serializable snapshot, cleanup on commit/abort).
3. **Predicate lock substrate (SIREAD)** for relation/page/tuple (and index
   range-target abstraction needed for phantom prevention).
4. **SSI conflict tracking** on read path (`conflict-in`) and write path
   (`conflict-out`) with explicit rw-edge bookkeeping.
5. **Pre-commit serialization-failure check** that raises SQLSTATE `40001`
   when a dangerous structure requires abort.
6. **Test-port promotion for serializable coverage** by moving applicable
   deferred isolation specs to pass-required once the blocker is removed.

## Out of Scope

- Full PostgreSQL-am parity for every predicate lock target in the first slice
  (for example, GIN/GiST/hash-specific lock nuances can remain staged).
- Read-only deferrable transaction optimization (`SERIALIZABLE READ ONLY
  DEFERRABLE`) in the initial delivery.
- Cross-node/distributed serializable scheduling.

## Definition of Done

1. `SET default_transaction_isolation = 'serializable'` and
   `SET TRANSACTION ISOLATION LEVEL SERIALIZABLE` drive a real SSI execution
   path (not REPEATABLE READ aliasing).
2. At least one known serializable anomaly pattern (write-skew / dangerous
   rw-cycle shape) is deterministically rejected with SQLSTATE `40001`.
3. Read/write conflict edges are tracked and released correctly through
   transaction commit/abort, with no lock leakage.
4. Applicable deferred isolation tests for SERIALIZABLE/SSI are promoted and
   passing in `internal/testport`.
5. Regression gates for existing RC/RR behavior remain green.

## Required Design Docs

Under `docs/design/`:

- `0104-0001-serializable-ssi-foundation.md` — architecture baseline for GUC
  parity, predicate locks, rw-conflict graph, and pre-commit abort policy.

## PostgreSQL References

This milestone follows upstream naming and SSI model from:

- `postgres/src/backend/utils/misc/guc_tables.c`
  (`default_transaction_isolation`, `transaction_isolation`,
  `max_predicate_locks_per_xact`, `max_predicate_locks_per_relation`,
  `max_predicate_locks_per_page`).
- `postgres/src/backend/access/transam/xact.c`
  (`DefaultXactIsoLevel`, `XactIsoLevel`, transaction lifecycle hooks).
- `postgres/src/backend/storage/lmgr/predicate.c`
  (`CheckForSerializableConflictIn`, `CheckForSerializableConflictOut`,
  `FlagRWConflict`, `PreCommit_CheckForSerializationFailure`).
- `postgres/src/include/storage/predicate.h` and
  `postgres/src/include/storage/predicate_internals.h`.
- `postgres/doc/src/sgml/mvcc.sgml` (Serializable Snapshot Isolation semantics).
