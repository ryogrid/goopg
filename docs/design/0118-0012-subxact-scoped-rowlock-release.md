# 0118-0012 — Subtransaction-scoped row-lock release & retry (`tuplelock-upgrade-no-deadlock` perm 9)

* Milestone: M0118-0004 (deadlock-detection slice)
* Spec: `postgres/src/test/isolation/specs/tuplelock-upgrade-no-deadlock.spec`, permutation 9
* Status: **partially implemented** — Gaps A/B/C landed & gated (spec divergence moved from expected L216 → L238); Gap D (locks stamped under the top-level xid, not the savepoint subxid) discovered mid-loop and deferred — see §6.
* Predecessors: [[0118-0008]] (chain-tail sentinel), [[0118-0009]] (read-side multixact-aware updater wait), [[0118-0010]] (write-side EPQ-wait), [[0118-0011]] (locker-preserving producer — got the spec to **8/9** permutations)
* Subxact infrastructure: [[0050-0001]] (subxact stack & state machine), [[0050-0002]] (subxact xid & visibility)

## 1. Problem

`tuplelock-upgrade-no-deadlock` is at **8/9** matching permutations after [[0118-0011]].
The single remaining failure is perm 9 — the "s2 retrying the overall tuple lock
algorithm after initially avoiding deadlock" case:

```
s1_keyshare s3_for_update s2_for_keyshare s1_savept_e s1_share s1_savept_f
s1_fornokeyupd s2_fornokeyupd s0_begin s0_keyshare s1_rollback_f s0_keyshare
s1_rollback_e s1_rollback s2_rollback s0_rollback s3_rollback
```

### 1.1 Required upstream behaviour (from `expected/...out` L194–253)

| step | row-lock event | who waits / wakes |
|------|----------------|-------------------|
| s1_keyshare | s1 FOR KEY SHARE | xmax = {s1:KS} |
| s3_for_update | s3 FOR UPDATE | **waits** (UPDATE conflicts with KS) |
| s2_for_keyshare | s2 FOR KEY SHARE | grants (KS∥KS); xmax = {s1:KS, s2:KS} |
| s1_savept_e; s1_share | s1 upgrades KS→SHARE **inside subxact e** | grants (SHARE∥KS) |
| s1_savept_f; s1_fornokeyupd | s1 upgrades SHARE→NO KEY UPDATE **inside subxact f** | grants (NKU∥KS) |
| s2_fornokeyupd | s2 upgrades KS→NO KEY UPDATE | **waits** (NKU conflicts with s1's NKU *and* with s1's SHARE) |
| s0_begin; s0_keyshare | s0 FOR KEY SHARE | grants (KS compatible with all current holders) |
| **s1_rollback_f** | releases subxact f's NKU; s1 reverts to SHARE | s2 **still waits** (NKU still conflicts with s1's SHARE) |
| s0_keyshare | s0 FOR KEY SHARE again | grants |
| **s1_rollback_e** | releases subxact e's SHARE; s1 reverts to KS | s2 **wakes & completes** (NKU no longer conflicts with KS) |
| s1_rollback; s2_rollback; s0_rollback | all release | — |
| s3_for_update | (was waiting since step 2) | **completes** — all conflicting holders gone |

Two distinct capabilities are required that goopg lacks:

1. **Subxact-scoped lock release**: a `ROLLBACK TO SAVEPOINT` must drop the lock-mode
   *upgrade* a subtransaction acquired, so a blocked waiter re-evaluates against the
   weaker remaining mode.
2. **Conflict-filtered waiter wake & retry**: the waiter (s2) must wait only on the
   *conflicting* holders, and must wake and re-run the acquisition when one of them
   goes away — even when the rest of the same backend's locks remain.

## 2. Current goopg model & the three concrete gaps

Row locks in goopg ride the tuple `xmax`/MultiXact + `WaitForXID`, **not** the
heavyweight lockmgr (lockmgr tuple locks are statement-scoped, released per Query
message — see [[lockmgr_locks_are_statement_scoped]]). A lock acquired inside a
savepoint is stamped with the *effective writer XID* = the subxact's XID
(`Context.EffectiveWriterXID`, `session.go`; `ctx.Tx.XID` is set to the subxid while
the savepoint is active, `operators_tx.go execSavepoint`). So the multixact members
in perm 9 are, after step 7: `{s1_top:KS, s2:KS, subE:SHARE, subF:NKU}` — each
upgrade is a *distinct member under a distinct subxid*, which is the right raw
material. The gaps are in how those members are interpreted:

### Gap A — subxids are invisible to liveness (`IsXIDActive`/`WaitForXID`)

`mvcc.Manager.AllocateSubXid` (manager.go:408-429) explicitly does **not** register
the subxid in the proc-array:

> "The sub-XID is not tracked in the proc-array (subxact XIDs are not independent
> top-level transactions); visibility is handled entirely by
> SeesCommittedXIDWithSubxacts via the subxact map."

`xidInProgress(xid)` (manager.go:654) scans proc-array slot xids only, so:

* `IsXIDActive(subE)` → **false** (subE never in a slot). Therefore
  `activeLockHolders` (operators_lockrows.go:1292) drops `subE`/`subF` from the
  active set entirely — goopg never even sees s1's upgrade as a live lock.
* `WaitForXID(subE)` (manager.go:573) returns **immediately** (`xidInProgress` false),
  so a waiter never blocks on a subxact's lock.

Net effect today: s2's `tupleLockConflicts` fires on the *infomask* (strongest mode
present = NKU), enters the wait branch, but `activeLockHolders` returns only the
*top-level* members `{s1_top:KS}` (s2 self-excluded). s2 then waits on `s1_top` — a
**non-conflicting** KS holder that only releases at the full `s1_rollback`, never at
`s1_rollback_e`. Wrong waiter, wrong wake point.

### Gap B — the wait set is not conflict-filtered

`activeLockHolders` returns *every* still-active member; the wait loop
(operators_lockrows.go:993) blocks on all of them. Upstream `MultiXactIdWait` waits
only on members whose lock mode **conflicts** with the request (`StatusesConflict`
already exists, multixact.go:212). Even with Gap A fixed, s2 must wait on
`{subE:SHARE, subF:NKU}` and **not** on `{s1_top:KS, s2:KS(self), s0:KS}`, or it
would block on KS holders that never conflict and complete at the wrong step.

### Gap C — no wake on subxact abort

`MarkSubxactAborted` (subxact_visibility.go:380) records a map entry but never calls
`commitCond.Broadcast()`. `WaitForXID` sleeps on `commitCond` (manager.go:591), so
even a subxact-aware waiter would not be woken by `ROLLBACK TO SAVEPOINT`.
`execRollbackTo` (operators_tx.go) calls `MarkSubxactAborted` but does nothing to
wake row-lock waiters.

## 3. Proposed design (implementation plan for the next loop)

Deliberately avoid registering subxids in the proc-array — that would change
`OldestXmin`, snapshot building, and VACUUM horizon semantics across the whole
engine (unbounded blast radius). Instead make *liveness of a subxid* a resolved
function of its top-level parent + the abort map, which is already tracked.

### 3.1 Subxact-aware liveness (Gap A)

Add `Manager.IsXIDActive`/`xidInProgress` resolution for subxids:

```
IsXIDActive(xid):
    if xidInProgress(xid):            // top-level fast path, unchanged
        return true
    top := TopLevelXid(xid)           // subxact map; == xid when not a subxid
    if top == xid: return false       // genuinely not a subxid
    return xidInProgress(top) && !IsAborted(xid)
```

A subxid is "active" iff its top-level parent is still running *and* the subxid has
not been individually rolled back. `WaitForXID(subxid)` then loops on the same
predicate (wait while parent in progress AND subxid not aborted).

> **Scope guard:** the new branch only changes behaviour for arguments that are
> subxids (present in the subxact parent map). For every top-level xid the fast path
> returns first, so the hot paths (snapshot visibility, FK waits) are byte-for-byte
> unchanged. This is what bounds the blast radius — but it still must be proven by
> the full gate suite (§4), because `IsXIDActive` is called from many sites.

### 3.2 Conflict-filtered wait set (Gap B)

Add `conflictingLockHolders(xmax, infomask, infomask2) []TransactionID` next to
`activeLockHolders`: resolve members through the store, keep a member iff
`IsXIDActive(m.Xid)` **and** `StatusesConflict(m.Status, o.lockMemberStatus())`
(self always excluded). Use it for the *wait targets* in the lock-only conflict
branch (operators_lockrows.go:971-999) and, symmetrically, in the updater-bearing
`multiHolders` branch (operators_lockrows.go:814-823). Keep `activeLockHolders` for
the membership-survivor logic in `stampMultiLock`.

### 3.3 Wake on subxact abort (Gap C)

`MarkSubxactAborted` must `commitCond.Broadcast()` after recording the abort (mirror
what top-level abort does). This wakes every `WaitForXID` sleeper so each re-checks
its (now subxact-aware) predicate. `execRollbackTo` already calls
`MarkSubxactAborted` for each discarded subxid, so no executor change is needed
beyond confirming every aborted level is marked.

### 3.4 Retry already exists

The wait branch already re-runs `stampLockInner(rel, ptr, depth+1)` after the waits
return (operators_lockrows.go:999) — this *is* the "retry the overall tuple-lock
algorithm" step. With §3.1–3.3 the trace resolves:

* s2 waits on conflicting `{subE:SHARE, subF:NKU}` (Gap B), both live via §3.1.
* `s1_rollback_f` aborts subF → broadcast → s2 wakes, subF no longer active, but
  subE:SHARE still conflicts → s2 re-probes and waits again. (Matches "still
  waiting".)
* `s1_rollback_e` aborts subE → broadcast → s2 wakes, no conflicting member remains
  → s2 grants via `stampMultiLock`. (Matches "completes".)
* s3's FOR UPDATE completes after the full `s1/s2/s0` rollbacks, unchanged.

### 3.5 Member hygiene on re-stamp

When s2 finally combines via `stampMultiLock`, the aborted `subE`/`subF` members are
dropped by the existing `IsXIDActive` survivor filter (operators_lockrows.go:1352) —
now correctly returning false for the aborted subxids. No extra cleanup needed; the
stale members are simply not carried into the new MultiXact.

## 4. Gates & risk (why this is its own loop)

This touches `internal/mvcc/manager.go` liveness — the highest-blast-radius
subsystem. Mandatory gates for the implementation loop (practice card — MVCC change):

* `go test -race ./internal/mvcc/... ./internal/multixact/... ./internal/executor/...`
* Full row-lock / multixact regression batch (the `0118-0011` batch) with `-race`.
* `TestPort_IsolationTuplelockUpgradeNoDeadlock` byte-identical vs PG 18.3, **and**
  re-run the other already-passing isolation specs (subxact-aware `IsXIDActive`
  could perturb FK-wait / skip-locked / nowait specs).
* CI-parity pgbench smoke (mandatory every commit).
* Recovery/standby unaffected (subxact liveness is in-memory, runtime-only; no
  on-disk format change), but run a recovery smoke since `IsXIDActive` is consulted
  during replay-adjacent paths.

**Risk concentration:** §3.1 changes a predicate consulted widely. The scope guard
(fast path for top-level xids) is the mitigation, but only the gate suite proves no
existing spec regresses — hence design-first, implement-and-fully-gate next loop, per
the M0118 hard-won rules.

## 5. What landed this loop, and the discovered 4th gap (Gap D)

**Landed & gated** (Gaps A, B, C):

* **Gap A** — `mvcc.Manager.xidActiveWithSubxact` (manager.go): subxid is active iff
  its top-level parent is in progress and the subxid is not individually aborted;
  wired into `IsXIDActive` and `WaitForXID`'s loop. Top-level fast path first, so
  ordinary xids are unchanged.
* **Gap B** — `lockRowsOp.conflictingLockHolders` (operators_lockrows.go): the
  lock-only conflict branch now waits only on members whose status conflicts
  (`StatusesConflict`) with the request, not every active member.
* **Gap C** — `MarkSubxactAborted` (subxact_visibility.go) now `commitCond.Broadcast()`
  under `waitMu` after recording the abort, so a blocked waiter wakes on ROLLBACK TO
  SAVEPOINT.

Result: perm 9 advanced from first-divergence at expected **L216 → L238** — s2 now
correctly waits across `rollback_f`/`rollback_e`, and the conflict-filtering lets it
re-probe. Verified: build/vet clean; `-race` on `internal/mvcc` /
`internal/multixact` / row-lock executor units; the full row-lock + deadlock + merge
isolation spec batch PASS (no regression on the currently-passing specs).

**Gap D — locks are stamped under the top-level xid, not the savepoint subxid
(DEFERRED).** The remaining L238 divergence: s2 completes at the *full* `s1_rollback`
instead of at `s1_rollback_e`, because s1's lock-mode upgrades are all recorded under
s1's **top-level** xid, not under `subE`/`subF`. Root cause: goopg's heap write path
stamps `ctx.Tx.XID`, and `ctx.Tx.XID` is `session.CurrentTransaction()` = the
top-level xid — **not** `EffectiveWriterXID()` (which returns the current savepoint
subxid). This is not lock-specific: `INSERT` also stamps `ctx.Tx.XID`
(operators_storage.go:2175/2177), i.e. the entire write path uses top-level xids and
`execSavepoint`'s "all heap mutations use the sub-XID" comment is aspirational. So
`stampMultiLock` re-adds s1's member at the *upgraded* strength under the **same**
top-level xid; a `ROLLBACK TO SAVEPOINT` cannot revert it (the top-level xid is still
live), and Gaps A/B/C have no subxid member to act on.

Closing Gap D requires the lock path (at least) to stamp under
`EffectiveWriterXID()`, **and** every row-lock self-identity check
(`activeLockHolders`/`conflictingLockHolders`/`stampMultiLock` `m.Xid == Tx.XID`, plus
the `tup.Header.Xmax != o.ctx.Tx.XID` conflict gates) to become **top-level-aware**
(self = same top-level ancestor via `mvcc.IsSelfXID`/`TopLevelXid`), so a subtxn does
not block on or clobber its own parent/sibling members. That is a higher-blast-radius
change (it alters what xid a lock is stamped under and touches every self-check on the
row-lock path) and contradicts the current uniform top-level-stamping convention — its
own loop, with the full gate suite of §4. Deferred (ledger 2026-06-22).

## 7. Out of scope (separately deferred)

* **UPDATE/DELETE conflict-WAIT on a *conflicting* lock-only locker.** [[0118-0011]]'s
  producer only *preserves non-conflicting* lockers; a conflicting locker is still
  dropped by the plain stamp (pre-existing behaviour, no regression). Making the
  writer wait on a conflicting locker is an independent slice.
* **`deadlock-parallel`** — needs a parallel-query lock-group abstraction goopg
  lacks; deferred at the milestone level.
