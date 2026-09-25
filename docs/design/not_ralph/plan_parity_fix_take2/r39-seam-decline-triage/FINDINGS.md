# R39 — the 8 remaining seam declines, triaged to their blockers

*Investigation round. No code change. Establishes what each remaining
decline actually needs, so nobody re-derives it.*

## Why declines matter more than category counts

A query the PG-shaped search DECLINES falls to the legacy planner and
**cannot converge on PG's plan by any amount of costing work** (K27,
the eligibility axis). These 5 queries are hard-blocked from `match`
regardless of everything else this workstream does.

## Census (fresh clone, HEAD = R36)

Unchanged by R36. `GOOPG_PGSHAPED_DP_TRACE=1`, all 99 TPC-DS queries:

| reason | count | relations |
|---|---|---|
| `outer-over-derived` | 3 | `web_returns, date_dim, web_page` |
| `outer-link-no-sjinfo` | 3 | same problem |
| `outer-spine` | 2 | `{web,store,catalog}_sales, date_dim` |

Five of the eight sit on ONE problem (`web_returns/date_dim/web_page`);
the other three are one shape repeated across the three sales channels.

## 1. `outer-over-derived` (3) — DELIBERATE, blocked on B-06

`relfromjoinlist.go:665`. Not a gap: a guarded refusal. Its own comment
records why — an outer join paired with a derived (CTE) input carries
no statistics, so every path prices at rows=1, and Q78's outer problem
once costed Nested Loop 3.07 against Hash 3.09 on that lie and ran
**15 s -> 327 s timeout**. Declining falls back to the syntactic tree,
whose legacy rewrites hash outer joins without a cost comparison.

Its stated resume is "lift when B-06 wires CTE-output stats".

**Verified: B-06 has NOT landed.** `cte_stats_synthesis.go` implements
step 2 part 1 and says so in its header — *"Registry + consumer wiring
ride in later slices; nothing here is called from production yet, so
this file cannot change a plan (inert by construction)."* No non-test
caller exists.

So this decline is correctly placed and must not be lifted first. Doing
so re-opens a measured 20x timeout.

## 2. `outer-spine` (2) — structural

`joinsearchseam.go:253`, raised when `splitOuterSpine` cannot peel the
pinned outer spine off the top. `planner.go:1515` already records the
shape as knowingly left on the legacy path.

## 3. `outer-link-no-sjinfo` (3) — structural

`joinsearchseam.go:477`: outer links in the node tree with no matching
`SpecialJoinInfo` in `ctx.joinInfoList`. This is the fail-closed guard
`outerLinksHaveSJInfos`. R27 fixed the Q49 instance of this class by
making the PLAN and `join_info_list` agree on join TYPE; these three are
a different instance and need their own diagnosis.

## Conclusion for planning purposes

None of the eight is a quick fix, and the largest group is correctly
refusing on purpose. The ordering that follows from the evidence:

1. **B-06 consumer wiring** (unblocks 3, and is a prerequisite the
   codebase already names). Its risk is measured and its guard exists.
2. **`outer-link-no-sjinfo` diagnosis** (3) — R27 supplies a worked
   precedent for the class.
3. **`outer-spine`** (2) — the deepest, and already knowingly deferred
   in two places.

## Cross-workstream note

This is the second blocker this session that terminates in another
in-flight workstream: K65 (column pruning) needs
`docs/design/not_ralph/minimize_datum/` for `DatumBytes`, and these
three declines need B-06. Neither is a defect in this workstream's
plan; both are real dependencies, and both should be stated as such
rather than re-investigated.
