# M0141-S7-cd-q64-reclassify — Q64's gap is NOT covered by the tracked GroupAggregate family

Status: accepted (landed `18390e924`, 2026-09-18). Read-only recon, no production change; files the newly-named gap as **M0141-S2b-8** (`addGroupingPaths`'s SORTED arm has no Incremental-Sort-over-partial-prefix-seed offer).

Parent: M0141-S7 — see [m0141-s7-readjudicate-and-scope-incremental-sort.md](m0141-s7-readjudicate-and-scope-incremental-sort.md)

## Update 2026-09-18c — M0141-S7-cd-q64-reclassify: Q64's gap is NOT covered
by the tracked GroupAggregate family — new task filed

The prior update's (a)/(b)/(c) decision tree needed one fact:
does `electOrderedGrouping`'s CTE-materialization caller shape already cover
a CTE's own internal aggregate-strategy election, the same way
M0141-S2b-0/S2b-7 cover a top-level statement's? Answer, from reading (not
tracing — no server needed for this half): **the question as posed doesn't
apply, because `electOrderedGrouping` is structurally irrelevant to
`cross_sales`'s own planning pass, independent of caller wiring.**

`preplanWithClause` (`with.go:210`) does plan a CTE's body through the exact
same `planSelectWithSettings` function the outer statement uses (`body, err
:= planSelectWithSettings(cte.Query, cat, ps, scope)` — confirmed by
reading, `cte.Query` is `cross_sales`'s own `parser.SelectStmt`). But
`electOrderedGrouping`'s call site (`planner.go:1960`) sits inside the block
that only runs `if len(s.OrderBy) > 0` (`planner.go` ~1870-1965, keys built
from `s.OrderBy`) — and `cross_sales`'s own `SELECT` has **no `ORDER BY`
clause at all** (`bench/tpcds/runtime_goopg/tpcds-data/queries/query64.sql`:
the CTE body ends at `GROUP BY ... d3.d_year`, no `ORDER BY`; only the outer
statement referencing `cross_sales cs1, cross_sales cs2` has one). So
`electOrderedGrouping` never runs for the CTE body regardless of whether its
caller reaches it — there is no ORDER BY for it to adjudicate.

**The actual mechanism PG's `Presorted Key: item.i_item_sk` / `GroupAggregate`
node exercises is a different one: choosing a sorted-input GROUP BY strategy
with no ORDER BY in the query at all**, purely because the join order PG
picked already happens to deliver (or partially deliver, via Incremental
Sort) the group key's order more cheaply than a HashAgg. goopg's equivalent
decision point is `addGroupingPaths` (`groupingpaths.go:379`), specifically
its SORTED arm (`groupingpaths.go:441-495`, gated on `aggNode.GroupingSets ==
nil`): when the presorted-keys shortcut doesn't apply, it always calls
`sortPathForBounded(seed, pathkeysForSortKeys(keys), cp, -1)`
(`groupingpaths.go:474`) — and `sortPathForBounded`
(`joinpathsmerge.go:480-515`) unconditionally builds a `PathSort` (full
`Kind: PathSort`) over the seed. It never inspects `seed.Pathkeys` for a
partial prefix match against the group keys the way
`addIncrementalSortPaths` (the M0141-S7 third arm) does for the OUTER
ORDER BY case — there is no Incremental-Sort-over-partially-ordered-seed
candidate offered to `addGroupingPaths`'s SORTED arm at all, for ANY query,
CTE-internal or not.

This is genuinely **case (c)** from the prior update's decision tree: NOT
covered by M0141-S2b-0/S2b-7's scope (which is specifically about
`electOrderedGrouping`'s handling of a GROUP BY that also has an ORDER BY —
an entirely different call site and condition than plain `addGroupingPaths`
choosing between Hashed and Sorted for a GROUP BY with no ORDER BY at all).
Q64 does NOT become a 6th witness of the already-tracked GroupAggregate
family (that family's fix — `electOrderedGrouping` populating
`SearchCandidates` — has nothing to do with `addGroupingPaths`, a different
file, a different call site, reached only when there is no outer ORDER BY at
all to route through `electOrderedGrouping` in the first place).

**Corpus tally correction**: the producer-shape table above ("Nested Loop
x4: Q4, Q11, Q35, Q64") should read **Nested Loop x3 (Q4, Q11, Q35)** — Q64
is not an outer-ORDER-BY `Nested Loop` witness (Update 2026-09-18b) and is
now confirmed not a member of the 5-query GroupAggregate family either. It
is the sole known witness (so far) of a newly-named gap: **`addGroupingPaths`'s
SORTED arm has no Incremental-Sort-over-partial-prefix-seed offer.** Filed
as **M0141-S2b-8** in `fix_plan.md` (M0141 section), scoped as an
implementation task (not recon) since closing it requires an actual new
candidate-builder arm in `groupingpaths.go`, consistent with this
milestone's item-4 "cost diagnosis only, no executor work" restriction —
the task is filed, not worked, this loop.

`GOOPG_INCREMENTAL_SORT` stays default-off (no code changed this update —
read-only recon closing an open question, same posture as 18b). No ledger
row: this closes M0141-S7-cd-q64-reclassify's own open question with a
definite, code-read-confirmed answer and files its own follow-up task
directly, the same pattern 18b used for M0141-S7-cd-q64-reclassify itself.

Gates run: none needed (no code changed; `go build ./...` re-confirmed
clean as a courtesy, no test/gate re-run required for a docs+fix_plan-only
change). No TPC-H/TPC-DS dependency — no server started, no cluster touched.
