# M0125-0047 — the comma-FROM join order was decided by Go's map-iteration randomiser

**Status:** fixed and landed 2026-08-03.
**Item:** `M0125-0047` (filed 2026-08-03 by `M0125-0002` commit 4).
**Code:** `internal/planner/joinorder.go` (`pickNextByEdge`),
`internal/planner/joinorder_determinism_test.go`.
**Evidence of the original sighting:**
`analysis/m0125-0002-c4-plans-20260803/README.md` §"q85".

## 1. The defect

`reorderCommaFromByCardinality` permutes a comma-`FROM` list with a greedy
nearest-neighbour walk: seed with the smallest relation, then repeatedly take
the smallest relation joined by an equality edge to something already placed.
The "take the smallest connected relation" step is `pickNextByEdge`, and it
ranked candidates like this:

```go
for _, j := range joined {
        for k := range edges[j] {          // edges[j] is a map[int]struct{}
                if used[k] { continue }
                if best == -1 || rowCounts[k] < rowCounts[best] {
                        best = k
                }
        }
}
```

Two facts combine into a defect:

1. `edges[j]` is a **map**, and Go randomises `range` order on every
   iteration, deliberately, to stop programs depending on it.
2. The comparison is a **strict** `<`. On a tie it keeps the incumbent — i.e.
   it keeps whichever tied candidate the map yielded **first**.

So when two candidates tie on row count, the winner is chosen by the map
randomiser. The comparison had no total order over relation indices, which is
the property that would have made the map order irrelevant.

A tie is not a corner case here. **A query that scans one table twice makes
the tie unavoidable**: the two aliases are the same relation, so they carry
identical statistics by construction, and no amount of `ANALYZE` can separate
them.

## 2. Why Q85

TPC-DS Q85 joins eight relations and names `customer_demographics` twice, as
`cd1` and `cd2`, both reached by an equality edge from `web_returns`
(`wr_refunded_cdemo_sk = cd1.cd_demo_sk`, `wr_returning_cdemo_sk =
cd2.cd_demo_sk`). At SF=1 they are the two **largest** relations in the list
(1,920,800 rows each), so the greedy walk places everything else first and the
final two steps are a pure two-way tie between them:

```
reason(35) → web_returns(71,763) → customer_address(50,000) → date_dim(73,049)
  → web_sales(719,384) → web_page(60) → { cd1, cd2 }   ← tie, map decides
```

Three restarts of one binary (`tmp/goopg-c4-after`) printed cd2-first twice
and cd1-first once. That is the observed 50/50.

## 3. Two distinct costs

**A PG divergence.** Upstream's planner is a deterministic function of its
inputs: `add_path` tie-breaks (`pathcmp`/`compare_path_costs_fuzzily`,
`src/backend/optimizer/util/pathnode.c`) are stable, and `RelOptInfo` sets are
`Relids` bitmapsets walked in index order rather than hash tables walked in
hash order. PG does not hand the same query two different plans across
restarts, so goopg doing it is a compatibility defect on its own.

**An instrument hazard, which is the more expensive one.** Every
EXPLAIN-based A/B in this repo — `plan_snapshots/`, `make plan-diff`, the
TPC-DS SF0.5 EXPLAIN capture — compares plan text between two arms. A
Q85-shaped query can produce a **phantom hunk**: a diff caused by nothing but
which arm's server start won the coin flip. `M0125-0002`'s commits 2–6 were
each accepted on one sweep per arm; commit 4's arms happened to agree, and the
flip surfaced only because a third (probe) arm was captured. So the plan-shape
instrument had an unquantified nondeterminism floor, and "22/22 byte-identical"
meant less than it read.

## 4. The fix

Give the tie-break a **total order** by comparing relation indices last:

```go
if best == -1 || rowCounts[k] < rowCounts[best] ||
        (rowCounts[k] == rowCounts[best] && k < best) {
        best = k
}
```

The result is now a pure function of the query text: for any map iteration
order, the same candidate wins.

Breaking the tie on the **lowest FROM index** is not an arbitrary pick among
the two stable options. It is the rule the other two pickers in this file
already used — `smallestUnused` ("ties broken by lowest index", a slice walk)
and `orderByConnectivity` (`if next == -1 || k < next`) — so all three pickers
now share one tie-break, and a tie resolves to source order, which is the
order the user wrote.

### What was audited and found already deterministic

- `smallestUnused` — walks a slice by index; ties already fall to the lowest
  index.
- `orderByConnectivity` — iterates the same `edges[j]` map, but its tie-break
  is `k < next`, a total order. This is the pass with the documented
  "cross-free source order is a fixed point" property, which only holds
  *because* it is deterministic.
- The bushy DP (`internal/planner/bushy.go`) — `g.edges` is a slice; subsets
  and splits come from the `enumerateSubsets`/`enumerateSplits` generators
  (bit enumeration, ordered); the `dp` map is looked up by mask and never
  ranged over. No map-order dependence.

## 5. Tests

`internal/planner/joinorder_determinism_test.go`. Go randomises map order on
every `range`, not once per process, so the defect reproduces **in-process** —
no three-restart shell probe is needed, and the guard runs in the unit suite.

| test | what it pins |
|---|---|
| `TestPickNextByEdgeTieBreaksOnIndex` | the defect at its own level: two equal-row-count candidates both edged to the placed relation, 200 calls, must always return the lower index |
| `TestJoinOrderQ85AliasTieIsDeterministic` | 200 re-planned Q85 `FROM` lists must agree with each other |
| `TestJoinOrderQ85AliasTieBreaksOnSourceOrder` | *which* of the two permutations: `cd1` before `cd2` |
| `TestPlanQ85IsDeterministic` | 100 whole-`Plan()` runs must produce identical alias-bearing fingerprints — the guard against a *second*, undiscovered map-order site downstream of the reorder |

All four were proven to FAIL against the pre-fix body before the fix landed
(`TestJoinOrderQ85AliasTieIsDeterministic` failed on iteration 1 with exactly
the recorded cd2-first/cd1-first pair; `TestPlanQ85IsDeterministic` on
iteration 9).

`TestPlanQ85IsDeterministic` renders its fingerprint by **reflection** rather
than a type switch, for a reason worth keeping: the package's existing
renderer `planShapeString` (`predp_test.go`) prints scans as
`x.Table.Name`, and `cd1`/`cd2` share one `*catalog.Table` — so it prints the
two Q85 permutations **identically** and cannot see this defect at all. The
fingerprint prints the alias, and reflection means a node type added later is
covered without editing the test.

## 6. Measured at SF0.5 (`analysis/m0125-0047/`)

Two instruments, because they answer different questions.

**The 96-query EXPLAIN capture** (`capture-plans.sh`, one arm per server start,
2m43s per arm; `EXPLAIN` only — 13 of the 99 do not finish inside the cap and
planning is the only thing under test):

| comparison | result |
|---|---|
| after1 vs after2 vs after3 (fixed binary, 3 restarts, pairwise) | **all 96 byte-identical** — the item's stated acceptance |
| before1 vs before2 (pre-fix binary, 2 restarts) | 96 byte-identical (the coin landed the same way twice) |
| before vs after | **96 byte-identical** — no plan moved |

The last row is the one that matters for the rest of the repo: the fix
converges on the plan the baselines **already contain** (Q85 keeps its
`cd2`-first shape, digest `6fb943ca2c7aa936`), so no snapshot needs re-pinning
and no earlier A/B is invalidated by this commit.

**The 10-restart Q85 probe** (`probe-q85-restarts.sh`), because a 4-arm capture
cannot tell "stable" from "the coin agreed":

| binary | 10 restarts |
|---|---|
| pre-fix | restart 1 `cd1,cd2`; restarts 2–10 `cd2,cd1` — **1 divergence in 10** |
| fixed | `cd2,cd1` ×10 — **0 divergences** |

The two binaries differ in nothing but the tie-break comparison, so this is a
causal reading, not a correlation.

**Read the probe honestly: on its own it is a weak instrument.** The observed
flip rate is ~10%, not 50%, so ten clean post-fix restarts would occur by
chance about a third of the time even if nothing had been fixed. The probe
establishes that the defect still reproduced at HEAD (it had only ever been
seen on a commit-4-era binary) and that it is gone from the fixed one; the
*proof* is the unit test, which is strong for a reason worth keeping in mind —
**Go re-randomises map order on every `range`, not once per process**, so 200
in-process iterations sample the randomiser 200 times, whereas 10 restarts
sample it 10 times at a cost of ~90 seconds each.

## 7. What this does and does not close

It closes the nondeterminism **in the comma-FROM reorder**, which is where the
Q85 flip lived, and the whole-`Plan()` test now certifies Q85 end to end.

It does **not** amount to a proof that goopg's planner is deterministic
overall. The audit in §4 covers the join-order passes; it is not an exhaustive
sweep of every map in the planner. `TestPlanQ85IsDeterministic` is the shape
of the guard a future sweep should generalise — apply the same repeat-and-
fingerprint harness to a wider query corpus.

**M0127 prerequisite.** `M0127-P5.4`'s deterministic tie-break builds on this
fix, and the 09 §4 plan-shape ratchet (a pinned mismatch budget that must not
grow across commits) cannot exist while plans flip across restarts. This is
one of the M0125 items M0127 waits on.
