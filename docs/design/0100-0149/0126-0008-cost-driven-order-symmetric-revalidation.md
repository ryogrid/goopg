# 0126-0008 — cost-driven order re-validation with symmetric timeouts

| field | value |
| --- | --- |
| status | superseded |
| superseded by | [leftdeep-joins/](../leftdeep-joins/) — MHJ retired (M0127) |
| date | 2026-07-31 |
| task | M0126-0008 |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage 3, **07** §5, **01** §2.0 (the timeout asymmetry) — read them first; this doc does not restate them |
| depends on | `0126-0003` (+`0126-0004` if triggered), `0126-0005`; `0126-0007` if the fusion fork was entered |

## 1. Scope

No planner code. Re-measure `GOOPG_COST_DRIVEN_JOINORDER=1` vs default at
post-Stage-0 HEAD with the **same timeout on both arms** — the 2026-07-24
evidence pair used 600 s vs 300 s and is not a valid comparison — and produce a
**decision document with a per-query table** against the pinned R0 baseline.
Bundle Stage 3 called this an external blocker; this milestone owns it, because
the terminal flip depends on its answer.

This task tests 07 §5's hypothesis at the lowest possible cost: **if Q9's
cost-driven time collapses with no planner change, the order was never wrong —
the executor was.**

## 2. Protocol

- Both arms: identical per-query timeout (600 s), identical host, matched fresh
  servers and warm-up (KS1-style A/B needs two server starts — match ages),
  cgroup cap via `scripts/goopg-test-run.sh`, orphan reaping, quiet host
  recorded. Never `-count=1`.
- Output table schema: `query | R0 s | integer s | cost-driven s | ratio |
  bar verdict (clause 1/2/3)` — a **clause-by-clause** verdict per query, not a
  narrative.
- Record to `analysis/cost-driven-second-try-200731/evidence/stage3-order-ab.txt`.
- Known prior failure set to watch: Q5/Q21 HANG, Q9 timeout, Q7/Q10/Q18 2–11×.
  **Q5 contains no MultiHashJoin** (verified) — if it still fails, it fails on
  order/cardinality, and no fusion result changes that.

## 3. Gates

SPOT per arm; evidence file committed. DS05 once at the measurement HEAD.

## 4. Stop / decision conditions

- **Zero queries fail the bar clauses** → -0009 and -0010 are skipped (each
  closed *not-triggered* with a ledger row); proceed to -0011.
- **Failures remain** → the per-query table is the input set for -0009.

Nothing downstream may assume cost-driven order is a net win until this file
says so.

## 5. Rollback

Nothing to roll back. A surprising result is preserved, not re-run until it
agrees.

## 6. What this doc deliberately does not decide

Why a query fails (that is -0009's attribution) and what to do about it
(-0010/-0013).
