# 0117-0001 — Wraparound-safe XID horizon comparison

Status: accepted
Milestone: M0117-0001 (CLOG ↔ PostgreSQL alignment, gap M2; P0 / correctness)
Author: Ralph
Date: 2026-06-15

## Problem

XIDs are 32-bit and wrap at 2³². PostgreSQL therefore never compares XIDs with a
plain `<`; it uses `TransactionIdPrecedes`
(`postgres/src/backend/access/transam/transam.c`):

```c
bool
TransactionIdPrecedes(TransactionId id1, TransactionId id2)
{
    int32 diff = (int32) (id1 - id2);
    return (diff < 0);
}
```

The signed difference orders two XIDs by their position on the modular circle:
for any two XIDs less than 2³¹ apart (the invariant anti-wraparound autovacuum
maintains), `id1` precedes `id2` iff `id1` is in the older half relative to
`id2`. A freshly-assigned XID just past the wraparound boundary has a *smaller*
raw value than an older XID near the top of the range, so plain unsigned `<`
reports the new XID as "older" — the exact failure mode that corrupts a
truncation/freeze horizon.

goopg already had the correct formula in `internal/mvcc/clog.go` (`txnPrecedes`),
but two CLOG-truncation horizon sites used plain `<` instead:

1. `catalog.(*InMemory).DatFrozenXID()` — selecting `min(relfrozenxid)` across
   user tables with `t.RelFrozenXID < oldest`.
2. `internal/initdb/open.go` checkpointer `TruncateCLOGFn` — computing
   `horizon = min(datfrozenxid, OldestXmin)` with `ox < horizon`.

Both feed `CLog.TruncateCLOG`. If a relfrozenxid or OldestXmin sits just across a
wraparound boundary from the others, plain `<` picks a too-recent horizon and
truncates CLOG status still consultable by older frozen tuples → silent
"could not access status of transaction" / wrong-visibility corruption. This is
a latent P0 because goopg does not yet drive XIDs across 2³² in tests, but the
code is wrong today.

## Decision

Add a single exported source of truth in the `storage` package (where
`TransactionID` is defined):

```go
// internal/storage/heap.go
func XIDPrecedes(a, b TransactionID) bool { return int32(a-b) < 0 }
```

and route every modular XID-horizon comparison through it:

- `catalog.DatFrozenXID`: `storage.XIDPrecedes(t.RelFrozenXID, oldest)`.
- `TruncateCLOGFn`: `storage.XIDPrecedes(ox, horizon)`.
- `mvcc.txnPrecedes` now delegates to `storage.XIDPrecedes` so the mvcc-internal
  helper and the new exported one cannot drift (sibling-path hygiene — both
  implemented the identical formula before).

### What deliberately stays plain `<`

The `datFrozen < FirstNormalTransactionID` and `horizon < FirstNormalTransactionID`
guards in `TruncateCLOGFn`, and the `oldest == InvalidTransactionID` first-value
guard in `DatFrozenXID`, are **not** modular comparisons. They are
`TransactionIdIsNormal`-style sentinel checks against the fixed low bootstrap
constants (Invalid=0, Bootstrap=1, Frozen=2, FirstNormal=3). PG keeps these as
plain comparisons too; converting them to modular form would be wrong.

## Why `storage` is the right home

`TransactionID` lives in `internal/storage`, which is imported by both
`internal/catalog` and `internal/initdb` (and `internal/mvcc`). Putting the
helper anywhere else would create an import cycle or force the comparison to be
re-implemented per-package — precisely the drift this change eliminates.

## Alternatives considered

- **Export `mvcc.txnPrecedes`.** Rejected: `catalog` must not depend on `mvcc`
  (layering), and `initdb` already pulls `storage`. The formula belongs next to
  the type it operates on.
- **Leave the two call sites alone (latent, not yet triggered).** Rejected: it is
  a P0 correctness gap (gap M2) and the fix is a few lines with no behavior
  change inside the 2³¹ window — there is no reason to defer a known data-
  corruption latent.

## Testing

- `internal/storage/xid_test.go`: `TestXIDPrecedes` (explicit boundary cases
  where plain `<` disagrees) + `TestXIDPrecedesAntisymmetry` (sweeps windows
  straddling the wraparound boundary, asserting exactly-one-precedes).
- `go test ./internal/mvcc/... ./internal/catalog/... ./internal/storage/... ./internal/initdb/...`
  to confirm the delegation and call-site edits are behavior-preserving inside
  the normal (non-wrapped) range.

## References

- `postgres/src/backend/access/transam/transam.c` — `TransactionIdPrecedes`.
- `postgres/src/backend/commands/vacuum.c` — `vac_update_datfrozenxid` uses
  `TransactionIdPrecedes` for the `min(relfrozenxid)` reduction this mirrors.
- `internal/mvcc/clog.go` — `txnPrecedes`, `CLOGPagePrecedes` (existing modular
  comparisons).
- `docs/analysis/clog-goopg-gaps-and-remediation-2026-06-14.md` — gap M2.
