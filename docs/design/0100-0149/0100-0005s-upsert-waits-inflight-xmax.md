# M0100-0005s — INSERT … ON CONFLICT waits for in-flight xmax on a visible match

## Problem

`INSERT … ON CONFLICT DO {NOTHING | UPDATE}` decides whether to insert or
take the conflict branch by probing the arbiter unique index for a
visible tuple at the candidate key.  Until this loop the probe ignored
in-flight deletions: a visible tuple whose `xmax` was an in-flight
non-self transaction was treated as a settled conflict, so DO NOTHING
returned immediately and DO UPDATE applied to a possibly-doomed row.

Upstream Postgres' `_bt_check_unique` (and the dirty-snapshot visibility
that backs it) *waits* on that xmax: if the deleter commits, the
apparent conflict was a phantom and the INSERT should proceed; if the
deleter aborts, the conflict survives and the conflict branch runs.

The first concrete miss surfaced by this gap is
`partition-key-update-2.spec`: s1 cross-partition-UPDATEs `(a=1)` to
`(a=2)` (delete-stamp on the old slot, fresh tuple in partition 2), and
s2's `INSERT INTO foo VALUES(1, …) ON CONFLICT DO NOTHING` is expected
to block until s1 settles.  Without the wait, s2 saw the visible
old-tuple-with-in-flight-xmax, treated it as a real conflict, and
returned `<no waiting>` — diverging from upstream's `<waiting …>`.

## Fix

Two changes in `internal/executor/operators_upsert.go`:

1. `findInProgressConflict` is extended to ALSO surface visible-being-
   deleted tuples.  Per matching index entry, after the existing
   in-flight-xmin check, look at the tuple's `xmax`:

   - if `xmax` is non-`Invalid` and not our own xact,
   - and `infomask` does NOT carry `HEAP_XMAX_LOCK_ONLY` (i.e. a real
     delete, not a row-lock holder),
   - and `xmin` is settled from this snapshot's view (committed to the
     snapshot, or our own xact),
   - and `xmax` is still active in the live manager,

   return `xmax` as the in-progress conflict XID.

2. `probeArbiterWaiting` is reordered: it now calls
   `findInProgressConflict` *first*; if any in-progress XID is found
   the upsert waits via `TxnMgr.WaitForXID`, refreshes the snapshot,
   and re-loops.  Only once the index has no in-progress entries is
   the final `probeArbiter` called for a settled visible/no-visible
   decision.  The previous flow (probe → if-no-match then check
   in-flight-xmin) missed the visible-being-deleted case entirely
   because it never reached the in-flight check.

The reorder is benign for the previously-passing paths:

- Non-conflicting INSERT: `findInProgressConflict` scans the index range
  and finds nothing → probeArbiter scans the same range, also nothing →
  returns `not found`.  Same outcome, one extra (empty) range scan in
  the no-conflict case.
- Concurrent in-flight INSERT (`InsertConflictDoNothing`,
  `InsertConflictDoUpdate`): Case 1 (in-flight xmin) was already wired,
  still detected and waited — same outcome.
- Settled conflict (no in-flight transactions on the key):
  `findInProgressConflict` returns `(0, false)`, fall through to
  `probeArbiter` — same as before.

## Why xmin must be settled before treating xmax-in-flight as a conflict

If `xmin` itself is in-flight, the tuple is being *speculatively
inserted* by another xact.  That xact's `xmin` is already the right
thing to wait on (Case 1), and treating its own xmax as a separate
conflict is misleading — we'd return the same xact's xmax under a
different name.  The `xmin` check fires first; the xmax-in-flight check
is a strict-second case for tuples whose insertion is already settled.

## What about partition routing for `INSERT INTO foo` on partitioned `foo`?

The partition-key-update-2 spec also requires `upsertOp` to route to
the correct partition before probing the arbiter, since the heap and
the local index live on the partition (`foo1`, `foo2`), not on the
parent.  That routing is a separate, larger piece of work and is NOT
addressed here — this loop only lands the visibility/wait fix that
will be load-bearing once routing is wired up.

## Regression test

`internal/server/upsert_waits_inflight_xmax_test.go::
TestUpsertDoNothing_WaitsForInFlightDelete` builds the minimum
two-session scenario on a non-partitioned table:

1. Setup: `CREATE TABLE up_wait (id PRIMARY KEY, label)`, insert
   `(1,'old')`.
2. s1 `BEGIN; DELETE FROM up_wait WHERE id=1` (xmax in-flight).
3. s2 `BEGIN; INSERT VALUES(1,'new') ON CONFLICT DO NOTHING` —
   asserted to block for at least 250 ms.
4. s1 `COMMIT` — s2 must unblock within 5 s.
5. Both commit, fresh-connection SELECT must show exactly one row
   `(1,'new')` (no row → s2 silently did nothing → fix not applied).

Pre-fix the test fails at step 3 (s2 returns before s1 commits) and
again at step 5 (zero rows).  Post-fix both gates pass.

## Files touched

- `internal/executor/operators_upsert.go` — `findInProgressConflict`
  extended; `probeArbiterWaiting` reordered.
- `internal/server/upsert_waits_inflight_xmax_test.go` — new regression
  test (added).
- `docs/design/0100-0005s-upsert-waits-inflight-xmax.md` — this doc.
- `docs/design/README.md` — index entry.
