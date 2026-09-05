# D-05 prerequisite #1 — entry-width fix: correct, and **performance-neutral**

Date: 2026-09-06. Follows `analysis/minimize-datum/d04-pack-prototype-20260905/`,
whose stopping-rule verdict was "fix the model first". This is that fix,
measured. **It refutes D-04's own central claim.**

## The defect, with an arithmetic witness

The hash-join entry model was **half priced on the narrowed row and half on
the full one**:

| half of `hashsize.EntryBytes(ncols, avgVarBytes)` | source | Q9 `orders` build |
|---|---|---|
| `ncols` | executor: build child schema, already cut by `narrowBuildInput` | **2** |
| `avgVarBytes` | `Join.AvgVarBytes` ← `Rel.AvgVarBytes`, summed over **every column of the table** | **74** |

`createplanjoin.go` took `AvgVarBytes` from the rel three lines after
`joinInputsFor` had already narrowed the inner input. From the server's own
`pg_stats`, those 74 bytes are `o_comment 50 + o_clerk 15 +
o_orderpriority 8 + o_orderstatus 1` — every one a column the build drops.
The two retained columns have `avg_width 0`.

Model 194 B/row against the executor's own accounting: `Memory Usage:
44026 kB ÷ (1.5 M / 4 batches)` = **120.2 B/row**.

Same defect on the other builds: `partsupp` 293→168, `part` 171→72,
`supplier⋈nation` 311→127.

## The fix

`RelOptInfo` carries `ColVarBytes` (column name → `AvgWidth`, unioned at
join rels keeping the wider width on a collision); `createHashJoinPlan`
sums it over the schema the build actually emits. Model now reads 120.0
against 120.2 measured.

**Direction of error: HIGH.** Any column it cannot attribute — a computed
Project target, a subquery output, a missing statistics row — falls back to
the whole-relation sum, i.e. the old over-stating value. A column absent
from the map is never charged zero. Under-stating is the OOM direction and
is unreachable by construction.

## The result: D-04's central claim is REFUTED

D-04 predicted "correcting `avgVarBytes` alone takes 194 → 120 and `nbatch`
4 → 2". The entry does go 194 → 120. **`nbatch` stays 4.**

`nbatch` is **non-monotone** in entry size at 1.5 M rows / 128 MB, because
shrinking the entry buys more buckets, and the bucket array is charged too:

| entry B/row | 63 | 72 | 96 | 111 | **112** | **120** | 128 | 168 | **194** |
|---|---|---|---|---|---|---|---|---|---|
| bucket array | 100.7 MB | 100.7 | 50.3 | 50.3 | 50.3 | 50.3 | 50.3 | 50.3 | 50.3 |
| **nbatch** | 4 | 4 | 2 | 2 | **4** | **4** | 4 | 4 | **4** |

Two batches need ≤ 111.8 B/row. Two retained Datums plus their slice header
are **already 120**. So no retention-format change short of packing reaches
2 here — and **D-04's "ideal packed ~63 B/row" lands back on 4**, because
the bucket array then doubles and takes back more than the rows gave up.

**The lever on this witness is `MapSlotBytes = 48` — the bucket array — not
the entry.** That is D-04's prerequisite #2, and it now looks like the only
one that matters.

## Measurements

22 queries, 3 reps per arm, interleaved, one binary per arm, fresh capped
server per arm, statistics pinned.

| | before | after |
|---|---|---|
| TOTAL (3 reps) | 128.15 / 125.89 / 126.46 | 124.99 / 125.97 / 126.41 |

**Timing-neutral**: the −0.4% median sits inside the before-arm's own 1.8%
spread, and the after arm always ran second, which flatters it via page
cache.

- **Values: PASS**, `24 MATCH` on five pairings including cross-rep ones.
- **Plans: byte-identical**, 0 differing lines. Expected — the fix touches
  the value handed to the executor's geometry, not `hashJoinCost`.
- **TPC-DS SF0.5: PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0.**
- Q12 shows a repeatable −5% that is **not attributable**: Q12's plan is a
  Merge Join with no hash join at all, and the changed function is called
  only from `createHashJoinPlan`. Recorded as an unexplained artifact, not
  a win.

## Recommendation, taken: keep it, record the negative result

It is correct, it is D-04's stated prerequisite, it costs nothing in time,
values or plans, and it errs high. It simply does not deliver what it was
expected to deliver.

## A larger divergence found and deliberately NOT touched

**The planner's COST side still prices the un-narrowed build.**
`pathNCols`/`pathAvgVarBytes` fall back to the rel for everything except an
index-only path, so `hashJoinCost` costs Q9's `orders` build at 9 cols +
74 B = **530 B/row → 8 batches**, while the executor runs 4. That is a
bigger planner↔executor disagreement than the one fixed here, but closing
it changes plan *choice* — the narrowing keep-set is derived after the
search, so any cost-side version is an approximation — and it would have
destroyed this experiment's isolation. It belongs in its own item.

## Not measured

- Memory. No heap sampling this round, so the peak-RSS effect of 194→120 is
  unquantified. It should be ≥0 (the presize reads the same geometry).
- The `partsupp` build's geometry move (4→2 batches, +25 MB of presized
  map): that join is the composite multi-key path and prints no
  `Batches:` line, so the runtime effect is unconfirmed either way.
