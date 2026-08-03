# 01 — Motivation and the measured evidence, re-derived

Every number in this chapter was recomputed from raw files in the repository. Where the
re-derivation disagrees with a summary previously circulated, the raw number wins and the
disagreement is called out by name.

**Nothing in this bundle was measured on a live server.** A TPC-DS sweep was occupying the
machine and a Ralph loop was editing the tree throughout; all checks are static reads of
committed artefacts. Every runtime figure below is a citation, not an observation.

## 1. What MHJ is, and why it exists

`rewriteMultiWayChain` (`internal/planner/bushy.go:1753-1800`) runs **once, post-DP**
(`internal/planner/planner.go:1003` region) and replaces a chain of `≥3` inner hash joins with a
single `*planner.MultiHashJoin`. `mhjPackingEnabled` defaults to `true` (`bushy.go:580`) and
`init()` forces it to `false` under `GOOPG_COST_DRIVEN_JOINORDER=1` (`bushy.go:17-22`).

The operator is `internal/executor/multi_hash_join.go` (651 lines):

- `Open()` drains every non-probe child into a hash table (`:170-240`).
- It derives a chain of `keyStep`s by BFS from the probe table over `plan.Keys` (`:126-165`).
- `Next()` walks an odometer over per-step cursors, emitting through a **single shared
  `VirtualSlot` composed from per-table `MaterializedSlot`s** (`:265-310`), so a match costs
  **zero row allocations**.
- Residual filters are partitioned by the deepest chain step they need (`partitionFilters`), so
  a failing filter prunes a prefix.

Its stated origin is memory: `multi_hash_join.go:14-17` records that the pre-M0043
full-materialisation path used ">19 GB heap + 91 % GC overhead on Q9".

PostgreSQL has no such node. That is the entire compatibility problem — and the bullet in bold
above is the entire *performance* story, as [02](02-premise-audit.md) shows.

## 2. The TPC-H SF1 A/B — re-aggregated

Sources, same HEAD `cb37d166`, same day 2026-07-24, both ANALYZEd:

- `docs/design/cost-model/evidence/sf1-r5-default-cb37d166.txt` — integer planner, MHJ on
- `docs/design/cost-model/evidence/sf1-r5-costdriven-cb37d166.txt` — `GOOPG_COST_DRIVEN_JOINORDER=1`, MHJ off

### 2.0 The two runs are not configured identically — state this whenever you cite them

The default run used `--per-query-timeout=600s`; the cost-driven run used
`--per-query-timeout=300s` with an external 340 s hard cap (line 2 of each file). A cost-driven
query that would have finished in 350 s is recorded as a failure. **The asymmetry inflates the
cost-driven failure count, and every conclusion drawn from these files inherits it.**

### 2.1 Queries completing under BOTH planners (20 entries)

| | default (MHJ) | cost-driven (binary) |
| --- | --- | --- |
| **total** | **1034.04 s** | **1160.96 s** |

Cost-driven is **12.3 % slower** on the completing set.

A "22 % faster on the 15 non-MHJ-shaped queries" figure has circulated. It is arithmetically
correct on that subset and **it is not a valid summary**: the subset was formed by partitioning
the query set by outcome and then summing one side of the partition. §2.3 shows the partition
itself does not hold.

### 2.2 The wins and losses are both extremely concentrated

| query | default | cost-driven | delta |
| --- | ---: | ---: | ---: |
| Q8 | 181.36 | 44.33 | **−137.03** |
| Q2 | 51.23 | 2.72 | **−48.51** |
| Q3 | 9.14 | 5.58 | −3.56 |
| *(9 more)* | | | −1.75 total |
| *(5 more)* | | | +0.48 total |
| Q18 | 28.24 | 120.26 | **+92.02** |
| Q10 | 9.61 | 109.27 | **+99.66** |
| Q7 | 138.36 | 264.37 | **+126.01** |
| Q5 | 6.43 | HANG (>300 s, cancellation not honored) | — |
| Q9 | 25.69 | ERROR 57014 after 300.12 s | — |
| Q21 | 20.08 | HANG (>300 s, cancellation not honored) | — |

Two queries carry 97 % of the win (185.54 s of 190.72 s). Three carry 99.8 % of the loss
(317.69 s of 318.40 s). Three more fail outright; under the default planner those three cost
**52.20 s combined**.

### 2.3 The "MHJ-shaped ⇒ collapses" partition is falsified — in BOTH directions

This is the single most important fact in the evidence, and it took a third pass to get right.

Both design passes reasoned about "the MHJ-shaped set" from a stale source. One cited
`internal/executor/operators_explain.go:1562-1572`, which records the set as an **M0054-0002
baseline observation**, not a statement about HEAD. The synthesis pass therefore re-derived it
directly from the committed plan text
(transcript: [evidence/judge-verifications-20260731.txt](evidence/judge-verifications-20260731.txt) V1/V7):

```
$ awk '/^=== Q/{q=$2} /Multi-Way Hash Join/{print q}' plan_snapshots/m0125-0043-after.txt
Q2  Q3  Q7  Q9  Q10  Q11  Q11  Q18  Q21
```

So at the 2026-07-31 tree the MHJ-shaped set is **{Q2, Q3, Q7, Q9, Q10, Q11, Q18, Q21}** — and
**Q5 is not in it.** Cross-referencing §2.2:

| | MHJ-shaped | not MHJ-shaped |
| --- | --- | --- |
| **cost-driven faster** | Q2 (19×), Q3, Q11 | the quiet majority |
| **cost-driven much slower / fails** | Q7, Q9, Q10, Q18, Q21 | **Q5 — 6.43 s → HANG** |

**Q5 is the worst regression in the entire evidence set and it contains no MultiHashJoin at
all.** Its collapse therefore cannot be caused by MHJ removal, and no amount of runtime fusion
could have prevented it. Symmetrically, three MHJ-shaped queries get *faster* without MHJ.

The variable that moves these numbers is **join order quality per query**, not the presence of a
fused operator. That conclusion determines the verdict in [02](02-premise-audit.md) and the
staging in [09](09-staged-implementation-plan.md).

> **Caveat that must travel with this result.** The snapshot is from 2026-07-31; the A/B is from
> `cb37d166` on 2026-07-24. Plans may have shifted between them. Do **not** hard-code this query
> set — re-derive it by running `EXPLAIN` at the measurement HEAD, which is exactly the
> discipline Stage 0c imposes (finding F15).

### 2.4 What doc 15 actually concluded

`docs/design/cost-model/15-mhj-in-cost-driven-star-shapes.md`, status block:

- A DP-integrated MHJ was implemented, was **correct** ("result parity green on all 22, both
  planners"), and was **reverted**.
- "The MHJ never wins on cost for Q9's key subset" — at a 100× intermediate-materialisation
  penalty the qualifying subset still costs 416673 as MHJ vs 393420 as the cascade.
- "**The blocker underneath is the join ORDER, not the MHJ cost.**"
- Cost-driven Q9 there is **804 s** at ~117 µs/row against the integer planner's fused MHJ at
  **118 s** / ~20 µs/row.

The 804 s / 118 s pair comes from doc 15's own `EXPLAIN ANALYZE` session. It is **not** the same
measurement as the 300 s-capped `sf1-r5-costdriven` line (cancelled at 300.12 s) nor the 25.69 s
default line. Never mix the two families of number in one table.

### 2.5 The "per-row operator overhead" attribution is weaker than doc 15 states

Doc 15 attributes the 117 µs/row → 20 µs/row gap to *"per-row cascade overhead"* in the sense of
"one operator boundary instead of N". Two facts in the tree argue that this is at best
incomplete:

1. The binary `joinOp` probe hot path is already slot-based: it emits a shared
   `lazyVirtualOut` with no per-row row concatenation (`ensureLazyVirtual`,
   `operators_join_agg.go:1034`; the emit path at `:1160-1190` only rebinds `slot.row`).
2. The int64 zero-allocation hash fast path (`lazyIntHash`, commit `0aeb7613`) landed
   **2026-07-23** — *before* the 804 s measurement — and its own code comment names the cost it
   removed: *"the GC-heavy cost that made the binary hash cascade slow where MultiHashJoin's
   int64 keys are fast"*.

What remains, and what [02 §2](02-premise-audit.md) locates precisely, is **re-materialisation of
the probe input at the seam between two stacked joins** — a bandwidth-and-GC cost, not an
operator-dispatch cost. This distinction is not academic: it changes what Stage 0 must touch and
it is why Stage 0 might make the whole fusion project unnecessary.

## 3. The memory asymmetry is real and is invisible to the cost model

`internal/executor/datum.go:119` pins `Datum` at 48 bytes:
`const _ uintptr = 48 - unsafe.Sizeof(Datum{})`. A goopg hash-table entry is a `[]Datum` — 48
bytes per column plus a slice header, plus Go map overhead — where PG stores a packed
`MinimalTuple`. For *r* rows × *c* columns goopg holds roughly `48·r·c` bytes. Nothing in the
cost model knows this; see [07 §2](07-cost-model-interaction.md).

This is the mechanism behind the recorded HANG class ("memory-thrash, cancellation not
honored"). It is **not** a missing cancellation check — `nextLazy` checks `ctx.Ctx.Err()` on
every call, the build loops check every 4096 rows, and `drainRowsCtx` checks every 1000
(`operators_join_agg.go:3355`). It is the Go runtime at `GOMEMLIMIT` with `GOGC` effectively
off, which `CLAUDE.md` already documents as "sweep-tail collapse".

## 4. What the evidence does and does not establish

**Established:**

- MHJ-shaped and binary-cascade plans can differ by more than an order of magnitude **in both
  directions**, per query. The repository already records exactly this
  (`docs/design/0125-0002-...:188-198`) and adds the operative lesson: *the direction is not
  predictable from the code change, so it must be measured per commit.*
- Under cost-driven order, three TPC-H queries enter a memory-thrash state they do not leave
  within the (asymmetric, 300 s) cap.
- The fused operator's per-row cost is materially lower than the cascade's on Q9.

**Not established:**

- That the per-row gap is intrinsic to having N operators instead of one. §2.5 and
  [02 §2](02-premise-audit.md) locate a mechanical, removable cost that no measurement has yet
  separated from it.
- That runtime fusion would have recovered any of Q5/Q9/Q21. For Q5 it demonstrably could not
  (§2.3): there is no MHJ there to restore.
- That cost-driven order is a net win on TPC-H SF1. On the completing set it is a 12.3 % net
  loss, subject to the §2.0 timeout asymmetry.
- Any figure in this bundle as a *current* measurement. All of it is 2026-07-24 vintage against a
  2026-07-31 tree.
