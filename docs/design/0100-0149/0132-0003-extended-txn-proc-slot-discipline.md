# 0132-0003 — Proc-slot discipline on the extended query protocol

status: accepted · date: 2026-08-13 · supersedes: none (M0132-S7)

## 1. The problem

`executeExtendedQueryViaExecutor` (`internal/server/dispatch_extended.go`) and
`dispatchCopyViaExecutor` (`internal/server/copy.go`) both begin the
per-statement **autocommit** transaction on a *computed* ProcArray slot rather
than the connection's own:

```go
const halfSize = mvcc.ConnSlotCount / 2
beginProcNum := (procNum + halfSize) % mvcc.ConnSlotCount
```

The comment says this keeps the autocommit transaction "off the connection's
own slot". That intent is wrong, and the mechanism is unsafe.

### The slot model

`mvcc.Manager` has two *separate* ownership flags per `procSlot`
(`internal/mvcc/procarray.go`):

- `connHeld` — a connection owns this slot for its whole lifetime. Claimed by
  `AcquireConnSlot` (round-robin over `[1, ConnSlotCount)`, `manager.go:494`),
  released by `ReleaseConnSlot` at disconnect.
- `inTxn` — a transaction currently occupies this slot. Set by `Begin`,
  cleared by `finish` (Commit/Rollback).

`Begin(iso, procNum)` (`manager.go:261`) does **not** check either flag: it
unconditionally `s.inTxn.Store(1)` on the requested slot. `AcquireConnSlot`
never hands out a `connHeld` slot, so a connection beginning a transaction on
*its own* slot is safe. But any caller that asks `Begin` for a slot it does not
own clobbers whatever already lives there.

### Why the offset is wrong

The offset `(procNum + halfSize) % ConnSlotCount` is a bijection on
`[0, ConnSlotCount)`. Because `AcquireConnSlot` assigns connection slots from
that same region, the offset slot is **always some other connection's own
slot** — the connection whose `AcquireConnSlot` result happens to equal
`(procNum + halfSize) % ConnSlotCount`. So:

1. Connection A (own slot `p`) runs an extended out-of-block `Execute`; its
   autocommit transaction lands on slot `q = (p + halfSize) % ConnSlotCount`.
2. Connection B (own slot `q`) runs *any* transaction on its own slot — a
   simple-path statement, an explicit block, or a `BEGIN ISOLATION LEVEL …`
   whose re-begin is on `ctx.ProcNum` (`txn_verb.go:223`).
3. Both transactions now think they own slot `q`. When the first finishes,
   `finish` clears `inTxn`, and the second gets `mvcc: unknown transaction`
   from `SnapshotFor`/`AssignXID`/`finish`.

This is exactly the failure doc 09 §5 I3 recorded: three `-S -M prepared`
clients aborting with `mvcc: unknown transaction` at 50 sustained clients.
The offset's bijection means pure-prepared clients never collide *with each
other* (their offsets are pairwise distinct), which is why the abort only
surfaces under connection churn past `halfSize` cumulative connections — or
whenever a connection runs a statement on its own slot (any simple-path or
mixed-protocol traffic) while a neighbour's offset slot overlaps it.

## 2. The fix

An out-of-block `Execute` must begin its autocommit transaction on the
connection's **own** slot (`procNum`), exactly as the simple path does
(`dispatch.go:236`, `pn = connTx.ProcNum`). This is safe because:

- The connection holds `connHeld` on that slot for its whole lifetime, and no
  other connection can obtain it (`AcquireConnSlot` skips `connHeld` slots).
- "Out of block" means the connection has no other live transaction, so the
  own slot is free (`inTxn == 0`).
- In-block `Execute`s already reuse the block's transaction via `connTx.Tx()`
  (M0132-S3), so the own slot is only ever contended by the connection itself,
  sequentially.

Concretely, `dispatch_extended.go`'s out-of-block branch collapses to
`TxnMgr.Begin(mvcc.IsolationReadCommitted, procNum)`. The `TxBegin` special
case (`beginProcNum = procNum`) disappears — every out-of-block begin is now on
`procNum`, which is what the `BEGIN` path wanted anyway. The
`BEGIN ISOLATION LEVEL <level>` re-begin inside `applyTransactionVerb`
(`txn_verb.go:223`) also lands on `ctx.ProcNum` (set to `procNum` by M0132-S6),
so the placeholder autocommit transaction and the correctly-levelled block
share the own slot sequentially — no collision, no XID/SSI leak.

## 3. The copy.go ruling

`copy.go:157-167` carries the *same* offset. It exists there for a different
reason: COPY **always** runs in its own autocommit transaction even when the
client has an open block (a pre-existing divergence — `BEGIN; COPY …; ROLLBACK`
does not roll back, on *both* protocols). So COPY needs a second slot only
*in* a block.

Ruling:

- **Out of block** (`connTx == nil` or `!connTx.InExplicit()`): use the
  connection's own slot (`connTx.ProcNum`), matching the discipline. This is
  the common case (COPY is almost never issued inside a block).
- **In block**: COPY's "own autocommit transaction" is the divergence. Use the
  manager's **auto-assign** path (`Begin(iso)` with no procNum, `manager.go:266`),
  which CAS-scans for an `inTxn == 0` slot — strictly safer than the
  deterministic offset, which always landed on a specific other connection's
  slot. Recorded in the deferral ledger (COPY-ignores-block), which is the
  milestone's required disposition; closing that gap (making COPY join the
  block) is out of this milestone's acceptance bar.

The `connTx == nil` case (a standalone/`copyTo` path with no connection state)
previously degenerated to `Begin(iso, 0)` — the shared "explicit procNum 0"
slot — and now uses auto-assign too.

## 4. Known residual hazard (out of scope, recorded not fixed)

The auto-assign scan (`manager.go:275`) checks only `inTxn`, not `connHeld`, so
an *internal* transaction (ANALYZE, VACUUM, the apply worker, CREATE/DROP
DATABASE, role DDL, and the in-block COPY above) can still grab an *idle*
connection's slot (`connHeld == 1, inTxn == 0`), and a collision occurs if that
connection then begins a transaction on its own slot before the internal one
finishes. This is a pre-existing, independent hazard in the auto-assign path —
not the offset scheme — and affects several call sites, so it is deliberately
left for a future, broader change (make auto-assign skip `connHeld` slots).
The S7 fix removes the *deterministic* collision; the *probabilistic* auto-assign
collision remains bounded and is far less likely in practice.

## 5. Reproduction & gates

- **Reproduction**: a server-level test reserves the offset slot (`Begin(RC,
  (procNum + halfSize) % ConnSlotCount)` held open) and then runs an extended
  out-of-block `Execute`; at HEAD the `Execute` clobbers the reserved slot and
  the reserved transaction's `SnapshotFor` fails with `ErrUnknownTransaction`;
  after the fix the `Execute` lands on the connection's own slot and the
  reserved transaction stays live. Pinned by
  `TestM0132S7_ExtendedAutocommitUsesOwnSlot` (red at HEAD, green after).
- **Gates**: `go test ./internal/server/ ./internal/mvcc/`, `make race-gate`
  (server + mvcc), and a ≥50-client `pgbench -S -M prepared` run with zero
  `mvcc: unknown transaction` aborts.
