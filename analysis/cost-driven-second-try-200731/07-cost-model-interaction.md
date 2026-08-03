# 07 — Interaction with the cost model

## 1. The change in the cost model's job

Today the cost model has two escapes when it mis-costs a wide fact-probe cascade: the
post-DP packer can rewrite the result into an MHJ (`rewriteMultiWayChain`), and doc 15's
abandoned v2 could have costed an MHJ candidate directly. After this proposal there is
exactly one plan shape — the binary cascade — so **any mis-costing of the cascade shows up
directly as a bad plan.** The cost model's job gets harder, not easier. That must be budgeted.

## 2. The missing term: build-side memory is ~6× PG's

`internal/executor/datum.go:119` fixes `Datum` at 48 bytes
(`const _ uintptr = 48 - unsafe.Sizeof(Datum{})`). A build-side hash entry is a `[]Row` bucket
of `[]Datum` — 48 bytes per column plus a 24-byte slice header per row plus Go map overhead —
where PG stores a packed `MinimalTuple` whose per-column cost is the datum's natural width.

PG's `final_cost_hashjoin` decides spill (batching) from `work_mem` against an estimate built
from `MinimalTuple` widths. Any goopg cost function ported from PG therefore under-estimates
goopg's real build footprint by roughly `6×` for narrow-column relations, and under-estimates
*when goopg will thrash* by the same factor. That is the mechanism behind the recorded
"memory-thrash, cancellation not honored" HANG class
([01 §3](01-motivation-and-measured-evidence.md)).

**Recommendation (narrow, deliberately):** introduce
`goopg_hash_entry_width_multiplier` (default `6.0`) and apply it **only where build-side
memory is judged** — the spill/batch decision and any memory-pressure penalty — and **not** to
the cost total.

The reason for that restriction is the single most important lesson in doc 15: pushing a
global penalty multiplier (`GOOPG_MAT_MULT=100`) to make one query's shape win "distorts all
22 queries' costs". A memory-realism correction applied to the memory decision is not a
thumb on the scale; a multiplier applied to the total is.

## 3. What Stage 0 changes in the cost model's inputs

If Stage 0 ([02 §4](02-premise-audit.md)) removes the per-level `VirtualSlot.Row()`
materialisation and the `lazyKeyRow` memcpy, then the cascade's true per-row cost drops and
becomes closer to width-independent. Any cost term that was tuned to compensate for that
overhead — implicitly or explicitly — becomes wrong in the other direction.

**Action:** before Stage 0 lands, grep the cost functions for any width-scaled per-row join
term and record its value; after Stage 0, re-measure. Do not tune anything in the same commit
as Stage 0; a performance fix and a cost-model retune in one commit is unbisectable.

## 4. INVARIANT — the cost model must never be told about fusion

> **The planner must cost the binary cascade as if fusion did not exist.**

Three reasons:

1. **It is the whole point.** The proposal's own benefit (2) is that fusion "no longer has to
   win on cost". Giving the DP a fusion discount re-creates exactly the coupling doc 15
   proved unworkable, with the added defect that the discount would be conditional on an
   executor predicate the planner cannot fully evaluate.
2. **The predicate is not knowable at plan time.** Q0 depends on `instrumentScope`
   (EXPLAIN ANALYZE), on the presence of `Gather` after parallel-degree selection, and on a
   GUC. A cost model that assumed fusion would systematically under-cost plans that then run
   unfused.
3. **Fail-closed becomes fail-expensive.** With a discount, every decline
   ([03 C14](03-semantic-contract.md)) turns a correctly-costed plan into an under-costed one.
   Without a discount, a decline is merely "no speedup" — the cost estimate stays honest.

Corollary: fusion may only ever make a plan **faster than its cost estimate**, never slower.
That is a safe direction. Any future proposal to cost fusion must first re-read doc 15 §status
in full.

## 5. The order problem is unchanged and remains the actual blocker

Doc 15: *"the DP correctly prefers joining the filtered `part` early (its green filter reduces
6M→322K, making later joins cheap) — which fractures the MHJ-eligible subset."*

Nothing in this proposal touches that. Two honest options, both out of scope here and both
recorded as Stage 3 candidates:

- **Fix the estimate, not the preference.** If joining filtered `part` early is genuinely
  cheaper *in PG's model*, PG would do it too — and PG runs Q9 in seconds. So the divergence
  is more likely in goopg's execution constants (the ones Stage 0 attacks) than in the DP's
  preference. Stage 0's measurement is the cheapest possible test of that hypothesis: if the
  cascade gets much faster and Q9's cost-driven time collapses without any planner change,
  the order was never wrong — the executor was.
- **Accept a shape preference.** Only if the above fails. Doc 15 already characterises this as
  "order surgery that changes every cost-driven query's plan", and the A/B in
  [01 §2](01-motivation-and-measured-evidence.md) shows five queries whose times swing by
  ±90 s or more from order alone. This is a large, separate project.

**The plan in [09](09-staged-implementation-plan.md) is written so that the first hypothesis
is tested first, at the lowest cost.**

## 6. What must NOT change in the cost model as part of this work

- No new penalty multipliers on join cost totals.
- No shape preference for left-deep-with-fact-outermost.
- No change to `mhjPackingEnabled`'s default before Stage 4.
- No change to `GOOPG_COST_DRIVEN_JOINORDER`'s default at any stage in this document set —
  the A/B says it is a net loss on TPC-H SF1 today, and this work does not by itself change
  that.

## 7. The corollary of §4 that must not be forgotten

The invariant in §4 — *never tell the cost model about fusion* — is correct, and it has a price
that the proposal does not state:

> **If fusion never has to win on cost, then the cost model never learns that the cascade is
> expensive.**

Cost-driven join order will keep choosing orders whose cascades are bad. Fusion will paper over
some of them (the ones that happen to come out as a fusable left-deep chain) and not others (the
bushy ones — see [02 §10](02-premise-audit.md), and note that Q5, the worst case in the whole
evidence set, is bushy *and* contains no MHJ). The result is a system whose performance depends
on an executor predicate the planner cannot see, which is a harder system to reason about than
one that is merely slow.

So §2's build-side memory realism is **required companion work, not an optional extra**. It is
the only mechanism in this design by which the planner can learn what the executor actually
costs. Without it, this bundle makes the plan shape honest and leaves the cost estimate dishonest
— and doc 15's order problem stays exactly where it is.
