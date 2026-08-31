# 0118-0106 — `eval-plan-qual.spec` PROMOTED: EPQ recheck over a join (M0118-0009)

Status: accepted
Date: 2026-06-25
Milestone: M0118 (Upstream Isolation Spec Suite Pass-Through), task M0118-0007 / M0118-0009 tail

## Summary

Promotes `eval-plan-qual.spec` from `failed` to `pass` (all 50 permutations
byte-identical to PG 18.3, `runIsoSpecStrict TestPort_IsolationEvalPlanQual`).
This closes the **M0118-0007 planner/output-format group** (its sibling
`eval-plan-qual-trigger` was promoted in 0118-0095; `drop-index-concurrently-1`
in 0118-0024).

The single remaining divergence was the `selectresultforupdate` permutation:

```sql
-- s2: wrjt
UPDATE jointest SET data = 42 WHERE id = 7;   -- non-key UPDATE, in-flight
-- s1: selectresultforupdate (blocks on FOR UPDATE OF jt, then c2 commits)
select * from (select 1 as x) ss1 join (select 7 as y) ss2 on true
  left join table_a a on a.id = x, jointest jt
  where jt.id = y for update of jt, ss1, ss2;
-- PG: 1|7| 1|tableAValue| 7|  42   (re-projects the updated jt row)
-- goopg (before): (0 rows)
```

The locked relation `jt` is the inner side of a join; after the concurrent
non-key UPDATE commits, the EvalPlanQual recheck must re-project the post-update
`jt` row. goopg dropped the whole row (`0 rows`).

## Root cause

goopg has no full EvalPlanQual-re-runs-the-subplan machinery; `lockRowsOp`
approximates EPQ by following the old tuple's CTID chain to the live successor
and re-applying a **recheck predicate** built from the locked table's own
columns (`epqRecheckFilter` decodes only the locked relation's tuple).

When the locked scan is an index scan, `lockRowsOp.Open` folds the index key
condition into that recheck predicate (so a committed *key* UPDATE that relocates
the row is caught — `lock-update-delete` blocker2). `indexScanPredicate` builds
`ColumnRef{id} = ix.Key`. For a standalone scan `ix.Key` is a constant; but when
the index scan is the **inner of a join**, `ix.Key` is the join lookup key — here
`y`, a column of another join input (`ss2`), whose `ColumnRef.Index` lives in the
**join output coordinate space** (`x,y,id,value,id,data` → `y` is index 1).

`epqRecheckFilter` evaluates that predicate against only the 2-column `jointest`
tuple `[id, data]`, so `ColumnRef{1}` (meant: `y`) is silently misread as
`jointest.data`. The recheck became `jt.id (7) = jt.data (42)` → `false` → the
row was rejected (`epqSkipped`) → `0 rows`.

The existing guard `filterPredMaxColRef(idxPred) < len(filterCols)` failed to
catch this: the misaligned index (1) coincidentally falls inside
`[0, len(jointest.Columns)=2)`.

## Fix

`internal/executor/operators_lockrows.go` — fold the index key condition into the
EPQ recheck **only when `ix.Key` is row-local** (a constant, e.g. `key = 1`). A
join/correlated index key is a join condition, not a local filter, and must not
be re-applied against the locked tuple alone:

```go
if ix, ok := o.scan.(*indexScanOp); ok && ix.plan != nil && len(o.filterCols) > 0 &&
    ix.plan.Key != nil && !exprRefsColumnOrOuter(ix.plan.Key) {
    // fold indexScanPredicate(ix.plan) into o.filterPred ...
}
```

New helper `exprRefsColumnOrOuter(expr)` reports whether an expression references
any `*planner.ColumnRef` or `*planner.OuterColumnRef` (a correlated outer ref).

Correctness: for a **non-key** UPDATE the join key is preserved on the successor,
so skipping the join-condition recheck yields the correct re-projected row. A
concurrent **key-column** change is still handled by the CTID-chain logic and any
local (constant) recheck predicate; the constant-key fold path (the
`lock-update-delete` motivation) is unchanged because a constant `ix.Key` does
not reference a column.

### Sibling fix — build-side ctid through a lazy hash join

Investigating with a primary-key schema variant surfaced a second, independent
latent bug: when the same query is planned as a **hash join** (`jt.id = ss2.y`
hash key) and the locked relation lands on the **build side**, that scan is
drained + closed at the join's `Open`, so its `currentTID()` is gone before
`lockRowsOp.drainAndStamp` runs — `FOR UPDATE` then neither blocked nor
re-projected and silently returned the **stale** pre-update row.

`internal/executor/operators_join_agg.go` now preserves build-side heap ctids
when a downstream `LockRows` needs them:

- `lockRowsOp.Open` calls `markJoinPreserveCTID(child, Locks[0].Table rel)`
  before `child.Open`, tagging every `joinOp` with `preserveCTIDRel`.
- A tagged `joinOp` whose build side contains that relation drains the build via
  `drainRowsCtxCTID` (capturing each row's ctid) and fills a parallel
  `lazyHashCTID` map (`buildHashRightWithCTID`).
- `nextLazy` stamps the matched build row's ctid onto the emitted slot; the
  existing `drainAndStamp` `ms.hasCTID` fallback recovers `(rel, ptr)`.

Blast radius is nil for normal queries: `preserveCTIDRel` is `nil` unless a
`FOR UPDATE`/`FOR SHARE` sits above the join, so the hot hash-join path (TPC-H)
returns the shared `VirtualSlot` untouched and never allocates the parallel map.
(The eval-plan-qual spec itself exercises the index-scan path; the hash-join fix
guards the build-side variant against silent stale-row results.)

## Tests / gates

- `TestPort_IsolationEvalPlanQual` strict PASS — all 50 permutations
  byte-for-byte vs PG 18.3 (promoted `runIsoSpec` → `runIsoSpecStrict`).
- Non-regression: `TestPort_IsolationEvalPlanQualTrigger`,
  `TestPort_IsolationLockUpdateDelete`, and the row-lock / FOR UPDATE family.
- `go test -race ./internal/executor/...`.
- TPC-H Q12/Q13 spot-check (hash-join hot path touched) — canonical counts.
- `go build ./...` + `go vet` clean; pgbench smoke = pre-commit hook.

## Files

- `internal/executor/operators_lockrows.go` — `exprRefsColumnOrOuter` guard on
  the index-key fold; `markJoinPreserveCTID` walker; preserve wiring in `Open`.
- `internal/executor/operators_join_agg.go` — `preserveCTIDRel` / `lazyHashCTID`
  / `lazyMatchCTIDs` fields, `buildHashRightWithCTID`, `nextLazy` ctid stamping.
- `internal/testport/isolation_port_test.go` — strict promotion.
- `docs/test-port/*` — inventory + D-002 narrative + regenerated md.
