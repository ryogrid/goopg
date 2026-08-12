# 0132-0001 — The explicit-transaction block state machine on the extended query protocol

status: draft · date: 2026-08-12 · supersedes: none (doc 09 §4.1's target design
is corrected here, not replaced wholesale) · milestone: M0132 (S2 + S3, with D4
covering S6 and D5 binding S10) · plan:
[`0132-extended-protocol-explicit-transactions.md`](0132-extended-protocol-explicit-transactions.md)

## 1. Scope

This doc designs the block state machine that makes `BEGIN` … `COMMIT` /
`ROLLBACK` work across Parse/Bind/Execute/Sync. It owns slices S2 (verb
dispatch) and S3 (in-block transaction reuse), sets the design that S6 (D4) and
S10 (D5) must satisfy, and defines the invariants S5–S9 test against. The
deferred-constraint proof (S4) is
[`0132-0002`](0132-0002-extended-commit-deferred-constraints.md); the proc-slot
work (S7) is `0132-0003`.

## 2. The two state layers, and why both matter

goopg models an explicit transaction at **two** layers, and a correct block
drives both:

| layer | type | what it holds | key methods |
|---|---|---|---|
| server | `*connTxState` (`internal/server/conn_tx.go:53`) | `active`, the `mvcc.Transaction`, the failed flag, buffered NOTIFYs, cursors, the wire status byte | `Begin` (`:364`), `InExplicit` (`:431`), `Tx` (`:442`), `Fail` (`:276`), `End` (`:481`), `wireStatus` (`:415`) |
| executor | `*executor.BasicSession` (reached via `connTx.Session()`, `:470`) | `InExplicitTransaction()`, `CurrentTransaction()`, `EndExplicitTransaction()`, the deferred-constraint queues | consumed by `transactionOp` (`internal/executor/operators_tx.go:42`) and by the simple `COMMIT`'s inline check sequence |

`connTxState.Begin(tx)` already bridges them — it sets `active`, stores the
transaction, lazily creates the `BasicSession`, and calls
`BeginExplicitTransaction` on it (`conn_tx.go:364-377`). This is the single
entry point the extended path must adopt; hand-setting `active` without it would
leave the executor layer unaware and break the deferred-check queues' lookup of
the current transaction.

The wire status byte partly falls out for free: `w.TxStatusFn = connTx.wireStatus`
(`internal/server/server.go:1526`) is installed once per connection, for both
protocols, and `Sync`'s `w.ReadyForQuery()` (`server.go:1763`) routes through it.
So `T` and `I` start working the moment an extended `BEGIN` calls
`connTx.Begin`. **`E` does not** — see §4 I3.

## 3. Design

### D1 — Extract the simple path's verb handling; do not mirror it

The simple path's `*planner.Transaction` arm runs from `dispatch.go:2696` to
`:2984` — **289 lines** covering, for `COMMIT` alone: the PG rule that COMMIT in
a failed block performs a ROLLBACK and reports `ROLLBACK` (`:2782-2800`), the
**inline** deferred FK/UNIQUE/EXCLUDE sequence (`:2818-2828`), the SSI
pre-commit check, enum-DDL undo, the `BeginLocalTransaction` /
`EndLocalTransaction` hooks, and the pending enum/composite/range-type queues.

Reimplementing that in `dispatch_extended.go` would create exactly the sibling
divergence this milestone exists to close. It is also how the deferred-check
sequence would get silently dropped: those checks are *not* reachable through
`transactionOp.execCommit` — `dispatch.go:2803-2807` records that the
simple-query dispatch deliberately bypasses `execCommit` and runs them itself,
and the arm returns at `:2980` without ever building the operator at `:2985`. A
hand-written extended `COMMIT` would therefore have to re-derive a sequence its
author may not know exists.

**Decision:** lift the arm into one shared helper — sketch:

```go
// applyTransactionVerb drives the explicit-block state machine for one
// BEGIN/COMMIT/ROLLBACK/SAVEPOINT verb, for BOTH protocols, including the
// COMMIT-time deferred-constraint and SSI checks. Returns the command tag to
// report ("ROLLBACK" for a COMMIT in a failed block, per PG).
func (s *Server) applyTransactionVerb(
    ctx *executor.Context, connTx *connTxState, txNode *planner.Transaction,
    autoCommitPtr *bool,
) (tag string, err error)
```

`dispatchSimpleQueryViaExecutor` keeps its current behaviour by calling it and
writing the returned tag; `executeExtendedQueryViaExecutor` replaces its
short-circuit (`dispatch_extended.go:110-112`) with the same call, returning
`&extendedQueryResult{CommandTag: tag}`. The refactor is behaviour-preserving
for the simple path — which is testable, and must be proven green **before**
S2's new behaviour is switched on.

Two asymmetries the helper must absorb rather than hide:

- The simple path has already begun a per-dispatch transaction by the time it
  reaches the arm, so its `TxBegin` *promotes* that transaction
  (`autoCommitPtr` → false, `dispatch.go:2701`). The extended path reaches the
  verb *before* beginning anything (the short-circuit sits at `:110`, the
  `TxnMgr.Begin` at `:139`). The helper takes the transaction to promote as an
  argument; the extended caller begins one explicitly for the `BEGIN` case.
- The simple path writes its own wire frames inside the arm
  (`w.WriteCommandComplete(...)`). The helper must return the tag instead, and
  each caller writes it in its own protocol's shape.

### D2 — In-block statements reuse `connTx.Tx()`

Sites 2 and 3 in the plan doc become conditional, mirroring
`dispatch.go:227-229` exactly:

```go
var tx mvcc.Transaction
autoCommit := true
if connTx != nil && connTx.InExplicit() {
    tx = connTx.Tx()          // the connection's own slot, XID-aware (conn_tx.go:442-451)
    autoCommit = false
} else {
    const halfSize = mvcc.ConnSlotCount / 2
    autoCommitProcNum := (procNum + halfSize) % mvcc.ConnSlotCount
    tx, err = s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted, autoCommitProcNum)
    ...
}
```

and the commit at `dispatch_extended.go:361-365` plus the rollback `defer` at
`:143-150` run only when `autoCommit` is true. `connTx.Tx()` deliberately
returns the session's *current* transaction when an XID has since been
materialised (`conn_tx.go:442-451`, M0100-0002) so a statement self-sees the
block's earlier writes — use the accessor, never a cached handle.

**This is also the fix for the mixed-protocol shape.** Today a block opened by a
simple-query `BEGIN` (what pgx and lib/pq actually send) is held on the
connection's own slot while every extended `Execute` opens and commits its own
transaction on the offset slot — two live transactions per connection. D2
collapses them to one.

### D3 — `Sync` stays a no-op with respect to transaction state

`server.go:1761-1765` is already correct (PG's `Sync` ends an *implicit*
transaction only). S9's guard test exists because the natural misreading —
"Sync = end of transaction" — would silently re-break every block.

### D4 — Isolation level is owned by `BEGIN`, not by the dispatch path

Once D1 routes the verb through the shared helper, `BEGIN ISOLATION LEVEL …`
works on both protocols from one implementation, including the subtleties at
`dispatch.go:2709-2738`: a placeholder transaction at the wrong level must be
rolled back before the correctly-levelled one is begun (so no XID or SSI
bookkeeping leaks), and the READ COMMITTED arm must re-capture the snapshot
(`:2729-2736`). This retires the `dispatch_extended.go:119-123` comment that
records SERIALIZABLE as unreachable on this path.

The *session-default* isolation (`default_transaction_isolation`,
`setTransactionOp` at `operators_tx.go:586`) is a separate input that **both**
dispatch paths currently ignore — each hardcodes READ COMMITTED
(`dispatch.go:236`, `dispatch_extended.go:139`) while `execBegin`
(`operators_tx.go:70`) honours `Session.IsolationLevel()`. That is
parity-neutral, so S6 may fix it or rule it out of scope, but must not land the
`BEGIN ISOLATION LEVEL` half and imply both are done.

### D5 — SAVEPOINT must be explicit, never a bare tag

`planner.TxSavepoint` / `TxRelease` / `TxRollbackTo` reach the same
`*planner.Transaction` node (`internal/planner/plan.go:2045-2052`). The helper
must handle them explicitly — either by implementing them (S10) or by returning
`0A000` `feature_not_supported` on the extended path — but **not** by falling
through to a bare tag, which is today's silent-no-op bug wearing a different
verb. The choice is S10's; the requirement that it be explicit is this doc's.

Note what is *not* in this node type: `PREPARE TRANSACTION` and
`LISTEN`/`NOTIFY` are separate server-layer handlers intercepted before planning
on the simple path only (`twophase.go:107` from `dispatch.go:2666`;
`execNotifyStmt` at `:2659`). They cannot be fixed here — S12 owns them — and
`twophase.go:227`'s `connTx.End()` means two-phase commit touches the very state
this doc introduces.

## 4. Invariants

- **I1 — one transaction per block, per connection.** Inside `BEGIN`…`COMMIT`,
  every `Execute` observes `connTx.Tx()`; exactly one commit occurs, at
  `COMMIT`; and no second transaction exists on the connection's behalf,
  whichever protocol opened the block.
- **I2 — `ROLLBACK` rolls back.** No `Execute` inside a block commits, so a
  `ROLLBACK` discards the whole block. (Today it discards nothing.)
- **I3 — the status byte matches the state.** `T` in a block, `E` in a failed
  block, `I` outside. `T`/`I` follow from D1 via the shared `w.TxStatusFn`; `E`
  does **not** — `connTx.Fail()` has exactly two call sites, both simple-path
  (`dispatch.go:950`, `:1019`), the extended loop's only `'Z'` write is the
  plain `ReadyForQuery()` (`protocol/messages.go:152`, `afterError=false`), and
  the `ReadyForQueryAfterError` escape hatch (`messages.go:156-164`) is not on
  this path. S5 must add the call site.
- **I4 — proc-slot uniqueness.** The offset slot is used only for out-of-block
  autocommit; an in-block statement uses the connection's own slot. No slot is
  concurrently used twice (S7 / `0132-0003`, which must also rule on
  `copy.go:157-167`'s identical offset).
- **I5 — the two layers never disagree.** `connTx.InExplicit()` is true iff the
  executor session reports `InExplicitTransaction()`. Enforced by going through
  `connTx.Begin`/`End` exclusively.

## 5. Failure modes to test

- **F1 — mixed protocols in one block.** `BEGIN` (simple) → `Execute`s
  (extended) → `COMMIT` (simple), and the mirror. This is the shape real drivers
  emit, so S8 is a primary slice, not a corner case.
- **F2 — error inside a block.** The block is marked failed
  (`connTxState.Fail()`), subsequent `Execute`s fail 25P02, and only `ROLLBACK`
  (or a `COMMIT` that PG converts to one) clears it.
- **F3 — client disconnects mid-block.** Teardown routes through `connTx.End()`
  (`server.go:1462`, the only cleanup path); the in-block transaction must be
  rolled back exactly once — not leaked, and not double-rolled-back once the
  per-`Execute` `defer` at `dispatch_extended.go:143-150` becomes conditional.
  Acceptance bar 9.
- **F4 — `COMMIT` with no open block.** PG warns and reports `COMMIT`. The
  extended path must not error.
- **F5 — nested `BEGIN`.** PG warns and no-ops; `execBegin`
  (`operators_tx.go:65-69`) models it. Same requirement.
- **F6 — `SET LOCAL ROLE` inside an extended block.**
  `connTx.SnapshotLocalRoleIfNeeded` (`conn_tx.go:390-396`) returns early when
  `!c.active`, so the extended path's calls (`dispatch_extended.go:252`,
  `extended.go:693`, `:712`) record no restore point today and `End()` never
  reverts. Setting `active` changes that silently — assert the new behaviour
  deliberately or suppress it.

## 6. Upstream reference

- `postgres/src/backend/tcop/postgres.c` — `exec_execute_message`;
  `start_xact_command` / `finish_xact_command` bracket a *command*, and the
  implicit commit is suppressed while a block is open.
- `postgres/src/backend/access/transam/xact.c` — `BeginTransactionBlock`,
  `EndTransactionBlock`, `AbortCurrentTransaction`, and the `TBLOCK_*` states.
  goopg's `connTxState` is the analogue: `active` ≈ `TBLOCK_INPROGRESS`,
  `failed` ≈ `TBLOCK_ABORT`, cleared ≈ `TBLOCK_DEFAULT`.
