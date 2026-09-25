# What "all plans match PG" actually requires

*Written 2026-09-09 after slice 2b closed K23. This is a roadmap note,
not a round: it reframes the remaining work using the category data,
because the goal is ALL queries matching and that is a conjunction, not
a sequence of independent wins.*

## 1. Current, measured

| corpus | match | shape-diff | unparsed | missing-node | error |
|---|---|---|---|---|---|
| TPC-H (22) | 2 | 20 | 0 | 0 | 0 |
| TPC-DS (99) | 0 | 72 | 0 | 24 | 3 |

## 2. How many queries each divergence class blocks

| category | TPC-DS (of 99) | TPC-H (of 22) |
|---|---|---|
| **join-order** | **95** | **17** |
| parallelism | 89 | 16 |
| aggregation-strategy | 81 | 10 |
| sort-strategy | 79 | 13 |
| join-method | 72 | 12 |
| scan-type | 72 | 14 |
| parameterisation | 42 | 6 |
| rendering | 35 | 7 |
| qual-placement | 13 | 6 |

## 3. The structural fact: it is a CONJUNCTION

**No query has join-order as its only divergence. Zero.** Every
non-matching query differs from PG in **four to seven categories at
once**.

That has a consequence worth stating plainly, because it governs how
the remaining work should be planned and judged:

> **No single fix will flip any query to MATCH.** Each of the ~20
> TPC-H and ~96 TPC-DS non-matching queries needs *all* of its
> categories closed before its verdict changes.

This explains a pattern that has recurred all session and been reported
each time as "the match count did not move": R1 (qpqual currency), R3
(hashagg spill), R6 (window Sort), R21 slice 2b (K23) were each correct
and PG-faithful, and none moved the match count. That was not
disappointing — it was arithmetic. Progress in this phase shows up as
**categories falling**, and only converts to matches at the very end.

It also means a round's success criterion should be its category, not
the match count — which is how R19's and R21's success tests were in
fact written, and why they could be scored honestly.

## 4. The ranking that follows

`join-order` is the largest single blocker on both corpora (95/99,
17/22), and nothing landed this session touches it. It is the join
search reproducing PG's `join_search_one_level` choices, which is a
different and much larger problem than anything attempted so far.

Ordered by queries unblocked:

1. **join-order** — 95 / 17. Untouched. The dominant item.
2. **parallelism** — 89 / 16. The `GOOPG_GATHER_PATHS` flip is the
   lever, now unblocked (K23 closed). Expect a partial reduction: K14's
   heap-density effect drives worker-count differences no planner
   change can fix.
3. **aggregation-strategy** — 81 / 10. Slice 3 (K12 B, the ordering
   contest) is the lever; goopg emits `GroupAggregate` 13 times against
   PG's 100.
4. **sort-strategy** — 79 / 13. Likely follows 3, since the sorted
   aggregate and the orderings it delivers are the same mechanism.
5. **join-method / scan-type** — 72 / 12, 72 / 14. Partly cost
   (root-causes §3's currency work, R1) and partly candidate
   generation.
6. **rendering** (35 / 7) and **qual-placement** (13 / 6) — smallest,
   and rendering is partly EXPLAIN faithfulness, which R2 and R7 showed
   is cheap to fix when it is a label.

## 5. Honest scope

Getting to "all plans match" requires closing categories 1–6 on both
corpora. Item 1 alone is a larger project than everything this session
landed. Items 2 and 3 have their levers identified and unblocked. The
two on-disk items (K14: `character(N)` padding, heap page fill) sit
underneath **parallelism** and **scan-type** and cannot be closed by
planner work at all.

Nothing here says the goal is unreachable. It says the remaining work
is a conjunction of six category-level programs plus two storage items,
and that the match count will stay near zero until they are nearly all
done — so it must not be used as the progress signal in the meantime.
