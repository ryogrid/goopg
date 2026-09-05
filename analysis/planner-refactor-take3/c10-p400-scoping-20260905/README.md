# C-10 (P4-00) — pre-phase scoping for Phase 4

Date: 2026-09-05. Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
C-10, scoping for take3 `08-target-design.md` §7 (upper planner as paths).
Document-only. The four filed items are C-10a…C-10d in TODO_ALL.

C-10 asked for four areas to become scoped items before Phase 4 starts.
Three became items. **One is a negative result that removes a checkbox.**

## Summary

| area | verdict | item | must precede | size | risk |
|---|---|---|---|---|---|
| grouping sets | item | C-10a | C-11; cardinality half before C-15/C-17 | 60–150 LOC | medium |
| `remove_useless_joins` | **no Phase-4 item** — reclassified to Phase 3 | C-10b (P3-09) | after C-04, beside C-09 | 200–350 LOC | low-med |
| `reduce_outer_joins` | item, smaller than expected | C-10c | C-11; fixture before C-15 | doc + 80–150 test LOC | low (buys off high) |
| FROM-subquery pull-up | item, structural | C-10d | **C-11 and P4-01** | doc (port: 400–700 LOC) | judgement (port: high) |

## 1. Grouping sets — C-10a

goopg lowers `GROUPING SETS / ROLLUP / CUBE` in PG's *shape* but not its
*strategy*: one `Aggregate` node carries `GroupingSets [][]int`, and the
hashed strategy is enforced by four independent early returns rather than
chosen by cost (`groupagg_hashagg.go:64`, `groupagg_presorted.go:47`,
`groupagg_indexorder.go:68`, `parallel_agg.go:117`).

Three facts make this Phase-4 work rather than a Phase-4 detail:

1. **The cardinality is silently wrong.** `estimateAggregate`
   (`cardinality.go:1087`) does not read `a.GroupingSets`, so an N-set
   query is priced as one set — an under-estimate up to N×. PG sums
   `estimate_num_groups` over every rollup level into `dNumGroups`.
2. **C-15's cost function already exists with no production caller.**
   `aggCost` (`cost_funcs.go:568`) implements only the AGG_HASHED arm; there
   is no AGG_SORTED and no spill arm. `PathAgg` is a declared-but-unproduced
   `PathKind`.
3. **The four declines are the guard.** C-15's stated deliverable is to
   retire those three aggregate rules — but they are what keeps grouping
   sets on the only strategy the executor implements.

12 of 100 TPC-DS queries use grouping sets.

## 2. `remove_useless_joins` — C-10b, and it leaves Phase 4

**Negative result.** goopg has no analogue (verified: no
`remove_useless_join`, `join_is_removable`, `rel_is_distinct_for`, or
`innerrel_is_unique` anywhere in `internal/`). More usefully, it touches
**no Phase-4 item**: PG runs it in `query_planner` before path generation,
changing the *joinlist*, and none of C-11…C-18 reads or produces a
joinlist. The only coupling is the ordinary "search changed, upper rels
re-price".

So P4-00 can shed this checkbox. It belongs beside C-09
(`reduce_unique_semijoins`), which shares the unique-inner oracle.

Both halves of the primitive already exist and are decline-biased, which is
the safe direction for join removal: `uniqueKeyColumnSets`
(`joinkeyproof.go:56`) answers `rel_is_distinct_for` for base relations,
and `pathindexonlyneed.go`'s needed/output name sets answer "unused above"
while deliberately over-stating "needed" and returning `ok=false` on
anything unenumerable.

Corpus weight is low: 10/100 TPC-DS and 1/22 TPC-H queries use `LEFT JOIN`
at all, before the "inner columns unused above" condition applies.

## 3. `reduce_outer_joins` — C-10c, and the SJI half is already done

`reduceOuterJoins` is an AST-level prep pass with **one** production call
site (`planner.go:2716`), immediately before `deconstructJointreeScoped`,
running once per planning scope. Phase 4 does not move it.

The landed C-01/C-02 work already documents its dependence on this
ordering: `specialjoin.go:107-109` records that only ANTI arrives at
deconstruction, via this pass's LEFT→ANTI demotion, and
`outerjoin_delay.go:142-144` states that strictness does not exempt a qual
because demotion already ran. So the SJI half of the interaction is scoped.

**What is not scoped is the consumer side.**
`pushSingleSideQualsIntoInnerJoinInputs` (`inner_join_qual_pushdown.go:88`,
run at `planner.go:1435`) has explicit recursion arms for `*Aggregate`,
`*WindowAgg`, `*Sort` and `*Limit` — exactly the nodes C-11/C-15/C-16/C-18
delete — and it is the **sole** consumer of the `delayedAboveOJ` oracle.
Whatever replaces it must consult the same oracle, or an upper-target
narrowing will cross an outer-join link and evaluate NULL-extended rows as
base rows. That is a wrong-answer class with green row counts.

Two edges recorded: the pass is per-scope and name-based, so a parent
predicate can never demote an outer join inside a derived table (couples to
§4); and C-15's applying cut of `stampAggregateInputTarget`
(`group_input_target.go:268`, today always called with `above == nil`) is
where the guard would be dropped.

## 4. FROM-subquery pull-up — C-10d, the structural one

goopg has **no** `pull_up_subqueries`. Every "pull-up" in the optimizer
refers to `unnest.go`'s *sublink* pull-up, which is a different pass. A
derived table is planned as an opaque sub-problem
(`planSubqueryRangeVar`, `planner.go:4295` → recursive
`planSelectWithParent`) and admitted to the enclosing search as **one
prebuilt leaf**; there is no `SubqueryScan` node in goopg at all.

`relfromjoinlist.go:26-29` already states the two costs: the enclosing
search cannot pick a differently-sorted path for the sub-problem, and the
sub-problem is priced for "produce all rows" because it does not know which
side of the enclosing join it lands on.

**Why this is Phase-4-blocking rather than merely unfortunate:** PG's
`GROUP_AGG` / `ORDERED` / `FINAL` rels sit above the scan/join rel of the
*same* `PlannerInfo`. TPC-H **Q9 — P4-01's own witness** — puts its entire
six-way join tree inside a derived table, so goopg's scan/join rel is one
planning level down. C-11 must decide whether an upper rel may sit above a
foreign planning scope, or whether goopg grows a pull-up so the two
coincide. That decision determines the struct, which is why the item must
land before C-11 rather than after.

It also bounds C-12, C-13 and C-17: an outer `LIMIT` or a top-N sort cannot
push a bound into a derived table across this boundary.

Corpus: TPC-H 5/22 (q7, q8, q9, q13, q22), verified by reading. TPC-DS
~41/100 — **regex-derived and known to over-count** parenthesised join
groups; re-derive from the parsed AST before this number enters a design
doc.

## Uncertainties, stated

- Reclassifying `remove_useless_joins` out of P4-00 is a plan edit, not
  just an analysis result. The evidence for "touches no Phase-4 item" is
  the absence of any joinlist consumer in C-11…C-18.
- Whether the Q9 derived-table boundary *defeats* P4-01 or merely makes it
  harder is inferred from `planSubqueryRangeVar`'s structure plus
  `relfromjoinlist.go:26-29`, not stated in the design. C-10d's
  measurement half is what settles it, which is why it is scoped as
  measure-then-decide rather than as a port.
