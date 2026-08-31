# 0118-0127 — `stats` enabler rung 5: cross-backend two-phase commit (M0118-0009)

Status: accepted — **enabler, NOT a promotion** (`stats.spec` stays `defer`).

## Summary

Advances `postgres/src/test/isolation/specs/stats.spec`'s first divergence
**L2036 → L2180** by implementing **detached, cross-backend `COMMIT PREPARED` /
`ROLLBACK PREPARED`** for READ COMMITTED / REPEATABLE READ prepared
transactions. The four "Check 2PC handling of stat drops" permutations — *S1
prepared / S1 commits*, *S1 prepared / S1 aborts*, *S1 prepares / **S2** commits
prepared*, *S1 prepared / **S2** aborts prepared* — now match PG 18.3
byte-for-byte. The new first divergence (L2180,
`pg_stat_get_numscans does not exist`) is the **relation tuple-stats** rung.

## Problem

The prior 2PC (design 0118-0110) was *same-backend only*: `PREPARE
TRANSACTION 'gid'` kept the transaction open as the originating connection's
active transaction, and `COMMIT/ROLLBACK PREPARED 'gid'` finalised it through
the canonical commit path **on that same connection**. A `COMMIT PREPARED` from
a different backend reported `prepared transaction … does not exist`, and the
originating connection could not run further transactions while the prepared one
was outstanding.

`stats.spec` needs both: after `s1_prepare_a`, session **s1 keeps running**
(`s1_func_call`, `s1_ff`, `s1_func_stats`) and then **s2** issues `COMMIT
PREPARED 'a'` / `ROLLBACK PREPARED 'a'` for the transaction s1 prepared.

Two obstacles in goopg's model:

1. **Slot ownership.** A transaction's handle is `procNum + 1`, tied to a
   reusable per-backend proc-array slot. If s1 keeps the prepared transaction on
   its own slot and then runs new statements, every `Manager.Begin(s1.procNum)`
   re-initialises that slot — clobbering the prepared transaction's XID / SSI
   state. By the time s2 commits, `Manager.Commit(parkedTx)` would operate on
   s1's *current* slot, not the prepared one.
2. **Connection finalisation context.** The canonical finalise path is wired to
   the originating connection's `connTxState` + session; another backend has no
   handle on it.

## Design

PostgreSQL hands a PREPAREd transaction to a *dummy `PGPROC`*, dissociating it
from the backend so any backend can finalise it. goopg mirrors this for RC/RR:

### 1. `mvcc.Manager.DetachToDedicatedSlot(tx) (Transaction, error)`

Relocates `tx` off its originating backend's proc slot onto a fresh **dedicated**
slot, returning the moved `Transaction` (new `Handle`, same XID / isolation /
snapshot). The dedicated slot stays `inTxn=1` (active) holding the same XID, so
the prepared transaction's writes remain visible as *in-progress* to every other
session's snapshot (`captureSnapshot` walks all slots by XID) and is
committable/abortable later from any backend via `Commit`/`Rollback` on the
returned handle. The originating slot is freed so the backend can begin new
transactions. Restricted to RC/RR (`ErrUnsupportedDetach` for SERIALIZABLE,
whose SSI predicate-lock / rw-conflict records are keyed by `Handle` and would
need re-keying).

### 2. Reserved proc-array region (collision fix)

Connections use `procNum = (pid-1) % ConnSlotCount` and `Manager.Begin`'s
auto-assign / the COPY + extended-query half-offset slots also live in the low
region. The dedicated 2PC slot must not be a slot any backend will later reuse,
so the top `ReservedPreparedSlots = 64` slots
(`[ConnSlotCount, DefaultProcArraySize)`) are reserved exclusively for detached
prepared transactions. All four low-region allocators were bounded to
`ConnSlotCount` (`server.go` ×2, `dispatch_extended.go`, `copy.go`), the
auto-assign scan to `[1, ConnSlotCount)`, and `DetachToDedicatedSlot` scans the
reserved high region. A parked slot is `inTxn=1`, so the auto-assign CAS can
never steal it even transiently.

### 3. `preparedXactStore` + detach/finalise wiring (`server/twophase.go`)

- A process-wide `preparedXactStore` (gid → parked `*connTxState`) on `Server`.
- `execPrepareTransaction`, for RC/RR: `DetachToDedicatedSlot` →
  `BasicSession.RelocateTransaction(newTx)` (updates the session's cached handle)
  → `connTxState.DetachPrepared(gid, newTx)` (moves session / deferred DROP
  FUNCTION drops / buffered NOTIFYs / pending enum DDL into a standalone holder
  and resets the live connection to free auto-commit, preserving its
  per-connection identities) → register. SERIALIZABLE keeps the unchanged
  same-backend keep-open path. Duplicate gid → SQLSTATE 42710.
- `execFinalizePrepared`: same-backend kept-open gid still finalises via
  `connTx`; otherwise look the gid up in the store and finalise the parked
  holder by retargeting the executor context (`ctx.Session`/`ctx.Tx`/`Snap`/
  pending-enum/`TxnLockBackendID`, with `Begin/EndLocalTransaction` suppressed)
  and routing the synthetic `COMMIT`/`ROLLBACK` through the **canonical**
  `executeOneSimpleStmt` path with the parked holder as the active transaction.
  This reuses the existing SSI check, deferred-drop application
  (`ApplyDeferredRoutineDrops` → `funcStats.dropFunction`), NOTIFY publication
  and lock release verbatim. Works from the same or a different backend.

For the stat-drop permutations the effect is: **COMMIT PREPARED** applies the
deferred DROP FUNCTION (and drops its cumulative function-stats); **ROLLBACK
PREPARED** discards the deferral (the function — and its stats — survive),
exactly as PG.

## Scope / limitations (accepted; no port spec exercises them)

- SERIALIZABLE cross-backend finalisation stays unsupported (kept same-backend).
- The parked holder shares the originating connection's lock/advisory backend
  identities; full dummy-`PGPROC` lock hand-off is deferred. The cross-backend
  specs hold no xact-scoped heavyweight/advisory locks across the
  PREPARE‥finalise window.
- `pg_prepared_xacts` view and crash-restart persistence (`pg_twophase`) remain
  deferred.

## Verification

- `go build ./...` clean; `go test ./internal/mvcc/...` + `-race` PASS;
  `go test ./internal/executor/... ./internal/server/... ./internal/config/...`
  PASS.
- `TestPort_TwoPhaseCommitSameBackend` PASS (regression: same-backend
  COMMIT/ROLLBACK PREPARED + visibility-before-commit still correct under the new
  detach path).
- `TestPort_IsolationPreparedTransactions` (SERIALIZABLE, strict) **and**
  `TestPort_IsolationPreparedTransactionsCIC` PASS (the reserved-region proc-slot
  change does not perturb SSI 2PC).
- `stats.spec` probe **L2036 → L2180** (`TestPort_IsolationStats` soft anchor).
- pgbench CI-parity smoke = pre-commit hook.

## Next rung

L2180 = relation tuple statistics (`pg_stat_get_numscans`,
`pg_stat_get_tuples_*`, `pg_stat_get_xact_*`), then SLRU stats
(`pg_stat_slru`).
