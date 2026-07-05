# root-0024 — Simple-query multi-statement batch: CREATE TABLE/INDEX atomicity on abort

## Context

Discovered as a fresh, non-view-specific finding while auditing `ALTER VIEW`
grammar (M0110-0001 DU-002 slice 444, deferral-ledger row 2026-07-04):

```
CREATE TABLE t (a int);
ALTER TABLE t ALTER COLUMN a SET STORAGE external;
SELECT 1/0;
```

sent as one `psql -c "stmt1; stmt2; stmt3"` simple-query message left `t`
permanently registered in the live catalog (`\d t` shows it, `INSERT INTO t`
works) but with **zero** `pg_attribute` rows — `pg_dump` emits
`CREATE TABLE t (\n);`. Real PostgreSQL's simple-query protocol treats the
whole multi-statement message as one implicit transaction
(`postgres/src/backend/tcop/postgres.c` `exec_simple_query`); a later
statement's failure rolls back *everything* in that message, including an
earlier successful `CREATE TABLE`. goopg's dispatch already begins exactly one
`mvcc.Transaction` per Query message (`dispatchSimpleQueryViaExecutor`,
`internal/server/dispatch.go`), so the transactional catalog-heap writes
(`pg_class`/`pg_attribute` rows, written through that same `tx`) correctly
rolled back — but `catalog.InMemory.RegisterTable`, the live in-memory
catalog mutation `execCreateTable` performs, is **not** transactional; it was
never undone.

## Root cause

`sess.RecordDDLCreate` (`internal/executor/session.go`) — the mechanism that
already lets an explicit `BEGIN; CREATE TABLE; ROLLBACK;` clean up correctly —
records a pending `DDLUndoEntry` any time `o.ctx.Session` is a
`*executor.BasicSession`. `ProcessRollbackUndos` (`internal/executor/
operators_tx.go`) drains that list and calls `rollbackDDLCreate` (which does
`catalog.DropTable`/`DropIndex` plus stamps `xmax` on the catalog-heap rows)
for each entry — but it was only ever invoked from two places: the explicit
`ROLLBACK` statement handler and the `TxRollback` shortcut, both keyed off
`connTx.Session()`.

`connTx.Session()` (`internal/server/conn_tx.go`) returns the `*BasicSession`
**only when an explicit `BEGIN` is active** — for a plain autocommit
multi-statement batch it returns `nil`, so `dispatchSimpleQueryViaExecutor`'s
`ectx.Session` stayed `nil` for the whole message. Two consequences:

1. `RecordDDLCreate`'s type assertion (`o.ctx.Session.(*BasicSession)`) never
   succeeded, so the `CREATE TABLE` was never tracked for undo in the first
   place.
2. Even had it been tracked, the top-level `defer` in
   `dispatchSimpleQueryViaExecutor` that rolls back the implicit `tx` on abort
   (`autoCommit && !commit`) never called `ProcessRollbackUndos` — it went
   straight to `s.cfg.TxnMgr.Rollback(tx)`.

## Fix

`internal/server/dispatch.go`:

- `ectx` is now predeclared (`var ectx *executor.Context`) above the abort
  defer so the defer can reach it (it is assigned by `executor.NewContext()`
  a few lines later, once the executor context is actually built).
- The connTx-wiring block that sets `ectx.Session = sess` when an explicit
  transaction is active now has an `else` arm: when `connTx.Session()` is
  `nil` (autocommit), it wires a **message-scoped, throwaway**
  `executor.NewBasicSession()` instead of leaving `ectx.Session` nil. This
  session is never shared with `connTx` and is discarded when the function
  returns — it exists purely so `RecordDDLCreate` has somewhere to record
  against for the duration of this one Query message.
- The abort defer now calls `executor.ProcessRollbackUndos(ectx, bs)` (type
  asserting `ectx.Session` to `*executor.BasicSession`) **before**
  `s.cfg.TxnMgr.Rollback(tx)`, mirroring the ordering `ProcessRollbackUndos`'s
  own doc comment requires (catalog lookups must still work at that point).

`internal/executor/session.go`:

- `RecordDDLCreate` had a dead-code guard, `if !s.inTx { return }`. Historically
  every prior caller of this method only ever ran with `inTx == true` — the
  only way to obtain a non-nil `*BasicSession` in `ectx.Session` before this
  fix was `connTx.Session()`, which is populated exclusively by
  `connTx.Begin()`, and `Begin()` unconditionally calls
  `BeginExplicitTransaction` (`inTx = true`) first. The guard was therefore
  never actually reached with `inTx == false` — until this fix's throwaway
  autocommit-batch session, which deliberately keeps `inTx == false` (see
  "Why `inTx` stays false" below). Removed; the doc comment now describes the
  real invariant: recording is unconditional, and a session that never aborts
  simply never has its list drained (`ProcessRollbackUndos` is the only
  reader, and it is only called on an actual abort path).

### Why `inTx` stays false on the throwaway session

`BasicSession.InExplicitTransaction()` (`== s.inTx`) gates roughly twenty
other call sites unrelated to this bug — deferred UNIQUE/EXCLUDE/FK constraint
timing (`deferred_unique.go`, `deferred_exclusion.go`, `operators_fk.go`),
`TRUNCATE`/`DROP TABLE`-inside-savepoint page-snapshotting, enum/composite
pending-create tracking, `pg_stat_*` cumulative-stats scoping. Flipping
`inTx = true` on the throwaway session (e.g. via `BeginExplicitTransaction`)
would silently change ALL of those for every autocommit statement, which is a
much larger blast radius than this fix's scope and was deliberately avoided —
see "Deferred" below. The throwaway session therefore stays `inTx == false`
end-to-end; only `RecordDDLCreate`/`ProcessRollbackUndos` were touched.

## Verification

- `TestSimpleQueryBatchAbortUndoesEarlierCreateTable`
  (`internal/server/dispatch_batch_atomicity_test.go`): drives the exact
  `CREATE TABLE ...; SELECT * FROM <missing>;` batch over a real wire
  connection (`startCopyExecServer`), asserts a `CREATE TABLE` CommandComplete
  followed by an ErrorResponse, then asserts the table is **not** found in the
  live catalog afterward (confirmed RED without the fix — the table survived).
  A second assertion confirms a standalone (non-aborting) `CREATE TABLE` in
  its own message still persists normally, guarding against an
  over-broad "autocommit DDL is now always transient" regression.
- Full suites: `internal/server`, `internal/executor`, `internal/catalog`,
  `internal/mvcc`, `internal/wal`, `internal/initdb` — all PASS, no
  regression.
- `-race`: `internal/mvcc`, `internal/wal`, `internal/server` — PASS (practice
  card requirement for transaction-rollback-adjacent changes).
- `scripts/tpch-spotcheck.sh`: PASS (Q12=2, Q13=33).
- pgbench smoke: enforced by the `.githooks/pre-commit` hook at commit time.

## Deferred

- ~~**Enum/composite-type creation ... NOT yet undo-aware for an autocommit
  multi-statement batch.**~~ **CLOSED for the single-message case — see
  "Follow-up: enum/composite-type creation now undo-aware for a
  single-message autocommit batch" below.** ~~TRUNCATE tracking remains
  explicit-transaction-only.~~ **CLOSED — see "Follow-up: TRUNCATE undo
  tracking now autocommit-batch-aware" below.** `DROP TABLE`-inside-savepoint
  undo tracking (`RecordDDLDrop`) is intentionally left `inTx`-gated: it is
  additionally gated on `SavepointDepth() > 0`, and `SAVEPOINT` itself raises
  `25P01` ("SAVEPOINT can only be used in transaction blocks") outside an
  explicit transaction (`execSavepoint`, `internal/executor/operators_tx.go`),
  so that code path is structurally unreachable from a message-scoped
  autocommit session regardless of its `InExplicitTransaction()` gate —
  nothing to fix there.
- ~~**A `CREATE TABLE t1(...); BEGIN; CREATE TABLE t2(...); ROLLBACK;`
  compound batch still loses `t1`'s undo entry.**~~ **CLOSED — see
  "Follow-up" below.**
- ~~**The mid-batch-`BEGIN` compound-batch combination is NOT covered by the
  enum/composite follow-up below.**~~ **CLOSED — confirmed already resolved,
  see "Follow-up: mid-batch-`BEGIN` combination confirmed already resolved
  for enum/composite" below.** The prior reasoning above (that the
  `connTx.InExplicit()` write-back guard skips carrying
  `ectx.PendingCreatedEnums`/etc. into `connTx` "before `BEGIN` promotes the
  session") was mistaken: the write-back runs unconditionally after **every**
  successful statement, including `BEGIN` itself, and by the time `BEGIN`'s
  own statement finishes, `connTx.InExplicit()` is already `true`
  (`connTx.Begin()` runs earlier in the same `TxBegin` case) — so the very
  next write-back after `BEGIN` already carries the pre-`BEGIN` autocommit
  entries into `connTx`. No code change was needed; this loop only added
  regression coverage and corrected the record.

Deferral ledger: row appended (M0110-0001, resolved) cross-referencing this
doc for both residuals.

## Follow-up: mid-batch `BEGIN` preserves the throwaway session's pending DDL creates (2026-07-04, loop #103)

Closes the second residual above. `internal/server/dispatch.go`'s
`planner.TxBegin` case (inside `executeOneSimpleStmt`) now drains
`ctx.Session`'s pending DDL-create undo list via
`(*executor.BasicSession).TakePendingDDLCreates()` **before**
`connTx.Begin(ctx.Tx)` lazily allocates the real, empty `*BasicSession` that
`ctx.Session` gets re-wired to for the rest of the batch, then replays each
drained entry onto the new session via `RecordDDLCreate` immediately after
the re-wire. This is a plain hand-off, not a new mechanism: `TakePendingDDLCreates`/
`RecordDDLCreate` already existed for `execRollback`'s ordinary use of the
list.

`t1`'s `CREATE TABLE` (recorded on the message-scoped throwaway session
before any explicit transaction exists, per root-0024's original fix) now
survives the mid-batch session hand-off, so a `ROLLBACK` later in the same
message correctly undoes it via `ProcessRollbackUndos` alongside `t2`
(recorded on the real session, unaffected either way).

Test: `TestSimpleQueryBatchExplicitBeginUndoesEarlierAutocommitCreateTable`
(`internal/server/dispatch_batch_atomicity_test.go`) drives
`CREATE TABLE t1(...); BEGIN; CREATE TABLE t2(...); ROLLBACK;` as one
simple-query message over a real wire connection and asserts neither table
exists afterward. Confirmed RED against the pre-fix tree (reverting only
`dispatch.go`): `t1` survives, `t2` does not.

The first residual (enum/composite-type creation and TRUNCATE/DROP-in-
savepoint tracking staying explicit-transaction-only for autocommit
batches) is untouched by this follow-up and remains open — it needs the
dedicated `BasicSession` flag described above, not this hand-off fix, since
those code paths are gated on `InExplicitTransaction()` rather than tracked
via an always-recording list like `pendingDDL`.

Gates: `go build ./...` clean; `go test ./internal/server/... ./internal/executor/...
./internal/catalog/...` PASS (full, no regressions); `go test -race
./internal/wal/... ./internal/mvcc/...` PASS (one unrelated pre-existing
flake in `internal/wal`'s `TestConcurrentAppendAcrossSegmentBoundariesNoOverflow`
reproduced identically on the pre-fix tree run in isolation and passed on
rerun — not caused by this change); TPC-H spotcheck; pgbench smoke via the
pre-commit hook.

## Follow-up: `connTxState.Session()` returned a stale reused session, corrupting UNRELATED autocommit rollbacks (2026-07-04)

Discovered while scoping the first residual above (enum/composite-creation
tracking): a *more severe*, previously-undocumented bug in this fix's own
message-scoped-throwaway-session mechanism. `connTxState.Session()`
(`internal/server/conn_tx.go`) is documented to "return nil when no explicit
transaction is active", but its implementation just returned `c.sess`
unconditionally. `c.sess` is allocated lazily by `Begin()` and — critically —
is **never reset to nil by `End()`** (only `c.active` flips back to `false`);
it is reused as the backing object for the connection's *next* explicit
transaction. So once a connection had run even one `BEGIN...COMMIT`/`ROLLBACK`
in its lifetime, `Session()` kept returning that same (idle, but non-nil)
object forever after — not nil.

Consequence: dispatch.go's per-message wiring
(`if sess := connTx.Session(); sess != nil { ectx.Session = sess } else {
ectx.Session = <fresh message-scoped throwaway> }`, the root-0024 original
fix) picked the **stale reused session** instead of a fresh throwaway one for
every LATER autocommit statement on that connection. A successful, standalone
autocommit `CREATE TABLE` then called `RecordDDLCreate` against that reused
session, appending to its `pendingDDL` list — and nothing drains that list on
a *successful* autocommit statement (only `ProcessRollbackUndos`, invoked on
abort, or `EndExplicitTransaction`, invoked on COMMIT/ROLLBACK, ever call
`TakePendingDDLCreates`). The stale entry sat there until a **wholly
unrelated, later** aborting autocommit batch on the same connection ran
`ProcessRollbackUndos` against that same reused session — draining and
undoing the old entry too, **dropping an already-committed table as
collateral damage**.

Reproduced directly: `BEGIN; CREATE TABLE warm(...); COMMIT;` then, on the
same connection, `CREATE TABLE survivor(...);` (succeeds) then
`CREATE TABLE unrelated(...); SELECT * FROM missing;` (aborts) — `survivor`
was incorrectly dropped by the second batch's rollback.

Fix: `Session()` now gates on `c.active`, returning nil unless an explicit
transaction is currently active — matching its own doc comment. All other
`Session()` call sites (`dispatch.go`'s `TxBegin`/`TxCommit`/`TxRollback`
cases, `twophase.go`) already run only after confirming `connTx.InExplicit()`
(or immediately after `connTx.Begin()`), so `c.active` is already true at
those call sites — this fix changes nothing there. It only changes the
dispatch-entry wiring's behavior for autocommit statements on a connection
that has previously run an explicit transaction, correctly giving them a
fresh, message-scoped throwaway session (discarded at the end of the
message) instead of the connection's stale reused one.

Test: `TestConnTxSessionNilWhenNotExplicit`
(`internal/server/conn_tx_session_reuse_test.go`) drives exactly the
warm/survivor/unrelated sequence above over a real wire connection. Confirmed
RED against the pre-fix tree (reverting only `conn_tx.go`): `survivor` is
incorrectly dropped.

This follow-up does not touch the still-open first residual (enum/composite-
type creation and TRUNCATE/DROP-in-savepoint tracking remaining
explicit-transaction-only for autocommit batches) — that still needs the
dedicated `BasicSession` flag described above.

Gates: `go build ./...` clean; `go test ./internal/server/...
./internal/executor/... ./internal/catalog/...` PASS (full, no regressions);
`go test -race ./internal/wal/... ./internal/mvcc/...` PASS;
`TestPort_PgDumpConnectionSetup` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via the pre-commit hook.

## Follow-up: enum/composite-type creation now undo-aware for a single-message autocommit batch (2026-07-04)

Closes the first residual above for the single-message case (the
mid-batch-`BEGIN` combination is a separate, still-open residual — see
"Deferred" above). Before this fix, `CREATE TYPE mood AS ENUM ('a','b');
SELECT 1/0;` sent as one autocommit simple-query message left `mood`
permanently registered despite the whole implicit batch transaction rolling
back everywhere else — the same bug class this design closed for `CREATE
TABLE`/`CREATE INDEX`, but enum/composite tracking was never reachable at all
for an autocommit batch: the four record sites in
`internal/executor/operators_ddl.go` (composite creation, enum creation,
`ALTER TYPE ... RENAME TO`, `ALTER TYPE ... ADD VALUE`) all gated on
`Session.InExplicitTransaction()`, which is `false` on the message-scoped
throwaway session by design (see "Why `inTx` stays false" above).

Fix, deliberately NOT touching `inTx`/`InExplicitTransaction()` itself (per
this doc's own resume point, to avoid perturbing the ~20 unrelated call sites
gated on it):

- `internal/executor/session.go`: new `Session.TracksDDLUndo() bool`
  interface method, implemented as `s.inTx || s.autocommitUndoScope` — true
  for both a real explicit transaction and the new
  `NewAutocommitUndoSession()` constructor (a thin wrapper over
  `NewBasicSession()` that sets `autocommitUndoScope = true`, `inTx` still
  `false`).
- `internal/server/dispatch.go`'s autocommit-branch wiring now constructs
  `executor.NewAutocommitUndoSession()` instead of `executor.NewBasicSession()`.
- The four record sites in `operators_ddl.go` now gate on
  `Session.TracksDDLUndo()` instead of `Session.InExplicitTransaction()`.
- The abort defer in `dispatchSimpleQueryViaExecutor` now also calls the
  newly-exported `executor.UndoEnumDDLOnAbort(ectx)` (a thin wrapper over the
  existing unexported `undoEnumDDLFromContext`) alongside its existing
  `ProcessRollbackUndos` call.

**The write-back hazard this fix had to close in the same change:**
`ectx.PendingCreatedEnums`/`PendingCreatedComposites`/`PendingEnumValues`/
`PendingEnumRenames` are fields on `*executor.Context`, not on the session —
`dispatch.go` already wrote them back into `connTx` unconditionally after
every successful statement (so a real, multi-message explicit transaction
carries its pending set across Query messages until its own COMMIT/ROLLBACK).
Before this fix that write-back was harmless for autocommit because the old
gate meant `ectx.PendingCreatedEnums` etc. stayed `nil` throughout an
autocommit batch. Making the record sites unconditionally-tracking for the
throwaway session meant the write-back would otherwise leak a *successfully
committed* autocommit type's pending-set entry into `connTx` past the end of
its own message — a later, wholly unrelated aborting autocommit batch on the
same connection would then call `UndoEnumDDLOnAbort` and incorrectly **drop
the already-committed type as collateral damage**, the identical bug class
the `connTxState.Session()` staleness follow-up above just closed for
`pendingDDL`. Closed by guarding the write-back with `connTx.InExplicit()`:
it now only fires while a real explicit transaction is open, which is also
the only case that still needs it (a single-message autocommit batch never
needs `connTx` to remember anything past its own message — `ectx` already
persists naturally across all statements within that one message without
help from `connTx`).

Test: `TestSimpleQueryBatchAbortUndoesEarlierCreateType`
(`internal/server/dispatch_batch_atomicity_test.go`) drives
`CREATE TYPE ... AS ENUM (...); SELECT * FROM <missing>;` over a real wire
connection, asserts a `CREATE TYPE` CommandComplete followed by an
ErrorResponse, then asserts the enum is **not** found in the live catalog
afterward (confirmed RED without the fix — the type survived). A second
assertion confirms a standalone (non-aborting) `CREATE TYPE` in its own
message still persists normally.

Gates: `go build ./...` clean; `go test ./internal/server/...
./internal/executor/... ./internal/catalog/...` PASS (full, no regressions,
`-count=1`); `go test -race -count=1 ./internal/wal/... ./internal/mvcc/...`
PASS; `scripts/tpch-spotcheck.sh` PASS; pgbench smoke via the pre-commit hook.

## Follow-up: mid-batch-`BEGIN` combination confirmed already resolved for enum/composite (2026-07-04)

While scoping the remaining open item in "Deferred" above (TRUNCATE/DROP-in-
savepoint tracking), re-examined the other listed residual — the
`CREATE TYPE mood AS ENUM(...); BEGIN; ...; ROLLBACK;` mid-batch-`BEGIN`
combination — before starting on the TRUNCATE work, since it was flagged as
"NOT yet built" by the loop that closed the single-message case. Tracing the
actual code (not re-deriving from the prior note) showed the claim was wrong:

- `ectx.PendingCreatedEnums`/`PendingCreatedComposites`/`PendingEnumValues`/
  `PendingEnumRenames` live on `*executor.Context` (`ectx`), which is
  allocated once per simple-query message and never replaced mid-message —
  unlike `pendingDDL`, which lives on the `*executor.BasicSession` object
  that `BEGIN` replaces (hence `pendingDDL` genuinely needed the
  `priorDDLCreates` drain-and-replay hand-off in the loop #103 follow-up
  above).
- `dispatch.go`'s `ectx.Pending*` → `connTx` write-back (guarded by
  `connTx.InExplicit()`, added by the enum/composite follow-up above) runs
  **after every successful statement**, not only after non-transaction-control
  ones. `BEGIN` is itself a successful statement that reaches this same
  post-statement code path, and by then `connTx.InExplicit()` is already
  `true` (`connTx.Begin()` executes earlier in the same `TxBegin` case, before
  `executeOneSimpleStmt` returns). So the write-back that fires right after
  `BEGIN` completes already copies whatever `ectx.PendingCreatedEnums` etc.
  held at that point — including any pre-`BEGIN` autocommit `CREATE TYPE`
  entries — into `connTx`. No special hand-off was ever required for this
  field group; the prior note's "resume point" was solving a problem that
  does not exist.

Verified empirically with throwaway probes (both the plain-`ROLLBACK` and the
`BEGIN`-then-a-later-statement-errors-then-a-separate-message-`ROLLBACK`
forms — the latter matters because a real PostgreSQL simple-query message
aborts entirely at the first error, so a trailing `ROLLBACK;` written in the
*same* aborting message is never executed; the actual `ROLLBACK` must arrive
in a follow-up message, exactly like a real client would issue it after
seeing the error) before writing the permanent tests, confirming the type
is correctly undone in both cases with zero code changes.

Formalized as four permanent regression tests in
`internal/server/dispatch_batch_atomicity_test.go`:
`TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitCreateType` (both the
plain-`ROLLBACK` and separate-message-`ROLLBACK`-after-mid-batch-error
forms), `TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitCreateComposite`,
and `TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitAddValue` — covering
all three of `TracksDDLUndo()`'s enum/composite record sites reachable this
way (enum creation, composite creation, `ALTER TYPE ... ADD VALUE`; `ALTER
TYPE ... RENAME TO` follows the identical code path and is not separately
tested).

This closes root-0024's first residual for enum/composite entirely (both
the single-message case, closed by the prior follow-up, and the
mid-batch-`BEGIN` case, confirmed already-working by this one). The one
remaining open item under "Deferred" above is the TRUNCATE/DROP-in-savepoint
tracking gap, which is unrelated (a genuinely un-implemented gate, not a
mistaken prediction) and still needs its own `TracksDDLUndo()` extension.

Gates: `go build ./...` clean; `go test ./internal/server/...
./internal/executor/... ./internal/catalog/...` PASS (full, no regressions);
no WAL/MVCC code touched (test-only + doc-only change), so the race gate and
`scripts/tpch-spotcheck.sh` are unaffected by this loop's diff but were
re-run anyway per the standing pre-commit gates; pgbench smoke via the
pre-commit hook.

## Follow-up: TRUNCATE undo tracking now autocommit-batch-aware (2026-07-04)

Closes the TRUNCATE half of root-0024's first residual (the last open item
under "Deferred" above, besides the structurally-unreachable
`DROP TABLE`-in-savepoint case). `truncateTableAndPartitions`
(`internal/executor/operators_ddl.go`) snapshots a table's (and its indexes')
pre-truncate pages into a `TruncateUndoEntry` and calls
`(*BasicSession).RecordTruncate` so `ProcessRollbackUndos` can restore them —
but the call was gated on `Session.InExplicitTransaction()`, so a
message-scoped autocommit throwaway session (`NewAutocommitUndoSession`)
never got an entry recorded: `TRUNCATE t; SELECT 1/0;` as one simple-query
message truncated `t` and left it empty even though the whole implicit batch
transaction rolled back everywhere else.

Fix: the gate is now `Session.TracksDDLUndo()` (added by the enum/composite
follow-up above), the same one-line pattern already used for the four
enum/composite record sites. No other plumbing was needed —
`ProcessRollbackUndos` (`internal/executor/operators_tx.go`) already
unconditionally drains and restores `TakePendingTruncates()` on abort
whenever `ctx.Pool != nil`, and dispatch.go's abort defer already calls
`ProcessRollbackUndos` against the same throwaway session for every
autocommit batch (the original root-0024 mechanism) — so recording the entry
was the only missing piece.

The sibling `RecordDDLDrop` call site (`execDropTable`,
`internal/executor/operators_ddl.go`) was audited and left unchanged: it is
additionally gated on `bsess.SavepointDepth() > 0`, and `SAVEPOINT` itself
raises `25P01` outside an explicit transaction (`execSavepoint`,
`internal/executor/operators_tx.go`), so `SavepointDepth()` can never be
nonzero on a session where `InExplicitTransaction()` is false — widening that
gate to `TracksDDLUndo()` would be a no-op given the existing
`SavepointDepth() > 0` conjunct, so it was left as `InExplicitTransaction()`
for clarity (no dead-code risk either way, but no reason to touch a working,
correctly-scoped call site).

Test: `TestSimpleQueryBatchAbortUndoesEarlierTruncate`
(`internal/server/dispatch_batch_atomicity_test.go`) creates a 3-row table,
runs `TRUNCATE t; SELECT * FROM zz_definitely_missing_relation;` as one
message, and asserts a follow-up `SELECT * FROM t` still returns all 3 rows.
Confirmed RED against the pre-fix tree (reverting only `operators_ddl.go`):
0 rows returned.

This closes root-0024's first residual in full — no open items remain under
"Deferred" above.

Gates: `go build ./...` clean; `go test ./internal/executor/...
./internal/server/...` PASS (full, no regressions); `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=33); pgbench smoke via the pre-commit hook. No WAL/MVCC
package code touched, so the race gate is not required by the practice card,
but the change is adjacent to transaction-rollback machinery, so
`go test -race ./internal/executor/... ./internal/server/...` was run anyway
(clean).
