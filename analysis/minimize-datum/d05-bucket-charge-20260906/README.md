# D-05 prerequisite #2 — bucket-array charge: implemented, measured, **REVERTED**

Date: 2026-09-06. Third measurement in the chain
D-04 → entry-width → this. Each one disproved the previous one's
prediction. Patch preserved at `tmp/d05p2-bucket-charge.patch`.

## D-04's premise was ALSO refuted: the buckets were already charged

D-04 claimed `spaceUsed` "counts only rows", so the bucket array escapes
the growth budget. Literally true of the *field*, false of the *test*:

- `join_batch.go` computes `space := sizing.SpaceAllowed -
  nbuckets*MapSlotBytes` — the budget is **pre-deducted**.
- growth then fires on `spaceUsed > spaceAllowed` counting rows only.

Algebraically that trigger is **identical** to PG's
`spaceUsed + nbuckets*sizeof(HashJoinTuple) > spaceAllowed`
(`nodeHash.c`). goopg already did what PG does here.

## What was actually wrong: `MapSlotBytes = 48` was a guess, 2× low

The constant was derived by hand as "16-byte string header + 24-byte slice
header + tophash + slack". Measured against go1.25's swisstable runtime
(live heap, presize alone, forced GC):

```
make(map[string][]Row, 1<<20)   100.7 MB  =  96.1 B/slot
make(map[int64][]Row,  1<<20)    84.0 MB  =  80.1 B/slot
```

That is D-04's 506 MB explained: Q9's `orders` key is `numeric`, which
takes the **string** lane at 96 B/slot, × 1,048,576 slots × 5 private
worker builds.

Three things PG does that goopg did not: its constant is exact
(`sizeof(HashJoinTuple)`), it folds the bucket array into the **reported**
`spaceUsed` ("Account for the buckets in spaceUsed (reported in EXPLAIN
ANALYZE)"), and it asserts `bucket_bytes <= hash_table_bytes/2`. With an
honest 96 that assert is *violated* on the Q9 witness — 100.7 MB of a
128 MB budget — and goopg had no such guard.

## The change and its result

`MapSlotBytes` 48 → 96 (errs HIGH: int-keyed builds over-charged 20%,
which batches earlier; under-charging is the OOM direction), PG's assert
implemented as a clamp, and `SpacePeak` including the bucket array.

**Memory: a large, real win.**

| Q9 `orders` build | before | after |
|---|---|---|
| Buckets | 1,048,576 | 524,288 |
| **Batches** | 4 | **4 — unchanged, no extra spilling** |
| true per-worker peak | 142,330 kB | **93,206 kB (−34.5%)** |
| live heap `presizeLazyHash` | 586.7 MB | **286.0 MB (−51.3%)** |

**Time: a large, real loss.**

| | before | after |
|---|---|---|
| TOTAL (median of 3) | 125.82 s | **138.92 s (+10.4%)** |
| TOTAL excluding Q14 | 125.40 s | 124.64 s (−0.6%) |
| **Q14** | **0.42 s** | **14.55 s (+3364%)** |

Values PASS (24 MATCH on all five pairings). Plans moved 64 lines: Q7 and
Q10 re-associate and get *faster*; Q14 flips Hash Join → Nested Loop and
loses catastrophically.

## Q14 diagnosed: the loss is the cost side, not spilling

Q14 ran `Batches: 1` in **both** arms. The executor never spilled. What
changed is which plan the *planner* chose:

| model input | entry | inner | buckets | total vs 134.2 MB | nbatch |
|---|---|---|---|---|---|
| cost side, un-narrowed 9 cols, M=48 | 555 | 111.0 MB | 12.6 MB | 123.6 → fits | 1 |
| cost side, un-narrowed 9 cols, **M=96** | 555 | 111.0 MB | 25.2 MB | **136.2 → 1.5% over** | **2** |
| executor side, narrowed 2 cols | 141 | 28.2 MB | 25.2 MB | 53.4 → fits | 1 |

The planner priced a spilling 9-column build **the executor never builds**.
The honest bucket price pushed that phantom build 2 MB past the budget, a
spill term appeared, and the nested loop won.

This is precisely the divergence the entry-width write-up identified and
deliberately left open: *"the planner's COST side still prices the
un-narrowed build … it belongs in its own item."*

## Verdict: REVERT (done), and re-sequence

−51% bucket heap for +10.4% wall time is not a win on one axis. Reverted.

But this is a *different* negative from D-04's. The change did exactly what
prerequisite #2 promised — the map halved, the batch count did not move,
Q9 timing was neutral, values matched. It failed on another mechanism
entirely, and that moves the blocker:

**D-05 prerequisite #2 is no longer the lever. It is blocked behind the
cost-side narrowing fix** (ledger `take3-D-05-costside-unnarrowed`). Until
the planner prices the build the executor actually builds, any correction
to `MapSlotBytes` amplifies a phantom build across the budget line, and the
failure mode is **plan flips, not spilling**.

The margin shows this is not merely marginal in the other direction: with
the narrowed input the same build sits at 53.4 MB of 134.2 MB — a 2.5×
margin, not a 1.5% one. Fix the cost side, re-apply this patch, re-run this
exact A/B.

## One cheap thing worth landing on its own

`SpacePeak` should include the bucket array, as PG's does. Zero behavioural
effect, and it is **the reason this line of work was flying blind**:
`Memory Usage: 44026kB` omitted the join's largest term. Filed separately.

## Not measured

TPC-DS SF0.5 (the change was reverted before the hour was worth spending);
a lane-aware `MapSlotBytes` — Q9 and Q14 are both on the 96 B string lane,
so it would not have saved Q14.
