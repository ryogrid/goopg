# root-0030 — `lockRowsOp` kept its drain state across a rescan, so `EXISTS (… FOR UPDATE)` returned FALSE on an EvalPlanQual recheck

Status: **implemented** (2026-07-28)
Scope: `internal/executor/operators_lockrows.go`
Nightly item: AI-20260725-011243-004 (`TestPort_IsolationEvalPlanQual`, "also failed
in the previous run")

## Symptom

`go test -run '^TestPort_IsolationEvalPlanQual$' ./internal/testport/` failed
deterministically at HEAD (`ddfb035e`, ~25 s). The first divergence was at
expected line 415, in permutation `wx1 wxext1 wnested2 c1 c2 read`:

```
L415 expected: s2: NOTICE:  lock_id: text checking = text checking: t
L415 actual:   s2: NOTICE:  upid: text savings = text checking: f
…
expected: checking |   -800
actual:   checking |    400
```

goopg emitted the four `lock_id`/`lock_bal`/`upid`/`up` NOTICEs PG emits, then
**skipped** the eight-NOTICE tail and left `accounts.checking` unmodified.

## The spec step

`wnested2` (eval-plan-qual.spec:290) is an UPDATE whose WHERE clause contains a
correlated `EXISTS` sublink that itself carries `FOR UPDATE`, with every
comparison routed through the NOTICE-emitting `noisy_oper()`:

```sql
UPDATE accounts SET balance = balance - 1200
WHERE noisy_oper('upid', accountid, '=', 'checking')
  AND noisy_oper('up', balance, '>', 200.0)
  AND EXISTS (SELECT accountid FROM accounts_ext ae
              WHERE noisy_oper('lock_id', ae.accountid, '=', accounts.accountid)
                AND noisy_oper('lock_bal', ae.balance, '>', 200.0)
              FOR UPDATE);
```

The NOTICEs make the sublink's execution count observable, which is exactly what
the permutation is testing: `s1` updates both `accounts` and `accounts_ext`, `s2`
blocks, `s1` commits, and PG's EvalPlanQual recheck re-runs the **whole** WHERE
clause — sublink included — against the new tuple version.

## Root cause

Reproduced outside the harness with two `psql` sessions against a throwaway
server (schema = the spec's `accounts` / `accounts_ext` / `noisy_oper` subset).
Temporary instrumentation in `existsImpl` and `epqFollowHOT` showed:

```
EPQDBG existsImpl op=*executor.lockRowsOp hasRow=true  err=<nil>          # first run
EPQDBG existsImpl op=*executor.lockRowsOp hasRow=false err=end of stream  # EPQ recheck
EPQDBG epqFollowHOT pred=*planner.BinaryOp pv={Kind:1 …}                  # ⇒ pred FALSE
```

The recheck run of the sublink returned zero rows **without emitting a single
NOTICE** — i.e. its inner plan never re-scanned.

`lockRowsOp` is a buffering operator: `Next` calls `drainAndStamp` once, sets
`o.drained = true`, and then serves `o.pending[o.pos++]`. Its `Open` is also its
rescan entry point — `classifySubPlan` maps any plan containing `LockRows` to
`rescanCloseOpen` (`internal/executor/subplan.go:217`) precisely because row
locks must be stamped for every qualifying outer row, so the retained tree is
`Close()`d and `Open()`ed rather than rebuilt.

But `Close` cleared only `pending`:

```go
func (o *lockRowsOp) Close() error {
	o.pending = nil
	return o.child.Close()
}
```

`drained` stayed `true` and `pos` stayed past the end. The second `Open` therefore
produced an operator that answers `Next` with `pos (1) >= len(pending) (0)` → EOF,
skipping `drainAndStamp` entirely. `existsImpl` read that as "no rows", the
`EXISTS` collapsed to FALSE, `epqFollowHOT`'s predicate failed, and `updateOp`
dropped the row as "no longer matching" — a silently lost update, not just a
missing NOTICE.

Upstream has no equivalent state to leak: `ExecLockRows` pulls one row at a time
from its subplan (`postgres/src/backend/executor/nodeLockRows.c`) and
`ExecReScan` resets the subtree wholesale.

## Fix

Reset the per-execution buffer at the top of `lockRowsOp.Open`, which is the
operator's `ExecReScan` equivalent:

```go
o.pending = nil
o.pos = 0
o.drained = false
```

Resetting in `Open` rather than `Close` covers both rescan shapes (`Close`+`Open`
and a bare re-`Open`) and keeps the invariant local to the one function that
starts an execution.

`drained` is unique to `lockRowsOp` — no sibling operator in
`internal/executor/` carries the same flag, so there is no twin path to change
alongside it.

## Result

With the fix the manual reproducer emits PG's full NOTICE sequence (outer
`upid`/`up` against the post-commit tuple, then the sublink re-executed twice —
once against the query-start snapshot version and once against the chain-followed
successor) and `accounts.checking` lands on `-800`, PG's value.

`TestPort_IsolationEvalPlanQual` now passes (27.6 s). The spec was already
`pass_required` (promoted 2026-06-25, design 0118-0106), so
`docs/test-port/postgres-oracle-port-status.csv` needs no change.

## Gates

| gate | result |
|---|---|
| `go test -run '^TestPort_IsolationEvalPlanQual$' ./internal/testport/` | PASS (27.6 s) |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS |
| `go test ./internal/executor/` | PASS |
| 21 row-lock isolation specs (`SkipLocked*`, `Nowait*`, `Tuplelock*`, `LockUpdate*`, `LockCommitted*`, `UpdateLockedTuple`, `PropagateLockDelete`, `EvalPlanQualTrigger`, `PartialIndex`, `IndexOnlyScan`) | PASS (80.8 s) |
| 14 FK / MERGE isolation specs (`ReferentialIntegrity`, `Fk*`, `RiTrigger`, `TemporalRangeIntegrity`, `Merge*`) | PASS (36.7 s) |
| `scripts/tpch-spotcheck.sh` | PASS (Q12 rows=2, Q13 rows=35) |

The TPC-DS SF0.5 gate was **not** run: the change is confined to the row-locking
operator, and no TPC-DS query builds a `lockRowsOp` (none uses `FOR UPDATE`/`FOR
SHARE`), so the gate has no path to the modified code. The TPC-H spotcheck was
run anyway as the standing executor-change bar.
