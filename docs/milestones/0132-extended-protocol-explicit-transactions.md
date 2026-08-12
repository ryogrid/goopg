# Milestone 0132 — Explicit transactions across the extended query protocol

**Status:** planned
**Filed:** 2026-08-12 (user directive)
**Priority placement:** filed, **not promoted**. The `## Current Priority`
banner in `.ralph/fix_plan.md` remains the sole ordering authority and still
names M0131 first; M0132 is queued behind it. Working M0132 requires a banner
edit — the filing directive was to raise the milestone, not to reorder the
queue.
**Reference plan:** `.ralph/fix_plan.md` (M0132 section, at the foot of the file)
**Implementation plan (authoritative task decomposition):**
`docs/design/0132-extended-protocol-explicit-transactions.md`
**Design of record:** `analysis/perf-optimize3-dash/08-improvement-designs/09-extended-protocol-explicit-txn.md`
(status `design`, 2026-07-14, base `a640d2b0`) — the original diagnosis and the
S1–S5 migration sketch this milestone supersedes and corrects.
**Deferral-ledger provenance:** `.ralph/deferral_ledger.md:347`, the 2026-07-14
row listing *"doc 09 extended-protocol explicit-txn"* among the items deferred
out of that loop, with the recorded reason *"a partial land would silently skip
INITIALLY DEFERRED constraints"*.
**Prerequisites:** none. The machinery this milestone needs — `connTxState` and
the simple path's COMMIT-time deferred-check sequence — already exists and is
exercised by the simple-query path every day.
**Branch:** inherits the current lineage and its discipline (worktrees off pinned
clean HEAD, explicit pathspec staging, guard re-runs after rebase/handoff).

## Background

PostgreSQL's extended query protocol (Parse/Bind/Execute/Sync) and its simple
query protocol (`Q`) share one transaction model. `exec_execute_message`
(`postgres/src/backend/tcop/postgres.c`) runs inside whatever transaction the
connection is already in; `start_xact_command` / `finish_xact_command` bracket a
*command*, not a *protocol message*, and an explicit `BEGIN` — a
`TransactionStmt` reaching `BeginTransactionBlock`
(`postgres/src/backend/access/transam/xact.c`) — puts the session into
`TBLOCK_INPROGRESS` so every later `Execute` joins that block until `COMMIT` or
`ROLLBACK`. The protocol layer never auto-commits per `Execute` while a block is
open.

**goopg implements this for the simple-query path only.** The extended path
begins and commits its own transaction on *every* `Execute`, and treats the
client's `BEGIN`/`COMMIT`/`ROLLBACK` as accepted-but-ignored command tags.

### The mechanism, verified at filing (2026-08-12, HEAD `28de7c44`)

1. **`BEGIN`/`COMMIT`/`ROLLBACK` short-circuit to a bare tag.**
   `internal/server/dispatch_extended.go:110-112`:

   ```go
   if tx, ok := node.(*planner.Transaction); ok {
       return &extendedQueryResult{CommandTag: transactionTag(tx.Verb)}, nil
   }
   ```

   No `connTx.Begin`, no commit, no rollback — the verb is parsed, planned, and
   thrown away.

2. **Every other `Execute` opens and commits its own transaction.**
   `dispatch_extended.go:134-139` allocates an *offset* proc slot and begins:

   ```go
   const halfSize = mvcc.ConnSlotCount / 2
   autoCommitProcNum := (procNum + halfSize) % mvcc.ConnSlotCount
   tx, err := s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted, autoCommitProcNum)
   ```

   and `dispatch_extended.go:361-365` commits unconditionally at the end of the
   call. Neither site consults `connTx.InExplicit()`.

3. **The state machine is reachable but unused.** The connection's
   `*connTxState` is *already threaded* into the extended path — it is the last
   parameter of `executeExtendedQueryViaExecutor` (`dispatch_extended.go:30`) —
   but only its `NonSuperuserRole` field (`:36`, `:46`, `:238`, `:250`) and the
   statement-logging helpers (`:68`, `:71`) read it. `connTx.Begin`
   (`conn_tx.go:364`), `InExplicit` (`:431`), `Tx` (`:442`) and `End` (`:481`)
   have **zero** call sites in `dispatch_extended.go` or `extended.go`.

4. **The simple path models the block** — `dispatch.go:227-229` reuses
   `connTx.Tx()` whenever `connTx.InExplicit()`, and `dispatch.go:2701` promotes
   the pending auto-commit transaction to an explicit block on `TxBegin`. This
   is what makes the gap a **sibling-path divergence** rather than a missing
   feature — with two qualifications recorded below (COPY, and the
   simple-path-only handlers), both of which this milestone must not paper over.

### How the deferred-constraint checks actually run (correction to doc 09)

Doc 09 and the first draft of this filing both said the extended `COMMIT` should
route through `transactionOp.execCommit`
(`internal/executor/operators_tx.go:118`). **That is not how the simple path
works.** `dispatch.go:2803-2807` states the opposite in the code:

> `// DEFERRED). The simple-query dispatch bypasses`
> `// transactionOp.execCommit, so the checks queued on the session`
> `// during INSERT/UPDATE/DELETE must be run here BEFORE`
> `// TxnMgr.Commit; a violation aborts the transaction with 23503,`

The deferred FK / UNIQUE / EXCLUDE passes are re-implemented **inline** at
`dispatch.go:2818-2828` (`executor.RunDeferredFKChecks` under
`mvcc.Manager.FreshSnapshot`, then `RunDeferredUniqueChecks`, then
`RunDeferredExclusionChecks`), with the SSI pre-commit check inline just after;
the arm returns at `:2980` and never reaches the operator build at `:2985`, so
`execCommit` is unreachable from a simple `COMMIT`. The bypass was never
removed — the 0119-0004 fix *added* the inline sequence.

The consequence for this milestone is structural, not cosmetic: **the extended
`COMMIT` must inherit the simple path's inline sequence, which it gets for free
if and only if S2 is a genuine extraction rather than a reimplementation.** That
is what the land-together rule now protects.

### Two qualifications the "simple path does the real thing" framing must carry

- **COPY ignores an open block on *both* protocols.**
  `internal/server/copy.go:157-167`: *"The COPY always runs in its own
  auto-commit transaction regardless of whether the client has opened an
  explicit BEGIN block"*, using the same
  `(connTx.ProcNum + halfSize) % mvcc.ConnSlotCount` offset that S7 exists to
  retire (`dispatch_extended.go:136` cites copy.go as its model). So
  `BEGIN; COPY …; ROLLBACK` does not roll back today, simple path included.
  This is a **pre-existing, adjacent divergence**: M0132 must rule on copy.go's
  proc slot in S7 and file a ledger row for the COPY-ignores-block gap, but
  closing that gap is not in this milestone's acceptance bar.
- **Some server-layer handlers are simple-path-only.**
  `PREPARE TRANSACTION` / `COMMIT PREPARED` / `ROLLBACK PREPARED`
  (`execTwoPhaseStmt`, `internal/server/twophase.go:107`, called **only** from
  `dispatch.go:2666`) and `LISTEN` / `NOTIFY` / `UNLISTEN` (`execNotifyStmt`,
  `dispatch.go:2659`) are intercepted before planning on the simple path only.
  They are **not** `planner.Transaction` verbs — that type has exactly
  `TxBegin, TxCommit, TxRollback, TxSavepoint, TxRelease, TxRollbackTo`
  (`internal/planner/plan.go:2045-2052`) — so over the extended protocol they
  fail at `internal/planner/planner.go:289` `"unsupported statement type %T"`.
  S12 covers this.

### Why this is a correctness milestone, not a performance one

Doc 09 found the divergence while measuring `pgbench -M prepared` and framed it
as a benchmark-fidelity problem. The filing investigation establishes that the
user-visible consequences are worse than the numbers suggest:

- **`ROLLBACK` does not roll anything back.** A client that sends
  `BEGIN` → `UPDATE` → `UPDATE` → `ROLLBACK` entirely over Parse/Bind/Execute
  has already durably committed both updates by the time the `ROLLBACK` arrives,
  and the `ROLLBACK` returns a success tag. No error, no warning, no way for the
  client to detect it from the protocol.
- **Atomicity is lost across statements.** A multi-statement block is applied as
  N independent transactions, so a failure at statement *k* leaves statements
  `1..k-1` committed. PG guarantees all-or-nothing.
- **The wire's transaction-status byte lies.** `ReadyForQuery`'s status is
  computed by `connTxState.wireStatus` (`conn_tx.go:415`, installed once per
  connection at `server.go:1526` as `w.TxStatusFn`) from `connTx.active`, which
  an extended `BEGIN` never sets. goopg answers `I` (idle) where PG answers `T`
  (in a transaction block) — an *observable protocol* divergence drivers use to
  decide whether to emit their own `BEGIN`.
- **Isolation levels above READ COMMITTED are unreachable.** The extended path
  hardcodes `mvcc.IsolationReadCommitted` (`dispatch_extended.go:139`), and the
  comment at `:119-123` states plainly that "the extended path always begins a
  fresh READ COMMITTED transaction below, so SERIALIZABLE cannot apply".

### The shape real drivers actually emit — and why it is worse

The all-extended sequence above is the clearest illustration, but it is **not**
what most drivers send. Both dominant Go drivers fall back to the *simple*
protocol for argument-less statements:

- `github.com/jackc/pgx/v5@v5.9.2/conn.go:515` —
  `// Always use simple protocol when there are no arguments.`
- `github.com/lib/pq@v1.12.3/conn.go:901` — `if len(args) == 0 { … simpleQuery }`

`BEGIN`/`COMMIT`/`ROLLBACK` carry no arguments; parameterised `INSERT`/`UPDATE`
do. So the dominant real-world shape is **a block opened on the simple path with
its DML executed on the extended path**, which produces a failure mode the first
draft of this filing did not model: the block transaction is opened and held on
the connection's own proc slot (`dispatch.go:227-229`) while each extended
`Execute` begins *and commits* its own transaction on the offset slot
(`dispatch_extended.go:137-139`, `:361`) — **two live transactions on one
connection**, with the client's writes landing in the one that auto-commits and
the `ROLLBACK` discarding the empty one.

Three consequences, all folded into the plan: the mixed shape is a first-class
acceptance-bar item (bar 2b), S8 is a **primary** slice rather than an
edge-case spec, and it is a better hypothesis for S7's observed
`mvcc: unknown transaction` aborts than "the offset scheme collides" alone.

### The performance finding, inherited as a gate

Measured on `prep100_a640d2b0` (scale 100, `-M prepared`), goopg `-N`
(doc 09 §1; its `SELECT` row, 0.265 → 0.328, is omitted here as unaffected):

| statement | simple (06) | prepared | what changed |
|---|---:|---:|---|
| `BEGIN` | 0.229 | 0.219 | still a no-op tag |
| `UPDATE` | 1.022 | **3.283** | now carries a full commit + fdatasync |
| `INSERT` | 0.279 | **3.331** | now carries a full commit + fdatasync |
| `END` | 3.263 | **0.247** | commit is gone — it is a no-op tag |
| **TPS** | 9,898 | **6,749** | 2 fsyncs/txn instead of 1 |

### Three corrections to doc 09 that this filing establishes

- **Doc 09's slice S1 is already done.** Its first slice was *"pass the
  connection's `connTxState` into `executeExtendedQueryViaExecutor`; no behavior
  change yet"*. That parameter has been present since the role-scoping work; it
  is simply unused for transaction purposes. S1 collapses into a verification
  step, and the milestone's first *code* slice is the state machine itself.
- **The `execCommit` route does not exist** — see the deferred-constraint
  section above. Doc 09 §2 and §4.1 name it; the simple path bypasses it.
- **`Sync` is already correct and must not be touched.** The handler
  (`server.go:1761-1765`) clears `syncRequired` and emits `ReadyForQuery`
  without altering transaction state. That is exactly PG's behaviour for an
  explicit block (`Sync` closes an *implicit* transaction only), so the correct
  action is a guard test that keeps it that way — the tempting "end the
  transaction at Sync" reading is wrong.

## Goals

1. **An explicit block opened over the extended protocol behaves as PG's
   does** — one transaction from `BEGIN` to `COMMIT`/`ROLLBACK`, spanning every
   intervening `Execute`, whatever mix of Parse/Bind/Execute/Sync carries it.
2. **A block opened on one protocol and driven on the other is one block** —
   the shape real drivers emit. Not an edge case; the common case.
3. **`ROLLBACK` over the extended protocol actually rolls back**, and a failed
   statement inside a block aborts the block rather than committing its
   predecessors.
4. **The wire tells the truth** — `ReadyForQuery` reports `T` inside a block,
   `E` in a failed block, `I` outside, on the extended path exactly as on the
   simple path.
5. **Deferred constraints are enforced at the extended `COMMIT`** — the block
   model must not become a way to skip `INITIALLY DEFERRED` FK/UNIQUE/EXCLUDE
   checks. This is the risk the 2026-07-14 ledger row cited, and it is why
   partial landings are forbidden below.
6. **`BEGIN ISOLATION LEVEL …` is honoured** over the extended protocol, so
   REPEATABLE READ and SERIALIZABLE are reachable.
7. **The two dispatch paths converge rather than fork** — the extended path
   adopts `connTxState` and the simple path's COMMIT-time sequence *by sharing
   the code*, not by copying it (`pattern_sibling_paths_must_agree`).
8. **Proc-slot discipline is fixed, not merely inherited.** The
   `(procNum + halfSize) % ConnSlotCount` offset exists only to keep the
   auto-commit transaction off the connection's own slot; an in-block `Execute`
   must use the connection's real slot. Doc 09 §5 I3 records that this scheme
   already produced `mvcc: unknown transaction` aborts for three
   `-S -M prepared` clients at 50 sustained clients — reproducing and closing
   that is in scope, not a follow-up.
9. **Prepared-mode benchmarking becomes meaningful again** — one commit per
   `-N` transaction, the commit back on `END`, and `-S -M prepared` showing the
   parse/plan ceiling doc 06-03 predicted.
10. **Adjacent divergences are recorded, not silently inherited** — COPY's
    unconditional auto-commit and the simple-path-only handlers
    (two-phase commit, LISTEN/NOTIFY) each get a slice or a ledger row.

## Task list (summary — the design/0132 plan doc is authoritative)

Themes: **A** foundation · **B** correctness · **C** concurrency & protocol
surface · **D** scope boundary · **E** measurement.

| task | what | theme |
|---|---|---|
| S1 | Verification-only: confirm the three doc-09 corrections; add the characterisation tests that fail against today's HEAD | A |
| S2 | `BEGIN`/`COMMIT`/`ROLLBACK` drive the real block state machine, by **extracting** the simple path's verb arm into a shared helper | A |
| S3 | In-block `Execute` reuses `connTx.Tx()`; auto-begin/auto-commit become conditional on `!InExplicit()` | A |
| S4 | Prove the extracted helper carries the simple path's inline deferred-constraint sequence onto the extended `COMMIT` | B |
| S5 | Aborted-block semantics: an in-block error sets `E`, later `Execute`s fail 25P02 until `ROLLBACK` | B |
| S6 | `BEGIN ISOLATION LEVEL …` **and** the session default (`default_transaction_isolation`) over the extended protocol | B |
| S7 | Proc-slot discipline: in-block `Execute` uses the connection's own slot; close the `mvcc: unknown transaction` abort; rule on copy.go's slot | C |
| S8 | **Primary slice** — mixed simple↔extended blocks, the shape real drivers emit | C |
| S9 | `Sync` guard test — `Sync` must NOT end an explicit block | C |
| S10 | SAVEPOINT / subtransactions over the extended protocol — implement or defer **with a ledger row**, against stated criteria | D |
| S11 | Perf acceptance: `-N -M prepared` one commit/txn and TPS ≥ simple mode; `-S -M prepared` parse/plan ceiling (with doc 09's O-XP-1 profile as its precondition) | E |
| S12 | Simple-path-only server-layer handlers (two-phase commit, LISTEN/NOTIFY) over the extended protocol — implement or ledger | D |

**Filing rule (inherited from M0130/M0131):** no task is deferred without a
strong reason recorded in the deferral ledger; every item's subtasks are listed
inline in the fix_plan task body; every non-trivial subsystem lands its design
doc (status `draft` → `accepted`) **within M0132** — a design doc punted past
the milestone is a milestone failure.

**Partial-landing rule, specific to this milestone.** **S2+S3+S4+S5 land
together or not at all.**

- Without S4, a block model that reaches `COMMIT` by any route other than the
  extracted helper would skip the deferred FK/UNIQUE/EXCLUDE sequence and commit
  a block that violates an `INITIALLY DEFERRED` constraint — a *new* correctness
  hole opened by the fix. Today every statement commits individually, so its
  constraints are checked individually; that is the property a half-landing
  destroys.
- Without S5, an error mid-block leaves the block open and un-failed — there is
  no `connTx.Fail()` call site on the extended path at all (its only two are
  `dispatch.go:950` and `:1019`) — so post-error `Execute`s keep running in the
  *same live* transaction and `COMMIT` commits them. PG aborts everything. Same
  argument, different verb.

## Acceptance bar

1. **Block spans Executes:** a test drives Parse/Bind/Execute for `BEGIN`,
   `INSERT`, `SELECT`, `COMMIT` on one connection and asserts the `SELECT` sees
   the uncommitted `INSERT`, that a *second* connection does not see it before
   the `COMMIT`, and that it does after. Fails against today's HEAD.
2. **`ROLLBACK` rolls back:** `BEGIN` → `UPDATE` → `ROLLBACK` over the extended
   protocol leaves the row at its original value, verified from a second
   connection. Fails against today's HEAD in the most damaging possible way (the
   update is already durable).
2b. **The driver shape:** the same assertion with `BEGIN`/`ROLLBACK` sent as
   *simple* queries and the `UPDATE` as Parse/Bind/Execute — what pgx and lib/pq
   actually emit. Must show one transaction, not two, and must fail against
   today's HEAD. Bar 2 without bar 2b tests a sequence most clients never send.
3. **Failure aborts the block:** a constraint violation at statement *k* inside
   a block leaves statements `1..k-1` invisible after the client's `ROLLBACK`,
   and every `Execute` between the error and the `ROLLBACK` fails with 25P02
   `current transaction is aborted`.
4. **Status byte:** `ReadyForQuery` reports `T` after an extended `BEGIN`, `E`
   after an in-block error, `I` after `COMMIT`/`ROLLBACK` — asserted at the
   frame level. (`T`/`I` follow from S2 via the already-shared `w.TxStatusFn`;
   `E` requires S5, which is why S5 is in the atomic set.)
5. **Deferred constraints:** an `INITIALLY DEFERRED` FK violated mid-block and
   repaired before `COMMIT` succeeds; one left unrepaired fails **at the
   `COMMIT`**, not at the statement, and the block is rolled back. Run against
   the FK isolation specs, not only a bespoke unit test.
6. **Isolation level:** `BEGIN ISOLATION LEVEL SERIALIZABLE` over the extended
   protocol produces a SERIALIZABLE transaction (asserted through observable SSI
   behaviour, e.g. a write-skew spec that must abort), not READ COMMITTED.
7. **Mixed paths compose:** `BEGIN` (simple) → `Execute`s (extended) →
   `COMMIT` (simple), and the mirror, form one coherent block — one commit, one
   rollback, one status byte, **one live transaction on the connection**.
8. **`Sync` does not end a block:** a `Sync` between two in-block `Execute`s
   leaves the block open (`T` after the `Sync`, and the second `Execute`'s work
   still rolls back on `ROLLBACK`).
9. **Teardown:** a client that disconnects mid-block has its transaction rolled
   back exactly once — not leaked, and not double-rolled-back once the
   per-`Execute` `defer` at `dispatch_extended.go:143-150` becomes conditional
   (`server.go:1455-1462` is the only cleanup path).
10. **Proc-slot discipline:** a `-S -M prepared` run at ≥50 sustained clients
    completes with zero `mvcc: unknown transaction` aborts (the doc 09 §5 I3
    failure), and a race-detector pass over `internal/server` and `internal/mvcc`
    is clean.
11. **Perf:** `-N -M prepared` at scale 100 shows the commit back on `END` (one
    fsync per transaction, not two) and TPS ≥ the simple-mode 9,898 baseline;
    `-S -M prepared` TPS exceeds the simple-mode 89,955, **or** the O-XP-1
    profile explains the shortfall and the explanation is recorded. Numbers go
    in `analysis/` with the commit hash.
12. **No regressions:** UNITS / SMOKE / SPOT green; `scripts/tpch-spotcheck.sh`
    canonical Q12=2 / Q13=35; `make plan-gate` clean; the D-002 isolation suite
    shows no new divergences; `make race-gate` clean; `make ralph-state-guard`
    clean. `SET LOCAL ROLE` over the extended protocol keeps its current
    observable behaviour or its change is asserted deliberately (see the plan
    doc's G5 note).
13. **Scope honesty:** anything not landed — SAVEPOINT over the extended
    protocol, the COPY-ignores-block gap, and the simple-path-only handlers
    above all — carries a deferral-ledger row naming the gap, the blocker and
    the resume point. The `defer`/`port` state of any affected oracle test in
    `docs/test-port/postgres-oracle-port-status.csv` is updated in the same loop
    that unblocks it.

## Required design docs

| doc | status | covers |
|---|---|---|
| `docs/design/0132-extended-protocol-explicit-transactions.md` | draft — created at filing | authoritative task decomposition (all S-tasks), the doc-09 corrections, the partial-landing rule |
| `docs/design/0132-0001-extended-protocol-explicit-txn-state-machine.md` | draft — created at filing | the core design: block state machine over Parse/Bind/Execute/Sync, the three edit sites, the extraction decision, PG's `TBLOCK_*` mapping, and the invariants |
| `docs/design/0132-0002-extended-commit-deferred-constraints.md` | draft — **within M0132 (S4, before code)** | proving the extracted helper carries `dispatch.go:2818-2828`'s inline deferred sequence onto the extended `COMMIT`; why a partial land is a regression |
| `docs/design/0132-0003-extended-txn-proc-slot-discipline.md` | draft — **within M0132 (S7, before code)** | retiring the `(procNum + halfSize)` offset for in-block statements, the two-live-transactions-per-connection case, the `mvcc: unknown transaction` reproduction, and the copy.go ruling |
| `docs/design/0132-0004-extended-protocol-savepoints.md` | draft — **within M0132 (S10) or a ledger row** | SAVEPOINT/subtransactions over the extended protocol (doc 09 O-XP-2); may resolve as an explicit deferral against the plan doc's stated criteria |

Smaller single-function changes may ride the implementation-plan doc per the
repo rule (a design doc is required for every *non-trivial subsystem*;
single-function changes with unit tests may cite this plan instead).
