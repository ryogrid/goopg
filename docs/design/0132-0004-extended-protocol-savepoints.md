# 0132-0004 — SAVEPOINT / RELEASE / ROLLBACK TO over the extended query protocol

status: draft · date: 2026-08-13 · milestone: M0132 (S10) · plan:
[`0132-extended-protocol-explicit-transactions.md`](0132-extended-protocol-explicit-transactions.md)
· state machine: [`0132-0001`](0132-0001-extended-protocol-explicit-txn-state-machine.md)

## 1. The ruling — implement, not defer

M0132-S10 was written as a decision slice: implement SAVEPOINT over the
extended protocol **if** the extracted helper already routes the three verbs
correctly and the D-002 savepoint specs pass unmodified, otherwise defer with a
ledger row. The ruling is **implement** — no new sub-transaction plumbing was
needed, because the plumbing already exists and is shared with the simple path.

The premise that motivated the deferral hazard was that the extended path had
to *re-derive* sub-transaction handling. It does not. `applyTransactionVerb`
(`internal/server/txn_verb.go`) deliberately returns `Handled == false` for
`TxSavepoint` / `TxRelease` / `TxRollbackTo`, and the extended caller falls
through to `executor.Build(node)` → `transactionOp` →
`execSavepoint` / `execRelease` / `execRollbackTo`
(`internal/executor/operators_tx.go:456`/`:497`/`:518`) — the *same* route the
simple path takes (M0097-0023). The known hazards the slice named
(`savepoint_before_write_parent_zero`, `savepoint_rollback_visibility_three_fixes`)
were already closed on that shared executor path and are inherited unchanged.

This satisfies D5 (SAVEPOINT must be explicit, never a bare tag): the verbs are
handled by the executor, not answered with a silent command tag, and not
rejected with `0A000`.

## 2. The one gap found — and fixed

Verification exposed exactly one divergence, and it was the out-of-block case:

| statement (no explicit block) | PG 18.3 | goopg simple | goopg extended **before** |
|---|---|---|---|
| `SAVEPOINT f` | `25P01` SAVEPOINT can only be used in transaction blocks | `25P01` | `XX000` transaction statements require Session in Context |

The extended path wired `ectx.Session` only inside `if inBlock`
(`dispatch_extended.go`). Outside a block, `ectx.Session` stayed `nil`, so
`transactionOp.Open` (`operators_tx.go:32-34`) failed on `ctx.Session == nil`
before `execSavepoint`'s `InExplicitTransaction()` guard could return `25P01`.
The simple path does not have this hole: its autocommit branch wires
`executor.NewAutocommitUndoSession()` (`dispatch.go:342`), a throwaway
`BasicSession` with `inTx == false` that lets the executor's own `25P01` guard
fire.

Fix: the extended path's out-of-block branch now wires the same
`executor.NewAutocommitUndoSession()`. `InExplicitTransaction()` stays `false`
on it, so nothing else changes — out-of-block INSERT/UPDATE/SELECT never read
`ectx.Session`, the pending-enum/composite queues are still not written back
(`writeBackConnTxState` guards on `InExplicit()`), and the throwaway is
message-scoped exactly as on the simple path.

This is the sibling-path-agreement class of defect the repo guards against
(`pattern_sibling_paths_must_agree`): the simple and extended paths now wire the
autocommit session identically.

## 3. Verification

New tests in `internal/server/extended_savepoint_test.go`:

- `TestM0132S10_ExtendedSavepointRollbackTo` — full block over the extended
  protocol, `INSERT → SAVEPOINT f → INSERT → ROLLBACK TO f → COMMIT`; only the
  first row survives.
- `TestM0132S10_ExtendedSavepointRelease` — `SAVEPOINT → INSERT → RELEASE →
  COMMIT`; the savepoint's work commits.
- `TestM0132S10_ExtendedSavepointSelfVisibility` — after `ROLLBACK TO f` the
  rolled-back row is invisible to a later `SELECT` *inside the still-open
  block*, not just after COMMIT (sub-XID visibility, the
  `savepoint_rollback_visibility_three_fixes` territory).
- `TestM0132S10_ExtendedSavepointOutsideBlock` — `SAVEPOINT f` with no block
  returns SQLSTATE `25P01` (was `XX000`).
- `TestM0132S10_MixedSavepoint` — the driver shape: simple `BEGIN`/`COMMIT`,
  extended `SAVEPOINT`/`INSERT`/`ROLLBACK TO`.

All five fail-verified against the pre-fix state (the outside-block test was the
only red one at first run; the other four were already green, confirming the
sub-transaction path was already correct).

## 4. Gates run

`go test ./internal/server/` PASS (39.7 s, full suite incl. the S1–S9 bar);
`go test ./internal/server/ -run 'TestM0132S10_'` PASS; the four
in-block/mixed savepoint tests passed at HEAD **before** the one-line session
wiring, proving the sub-transaction machinery is shared and intact.
