# Executable Isolation Tests (READ COMMITTED, REPEATABLE READ, SERIALIZABLE)

This document describes the upstream PostgreSQL isolation spec suite as an
execution-porting target for goopg, across **all** transaction isolation levels
goopg implements — not just READ COMMITTED.

## Implemented isolation levels (source of truth)

goopg implements three of the four SQL isolation levels with distinct snapshot
semantics (READ UNCOMMITTED is folded to READ COMMITTED, matching PostgreSQL):

- **READ COMMITTED** — a fresh statement-level snapshot per command.
  `internal/mvcc/manager.go` `SnapshotFor` (the `IsolationReadCommitted` branch
  re-captures on every call).
- **REPEATABLE READ** — a single transaction-level snapshot pinned at the first
  non-BEGIN statement and reused for the whole transaction.
  `internal/mvcc/manager.go` `SnapshotFor` (the `IsolationRepeatableRead` branch
  pins `firstSnap`); level parsing in `internal/mvcc/snapshot.go`
  `ParseIsolationLevel`; surfaced via `SET TRANSACTION` / `BEGIN ISOLATION LEVEL`
  and the `default_transaction_isolation` GUC.
- **SERIALIZABLE** — the REPEATABLE READ snapshot plus real Serializable Snapshot
  Isolation (SSI): predicate (SIREAD) locks, rw-conflict tracking, and a
  pre-commit dangerous-structure check that raises SQLSTATE `40001`.
  `internal/mvcc/ssi.go`, `ssi_conflict.go`, `ssi_precommit.go`, `predlock.go`;
  executor wiring in `internal/executor/ssi.go` and the pre-commit hook in
  `internal/executor/operators_tx.go` (`execCommit`). Delivered by milestone
  M0104 (building on the M0100 READ COMMITTED suite).

Because all three levels are implemented, the **entire** upstream isolation suite
is in scope — specs that begin transactions at REPEATABLE READ or SERIALIZABLE,
or that assert serialization failures, are first-class targets rather than being
excluded.

## How the specs execute

The multi-session isolation scheduler in
`internal/testport/framework/isolation_runner.go` runs each spec's permutations
against goopg at whatever isolation level the spec itself declares (per-session
`setup` / `BEGIN ISOLATION LEVEL …` / `SET default_transaction_isolation`). It
implements PostgreSQL `isolationtester` semantics: per-session dedicated
connections, step goroutines, blocking detection, completion-marker (`<…>`)
handling, and per-session NOTICE capture. Entry points are `TestPort_Isolation*`
in `internal/testport/isolation_port_test.go`; upstream specs live in
`postgres/src/test/isolation/specs/*.spec`. Output is normalized against vanilla
PostgreSQL 18.3 (`./postgres/local_install`); any divergence is a goopg bug.

## Coverage and current status

The authoritative, per-spec status is the generated
[`upstream-isolation-coverage.md`](upstream-isolation-coverage.md) (rendered from
`postgres-oracle-target-inventory.csv` by `cmd/gen-isolation-coverage`). As of
this writing the 121 upstream isolation specs classify as:

- **pass** — output matches the PG 18.3 oracle.
- **failed** — in scope and targeted, but not yet matching (either attempted with
  a known goopg gap, or newly brought in scope now that REPEATABLE READ and
  SERIALIZABLE exist). Driving these to pass is tracked by milestone **M0118**.

There are no longer any `not-tried` isolation specs: the suite that was parked
while only READ COMMITTED was supported is now fully targeted.

## Representative specs by isolation-level relevance

The full set is in the coverage doc; the table below highlights specs whose
behavior is specifically tied to each level, to guide staged porting.

| spec | isolation-level relevance |
| ---- | ------------------------- |
| `eval-plan-qual.spec` | EvalPlanQual recheck under READ COMMITTED concurrent updates. |
| `eval-plan-qual-trigger.spec` | RC and RR EPQ paths for trigger behavior, side by side. |
| `lock-committed-keyupdate.spec` | Locking of committed key updates; outcome differs across levels. |
| `lock-committed-update.spec` | Explicit RC / RR / SERIALIZABLE comparison of committed-update locking. |
| `insert-conflict-do-update*.spec` | ON CONFLICT DO UPDATE MVCC effects (RC-specific permissions and RR/SER conflicts). |
| `insert-conflict-specconflict.spec` | Speculative-insertion conflict handling under a configured isolation level. |
| `fk-snapshot.spec` | RC vs RR FK snapshot permutations (RR raises `40001`). |
| `partition-key-update-*.spec` | Concurrent partition-key updates across isolation levels. |
| `merge-*.spec` | MERGE update/delete/insert/recheck/join conflict scheduling. |
| `drop-index-concurrently-1.spec` | Result differs at READ COMMITTED vs stricter levels. |
| `simple-write-skew.spec`, `matview-write-skew.spec` | SERIALIZABLE write-skew anomalies; expect `40001` under SSI. |
| `read-only-anomaly*.spec` | SERIALIZABLE read-only anomaly detection. |
| `read-write-unique*.spec` | SERIALIZABLE rw-unique conflicts via predicate locks. |
| `serializable-parallel*.spec` | SERIALIZABLE behavior with parallelism. |
| `two-ids.spec`, `total-cash.spec`, `receipt-report.spec`, `project-manager.spec`, `classroom-scheduling.spec` | Classic SSI dangerous-structure scenarios. |
| `predicate-gin.spec`, `predicate-gist.spec`, `predicate-hash.spec`, `predicate-lock-hot-tuple.spec` | Predicate-lock granularity per access method. |

## Notes

- This document is the human-facing scope overview; the machine-maintained,
  per-spec source of truth is `postgres-oracle-target-inventory.csv` (and its
  rendered `upstream-isolation-coverage.md`).
- For execution planning, the targeted set splits naturally into:
  - READ COMMITTED correctness (EPQ, lock-committed, ON CONFLICT, MERGE).
  - REPEATABLE READ snapshot/serialization-failure behavior.
  - SERIALIZABLE / SSI anomaly prevention (write-skew, read-only anomaly,
    rw-unique, predicate-lock access methods, parallel SSI).
  - DDL / VACUUM / row-locking / FK concurrency that is level-independent but was
    parked alongside the isolation suite.
- Milestone **M0118** decomposes the work to bring the targeted specs to `pass`.
