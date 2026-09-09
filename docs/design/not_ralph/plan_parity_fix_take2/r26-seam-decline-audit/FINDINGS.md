# R26 — the seam-decline audit: 7 TPC-DS queries can never match PG

*2026-09-09. Acts on K27, which the Q30 fix produced: a query the
PG-shaped search DECLINES falls to the legacy path and cannot converge
on PG's plan by any amount of costing work. Nobody had counted them.*

## 1. Result

| corpus | seam declines | queries affected |
|---|---|---|
| **TPC-H** | **0** | **none** — all 19 problems admitted |
| **TPC-DS** | 13 | **7** (Q49, Q51, Q68, Q77, Q78, Q93, Q97) |

By reason, TPC-DS:

| reason | count | queries |
|---|---|---|
| `outer-link-no-sjinfo` | 7 | Q49 (3), Q78, Q93 |
| `outer-over-derived` | 3 | Q77 (2), Q78 |
| `outer-spine` | 2 | Q51, Q97 |
| `lateral` | 1 | Q68 |

## 2. What each result means

**TPC-H's zero is the more informative half.** Admission is *not* a
blocker there: every one of its 22 queries reaches the PG-shaped
search, so its 2/22 match rate is entirely costing and candidate
generation. No amount of seam work will help TPC-H, and that is now
established rather than assumed.

**TPC-DS's 7 are a different class from the other 92.** The other 92
are admitted and lose on cost — reachable, in principle, by cost work.
These 7 never enter the search at all, so they are unreachable by ANY
cost work. They are a hard floor on TPC-DS's match count: even a
perfect cost model leaves them at legacy-planner shapes.

That distinction matters for sequencing. `ROADMAP-to-all-match.md`
ranks work by how many queries a category blocks; this adds a second
axis — whether the query is even *eligible*.

## 3. The top reason is fail-closed, and correctly so

`outer-link-no-sjinfo` (7 of 13) fires when
`outerLinksHaveSJInfos(outerLinks, ctx.joinInfoList)` is false. The
guard's own comment states the stakes: without `SpecialJoinInfo`,
`join_is_legal` "knows nothing except what `ctx.joinInfoList` tells
it", so a pairing that reorders across an outer link "would emit an
INNER join where the statement wrote an outer one — unmatched rows
silently dropped."

So this is not a guard to relax. The round it implies is: **why is
`joinInfoList` unpopulated for these shapes?** The comment names the
production populator (`deconstructJointreeScopedSJI`, planner.go) and
says the seam deliberately does not assume it ran. On Q49/Q78/Q93 it
evidently did not, or produced a mismatched list.

## 4. Next round, scoped

1. Instrument `deconstructJointreeScopedSJI` on Q49 (3 declines, the
   densest single query) — does it run, and what does it produce?
2. Decide from that whether the gap is population, scoping, or a shape
   the populator does not model.
3. Only then touch the guard, and never by weakening it.

**Method note, from this session's record:** three of my diagnoses this
round were wrong until measured (costing, lateral CTE cache, CTE
re-materialisation), and the trace channel answered in one run each
time. Instrument first.

## 5. Not claimed

- That fixing all 13 declines yields matches. These queries would
  become *eligible*; they would still face join-order and the rest of
  the conjunction.
- That 7/99 is the dominant blocker. It is not — join-order blocks
  95/99. It is a hard floor, not the tall wall.
