# 0118-0115 — intra-grant-inplace enabler: deadlock-victim releases its XID in place (perm 8 ordering)

Status: accepted
Milestone: M0118-0009 (upstream isolation spec suite pass-through)
Spec: `postgres/src/test/isolation/specs/intra-grant-inplace.spec`
Predecessors: 0118-0109 (GRANT-`xmax` half), 0118-0113 (`pg_class` rowmark half),
0118-0114 (reverse-direction wait + deadlock detection)

## Summary (Enabler, NOT a promotion)

0118-0114 made permutation 8's **deadlock line** byte-exact: in
`b2 sfnku2 b1 grant1(addk2) addk2(*) c2 c1 read2`, `grant1` (s1) awaits the
`sfnku2` rowmark xmax (s2) while `addk2` (s2) blocks behind `grant1`'s ACL-change
xmax, forming the cycle `s2→s1→s2`; `addk2` is the `(*)` victim and raises
SQLSTATE 40P01 "deadlock detected".

The **only** residual divergence was a completion-order swap:

```
expected            actual (before this loop)
step grant1: <...>  step c2: COMMIT;
step c2: COMMIT;    step grant1: <...>
```

PostgreSQL's `AbortTransaction` releases the victim's XID the instant the deadlock
is reported, so `grant1` — blocked on `sfnku2`'s xmax (= s2's XID) — unblocks at the
abort, **before** s2's explicit `c2` COMMIT. goopg instead kept the victim's XID
active in its proc-array slot until the explicit COMMIT/ROLLBACK, so `grant1`
unblocked only at `c2`.

This loop releases the deadlock victim's XID **in place** at the abort, advancing
the first divergence L184 → L206. Permutations 1–8 are now byte-identical; the spec
stays `defer` because permutations 9 (a `DO $$ … REVOKE … $$` plpgsql body — the
parser rejects bare `REVOKE` in a DO block) and 10 (`DELETE FROM pg_class` —
virtual-catalog tuple delete) remain on distinct unbuilt subsystems.

## Why "release in place" rather than a full early rollback

The natural first attempt — call `TxnMgr.Rollback` on the victim at abort time —
clears the proc-array slot. But the victim's connection block stays open (the
spec's next victim step is the explicit `c2` COMMIT), and goopg's later
COMMIT/ROLLBACK finalisation re-enters `TxnMgr.Commit`/`Rollback` on that
now-cleared handle, surfacing `ERROR: mvcc: unknown transaction`. (goopg does not
mark a deadlock-victim's connection `failed`, so `c2` does not take the
COMMIT-in-failed-block path that would have swallowed the error.) Desyncing the
slot from the still-open connection block is the wrong primitive.

Instead we keep the slot **open** and only suppress the XID for the two wait
predicates `WaitForXID` / `IsXIDActive` use. The victim is **write-less** in this
spec (its `ADD PRIMARY KEY` errored before writing any heap tuple), so leaving
snapshot visibility to read the still-open slot/CLOG unchanged is correct — there
is nothing to make visible or to undo. The explicit `c2` then finalises the
(empty) transaction through the canonical path and prints the normal step line.

## Mechanism

1. **mvcc.Manager.ReleaseXIDWaiters(xid)** (`internal/mvcc/manager.go`)
   - Records `xid` in a new `releasedWaiterXIDs` set (guarded by
     `releasedWaiterMu`) and broadcasts `commitCond` to wake blocked waiters.
   - `xidActiveWithSubxact` (the shared liveness predicate behind `WaitForXID`
     and `IsXIDActive`) returns `false` for a released XID (checked for both the
     queried XID and its resolved top-level parent), **without** clearing the
     proc-array slot.
   - `finish` (commit/abort) deletes the XID from the set, so the marker never
     outlives the slot. Snapshot visibility never consults the set.

2. **Context.DeadlockVictim** (`internal/executor/context.go`)
   - A per-statement flag set by `waitPgClassInplaceXID` (the deadlock-aware
     pg_class in-place wait) the moment it detects the cycle and returns the
     victim verdict. Reset in the wire dispatch per-statement reset block.

3. **connTxState.AbortInPlaceOnFail(mgr)** (`internal/server/conn_tx.go`)
   - Calls `mgr.ReleaseXIDWaiters(currentTx.XID)`, gated on `SavepointDepth()==0`
     exactly like `Fail()` / `ReleasePinnedSnapshotOnFail` (a subtransaction error
     is undone by ROLLBACK TO SAVEPOINT, which resumes the same top-level XID).

4. **Wire dispatch failure handler** (`internal/server/dispatch.go`)
   - On the `errQueryErrorSent` path inside an explicit transaction, after
     `Fail()` + `ReleasePinnedSnapshotOnFail`, calls `AbortInPlaceOnFail` **only**
     when `ectx.DeadlockVictim` is set — confining the new behaviour to the
     pg_class in-place deadlock path.

## Blast radius

`DeadlockVictim` is set exclusively in `waitPgClassInplaceXID`, which only the
three intra-grant-inplace pg_class waits (`waitForTableACLChange`,
`waitForPgClassRowMarks`, and the GRANT/REVOKE reverse wait) route through. The
heavyweight-lock deadlock detectors (`context.go`) and the row-lock EPQ deadlock
path (`epqWait`) do **not** set the flag, so the `deadlock-{hard,simple,soft,
soft-2}` and row-lock specs are untouched — their winners already unblock via
heavyweight-lock release in `Fail()`, never via a `WaitForXID` on the victim. For
every non-released XID, `xidActiveWithSubxact` adds one map lookup against an
empty/nil set and is otherwise byte-for-byte unchanged.

## Tests / gates

- `TestReleaseXIDWaiters` (`internal/mvcc`): waiter wakes at release while the slot
  stays open; the slot still commits through the canonical path; marker cleared at
  finish. `-race` green.
- Probe of the full spec: first divergence L184 → L206 (perms 1–8 byte-identical).
- Non-regression strict specs PASS: `deadlock-{hard,simple,soft}`,
  `tuplelock-upgrade-no-deadlock`, `multixact-no-deadlock`, `intra-grant-inplace-db`,
  `truncate-conflict`, `lock-committed-{update,keyupdate}`, `update-locked-tuple`,
  `propagate-lock-delete`, `skip-locked`, `nowait`, `fk-deadlock`,
  `prepared-transactions`.
- `-race ./internal/mvcc/...`; executor deadlock/lock race tests; server + mvcc unit
  tests; CI-parity pgbench smoke (pre-commit hook).

## Remaining for promotion

- Perm 9: `DO $$ BEGIN REVOKE … END $$` — plpgsql parser must accept a bare
  `REVOKE` statement in a DO body; the REVOKE must also take a pg_class rowmark and
  await the conflicting ACL/rowmark xmax (`sfu3`-after-`grant1`).
- Perm 10: `DELETE FROM pg_class WHERE relname = …` — virtual-catalog pg_class
  tuple delete (deferred drop at commit) + `SearchSysCacheLocked1` find-then-none
  ("cache lookup failed" path).
