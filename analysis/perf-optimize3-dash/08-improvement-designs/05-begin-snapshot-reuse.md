# 08-05 — Lazy first-statement snapshot for single-statement transactions

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-tpch,
D-002 isolation, G-perf → [README](README.md)

## 1. Problem and numbers

`BEGIN` costs 0.229 ms on goopg `-N` vs PG's 0.085 ms — 2.7× (06-01), the
third-ranked write-path fix (06-02 #3). `captureSnapshot` is **5.4 % of `-N` CPU**
at scale 100 (06-02) and 4.4 % of `-N` CPU at scale 500 (07-02); on the read
path it is 2.4 % of `-S` CPU (06-03). PostgreSQL takes the transaction snapshot
**lazily at the first statement** that needs it, not eagerly at `BEGIN`; a
read-committed single-statement transaction can often reuse one snapshot for its
lone statement, and a `BEGIN` with no following data statement pays nothing.

## 2. Current-code map (verified at `a640d2b0`)

- **`Manager.captureSnapshot()`** — `internal/mvcc/manager.go:1321`: builds the
  MVCC snapshot (active-xid scan). Called per statement / per transaction start.
- The `-N` transaction's `BEGIN` (simple protocol) drives transaction setup that
  includes an eager snapshot; each subsequent statement in a read-committed
  transaction also re-captures (correct for RC — each statement sees a fresh
  snapshot).

## 3. PostgreSQL reference

- `src/backend/utils/time/snapmgr.c` — `GetTransactionSnapshot()` returns the
  already-taken snapshot or takes it on first call;
  `src/backend/storage/ipc/procarray.c` — `GetSnapshotData()` is the active-xid
  scan. In READ COMMITTED, each command calls
  `GetTransactionSnapshot` which re-takes; in REPEATABLE READ the first one is
  frozen. The snapshot is **not** taken at `StartTransaction` — it is deferred
  to first need.

## 4. Target design

- **Defer snapshot capture from `BEGIN` to first data statement.** A `BEGIN`
  followed immediately by `COMMIT`/`ROLLBACK` (or a `BEGIN` tag with no data
  statement, as in the extended-protocol no-op case) never captures a snapshot.
- **Reuse within a single-statement autocommit transaction.** The autocommit
  path (one statement = one transaction) captures exactly one snapshot and uses
  it for that statement; avoid a redundant transaction-level + statement-level
  capture.

### Decision log

- **D1 — preserve READ COMMITTED semantics.** Each *statement* in a multi-
  statement RC transaction must still see a fresh snapshot; the optimization is
  (a) skip the eager BEGIN-time capture, (b) avoid double-capture in the
  single-statement case. It must NOT freeze a snapshot across statements in RC.
- **D2 — REPEATABLE READ / SERIALIZABLE unchanged.** Those freeze at first
  statement already; the lazy capture matches their semantics exactly.

## 5. Invariants and failure modes

- **I1 — visibility unchanged.** A statement sees exactly the snapshot it sees
  today; only the *timing* of the first capture moves (BEGIN → first statement).
  D-002 isolation specs are the guard.
- **F1 — a statement that needs a snapshot before "first data statement".**
  Some setup (e.g. `SET TRANSACTION`, catalog lookups) may implicitly need a
  snapshot; the lazy path must capture on first *actual* need, not assume the
  first data statement is the first need.

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | lazy capture | move snapshot capture from BEGIN to first-need; single-statement path captures once. | G-race, D-002 |
| S2 | perf acceptance | `-N` BEGIN latency drops toward PG's; `captureSnapshot` CPU share falls. | G-perf, G-tpch |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| snapshot capture / visibility | `internal/mvcc/manager_test.go`, `snapshot_clog_fallback_test.go` | S1 |
| isolation (RC per-statement freshness, RR freeze) | D-002 isolation suite | S1 |
| TPC-H spotcheck | `scripts/tpch-spotcheck.sh` | S1 |

## 8. Performance verification

`run_rw50.sh` `-N`: BEGIN per-statement latency (0.229 ms) drops toward PG's
0.085; `captureSnapshot` `-N` CPU share (4.4–5.4 %) falls. No isolation regression.

## 9. Open questions

- **O-BS-1** — Interaction with doc 09: the extended-protocol explicit-txn model
  changes when transactions begin; coordinate the lazy-capture point so both
  paths defer identically.
- **O-BS-2** — Does goopg capture a snapshot for the `BEGIN` no-op tag today
  (extended path), and does doc 09's change already remove that? Confirm no
  double-fix.
