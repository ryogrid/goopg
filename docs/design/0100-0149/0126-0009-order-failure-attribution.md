# 0126-0009 — order-failure attribution (diagnosis only, bounded)

| field | value |
| --- | --- |
| status | superseded |
| superseded by | [leftdeep-joins/](../leftdeep-joins/) — MHJ retired (M0127) |
| date | 2026-07-31 |
| task | M0126-0009 — **CONDITIONAL: M0126-0008 leaves ≥1 query failing a bar clause** |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `docs/design/cost-model/14-fk-aware-and-mcv-join-selectivity.md` (its §2-§5 thesis is measured and refuted), `15-mhj-in-cost-driven-star-shapes.md` (order is the blocker), bundle **01** §2.3 — read them first; this doc does not restate them |
| depends on | `0126-0008` (its per-query table is the input set) |

## 1. Scope

**No code changes.** For each query -0008 leaves failing: `EXPLAIN ANALYZE` on
both arms (goopg reports per-node actual time/rows), the per-operator time
split, and a verdict naming **exactly one** mechanism from the closed list:

| class | mechanism | routing |
|---|---|---|
| (a) | cardinality estimate wrong (cite cost-model 14 (`14-fk-aware-and-mcv-join-selectivity.md`) — its composite-unique no-fan-out fires correctly on Q9's FK joins and Q9 still timed out, so do not re-test that thesis) | held for -0010 |
| (b) | join-order preference (cite doc 15: the DP correctly prefers filtered `part` early, fracturing the streamable shape) | held for -0010 |
| (c) | **build-side memory not modelled** — the planner has no `work_mem` analogue at all (`cost_funcs.go:100-112` omits batching by its own comment). **Expected for Q5/Q9/Q21**: the HANGs are memory-thrash, not duration. | recorded as -0013 trigger evidence |
| (d) | executor per-row cost surviving Stage 0 | back to -0004/-0005 scope question |

Q5 note, fixed in advance: it contains **no** MultiHashJoin (verified,
`evidence/judge-verifications-20260731.txt` V1/V7) — its class is (a)/(b)/(c),
never a fusion question.

## 2. Budget (binding)

**One attribution pass per query, max 2 measured probes each.** A query that
resists attribution after that budget is written up as *unattributed*, gets a
`.ralph/deferral_ledger.md` row, and becomes a **no-go input** to -0012 — it is
not re-worked here.

## 3. Artefacts

One `analysis/cost-driven-second-try-200731/evidence/order-attribution-Q<N>.txt`
per query + a summary table (query → class → routing). These files are the
*only* deliverable.

## 4. Gates

Evidence artefacts committed; SPOT after any server the probes ran (hygiene).

## 5. Stop / decision conditions

**Not-triggered close:** if -0008 reports zero bar-clause failures, this task
closes with a `.ralph/deferral_ledger.md` row citing
`evidence/stage3-order-ab.txt` — never silently.

The budget in §2. Class-(c) verdicts do **not** trigger -0013 by themselves —
-0013's trigger is -0012's no-go; the class-(c) files are its evidence when it
fires.

## 6. Rollback

Nothing to roll back.

## 7. What this doc deliberately does not decide

Any fix. Fixes live in -0010 (classes a/b, bounded) and -0013 (class c,
remediation fork).
