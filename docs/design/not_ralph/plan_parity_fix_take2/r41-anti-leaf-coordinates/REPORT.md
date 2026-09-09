# R41 — leaf numbering re-synchronised with binding order (K74/K75)

*2026-09-09. Landed on the second attempt, after R42 removed the blocker
that forced the first one to be reverted. All gates green.*

## 1. Result

| | before | after |
|---|---|---|
| `leaf-count` declines | 3 | **0** |
| TPC-DS declines, total | 8 | **5** |
| Q78 plan SHAPE | `Hash Anti Join` + `Filter: (d_year = 1998)` | **unchanged** |
| Q78 runtime | 14 s | 14 s |
| TPC-DS match | 0/99 | 0/99 |
| TPC-H match | 1/22 | 1/22 |

**What this round bought is eligibility, and nothing else.** Three queries
that the PG-shaped search previously DECLINED are now admitted. Their plans
come out shape-identical to what the legacy fallback produced — only the
costs differ, because the search now prices them instead of the rule-based
path. Per K27 a declined query cannot converge on PG's plan at any cost
setting, so this removes a hard block rather than making progress visible
in the match count.

The match count did not move, exactly as `DESIGN.md` §6 predicted before
implementation.

## 2. The first attempt, and why it was reverted

R41's implementation was written, measured and **reverted** once already.
Landing it alone made Q78's three `date_dim` scans lose
`Filter: (d_year = 1998)` (rows 149 → 73049) and the query ran 14 s → 26 s.
PG *does* carry that restriction, so that was a **qual-placement parity
regression**, not merely a timing one — the goal's "ignore execution-time
degradation" clause covers PG-identical plans, and this was not one.

The cause (K76) was not in R41 at all: the qual-pushdown descent could not
cross the `Project` that the PG-shaped search's boundary introduces, so
admitting a body to the search lost a restriction the legacy tree kept.
R42 fixed that. With R42 in, the same R41 code now keeps the filter:

```
->  Hash Anti Join   (x3, one per CTE)
->  Seq Scan on date_dim   rows=362   Filter: (d_year = 1998)
```

and Q78 is back to 14 s. The revert-then-reland order was the right call:
landing R41 first would have shipped a measured parity regression to buy an
eligibility gain.

## 3. What landed

Per `DESIGN.md` §4 — a **renumbering**, not a coordinate remap:

1. `antiCollapsedJoins` (`collapse.go`): the number of LEADING SEMI/ANTI
   links whose right side gets no leaf index. One helper, read by all three
   consumers, because they must agree leaf for leaf.
2. `deconstructFromItemScoped` skips those links entirely — no leaf, no
   joinlist item, and **no `SpecialJoinInfo`** (its nullable hand would name
   a relation with no bit; the plan side raises no `outerChainLink` for the
   link either, so the two descriptions still agree).
3. `fromItemRels` subtracts them, keeping numbering in step across comma
   FROM items.
4. `newSjiScope` skips exactly the same sides, or every SJI hand to the
   right of a collapsed link would name the wrong relation.
5. A `prefix-exceeds-bindings` fail-closed guard at the seam (K75).

**Only LEADING links collapse.** The opaque plan leaf stands for the whole
subtree under the SEMI/ANTI node; when the link is first in the chain that
subtree is exactly the base range variable, so one leaf index names it. A
link at position k>0 would collapse k+1 relations and renumber everything
to its left — a much larger change. Those keep today's pinned path and
today's decline: fail-closed, never a wrong answer.

## 4. K75 — the decline was preventing a panic

With `len(ctx.bindings)==2` and `nprefix==3`, `ctx.bindings[:nprefix]` is
index-out-of-range. The `leaf-count` check was the only thing that happened
to prevent it. The two numbers come from *separate* computations —
`demotedForPlan` for the plan tree, the in-place `reduceOuterJoins` for the
joinlist — so a future divergence about which links became SEMI/ANTI would
desynchronise them again with nothing left to catch it. Hence the new
guard, which turns that into a decline. It is deliberately `nprefix > nrels`
and **not** an equality: a peeled outer spine legitimately leaves the prefix
narrower, which is the normal admitted case.

## 5. Gates

| gate | result |
|---|---|
| optimizer + executor suites | green |
| **TPC-DS SF0.5 sweep** | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0** |
| Q78 checksum | `ck=8f67acff3895183f` — unchanged throughout |
| TPC-DS plan shape | 1 changed (Q78), cost-only; shape identical |
| TPC-DS total runtime | −0.7 %, no query moved ≥ 2× |
| TPC-H values digest, 22 queries | byte-identical |
| TPC-H plan STRUCTURE, 22 queries | identical |
| decline census | `leaf-count` 3 → 0, total 8 → 5, **no new class** |

The census is read **by class, not by total** — R40's lesson was that a
class can be *converted* rather than removed, which is exactly what
happened when R40 turned `outer-link-no-sjinfo` into `leaf-count`. This
time no new class appeared.

## 6. What remains for Q78

Q78 still does not match, and the reason is structural: PG places
`date_dim` **below** the anti join (`Nested Loop Anti Join` over
`Parallel Hash Join`), because its DP searches 3 leaves and can reorder
into the anti pair. goopg's collapsed 2-leaf problem can only build
`(sales anti returns) ⋈ date_dim`. Closing that is `DESIGN.md` §6's
**Option B** — teach the plan walk to descend into ANTI — which needs an
ANTI path producer (`pinnedUnsearchable` refuses ANTI today) plus an
"unpublished leaf" concept in the coordinate model. Strictly larger than
this round, and the PG-faithful end state.

Remaining declines: `outer-over-derived` 3 (K68, blocked on the separate
B-06 CTE-stats workstream — lifting it re-opens a measured 20× timeout) and
`outer-spine` 2 (K70).
