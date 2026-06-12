# M0100-0010: EPQ RETURNING fix in updateWithFrom

**Status**: accepted  
**Milestone**: M0100-0010

## Problem

`TestPort_IsolationEvalPlanQual` was failing at the `simplepartupdate_noroute
complexpartupdate_doesnt_route` permutation. The step:

```sql
with u as (update another_parttbl set a = 1 returning another_parttbl.*)
update parttbl p set a = 3 - p.b from u where p.a = u.a and p.c = 1 returning p.*;
```

produced `RETURNING 2|1|1|1003` instead of `1|2|1|3`.

## Root Cause

`updateOp.updateWithFrom` has three phases:

1. **Collect FROM rows** (execute CTE / FROM subqueries).
2. **Scan target table** — cross-product each target row with FROM rows, build
   `pending[]` with `pu.newRow` and `pu.retNewRow`.
3. **Apply pending updates** — stamp xmax and write new rows.

When the scan (phase 2) runs while a concurrent transaction holds a tuple lock,
`pu.newRow` is built from the *stale snapshot* row.  For a partition-key change
(`a = 3 - p.b` with old `b=1` gives `a=2`), the code routes the new row to a
different partition and sets `pu.retNewRow = parentNewRow` (the stale value).

In phase 3, when EPQ fires (concurrent tx committed), `epqFollowHOT` returns the
updated row (`b=2`), SET is re-evaluated (`a = 3 - 2 = 1`), and `pu.newRow` is
updated to the EPQ-corrected value.  However, **`pu.retNewRow` was not cleared**.

The RETURNING logic:

```go
retForRet := pu.newRow
if pu.retNewRow != nil {
    retForRet = pu.retNewRow  // stale value wins
}
o.appendUpdateRetRowWithFrom(retForRet, pu.fromPortion)
```

…used the stale `pu.retNewRow` (a=2, b=1) for output, even though the actual row
written to disk used the EPQ-corrected `pu.newRow` (a=1, b=2).

## Fix

In the EPQ path of `updateWithFrom` (after recomputing `parentNewRow`), clear
`pu.retNewRow` so the RETURNING code falls back to `pu.newRow`:

```go
pu.newRow = parentNewRow
pu.retNewRow = nil  // EPQ recomputed parentNewRow into pu.newRow; clear stale retNewRow
pu.oldRow = cloneRow(epqRow)
```

`retNewRow` is non-nil only when an initial cross-partition move was detected
during the scan phase.  After EPQ corrects the SET expression, the destination
partition may differ from (or coincide with) the original, and `pu.newRow`
already holds the parent-aligned corrected value — so `retNewRow = nil` is
exactly the right sentinel.

## Verification

- `TestPort_IsolationEvalPlanQual` → **PASS** (was SKIP).
- All executor/mvcc/server/planner unit tests pass with `-race`.
- Isolation suite: **20 PASS, 2 SKIP** (up from 19 PASS).
