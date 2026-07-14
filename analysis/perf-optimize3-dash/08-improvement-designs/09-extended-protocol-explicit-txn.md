# 08-09 — Explicit transactions across the extended protocol (prepared-mode enabler)

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-unit, G-tpch,
D-002 isolation, G-perf → [README](README.md)

## 1. Problem and numbers

The 06-03 read-path analysis noted that plan+parse cost (~20 % of `-S` CPU)
"drops to ~0 by design — pgbench `-M prepared` would show the ceiling." The user
asked to switch the benchmark to `-M prepared`. Measuring it exposed that goopg's
extended-query protocol **auto-commits one transaction per `Execute`**, so
`-M prepared` does not measure the prepared ceiling faithfully — it changes the
write-path transaction semantics.

Validation run `prep100_a640d2b0` (scale 100, `-M prepared`, this session),
goopg `-N` per-statement latencies vs the simple-mode 06 baseline:

| statement | simple (06) | **prepared (`prep100`)** | what changed |
|---|---:|---:|---|
| `BEGIN` | 0.229 | 0.219 | still a no-op tag |
| `UPDATE` | 1.022 | **3.283** | now carries a full commit + fdatasync |
| `SELECT` | 0.265 | 0.328 | — |
| `INSERT` | 0.279 | **3.331** | now carries a full commit + fdatasync |
| `END` | 3.263 | **0.247** | commit is gone — it is a no-op tag |
| **TPS** | 9,898 | **6,749** | 2 fsyncs/txn instead of 1 → slower |

The commit cost moved off `END` onto each write statement: goopg treats the
client's `BEGIN`/`END` as accepted-but-ignored tags and commits every `Execute`
independently, so the `-N` transaction does **two** durable commits (UPDATE,
INSERT) instead of one. `-S` (read) TPS also did not improve (84,739 vs 89,955)
— the parse/plan saving is offset by the extended path's per-`Execute`
transaction-setup + message overhead. So the prepared-mode benchmark is
currently invalid for the write path and inconclusive for reads; this design
fixes both.

## 2. Current-code map (verified at `a640d2b0`)

- **`Server.executeExtendedQueryViaExecutor(...)`** —
  `internal/server/dispatch_extended.go:30`. For each `Execute`:
  - A `*planner.Transaction` node (BEGIN/COMMIT/ROLLBACK) **short-circuits** at
    `dispatch_extended.go:110`, returning only a `CommandTag` — it does **not**
    drive an explicit-transaction state machine.
  - Otherwise it auto-begins its own transaction: `autoCommitProcNum := (procNum
    + halfSize) % mvcc.ConnSlotCount` (:118) then `s.cfg.TxnMgr.Begin(...)`
    (:119), and commits at the end of the call — one txn per `Execute`.
  - A cross-session plan cache already exists (`dispatch_extended.go:79`,
    `planner.Plan` at :92/:103, invalidated on DDL at :335) — so prepared-plan
    reuse is **already implemented**; only the transaction model is missing.
- **The simple-query path already models explicit transactions** —
  `Server.dispatchSimpleQueryViaExecutor` (`dispatch.go:97`) threads a
  `*connTxState` (`internal/server/conn_tx.go:52`): `InExplicit()` (:392),
  `Tx()` (:403), `Begin(tx)` (:357); at `dispatch.go:225` it reuses
  `connTx.Tx()` when `connTx.InExplicit()`. Deferred-constraint checks wire into
  the COMMIT here and into `transactionOp.execCommit`
  (`internal/executor/operators_tx.go:118`).

So the machinery to model explicit transactions exists (the simple path's
`connTxState` + `execCommit`); the extended path simply never adopted it.

## 3. PostgreSQL reference

- `src/backend/tcop/postgres.c` — `exec_execute_message` runs inside whatever
  transaction the connection is in; `start_xact_command`/`finish_xact_command`
  begin/commit only around the *command*, and an explicit `BEGIN` (a
  `TransactionStmt`) puts the connection into `TBLOCK_INPROGRESS` so subsequent
  `Execute`s share the transaction until `COMMIT`. The protocol layer does not
  auto-commit per `Execute` when an explicit block is open.
- `src/backend/access/transam/xact.c` — `BeginTransactionBlock` /
  `EndTransactionBlock` drive the block state machine that goopg's
  `*planner.Transaction` short-circuit currently stubs out.

## 4. Target design

Make the extended path share the simple path's `connTxState`, so BEGIN/COMMIT
over `Execute` drive a real explicit-transaction block and non-BEGIN `Execute`s
inside a block reuse the open transaction instead of auto-committing.

### 4.1 Wire `connTxState` into the extended path

- `executeExtendedQueryViaExecutor` takes the connection's `*connTxState` (as
  the simple path does).
- The `*planner.Transaction` case (`dispatch_extended.go:110`) stops being a
  no-op: `BEGIN` → `connTx.Begin(TxnMgr.Begin(...))`; `COMMIT`/`ROLLBACK` →
  run the same `execCommit`/rollback the simple path uses (including the
  deferred-constraint checks — X2 in the README).
- The auto-begin/commit at :118–119 becomes conditional: **if
  `connTx.InExplicit()`, reuse `connTx.Tx()`** (no begin, no commit); else keep
  today's per-`Execute` autocommit (the correct behavior for a statement sent
  outside any block).

### 4.2 Decision log

- **D1 — reuse `connTxState`, do not build a second state machine.** The simple
  path's block model is battle-tested (deferred FK/UNIQUE, savepoints); the
  extended path must converge on it, not fork it. This is the
  `pattern_sibling_paths_must_agree` discipline applied to the two dispatch
  paths.
- **D2 — the deferred-constraint hook is in scope (X2).** Adding the block model
  without wiring `execCommit`'s deferred checks would let `-M prepared` silently
  skip INITIALLY DEFERRED constraints — a correctness regression. The COMMIT
  path must call the same check `dispatch.go`'s COMMIT block does.
- **D3 — the prepared-plan cache stays as-is.** It already works
  (`dispatch_extended.go:79`); this design touches only the transaction model,
  so the plan+parse ceiling (06-03 #3) is delivered by the *existing* cache once
  the per-`Execute` transaction overhead is removed.
- **D4 — savepoints/subtransactions over extended protocol are a follow-up.**
  v1 models the top-level block (BEGIN/COMMIT/ROLLBACK); SAVEPOINT over
  `Execute` is O-XP-2.

## 5. Invariants and failure modes

- **I1 — one transaction per open block.** Inside `BEGIN…COMMIT`, every
  `Execute` shares `connTx.Tx()`; exactly one commit happens, at `COMMIT`. This
  restores the single-fsync-per-`-N`-transaction semantics and makes the
  prepared-mode write measurement PG-faithful.
- **I2 — deferred constraints enforced at COMMIT.** The extended COMMIT runs the
  same deferred-check path as the simple COMMIT (X2). G-tpch + FK isolation
  specs gate this.
- **I3 — proc-slot discipline.** The auto-commit offset slot
  (`autoCommitProcNum`, :118) is only used for the *out-of-block* autocommit
  case; an in-block `Execute` uses the connection's own slot via `connTx.Tx()`.
  No slot is used twice concurrently (the proc-slot-wrap hazard from the
  perf-optimize3 collateral fixes). **This is not hypothetical:** the
  `prep100_a640d2b0` validation run (doc 13 §3) aborted three `-S -M prepared`
  clients with `mvcc: unknown transaction`, consistent with the offset scheme
  `(procNum + halfSize) % ConnSlotCount` colliding under 50 sustained clients.
  This design must fix that discipline, not merely add a transaction model —
  reproducing and closing the `mvcc: unknown transaction` abort is an
  acceptance criterion for S2.
- **F1 — mixed simple+extended in one block.** A client that opens `BEGIN` via
  simple query then sends `Execute`s (or vice-versa) must see one coherent
  block — both paths share the same `connTxState`, so this composes; add an
  isolation spec for it.
- **F2 — abort/error mid-block.** An error inside an open extended block must
  mark the block aborted (subsequent `Execute`s error with "current transaction
  is aborted" until ROLLBACK) — the simple path's behavior, inherited via
  `connTxState`.

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | thread `connTxState` | pass the connection's `connTxState` into `executeExtendedQueryViaExecutor`; no behavior change yet (still autocommits). | G-unit |
| S2 | explicit block state machine | wire the `*planner.Transaction` case to `connTx.Begin`/`execCommit`/rollback; reuse `connTx.Tx()` when `InExplicit()`. | G-unit, D-002 isolation |
| S3 | deferred-constraint hook | extended COMMIT runs the deferred FK/UNIQUE checks (X2). | G-tpch, FK isolation specs |
| S4 | abort/mixed-path semantics | aborted-block state; simple+extended interleaving spec. | D-002 isolation |
| S5 | perf acceptance | re-measure `-N -M prepared` — one commit per transaction, `END` carries the commit again, TPS recovers to/above the simple-mode number; `-S -M prepared` shows the parse/plan ceiling. | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| extended-protocol sequence tests | `internal/server/extended_test.go` | S1, S2 |
| explicit-txn over extended (new) | `internal/server/` + D-002 | S2, S4 |
| deferred FK/UNIQUE (extended COMMIT) | FK isolation specs, `internal/testport/` | S3 |
| mixed simple+extended block (new) | D-002 isolation | S4 |
| pgbench `-M prepared` smoke | commit hook | S5 |

## 8. Performance verification

`run_rw50.sh` `-M prepared` at scale 100. Success criteria:

1. `-N`: per-statement latency shows the commit back on `END` (one fsync/txn);
   TPS recovers to ≥ the simple-mode 9,898 (06) and ideally higher (prepared
   removes parse/plan from UPDATE/INSERT).
2. `-S`: TPS rises above the simple-mode 89,955 (06) — the parse/plan ceiling
   06-03 predicted, now unmasked by removing the per-`Execute` transaction
   overhead.
3. `aux2_fsync_probe.sh`: goopg `-N` fsync count per 60 s drops back to ~one per
   transaction group (from the doubled prepared-mode count).

## 9. Open questions

- **O-XP-1** — Where exactly does the per-`Execute` transaction overhead that
  ate the `-S` parse/plan saving live (message parsing, `TxnMgr.Begin` cost,
  snapshot capture per Execute)? Profile `-S -M prepared` before S5 to confirm
  the ceiling is actually reached once the txn model is fixed. (Doc 05's lazy
  snapshot and doc 10's force-GC gate both help here.)
- **O-XP-2** — SAVEPOINT / subtransactions over extended protocol (deferred to a
  follow-up; note the simple path's savepoint machinery as the model).
- **O-XP-3** — Portal/`Bind` semantics for multi-statement blocks: does goopg's
  portal model already support an open portal across a block, or is that
  additional work?
