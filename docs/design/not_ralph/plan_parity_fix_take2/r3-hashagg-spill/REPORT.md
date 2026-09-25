# R3 results — the memory-blind HashAggregate

*Round 3 of `../TODO.md`. Design: `DESIGN.md` (committed `c065f0e5e`).
Implemented, gated and measured 2026-09-08.*

## 1. Verdict

The arm is implemented faithfully and works. It moved goopg's
aggregation choice in the right direction but **explains far less of the
gap than the design predicted**, and the measurement that followed
identified the actual dominant cause — which is not spill.

| | goopg before | goopg after | PG 18.3 |
|---|---|---|---|
| TPC-DS `GroupAggregate` | 1 | **13** | **100** |
| TPC-DS `HashAggregate` | 133 | 121 | 29 |

| corpus | parity before | parity after |
|---|---|---|
| TPC-H | 2 / 20 / 0 / 0 | 2 / 20 / 0 / 0 (unchanged) |
| TPC-DS | 0 / 72 / 0 / 24 / 3 | 0 / 72 / 0 / 24 / 3 (unchanged) |

Values green on both corpora: TPC-H 22/22 byte-identical to R2; TPC-DS
SF0.5 `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`.

### The prediction, scored

DESIGN §6 predicted: *TPC-DS `GroupAggregate` adoption rises
substantially from 1; TPC-H moves little; match counts rise or stay
flat.* Scored honestly:

- **TPC-H moves little — correct.** It did not move at all, for the
  predicted reason (§3's inertness: TPC-H's groupings fit).
- **Match counts do not fall — correct.** They did not move either.
- **"Rises substantially" — partly wrong.** 1 → 13 out of PG's 100 is
  a 13x rise that closes about 12% of the gap. Called substantial in
  advance; in fact the arm is a minor contributor.

## 2. What the measurement then showed — spill is not the main cause

Having the arm in place made the real cause visible. Two queries where
PG picks `GroupAggregate` and goopg still hashes, with the estimates
both engines print:

| | goopg | PG |
|---|---|---|
| Q81 inner aggregate | `HashAggregate rows=146` | `GroupAggregate rows=351` |
| Q12 aggregate | `HashAggregate rows=4572` | `GroupAggregate rows=41` |

**146, 351, 4,572 and 41 groups all fit trivially in a 64 MB budget.**
Neither engine is spilling on these. So PG is not choosing
`GroupAggregate` here because the hash table does not fit — the spill
arm is irrelevant to the majority of the gap, and would have been at any
tuning.

The reason is visible in the shapes (R2 §5's adjudication, re-read with
this in hand). TPC-DS Q12:

```
goopg:  WindowAgg -> HashAggregate
PG:     WindowAgg -> Sort -> GroupAggregate
            Window: w1 AS (PARTITION BY item.i_class)
```

PG's `WindowAgg` needs its input ordered by the partition key. PG
therefore has to pay a `Sort` *somewhere*, and putting it **below** the
aggregate makes the sorted aggregate nearly free — so `GroupAggregate`
wins a contest `HashAggregate` cannot enter, because it alone delivers
the ordering the node above requires. This is PG's pathkey machinery:
`create_grouping_paths` offers sorted aggregate paths carrying their
pathkeys, and the upper planner selects the cheapest path *that
satisfies the required ordering*, not merely the cheapest.

goopg's plan has no `Sort` between `WindowAgg` and its input at all —
its `WindowAgg` orders internally — so the ordering requirement never
reaches the planner, no path is credited for satisfying it, and the
sorted aggregate never has a reason to win.

**The dominant cause of the aggregation inversion is that goopg's upper
planner does not model the ordering requirements that make PG choose
sorted aggregation.** That is R4's subject and it is now evidenced
rather than hypothesised.

## 3. Was the round worth landing?

Yes, on three grounds, and this is argued rather than assumed:

1. **It is PG-faithful.** `costAgg`'s hashed arm was memory-blind and
   PG's is not. The goal is that goopg reach PG's plans by running PG's
   cost computation; a knowingly-absent term in that computation is a
   permanent obstacle regardless of how much of today's gap it explains.
2. **It removes an OOM exposure.** The 13 queries that moved are the
   large-cardinality groupings — precisely the ones where goopg's
   non-spilling `aggregateOp` was the risk the old comment named
   (*"risks the OOM instead"*). They now plan as `GroupAggregate`, whose
   input `Sort` goopg can spill.
3. **The old objection did not reproduce.** The comment predicted TPC-H
   Q3/Q10/Q13/Q18 would move to sorted, away from PG's hash. Measured
   against the corrected live reference: **TPC-H did not move at all**,
   and its parity verdicts are identical. The design's stated
   falsification condition — goopg going sorted where PG stays hashed —
   did not trigger.

## 4. A test whose invariant the change legitimately broke

`TestPartialAggVerdictIsScaleFree` failed. It pins that scaling rows and
distinct counts together cannot change the partial-aggregation verdict,
which is what licenses `partialAggNotionalRows` to substitute a notional
row count when goopg cannot see a real one.

The spill arm is a memory **threshold**, so it is deliberately not
scale-free — and neither is PG's cost model, for the same reason.
Probed to locate the break rather than assumed: at `c19gParams`'s 1 GB
budget and this fixture's 136-byte entry, ~7.9M groups fit, and the
failing case was 50M groups. The old test's 100M-row arm was crossing
the threshold and flipping **correctly**.

Resolved by bounding the property rather than deleting it: the row range
now stops below the threshold, and a companion test
(`TestPartialAggVerdictCrossesTheSpillThreshold`) pins that the
non-homogeneity is real and located there, so the narrowed range cannot
be misread later as a quietly-trimmed flaky test.

**A limitation is recorded, not erased**: the blind arm's licence is now
regime-bounded too. A notional row count that lands on the wrong side of
the spill threshold can flip a verdict a real one would not. Filed in
the TODO; it needs a real row count to fix, not a cost tweak.

## 5. Gates

- `go test ./internal/optimizer/` — green, incl. 3 new pins:
  inertness below the threshold (the load-bearing one), the charge and
  its startup/total asymmetry above it, and the two transcribed helpers
  against PG's formulas on worked values.
- `go test ./internal/executor/` — green.
- TPC-H values 22/22 byte-identical (`values-tpch-r3.txt`).
- TPC-DS SF0.5 sweep all-zero (`values-tpcds-sf05-r3.txt`).
- All captures `VERIFIED` against the built binary (K5); parity measured
  against the live references (K9/K10).

## 6. Filed

- **R4 (next): model the ordering requirement.** §2's cause. The
  largest single lever left on TPC-DS aggregation.
- **`partialAggNotionalRows` is now threshold-sensitive** (§4).
- **`transitionSpace = 0`** in `hashAggEntrySize`: goopg has no
  per-aggregate transition-space estimate, so the entry size is a
  slight under-estimate for aggregates with large transition values
  (`array_agg`, `string_agg`). Under-charging can only fail to predict a
  spill PG predicts — never invent one — so it is safe in the
  conservative direction, and it is a real remaining difference from PG.
