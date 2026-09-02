# FINDING — planner settings stop at the top of the plan; the hardwired default is load-bearing

**Status:** open. Blocks P2-02b. Discovered 2026-09-03 while attempting P2-02b.

## Claim

`PlanWithSettings` accepts a `PlannerSettings` and stamps it at **exactly one**
site (`internal/optimizer/planner.go:1153`, `ctx.settings = plannerSet`).
`newResolveContext` (planner.go:525) initialises `settings:
DefaultPlannerSettings()`, and there are **30** call sites. Every context that
is not the one stamped site therefore plans under the hardwired defaults —
including the contexts built for nested subqueries.

The consequence is not a cosmetic one. For any query whose work lives inside a
subquery, the join search runs under `DefaultPlannerSettings()` regardless of
what the session, or `postgresql.conf`, says. **The hardwired default is
load-bearing for real query planning**, not a fallback.

## Evidence

TPC-H Q9 is `select ... from ( select ... 6-way join ... ) profit group by ...`
— its entire join tree is inside the subquery. Measured on the SF=1 bench
cluster (`work_mem = 64MB` in `postgresql.conf`), one variable changed, fresh
server per arm:

| `hashsize.DefaultMemLimitBytes` | Q9 | plan |
|---|---|---|
| `512 << 20` (current) | 15.5 – 16.7 s | `Gather` over 4 workers, hash joins over seq scans |
| `4 << 20` (PG's `work_mem`) | 69.3 – 72.0 s | no `Gather`, index-nested joins |

At the moment of planning, the settings were verified correct and were ignored:

```
DBG SETTINGS WorkMem=67108864 HMM=2 getSetting(work_mem)="65536"/true
DBG PLAN   ... no Gather, index-nested shape ...
```

Ruled out along the way, each by direct measurement rather than reading:

- **Executor budget.** `ectx.WorkMem = 67108864`, `sess` non-nil. A one-shot
  `runtime.Stack` dump inside `hashsize.EffectiveMemLimit`'s `workMem <= 0`
  branch **never fired** during Q9 — the executor fallback is not reached.
- **Plan cache.** Disabling the cache entirely left Q9 at 71.3 s.
- **Session budget.** `SET work_mem='512MB'` left Q9 at 76 s.
- **`ctxPlannerSettings`.** Verified live to return `WorkMem=67108864` from the
  conf-file value.

So the constant is consumed *directly* by `defaultCostParams()`
(`cost_funcs.go:77,100`) on a planning path the session's settings never reach.

## Why this was invisible until now

The default was 512 MB — 128x PG's `work_mem` — so every unstamped context
happened to plan under a generous budget that produced good TPC-H plans. P2-03
(`hash_mem_multiplier`) doubled it again to 1 GB, which is a large part of why
that commit moved the corpus 37 %: it widened the budget on the path that
actually plans these queries. Lowering the default to PG's 4 MB removes the
accident and the corpus collapses.

This also means the P2-03 result should be re-read: it is partly a *default*
change, not only a session-GUC change.

## Consequences

1. **P2-02b cannot land** until settings propagate. Correcting the `work_mem`
   BootVal to PG's 4 MB is a PG-compat requirement, but on its own it is a
   large, silent performance regression (TPC-H 245.7 s -> 314.4 s measured, Q9
   +434 %, Q7 +109 %, Q2 +82 %).
2. **P2-02 is only half-landed.** Its gates passed because the sites it
   converted are real and its live probe used a single-level statement. A
   subquery probe would have failed it.
3. `plannerCostGUCsOverridden` uses `HasSessionOverride`, which sees only an
   explicit `SET` and is blind to `postgresql.conf`. That is *safe* for the plan
   cache (conf values are uniform across sessions, so sharing is correct), so it
   is deliberately left alone — but it means the predicate's name overstates it.

## Progress

**Landed (f93ea20dd):** settings are an explicit parameter of
`newResolveContext`, threaded through the FROM-clause path (`planFromClause`,
`planFromRangeVars`, `planFromItem`, `planScanRangeVar`) and inherited by the
aggregate and window stages from their input scope. Gated by
`TestPlannerSettingsReachSubqueryScan`. TPC-H 242.91 s -> 258.28 s (+6.3 %),
24 MATCH on values. The slowdown is the correction itself: the bench now plans
at the 64MB its conf specifies instead of an accidental 1 GB.

**Still open — the derived-table path.** `planSelectWithParent` (planner.go
~13813) calls `planSelect`, i.e. the defaulting wrapper, so a `(SELECT ...) AS
alias` FROM item still plans under the defaults. That is Q9's subquery, which is
why P2-02b remains blocked.

**Second attempt (2026-09-03) — reverted, not merely slower.** Threading
`planSelectWithParent` / set-operation operands / scalar-subquery sites by a
mechanical compiler-driven pass, together with P2-02b, produced Q9 = 262 s. That
is NON-MONOTONIC: the same query is 15 s at a 1 GB budget and 70 s at 8 MB, so
128 MB cannot legitimately land at 262 s. The pass therefore introduced a bug —
most likely an argument threaded from the wrong scope by the regex-driven edit —
rather than merely tightening a budget. It was reverted rather than debugged
under time pressure.

The lesson for the next attempt: this path must be threaded by hand, one caller
at a time, each with its own assertion of which scope's settings it should
inherit. `psFromParent`-style inheritance in particular needs review — the
P2-A hazard (resolveContext.parent is assigned from the package-global
planParent) applies to some of these callers and not others, and the mechanical
pass did not distinguish them.

## Proposed fix (not yet done)

Thread the statement's `PlannerSettings` to every `newResolveContext` call.
Mechanically the settings belong on the per-statement planning context rather
than being re-defaulted per resolve context; the 30 sites are the work. A
package-global is **not** acceptable here — the P2-A review already rejected one
for reading another session's GUCs.

Gate it by asserting the propagation directly: plan a subquery-wrapped statement
under a non-default `work_mem` and assert the resulting `costParams.workMem`,
rather than trusting a timing A/B.

---

# ROOT CAUSE (2026-09-03) — it is the missing projection, not the plumbing

The derived-table slice was threaded again, **by hand**, one variable at a time.
It builds, passes the unit suites, and is still not shippable: TPC-H Q9 goes
15.4 s -> 187 s. A memory-GUCs-only variant (session `work_mem` /
`hash_mem_multiplier`, every other field left at the defaults) gives 192 s, so
`work_mem` alone accounts for it. The earlier "non-monotonic" reading was an
artefact of comparing arms whose derived tables had *different* propagation
states; with propagation held on, the relationship is monotonic —
1 GB -> 15 s, 128 MB -> ~190 s.

So the question is why goopg needs a gigabyte where PostgreSQL needs tens of
megabytes. `EXPLAIN ANALYZE` on the same query, same cluster, same 64MB
`work_mem` x 2 answers it:

| | PostgreSQL 18.3 | goopg |
|---|---|---|
| tuple widths through the join tree | 23 / 32 / 54 / 81 B | 1542 / 2616 / 3164 B |
| peak hash memory | 38 MB, `Batches: 1` everywhere | 97 MB, `Batches: 8` |
| rows through the middle join | ~319 k | 24,005,020 |
| Q9 total | 6.2 s | 187 s |

goopg's tuples are roughly **39x wider** than PostgreSQL's at the same point in
the same plan, because there is no `PathTarget` and therefore no projection: the
join tree carries every column of every base relation from the leaves to the
top. A hash table over those tuples needs ~39x the memory for the same rows, so
at PostgreSQL's configured budget goopg batches where PostgreSQL does not, and
its multi-batch path is slow enough to cost two orders of magnitude.

## What this explains

- **Why the 512MB default was load-bearing.** It is 8x PG's `work_mem`, which
  is roughly what a 39x-wide tuple needs to stay single-batch on this corpus.
- **Why P2-03 won 37 %.** Doubling the budget via `hash_mem_multiplier` bought
  headroom against the width, rather than fixing a mis-costing.
- **Why P4-01 was attractive and why it produced wrong answers.** Narrowing the
  leaf schema attacks exactly this, and is the right idea; the reverted attempt
  failed on the mechanism (it disabled the join search's seam offsets), not on
  the diagnosis.
- **Why P2-02b cannot land.** Correcting `work_mem` to PG's 4MB removes the
  headroom the width depends on.

## Consequence for the plan

P2-02b is **not blocked on settings propagation** — that was the intermediate
diagnosis, and it is wrong. It is blocked on **P4-01: a real `PathTarget` with
projection**. The remaining propagation work (derived tables, set operations,
scalar subqueries) is correct and should land, but it must land *after* the
width is fixed, or each slice pays the same regression.

Recommended order, replacing the bundle's:

1. **P4-01 proper** — `PathTarget` + `setrefs`-style projection, so a join
   carries only the columns above it need. Gate on `tpch-runner -digest`
   (values, not row counts — the reverted attempt matched 21/24 and Q18's row
   count while returning wrong tuples).
2. Then the derived-table / set-op / scalar-subquery propagation slices.
3. Then P2-02b, which should be close to free once 1 and 2 are in.

The hand-threaded derived-table patch is not committed; it is a two-line change
(`planSelectWithParent` takes the settings and calls `planSelectWithSettings`,
`planSubqueryRangeVar` forwards them) and is trivially reproducible when step 1
lands.
