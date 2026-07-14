# 08-05 — Lazy first-statement snapshot for single-statement transactions

status: **PARTIAL** (S1 landed; pre-loop/BEGIN capture elimination deferred) ·
date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-tpch, D-002 isolation,
G-perf → [README](README.md)

> **Landed 2026-07-14 (bounded, provably-safe subset):** in the RC per-statement
> branch of `server/dispatch.go`, the FIRST statement now **reuses the pre-loop
> snapshot** (`SnapshotFor(tx)` already taken for the Query message) instead of
> re-capturing — removing the redundant second `captureSnapshot` on the single-
> statement autocommit hot path (pgbench `-S`). Semantics identical (same RC
> command-start snapshot, microseconds apart, no intervening commit). Gates:
> server suite, isolation D-002 (`ReadWriteUnique1-4`, `FkSnapshot`,
> `ReadOnlyAnomaly1-3`), mvcc RC/RR timing, tpch-spotcheck Q12=2/Q13=33 — all green.
>
> **Deferred (ledgered):** eliminating the pre-loop `dispatch.go` capture entirely
> and the RC eager-BEGIN capture at `operators_tx.go` (the `-N` BEGIN-latency
> half). These require proving `ectx.Snap` is never read before the in-loop
> capture across *every* statement type (utility/RR-non-data), and untangling the
> pre-loop / `execBegin` / in-loop interaction incl. the stale-`tx`-after-promotion
> inconsistency — deeper multi-session verification than this run's gate battery
> covers. See `.ralph/deferral_ledger.md`.

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
  MVCC snapshot (active-xid scan), reached only via `Manager.SnapshotFor`
  (`manager.go:406`, the sole primitive): RC re-captures per call, RR/SSI pins
  `firstSnap` on the first call.
- **Correction (verified at HEAD):** `Manager.Begin` (`manager.go:249`) does
  **not** capture a snapshot — transaction-start is already snapshot-free at the
  MVCC layer. The eager capture lives in the **dispatch layer**: a pre-loop
  `SnapshotFor(tx)` per Query message (`server/dispatch.go`, stored into
  `ectx.Snap`), then a per-statement in-loop capture (RC refreshes each
  statement; RR/SSI pins the first data statement via `stmtTakesSnapshot`,
  `notify.go`). The RC eager-BEGIN capture in `execBegin` (`operators_tx.go`) is
  a third, redundant one (its own comment notes RC "is refreshed per-statement
  anyway"). So the redundancy the design targets is the **pre-loop + in-loop
  double capture per autocommit statement**, not a `Begin`-time capture.

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
