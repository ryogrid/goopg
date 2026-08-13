# M0132 — Explicit transactions across the extended query protocol (implementation plan)

status: draft · date: 2026-08-12 · supersedes (on landing): the S1–S5 migration
sketch in
`analysis/perf-optimize3-dash/08-improvement-designs/09-extended-protocol-explicit-txn.md`
(status `design`, 2026-07-14) · milestone:
[`docs/milestones/0132-extended-protocol-explicit-transactions.md`](../milestones/0132-extended-protocol-explicit-transactions.md)

This is the **authoritative task decomposition** for M0132. The technical core —
the block state machine itself — is
[`0132-0001`](0132-0001-extended-protocol-explicit-txn-state-machine.md); this
doc owns the slicing, the ordering constraints, the gates, and the scope
boundary.

## 1. What is wrong

goopg's extended query protocol commits one transaction per `Execute` and
ignores the client's `BEGIN`/`COMMIT`/`ROLLBACK`. The simple query protocol does
not. The consequences are enumerated in the milestone doc; the headline is that
**`ROLLBACK` over the extended protocol does not roll back** — the work is
already durably committed when it arrives, and the client is told the rollback
succeeded.

The three code sites that produce this, all verified at HEAD `28de7c44`
(2026-08-12):

| # | site | today | required |
|---|---|---|---|
| 1 | `internal/server/dispatch_extended.go:110-112` | `*planner.Transaction` returns a bare `CommandTag` | drive the shared block state machine |
| 2 | `internal/server/dispatch_extended.go:134-139` | unconditional `TxnMgr.Begin` on an offset proc slot | conditional: reuse `connTx.Tx()` when `InExplicit()` |
| 3 | `internal/server/dispatch_extended.go:361-365` | unconditional `ectx.CommitTransaction(tx)` | conditional: no commit inside a block |

**The shape that matters most is mixed.** pgx v5 (`conn.go:515`,
*"Always use simple protocol when there are no arguments"*) and lib/pq
(`conn.go:901`) send argument-less statements — including `BEGIN`, `COMMIT`,
`ROLLBACK` — down the *simple* path while parameterised DML goes down the
extended one. Today that produces **two live transactions on one connection**:
the block held on the connection's own slot (`dispatch.go:227-229`) and each
`Execute`'s auto-commit transaction on the offset slot. S8 is therefore a
primary slice, not an edge case.

## 2. Three corrections to doc 09

**C1 — doc 09's S1 is already done.** Its first slice was *"pass the
connection's `connTxState` into `executeExtendedQueryViaExecutor`; no behavior
change yet"*. That parameter is already there:

```go
// internal/server/dispatch_extended.go:30
func (s *Server) executeExtendedQueryViaExecutor(ctx context.Context, sess *config.SessionRegistry,
    query string, params []boundParam, procNum int32, dbName string, connTx *connTxState) (...)
```

It arrived with the role-scoping work and is read for `connTx.NonSuperuserRole`
(`:36`, `:46`, `:238`, `:250`) and statement logging (`:68`, `:71`) — never for
transaction state. `connTx.Begin` / `InExplicit` / `Tx` / `End` have **zero**
call sites in `dispatch_extended.go` or `extended.go` (the only mention is a
comment at `dispatch_extended.go:234`). The plumbing slice is therefore a
verification step, and M0132's first *code* slice is the state machine.

**C2 — there is no `execCommit` route to adopt.** Doc 09 §2/§4.1 says the
extended `COMMIT` should run *"the same `execCommit`"* the simple path uses. The
simple path does not use it. `dispatch.go:2803-2807`:

```
// DEFERRED). The simple-query dispatch bypasses
// transactionOp.execCommit, so the checks queued on the session
// during INSERT/UPDATE/DELETE must be run here BEFORE
// TxnMgr.Commit; a violation aborts the transaction with 23503,
```

The deferred FK / UNIQUE / EXCLUDE passes are inline at `dispatch.go:2818-2828`
(`executor.RunDeferredFKChecks` under `mvcc.Manager.FreshSnapshot`, then
`RunDeferredUniqueChecks`, then `RunDeferredExclusionChecks`), the SSI
pre-commit check inline just after, and the arm returns at `:2980` without ever
reaching the operator build at `:2985`. The bypass was never removed; the
0119-0004 fix *added* the inline sequence.

This reshapes S4. The extended `COMMIT` must inherit that inline sequence, and
it does so **for free if S2 is a genuine extraction**. S4 is therefore the proof
obligation on S2's extraction, not an independent wiring task — and the
land-together rule protects exactly that.

**C3 — `Sync` is already correct; do not "fix" it.** The handler at
`internal/server/server.go:1761-1765` clears `syncRequired` and writes
`ReadyForQuery` without touching transaction state:

```go
case protocol.MsgSync:
    extended.syncRequired = false
    if err := w.ReadyForQuery(); err != nil {
        return
    }
```

That matches PG, where `Sync` closes an *implicit* transaction only and leaves an
explicit block open. The risk during implementation is the opposite of a bug
report: someone reads "Sync ends the transaction" in the protocol docs and adds
an `End()` here, silently converting every block into a one-statement
transaction again. S9 exists to nail this down with a test.

## 3. Ordering constraints

```
S1 (verify + characterisation tests)
      │
      ▼
S2 (block state machine, by EXTRACTION) ──┐
S3 (in-block tx reuse)                 ───┤
S4 (deferred-constraint proof)         ───┼──► ONE commit ──► S6, S7, S8, S9, S12
S5 (aborted-block semantics)           ───┘                        │
                                                                   ▼
                                                             S10 (scope call)
                                                                   │
                                                                   ▼
                                                             S11 (perf accept) ──► S13 (prepared A/B)
```

**The S2+S3+S4+S5 atomicity rule.** Two independent arguments, both of the form
*"the half-landing opens a hole that does not exist today"*:

- **S4.** Today every statement commits individually, so its deferred
  constraints are checked individually by `dispatch.go:2818-2828` on the
  auto-commit path. A block model whose `COMMIT` reaches `TxnMgr.Commit` by any
  route other than the extracted helper would skip that sequence and commit a
  block violating an `INITIALLY DEFERRED` constraint. This is the risk the
  deferral ledger recorded on 2026-07-14 (*"a partial land would silently skip
  INITIALLY DEFERRED constraints"*) and why that loop deferred rather than
  landing half.
- **S5.** There is **no** `connTx.Fail()` call site on the extended path — its
  only two are `dispatch.go:950` and `:1019`. Without S5, an error mid-block
  leaves the block open and un-failed, so post-error `Execute`s keep running in
  the *same live* transaction and `COMMIT` commits them. PG aborts everything.

The four slices may be developed separately; they land in one commit. S6–S9 and
S12 are independent and may land in any order after it. S10 is a decision. S11
and S13 are measurement and run last; S13's prepared>simple gate additionally
requires the S2–S8 set to have landed.

## 4. Slices

### S1 — Verification + characterisation tests (no behaviour change)

- Confirm and record C1, C2 and C3 above.
- Add the tests that *fail against HEAD* and become the acceptance bar:
  block-spans-Executes, rollback-actually-rolls-back, **the mixed driver shape**
  (bar 2b), status-byte, aborted block. They go red at S1 and green at S2–S5; a
  milestone whose first slice cannot produce a red test has not reproduced the
  bug.
- Gates: `go test ./internal/server/` (new tests red, everything else green).

### S2 — `BEGIN`/`COMMIT`/`ROLLBACK` drive the block state machine

Replace the short-circuit at `dispatch_extended.go:110-112` by **extracting**
the simple path's verb arm (`dispatch.go:2696-2984`, 289 lines) into one shared
helper both dispatchers call. Design:
[`0132-0001`](0132-0001-extended-protocol-explicit-txn-state-machine.md) §3 D1.
Extraction — not mirroring — is what makes S4 free and keeps the two paths from
diverging again.

- Gates: `go test ./internal/server/ ./internal/executor/`, D-002 isolation.
- **Lands with S3+S4+S5.**

### S3 — In-block `Execute` reuses the open transaction

Sites 2 and 3 from §1 become conditional on `connTx.InExplicit()`, mirroring
`dispatch.go:227-229`:

```go
if connTx != nil && connTx.InExplicit() {
    tx = connTx.Tx()
    autoCommit = false
}
```

- Gates: as S2, plus `scripts/tpch-spotcheck.sh` (canonical Q12=2/Q13=35) and
  `make plan-gate`. **Note the rationale:** the spotcheck's `cmd/tpch-runner`
  uses `lib/pq` with zero-argument `QueryContext` calls
  (`cmd/tpch-runner/main.go:237`, `:392`), so it exercises the **simple**
  protocol. It is the right gate because S2 refactors the simple path — not
  because it covers the extended one. Extended-path coverage comes from the S1
  tests and S11's pgbench `-M prepared` run.
- **Lands with S2+S4+S5.**

### S4 — Deferred-constraint checks at the extended `COMMIT`

Prove that the extracted helper carries `dispatch.go:2818-2828`'s inline
deferred FK/UNIQUE/EXCLUDE sequence (and the SSI pre-commit check that follows
it) onto the extended `COMMIT`. If S2's extraction is faithful this is a test,
not a change; if it is not faithful, S2 is wrong and this slice is where that
surfaces. Design: `0132-0002` (write before code).

- Gates: FK isolation specs + `internal/testport/` deferred-constraint tests — a
  bespoke unit test alone does not discharge this.
- **Lands with S2+S3+S5.**

### S5 — Aborted-block semantics

An error inside an open block marks it failed via `connTxState.Fail()`
(`conn_tx.go:276`), so `wireStatus` reports `E` and subsequent `Execute`s fail
25P02 `current transaction is aborted` until `ROLLBACK` (or a `COMMIT` that PG
converts to one). The extended message loop has no `Fail()` call site today, and
its only `'Z'` write is the plain `ReadyForQuery()` (`protocol/messages.go:152`,
`afterError=false`), so the `ReadyForQueryAfterError` escape hatch
(`messages.go:156-164`) that rescues the simple path is unavailable — this slice
must add the call site, not assume inheritance.

- Gates: `go test ./internal/server/`, D-002 isolation.
- **Lands with S2+S3+S4.**

### S6 — Isolation level over the extended protocol

Two inputs, both currently ignored:

1. `BEGIN ISOLATION LEVEL …` — honour `txNode.IsolationLevel` the way
   `dispatch.go:2709-2738` does, including its subtlety that the placeholder
   transaction at the wrong level must be rolled back before the
   correctly-levelled one is begun (so no XID or SSI bookkeeping leaks) **and**
   the RC snapshot re-capture at `:2729-2736`.
2. The session default — `dispatch_extended.go:139` hardcodes
   `mvcc.IsolationReadCommitted` and ignores `default_transaction_isolation` /
   `setTransactionOp` (`operators_tx.go:586`). Note `execBegin`
   (`operators_tx.go:70`) *does* honour `Session.IsolationLevel()` while neither
   dispatch path does, and the simple path hardcodes RC identically at
   `dispatch.go:236` — so input 2 is **parity-neutral**, not a divergence. Fix
   it or state that it is deliberately out of scope; do not land input 1 and
   imply both are done.

Retires the `dispatch_extended.go:119-123` comment recording SERIALIZABLE as
unreachable.

- Gates: SSI/RR isolation specs (a write-skew spec must abort under
  SERIALIZABLE opened over the extended protocol).

### S7 — Proc-slot discipline

`(procNum + halfSize) % mvcc.ConnSlotCount` (`dispatch_extended.go:137-138`)
exists to keep the per-`Execute` autocommit transaction off the connection's own
ProcArray slot; after S3 it is correct **only** for the out-of-block case. Doc
09 §5 I3 records three `-S -M prepared` clients aborting with
`mvcc: unknown transaction` at 50 sustained clients — reproduce it, then close
it. The mixed-protocol shape (§1) is the leading hypothesis: two live
transactions per connection on two slots is the pre-existing condition, which
the "offset scheme collides" framing alone does not explain.

This slice must also **rule on `copy.go`'s slot**
(`internal/server/copy.go:157-167`, the same offset, cited by
`dispatch_extended.go:136` as its model) — either bring it into the same
discipline or state why it stays. Design: `0132-0003` (write before code).

- Gates: `make race-gate`, a ≥50-client `-S -M prepared` run with zero aborts.

### S8 — Mixed simple↔extended blocks (**primary**)

`BEGIN` via simple query then `Execute`s via the extended protocol, and the
mirror, must form ONE block. Per §1 this is what pgx and lib/pq actually emit,
so it is the slice that decides whether real clients are fixed.

Concrete scope (this slice is not "write a spec and see"):

- one live transaction on the connection — assert the extended `Execute` does
  **not** allocate a second one on the offset slot;
- `COMMIT`/`ROLLBACK` on either protocol finalises work done on the other;
- the status byte is coherent across the switch;
- an in-block error on one protocol aborts statements on the other;
- a D-002 isolation spec pins the interleaving, and a driver-level test using
  `lib/pq` (already vendored, already used by `cmd/tpch-runner`) proves the
  real-world shape end to end.

- Gates: D-002 isolation spec + the driver-level test.

### S9 — `Sync` guard

A test that keeps C3 true: a `Sync` between two in-block `Execute`s leaves the
block open (`T` after the `Sync`; the second `Execute`'s work still rolls back
on `ROLLBACK`).

- Gates: `go test ./internal/server/`.

### S10 — SAVEPOINT over the extended protocol (scope call)

Doc 09 O-XP-2 deferred subtransactions. Decide against stated criteria rather
than by feel — **implement if** the extracted helper already routes
`TxSavepoint`/`TxRelease`/`TxRollbackTo` correctly and the D-002 savepoint specs
pass unmodified over the extended protocol; **defer with a ledger row if**
either requires new sub-transaction plumbing, since the known hazards
(`savepoint_before_write_parent_zero`,
`savepoint_rollback_visibility_three_fixes`) make that its own milestone-sized
risk.

Either way the helper must handle the three verbs **explicitly** — implemented,
or returning `0A000 feature_not_supported` on the extended path. Falling through
to a bare tag is today's silent-no-op bug wearing a different verb and is not an
acceptable outcome. Model: `operators_tx.go:456` / `:497` / `:518`.

### S11 — Perf acceptance

`analysis/perf-optimize3/scripts/run_rw50.sh` `-M prepared` at scale 100.

**Precondition (doc 09 O-XP-1, adopted here rather than dropped):** profile
`-S -M prepared` *before* judging criterion 2, to confirm where the per-`Execute`
overhead that ate the read-path parse/plan saving actually lives (message
parsing, `TxnMgr.Begin`, snapshot capture). Without it, criterion 2 can fail for
reasons M0132 does not control and the milestone has no ruling on what a miss
means.

1. `-N`: commit back on `END` (one fsync per transaction); TPS ≥ the simple-mode
   9,898 baseline.
2. `-S`: TPS above the simple-mode 89,955 — or the O-XP-1 profile explains the
   shortfall and the explanation is recorded in `analysis/`.
3. `analysis/perf-optimize3/scripts/aux2_fsync_probe.sh`: `-N` fsync count per
   60 s back to ~one per transaction group.

Per the benchmarking practice card: hold server age constant across the A/B, run
capped (`scripts/goopg-test-run.sh`, distinct `GOOPG_CG_UNIT`), and record the
numbers in `analysis/` with the commit hash.

### S12 — Simple-path-only server-layer handlers

`PREPARE TRANSACTION` / `COMMIT PREPARED` / `ROLLBACK PREPARED`
(`execTwoPhaseStmt`, `internal/server/twophase.go:107`, called **only** from
`dispatch.go:2666`) and `LISTEN` / `NOTIFY` / `UNLISTEN` (`execNotifyStmt`,
`dispatch.go:2659`) are intercepted before planning on the simple path only.
They are not `planner.Transaction` verbs — that type has exactly
`TxBegin, TxCommit, TxRollback, TxSavepoint, TxRelease, TxRollbackTo`
(`internal/planner/plan.go:2045-2052`) — so over the extended protocol they fail
at `internal/planner/planner.go:289` `"unsupported statement type %T"`.

Implement the interception on the extended path, or record a ledger row per
family. Note `twophase.go:227` calls `connTx.End()`, so two-phase commit
interacts directly with the state machine S2 introduces and cannot simply be
ignored.

- Gates: `go test ./internal/server/`, plus the two-phase and LISTEN/NOTIFY
  isolation specs if implemented.

### S13 — Prepared-statement verification + `-M prepared` vs simple A/B

Prepared statements are the extended protocol's Parse/Bind/Execute path, so they
sit inside this milestone's surface whether or not they touch explicit
transactions. This slice closes the loop on two things S1–S12 leave implicit:
that the prepared path *works* at all, and that it is *faster* than the simple
path once the correctness set lands.

1. **Verify.** Run `pgbench -M prepared` against a fresh goopg server and confirm
   the prepared-statement path (Parse/Bind/Execute/Describe/Sync) returns correct
   results with no errors. If it is broken, fix it and re-verify — a non-trivial
   fix lands its own sub-design doc per the repo rule.
2. **Measure, under the pre-commit hook's conditions.** Scale 1, `-c 2 -j 2 -T
   30`, the pinned `postgres/local_install/bin/pgbench`, a capped server and
   `--no-sync` init — the exact Part-2 shape `.githooks/pre-commit` runs
   (`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`). Warm up first
   with a plain (no `-M`) run, then run the standard / `-N` / `-S` workloads
   once without `-M` and once with `-M prepared`, recording TPS and latency for
   both.
3. **Assert prepared > simple.** Prepared-mode TPS must exceed simple-mode TPS.
   At HEAD it does not — the per-`Execute` auto-commit makes `-M prepared`
   slower (doc 09 §"The performance finding": `-N` prepared 6,749 vs simple
   9,898) — so this gate is satisfiable only after S2–S8; the verification half
   may run first, the A/B runs after. For the `-S` case only, doc 09's O-XP-1
   read-path profile is the escape hatch: if prepared `-S` does not beat simple,
   profile *where* the per-`Execute` overhead lives before judging.

- Gates: `scripts/tpch-spotcheck.sh` (Q12=2/Q13=35), pgbench smoke via the hook.
  Numbers recorded in `analysis/` with the commit hash (per the benchmarking
  practice card: hold server age constant across the A/B, run capped with a
  distinct `GOOPG_CG_UNIT`).

## 5. Test-impact matrix

| test | file | slice |
|---|---|---|
| extended-protocol sequence tests | `internal/server/extended_test.go` | S1, S2, S3 |
| explicit-txn over extended (new) | `internal/server/` + D-002 | S2, S3, S5 |
| **mixed simple↔extended, incl. a `lib/pq` driver test (new)** | `internal/server/` + D-002 | S1 (red), S8 |
| deferred FK/UNIQUE/EXCLUDE at extended COMMIT | FK isolation specs, `internal/testport/` | S4 |
| isolation level over extended | SSI/RR isolation specs | S6 |
| proc-slot / concurrency | `make race-gate`, pgbench `-S -M prepared` | S7 |
| `Sync` does not end a block (new) | `internal/server/` | S9 |
| teardown mid-block (new) | `internal/server/` | S3 (bar 9) |
| two-phase / LISTEN-NOTIFY over extended | `internal/server/`, D-002 | S12 |
| pgbench `-M prepared` | `analysis/` run + commit hook | S11 |
| pgbench `-M prepared` vs simple A/B (hook conditions) | `analysis/` run + commit hook | S13 |

## 6. Adjacent divergences this milestone records but does not close

- **COPY ignores an open block on both protocols.** `copy.go:157-167` says so
  outright. `BEGIN; COPY …; ROLLBACK` does not roll back today, simple path
  included. S7 rules on its proc slot; the behavioural gap gets a ledger row.
- **`SET LOCAL ROLE` semantics change silently under S2 (G5).**
  `connTx.SnapshotLocalRoleIfNeeded` (`conn_tx.go:390-396`) returns immediately
  when `!c.active`, so the extended path's calls
  (`dispatch_extended.go:252`, `extended.go:693`, `:712`) currently never record
  a restore point and `End()`'s revert never fires. The moment S2 sets `active`,
  `SET LOCAL ROLE` over the extended protocol starts reverting at
  COMMIT/ROLLBACK. That is arguably the *correct* PG behaviour, but no slice
  claims it and no gate covers it — S2 must assert the new behaviour
  deliberately or suppress it explicitly.

## 7. Open questions

- **O-1 (from doc 09 O-XP-3)** — portal semantics across a block: does goopg's
  portal model already support an open portal spanning statements inside a
  block, or is that additional work? Answer before S3 closes.
- **O-2** — `executeExtendedQuery`'s pre-parse fast paths return without
  touching a transaction: the empty-query check (`extended.go:436-438`) and the
  literal `SELECT 1` short-circuit (`:440-453`), plus the `SHOW`/`SET` family
  below them. Inside an open block that is *probably* fine (they read nothing
  transactional), but each needs an explicit ruling, because a fast path that
  bypasses the block is exactly the shape of bug this milestone exists to fix.
- **O-3** — does the extracted helper change `twophase.go`'s `connTx.End()`
  interaction (`:227`)? S12 owns the answer; S2 must not land assuming it is
  unaffected.

## 8. Upstream reference

- `postgres/src/backend/tcop/postgres.c` — `exec_execute_message`,
  `start_xact_command` / `finish_xact_command`: a command is bracketed, a
  protocol message is not; an open block suppresses the implicit commit.
- `postgres/src/backend/access/transam/xact.c` — `BeginTransactionBlock` /
  `EndTransactionBlock` and the `TBLOCK_*` state machine goopg's
  `*planner.Transaction` short-circuit currently stubs out.
- `postgres/official_docs_in_md/` — protocol chapter, for the `ReadyForQuery`
  transaction-status byte semantics (`I` / `T` / `E`) asserted in acceptance
  bar 4.
