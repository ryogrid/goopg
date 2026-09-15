# M0142-0008a — scoping recon: SEMI/ANTI decorrelation vs. the DP search's joinrel machinery

**Status:** DONE (measurement only, no production diff). 2026-09-16.
**Filed by:** M0142-0008 (`docs/design/0100-0149/m0142-0008-forced-rewrites-vs-search-census.md`).
**Task:** `.ralph/fix_plan.md` M0142-0008a.

## Question

M0142-0008's recon named Q4's `EXISTS`-decorrelated semi-join as one of two
structural search-coverage gaps: `unnestExistsExpr` (`unnest.go:4110`) builds
the physical `Join{Semi/Anti}` node directly, and the search never assigns it
a `SpecialJoinInfo`/joinrel of its own — "no sjinfo -> no joinrel -> no path
-> no price" (the M0139-0005/F11/K63 lineage). M0139-0005 itself declined to
size wiring this properly ("materially larger task, out of this recon's
scope"). This task's job, per its own fix_plan line: (1) a per-query census of
which TPC-H/TPC-DS queries hit this exact bypass, (2) a concrete sizing (one
slice, or a decomposition like M0140-0006's), (3) the resume point for
whichever piece is smallest. Measurement/reading only — no code change.

## Correction to the M0142-0008 framing (found this loop)

The bypass is **narrower than "no search runs on these queries at all."**
Since S5a (`GOOPG_UNNEST_PREDP`, default ON — `unnest.go:39-45`), correlated
`EXISTS`/`NOT EXISTS` sublinks in a `WHERE` clause are pulled up **before**
`tryJoinSearch` runs, and `runJoinSearchBelowPinned` (`predp.go:72-201`) then
explicitly runs the DP search on the subtree **below** the pulled-up
semi/anti join(s) — access-method and join-order selection for the ordinary
tables in that subtree DOES go through `addPath`. What never happens: the
semi/anti join itself never becomes a joinrel the search can reorder relative
to its siblings, and its algorithm (`JoinAlgoHash` unless the zero-equijoin
fallback to `JoinAlgoNestedLoop`, both decided by `unnestExistsExpr`'s own
control flow, `unnest.go:4360-4367`) is never chosen by `addPath` cost
comparison against an NLI-semi alternative. `runJoinSearchBelowPinned`'s own
`descend` loop (`predp.go:87-106`) walks past every `*Join{Semi,Anti}` on the
spine purely to find the bottom `*Filter`/pre-existing chain to search below
— the spine joins themselves are collected only so their column references
can be re-resolved after the splice (`reresolveJoinByName`,
`layoutPosMap`/`remapByPosMap`), never so they can compete in `addPath`.

This matters for sizing: it means the "smaller" half of the problem (basic
table access-method/order selection under a correlated `EXISTS`) is **already
solved** by S5a. The remaining gap is specifically **semi/anti join
placement-and-algorithm selection**, which is where PG's own planner
complexity concentrates (see "PG oracle" below) — so the gap, while narrower
in surface area than first framed, is not narrower in implementation
difficulty.

A second, disqualifying case exists: when a `WHERE` clause mixes a correlated
`EXISTS` with a **scalar** subquery (e.g. `> (SELECT avg(...) ...)`),
`whereEligibleForPreDPUnnest` (`predp.go:34-43`) returns false for the whole
predicate (its check has no per-sublink granularity — one disqualifying
sibling blocks the entire WHERE clause's pre-DP treatment), so the query falls
through to the **legacy post-DP path** (`planner.go:1537-1558`, the
`else if f, ok := node.(*Filter)`, arm): `tryJoinSearch` runs first over the
*undecorrelated* CROSS chain, and `unnestSubqueriesInPlan` only unnests the
`EXISTS` afterward (`planner.go:1619-1620`), producing a plain **total**
bypass — the semi/anti join is spliced onto an already-finished plan with no
re-optimization opportunity at all, not even the "search runs below it" partial
win S5a gives the pre-DP-eligible case. TPC-H Q22 is this shape (see census).

## Method

Grepped both corpora for `\bexists\s*\(` and `\bin\s*\(\s*select` (case
insensitive), then read every match's surrounding WHERE clause by hand to
classify: correlated vs. non-correlated (per `canUnnestExistsExpr`'s /
`canUnnestInExpr`'s own definition — a subquery is correlated when its body
references a column from an enclosing query scope, not merely because it sits
inside a WHERE clause), and, for correlated EXISTS/NOT EXISTS, whether the
enclosing WHERE clause is pre-DP eligible (`whereEligibleForPreDPUnnest`) or
falls to the legacy post-DP path.

**Corpus:** TPC-H `q01..q22`
(`analysis/tpch/goopg-pg-tpch-plan-compare-260718/queries/`) and TPC-DS
`query1..query99` (`bench/tpcds/runtime_goopg/tpcds-data/queries/`, 99 files;
`query_0.sql` is a corpus variant bundling several `queryNN`-shaped statements
in one file — not a numbered TPC-DS query, included for completeness since it
was already grepped by M0142-0008b's census too).

## Findings — correlated EXISTS/NOT EXISTS (the pinned-spine population)

| query | shape | pre-DP eligible? | pinned joins |
|---|---|---|---|
| TPC-H Q4 | `EXISTS` (lineitem vs. orders, `l_orderkey=o_orderkey`) | yes | 1 (Semi) |
| TPC-H Q21 | `EXISTS` + `NOT EXISTS`, both correlated to `l1` (self-join lineitem) | yes | 2 stacked (Semi, Anti) |
| TPC-H Q22 | `NOT EXISTS` (orders vs. customer) **co-resident with a scalar `> (SELECT avg(...))`** in the same WHERE | **no — legacy post-DP total bypass** | 1 (Anti), zero search participation even for the subtree below it |
| TPC-DS query10 | 3-way `EXISTS`/`OR EXISTS`/`EXISTS`, all correlated to `c.c_customer_sk` (store/web/catalog channel checks) | yes | 3 stacked (Semi x3) |
| TPC-DS query35 | same 3-way channel-check shape as query10 | yes | 3 stacked (Semi x3) |
| TPC-DS query69 | same channel-check shape, `NOT EXISTS` variant | yes | 3 stacked (Anti x3) |
| TPC-DS query16 | `EXISTS` + `NOT EXISTS`, correlated to `cs1.cs_order_number` (catalog self-join + returns) | yes | 2 stacked (Semi, Anti) |
| TPC-DS query94 | `EXISTS` + `NOT EXISTS`, correlated to `ws1.ws_order_number` (web self-join + returns) | yes | 2 stacked (Semi, Anti) |
| TPC-DS `query_0.sql` | bundles the query10/16/35/69/94 shapes together | yes (per-statement) | (sum of the above) |

**8 distinct real TPC-DS/TPC-H queries carry a correlated EXISTS/NOT EXISTS**
(`query_0.sql` is a corpus artifact, not a 9th canonical query), **6 of them
stack 2-3 pinned semi/anti joins**, and one (Q22) hits the total-bypass legacy
path. This is not a niche gap — every TPC-DS "channel comparison" query family
(store/web/catalog cross-checks, a recurring TPC-DS idiom) goes through it.

## Findings — IN(subquery) census (secondary, mostly out of scope)

TPC-H `q16`/`q18`/`q20` and all 9 flagged TPC-DS `IN (SELECT ...)` matches
(`query14/23/33/45/56/58/60/95/query_0`) are **non-correlated** — every
subquery body is either a CTE reference (`cross_items`,
`frequent_ss_items`, `best_ss_customer`, `ws_wh`) or a self-contained
`SELECT` with no outer-column reference. These go through
`unnestNonCorrelatedInExpr`, a distinct mechanism from the pinned-spine path
this task studies (a non-correlated `IN` is a single materialize-once
semi-join build side, not a per-outer-row correlated probe) — **not a witness
for this gap**, not sized further here. Q20's *nested* `ps_availqty > (SELECT
0.5*sum(...) ...)` scalar subquery is correlated, but it is a scalar
subquery, not `EXISTS`/`IN` — out of this task's scope (M0142-0008's own
classification only named EXISTS/IN sublink decorrelation, not scalar
subquery decorrelation, as this gap).

## PG oracle citation — why this is genuinely hard, not just unwired

PG's `pull_up_sublinks` (`postgres/src/backend/optimizer/prep/prepjointree.c:468`)
does the same "convert correlated EXISTS/NOT EXISTS into a
JOIN_SEMI/JOIN_ANTI on the join tree" transform S5a's pinned-spine already
approximates — but PG's version builds a real `SpecialJoinInfo`
(`postgres/src/include/nodes/pathnodes.h:3027-3042`) for each one, populating
`min_lefthand`/`min_righthand`/`syn_lefthand`/`syn_righthand`. Those fields
are what let `join_is_legal` (`postgres/src/backend/optimizer/path/joinrels.c:350`)
answer, for every candidate joinrel pair the DP search considers, whether
forming that pair right now would violate the semi/anti join's placement
constraint — PG's join order for a SEMI/ANTI-bearing query is not free
commutation, it is commutation **subject to those constraints**, checked at
every `make_join_rel` step. Porting "wire the semi/anti join into the
search" therefore is not merely "let `addNLIPaths` see one more candidate" —
it requires:

1. A `SpecialJoinInfo`-equivalent built at (or before) `deconstructFromItemScoped`
   time for each correlated EXISTS/NOT EXISTS, replacing the current ad-hoc
   pinned-spine `*Join{Semi,Anti}` node construction in `unnestExistsExpr`.
2. `joinsearchlevel.go`'s level-by-level joinrel enumeration extended with a
   legality check mirroring `join_is_legal`'s SEMI/ANTI cases (PG's function
   is ~250 lines of special-case reasoning across all five join types) —
   today `runJoinSearchBelowPinned` sidesteps this entirely by never
   presenting the semi/anti join to the search as a joinrel at all.
3. Re-deriving or retiring `runJoinSearchBelowPinned`'s post-hoc
   splice-and-reresolve mechanism (`layoutPosMap`/`reresolveJoinByName`/
   `assertSpineConsumesIdentityBoundaryMap`) — a currently-working, carefully
   comment-documented piece of machinery landed specifically to give S5a's
   win without this wiring; replacing it risks regressing that win if the
   new path does not subsume every case S5a covers today (in particular the
   Q22-class total-bypass path, which S5a's own eligibility gate currently
   routes around entirely rather than fixing).
4. `addNLIPaths` reportedly already admits SEMI/ANTI nominally
   (`joinpathsnli.go:270,276`, per M0142-0008) — verifying it actually prices
   a real candidate once reachable, rather than assuming from the code read,
   is itself unverified and would need a live trace once (1)-(3) exist.

## Verdict

**M0139-0005's "materially larger task" characterization is confirmed, not
merely asserted.** This is not a same-shape sibling of S2b-1 (DISTINCT) or
S2b-4 (SETOP) — those wired an *existing* multi-candidate `Pathlist` through
one more translation seam. This gap requires building the legality-constraint
machinery PG's planner uses SpecialJoinInfo for, which goopg's DP search has
never needed before because every other join type it searches
(INNER/LEFT/RIGHT/FULL from explicit syntax) either has no placement
constraint or already gets one for free from `deconstructFromItemScoped`'s
existing outer-join handling. **Do not attempt in one sitting** (same K24
precedent S2b-2 already carries).

**Corpus payoff if solved:** 6 TPC-DS queries with stacked semi/anti joins
(query10/16/35/69/94, each currently forced into whatever join order/algorithm
`unnestExistsExpr`'s own construction order produces, never cost-compared)
plus TPC-H Q4/Q21 (already-known witnesses) plus Q22's total-bypass class —
among the largest single-mechanism corpus populations found by any M0141/
M0142 recon to date (S2b-2's own base-join/scan gap and this one are now the
two biggest-payoff, biggest-difficulty items in the backlog).

## Sizing / resume point — decomposition, not implementation

Filed as three further scoping/implementation sub-tasks in `.ralph/fix_plan.md`
under M0142-0008a (none started this loop):

- **M0142-0008a-1** — design-only: read PG's `join_is_legal`
  (`joinrels.c:350`) and `SpecialJoinInfo` construction in
  `pull_up_sublinks`/`deconstruct_jointree` in full, and produce a concrete
  goopg design (data structure + where it is built + how
  `joinsearchlevel.go` consults it) — the actual "further scoping pass" K24
  demands, since the PG mechanism is complex enough that reading code
  snippets (as this recon did) is not sufficient to size an implementation.
- **M0142-0008a-2** — implement the `SpecialJoinInfo`-equivalent construction
  for correlated `EXISTS`/`NOT EXISTS`, gated behind a rollback flag
  alongside `GOOPG_UNNEST_PREDP` (same operational-safety precedent), without
  yet changing `joinsearchlevel.go`'s enumeration — a landable, testable
  slice on its own (verifiable via unit tests on the constructed legality
  sets against hand-built fixtures, no plan-shape change expected yet).
- **M0142-0008a-3** — wire `joinsearchlevel.go`'s enumeration to consult the
  new legality sets and let `addPath`/`addNLIPaths` cost-compare semi/anti
  placement and algorithm; retire or bypass `runJoinSearchBelowPinned`'s
  splice-and-reresolve path for the now-natively-searched cases (keeping it
  for Q22's legacy-post-DP class until that is separately addressed). This
  is the slice that can actually move TPC-DS query10/16/35/69/94 and
  TPC-H Q4/Q21's plan shapes.

**Q22's total-bypass class is a separate, smaller, independently-schedulable
item**: relaxing `whereEligibleForPreDPUnnest`'s all-or-nothing disqualification
to per-sublink granularity (letting the correlated `EXISTS` still pre-DP-unnest
even when a *sibling* scalar subquery in the same WHERE clause is not
S5a-eligible) would upgrade Q22 from "no search at all" to at least the
partial win S5a already gives Q4/Q21/the TPC-DS witnesses, independent of
whether M0142-0008a-1..3 land. Not filed as its own numbered task this loop
(the recon's scope was census + sizing of the named gap, not discovering new
ones) — noted here as a resume-point hint if a future loop picks up S5a's own
eligibility gate.

## Floor measurements (mandatory for M0137-M0143 recon tasks)

No production code changed this loop (pure read/classification/grep census):
`git diff --stat -- internal/` empty. No plan-parity/`ea-ratchet` floor shift
is possible from a no-diff loop, so the mandatory TPC-H `match=8/22` / TPC-DS
`match=2/99` / `ea-ratchet` baseline pins are unchanged by construction (same
precedent as M0142-0007/0008/0008b/0013/0015).

## Follow-up

**M0142-0008a-1/-2/-3** filed in `.ralph/fix_plan.md` under this milestone.
No ledger row needed (this is a scoping recon, not a deferred implementation —
the sub-tasks themselves carry the resume points, following the M0140-0006
decomposition precedent, which also did not add a ledger row for the split).
