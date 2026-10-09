# M0146-0043 — txid_current() / pg_current_xact_id() assign the transaction's xid

Status: done (2026-10-03). Parent: M0146 (banner item 2a, third S2 batch).

## The defect

goopg allocates xids lazily (M0093): a transaction gets one at its first
write. `txid_current()` and `pg_current_xact_id()` returned `ctx.Tx.XID`
as they found it, so a read-only transaction, or one that had not yet
written, returned 0. PG 18.3's `txid_current` calls
`GetTopTransactionId()` (xact.c), which assigns the top-level xid on first
use and returns it, stable for the rest of the transaction.

## The fix

- `txid_current()` / `pg_current_xact_id()` return the top-level xid if one
  is assigned. Otherwise they call `ctx.MaterializeWriterXID()` (the lazy
  assignment every write path uses) and return the new xid.
- `topLevelXID` reads the session's top-level xid inside a savepoint,
  where `ctx.Tx.XID` holds the subtransaction's xid. PG returns the
  top-level one there too.
- `txid_current_if_assigned()` reports the same top-level xid, or NULL
  when none is assigned (`GetTopTransactionIdIfAny`).

## Verification

- Behaviour probe against PG 18.3 on a live server, identical:
  - autocommit calls advance;
  - the value is stable within a transaction;
  - `txid_current_if_assigned()` is NULL before the first call and equal
    after;
  - inside a savepoint (before and after a write) it is the top-level
    xid.
- `TestTxidCurrentAssignsXid` pins that the xid is non-zero, that the two
  functions agree, and that `if_assigned` follows.
- Regress A/B: `txid` shrinks 148→93 lines; `xid` is identical. No
  isolation spec calls these functions.
- Gates: units, spotcheck, sweep 96/96, fire set, TPC-H arm.

## Residuals

- Inside a savepoint whose parent never wrote, goopg has a subxact xid
  but no top-level one (the known "SAVEPOINT before first write → parent
  = 0" gap). `topLevelXID` then reports the subxact xid; PG would assign
  the top-level xid first (ledgered).
- goopg exposes no `xmin` system column (`SELECT xmin FROM t` fails with
  "column xmin does not exist"), so the probe could not compare row xmins
  (ledgered).
- xid8 epochs are not modelled; the value equals the 32-bit xid.
