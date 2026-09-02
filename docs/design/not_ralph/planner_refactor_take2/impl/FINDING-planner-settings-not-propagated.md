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

## Proposed fix (not yet done)

Thread the statement's `PlannerSettings` to every `newResolveContext` call.
Mechanically the settings belong on the per-statement planning context rather
than being re-defaulted per resolve context; the 30 sites are the work. A
package-global is **not** acceptable here — the P2-A review already rejected one
for reading another session's GUCs.

Gate it by asserting the propagation directly: plan a subquery-wrapped statement
under a non-default `work_mem` and assert the resulting `costParams.workMem`,
rather than trusting a timing A/B.
