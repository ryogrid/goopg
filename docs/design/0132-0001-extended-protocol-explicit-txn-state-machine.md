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

## 7. S1 verification record (2026-08-13, HEAD `1d648659`)

M0132-S1 executed the three doc-09 corrections as *measurements*, not readings,
and added the acceptance bar as
`internal/server/extended_txn_block_test.go`. Results:

| correction | verdict at HEAD | evidence |
|---|---|---|
| (a) `connTxState` is already threaded into the extended path | **confirmed** | `dispatch_extended.go:30` takes `connTx *connTxState`; its only reads are `NonSuperuserRole` and statement logging. The first *code* slice is therefore the state machine (S2), not plumbing. |
| (b) there is no `execCommit` route to adopt | **confirmed** | `dispatch.go:2803-2807` states the bypass in so many words, and the deferred FK/UNIQUE/EXCLUDE sequence is inline at `:2818-2828`. S4 is a proof obligation on S2's extraction, not independent wiring. |
| (c) `Sync` is already correct | **confirmed** | `server.go`'s `MsgSync` arm clears `syncRequired` and calls `w.ReadyForQuery()`; it touches no transaction state. Pinned by `TestM0132S1_SyncDoesNotEndAnOpenBlock`, which passes today and must keep passing. |

### The bar, and how its redness is proven

The file carries one const, `m0132ExtendedBlocksLanded = false`. Every bar runs
its scenario unconditionally and then asserts either the PostgreSQL outcome (const
true) or today's divergence (const false). At HEAD all 8 tests pass; flipping the
const turns 6 red — the two that stay green are the no-divergence guards
(`ExtendedCommitPersistsBlockWork`, `SyncDoesNotEndAnOpenBlock`). The arrangement
is what makes the bar committable before the fix while keeping it a real red
test: when S2–S5 land, the divergence arms start failing because the divergence
is gone, so the fix cannot land without flipping the const and the const cannot
be flipped without the fix.

Measured divergences (const-false arms, i.e. today's behaviour):

- `BEGIN`/`INSERT`×2/`ROLLBACK` entirely over the extended protocol leaves **2
  rows**; PG leaves 0.
- `ReadyForQuery` reports `I` after an extended `BEGIN` and after an in-block
  extended `INSERT`; PG reports `T`.
- The **mixed** shape (simple `BEGIN` → extended parameterised `INSERT` → simple
  `ROLLBACK`) leaves **1 row**: the write went to the auto-committing offset-slot
  transaction, so the `ROLLBACK` discarded an empty one. This is the shape pgx
  and lib/pq actually emit.
- An extended `Execute` inside a **failed** block runs and commits; PG raises
  25P02.

### Two SIMPLE-path gaps discovered while writing the bar

Both are new (they belong to S5's scope, and S5 will inherit them if it copies
the simple path's placement); both are pinned by tests and carry a ledger row.

1. **A plan-time error does not fail the block.** `connTx.Fail()` has exactly two
   call sites (`dispatch.go:950`, `:1019`), both on the executor-error path
   (`errQueryErrorSent`). An error raised *before* execution — e.g. `INSERT INTO
   <missing table>` — leaves the block live and healthy: goopg answers the next
   statement normally and reports `T`. PG aborts a block on **any** error inside
   it (the abort is driven from `postgres.c`'s error handler via
   `AbortCurrentTransaction`, not from the executor). The `E` a client sees
   immediately after such an error is produced by the `afterError` argument to
   `wireStatus`, not by persisted state, which is why the divergence hides:
   the status byte is right once and wrong from then on.
   Test: `TestM0132S1_PlanTimeErrorDoesNotFailTheBlock`.
2. **A constant `SELECT 1` bypasses the 25P02 gate.** The gate at
   `dispatch.go:638` rejects `SELECT * FROM items` and `INSERT …` in a failed
   block, but a constant-only select is answered normally. PG rejects everything
   except `COMMIT`/`ROLLBACK`/`ROLLBACK TO`.
   Test: `TestM0132S1_ConstantSelectBypassesTheAbortedBlockGate`.

## 8. S2 step 1 record — the extraction landed, behaviour-preserving (2026-08-13, HEAD `49382001`)

S2 is a two-step slice, and this is step 1: **the extraction, with the simple
path as its only caller.** No extended behaviour is switched on, so the
land-together rule (S2+S3+S4+S5 in one commit) is not touched — that rule
governs the moment the extended path starts driving the machine, and D1 above
explicitly requires the refactor be "proven green **before** S2's new behaviour
is switched on". A pure extraction landing alone opens no hole: it cannot,
because it changes nothing observable.

**What landed.** `internal/server/txn_verb.go`. The 289-line arm at
`dispatch.go:2692-2984` is now 17 lines that render an outcome; the machine is
`(*Server).applyTransactionVerb(ctx, connTx, txNode, autoCommitPtr)
txnVerbOutcome`.

**One correction to D1's sketch.** The sketch returned `(tag string, err
error)`. That signature cannot carry the arm's real outputs, and discovering
this is what makes the extraction faithful rather than approximate:

- **The 25P01 WARNING is not an error.** `COMMIT`/`ROLLBACK` outside a block
  emits `WriteNoticeResponse` *and then* a success tag (`:2944-2951`,
  `:2973-2980`). A `(tag, err)` pair has nowhere to put "succeeded, but notice
  first", so it would have been dropped or promoted to an error. It is now
  `txnVerbOutcome.Warn`, rendered by `noTransactionInProgressNotice()`.
- **The errors carry structured fields, not just text.** The deferred-check
  failure passes DETAIL and POSITION, and the SSI failure passes DETAIL + HINT
  with a bare primary message (predicate.c parity) — a plain `error` loses all
  of it, and the SQLSTATE with it (23503 vs 23505 vs 23P01 vs 40001 comes from
  the `ExecError`'s own `Code`). It is now `*txnVerbError{Code, Msg, Fields}`.
- **"Not handled" is a third outcome, distinct from success and failure.**
  SAVEPOINT / ROLLBACK TO / RELEASE fall *through* to `BuildFastIterator`
  (M0097-0023). `Handled: false` says so explicitly. This is also the hook S10
  needs: its ruling ("implement, or `0A000` on the extended path") is a decision
  about what the extended caller does with `Handled == false`, and having the
  case named means the extended path cannot silently inherit today's bare-tag
  bug wearing a different verb.

**One structural change inside the extraction, deliberate.** The five terminal
paths of the arm each repeated an identical seven-line teardown (enum-DDL undo,
`connTx.End()`, `EndLocalTransaction`, five pending-queue clears). They are now
one `(*Server).endExplicitBlock`. This is the only edit that is not a
mechanical move, and it is the one that makes "all five paths tear down
identically" checkable by reading rather than by diffing five copies.

**Gates run:** `go build ./...` and `go vet ./internal/server/` clean; `go test
./internal/server/` PASS (38 s); `go test ./internal/executor/` PASS; `go test
-run TestPort_Isolation ./internal/testport/` PASS (413 s — the D-002 gate S2
names, and the one that would catch a dropped SSI or deferred-check path);
`RALPH_PRECOMMIT_SCOPE=units` and the pgbench smoke via the commit hook.

**Next (step 2, and it is the land-together commit):** switch
`dispatch_extended.go:110-112` to call `applyTransactionVerb`, make `:134-139` /
`:361-365` / the `:143-150` rollback defer conditional on `connTx.InExplicit()`
(S3), assert the deferred sequence arrives on the extended COMMIT (S4), add the
missing `connTx.Fail()` call site (S5) — and flip
`m0132ExtendedBlocksLanded` to `true` in the same commit.

## 9. S2 step 2 record — the land-together commit (2026-08-13)

Step 2 is where the extended path starts driving the machine, so S2+S3+S4+S5
landed as one commit. Each slice is a *verification* of the extraction more than
a fresh change — D1's whole point was that the extended caller gets the deferred
sequence, the SSI check, enum-DDL undo and the `Begin/EndLocalTransaction` hooks
for free by calling the same function the simple path calls.

**S2 (wiring).** `dispatch_extended.go` replaces the bare command-tag
short-circuit with `applyTransactionVerb`. The extended caller differs from the
simple one in one respect D1 anticipated: the simple path *promotes* an
already-begun transaction, while the extended path reaches the verb before
beginning anything — so the extended path begins the `TxBegin` transaction
itself (on the connection's own slot, `beginProcNum = procNum`) and passes
`autoCommit = ownTx` so the shared deferred-rollback respects the promotion.
`txnVerbOutcome` renders through the extended protocol's shapes:
`Warn` → `verbWarnFields` (`NoticeResponse`, 25P01), `*txnVerbError` →
`extendedQueryErrorFromVerb` (carrying DETAIL/HINT/POSITION and the error's own
SQLSTATE), `Handled == false` → fall through to the executor (SAVEPOINT/RELEASE/
ROLLBACK TO, where S10's ruling attaches).

**S3 (in-block reuse).** `inBlock := connTx != nil && connTx.InExplicit()`;
the begin, the `ownTx` commit at the foot, and the rollback defer all key off
it, and the in-block statement runs against `connTx.Tx()` (the accessor) plus
`connTx.Session()` so a second Execute self-sees the first's writes.

**S4 (deferred sequence).** No code change was needed — the extraction was
faithful, so the deferred FK/UNIQUE/EXCLUDE check and the SSI pre-commit check
arrive on the extended COMMIT by construction. Pinned by
`TestM0132S4_ExtendedCommitRunsDeferredFKChecks` (a `DEFERRABLE INITIALLY
DEFERRED` FK violation raises 23503 at the extended COMMIT and leaves the block
status `I`) and discharged by the FK isolation specs plus the
`internal/testport/` deferred-constraint set. The promised
`0132-0002` design doc was not needed: S4 is a proof obligation on S2, not an
independent wiring slice, and the proof came out clean.

**S5 (aborted-block semantics).** `failExplicitBlock` (`txn_verb.go`) marks the
block failed and now fires on **every** extended error path — Execute
(`handleExecuteFrame`), Parse, Bind and Describe (`runPostStartupLoop`) — where
the extended loop's only `'Z'` write was the plain `ReadyForQuery()` and the
`ReadyForQueryAfterError` escape hatch is unavailable. The two SIMPLE-path gaps
S1 filed are closed in the same commit: a plan-time error now fails the block
(`dispatch.go`, the cache-miss planning site that had returned straight out of
the loop), and a constant `SELECT 1` / `SHOW` / `SET` is gated ahead of the
fast paths (`query.go` simple path, `extended.go` extended path) via
`allowedInAbortedBlock`/`abortedBlockMessage` — matching PG's reject-everything-
except-block-ending-verbs rule.

**Gates.** `go build ./...` + `go vet ./internal/server/` clean; `go test
./internal/server/` PASS (38 s, all 8 bar tests green with the const flipped);
`go test ./internal/executor/` PASS; `go test -run TestPort_Isolation
./internal/testport/` PASS (413 s — D-002); the `internal/testport/`
deferred-constraint set PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35);
`make plan-gate` diffed 22/22 against `warm-stats-base.txt` — a **stale-baseline
confound, not a regression**: that snapshot predates M0127-P6.2's MHJ deletion
(`4e08d4b7`), so every MHJ node it records now legitimately renders as a plain
`Hash Join`, and S2 touches no planner code.

## 10. S6 record — isolation level over the extended protocol (2026-08-13)

D4's two inputs are resolved as follows.

**(1) `BEGIN ISOLATION LEVEL <level>` — landed, and it was almost free.** D1's
extraction carried the M0104-0008 block (txn_verb.go, "honour
`txNode.IsolationLevel`") into `applyTransactionVerb`, so the extended BEGIN
already parsed the level, rolled back the placeholder auto-commit transaction,
re-began at the requested level, and re-captured the READ COMMITTED snapshot —
identical to `dispatch.go:2709-2738`. The single missing piece was the
connection's ProcArray slot: the simple path sets `ectx.ProcNum = connTx.ProcNum`
(`dispatch.go:513`) but the extended path never did, so `applyTransactionVerb`'s
re-begin called `TxnMgr.Begin(parsedLvl, ctx.ProcNum)` with `ctx.ProcNum == 0`.
Two concurrent `BEGIN ISOLATION LEVEL SERIALIZABLE` blocks over this protocol
therefore both re-began on slot 0 and the second aborted with
`mvcc: unknown transaction` (C58000) — the same signature S7's proc-slot slice
records. Fix: `dispatch_extended.go` sets `ectx.ProcNum = procNum` next to
`ectx.Tx = tx`, the extended sibling of `dispatch.go:513`. Proved load-bearing:
reverting it to `0` turns the gate below red with exactly that `mvcc: unknown
transaction`.

**(2) Session-default isolation — ruled OUT OF SCOPE, parity-neutral.** Both
dispatch paths still hardcode READ COMMITTED when they begin the auto-commit
transaction (`dispatch.go:236`, `dispatch_extended.go:160`), so a non-default
`default_transaction_isolation` (or `SET SESSION CHARACTERISTICS …`) is ignored
by both. `execBegin` (`operators_tx.go:70`) honours `Session.IsolationLevel()`
and only the executor-routed BEGIN (SAVEPOINT-adjacent) reaches it. PG's own
default is `read committed`, so goopg is correct for the default; the gap is a
pre-existing, symmetric engine limitation, not an extended-protocol defect, and
is left for a future milestone rather than implied-fixed by (1). The
`dispatch_extended.go:119-123` comment D4 said this retires had already been
replaced by the S2 refactor (those lines now carry the P6 Gather comment).

**Gate.** `TestM0132S6_ExtendedSerializableBlockAbortsWriteSkew` opens both
blocks of the canonical simple-write-skew permutation over the **extended**
protocol and asserts the second committer aborts with 40001;
`TestM0132S6_ExtendedReadCommittedBlockAllowsWriteSkew` is the READ COMMITTED
control (same interleaving, no abort). The write-skew helper deliberately leaves
the block OPEN so the caller interleaves the two COMMITs — a helper that
committed each side inline would serialise the blocks into the no-overlap
permutation and the gate would pass for the wrong reason. Gates run: `go test
./internal/server/` PASS (all 8 S1 bar tests + the S4 deferred-FK probe + both
S6 tests).
