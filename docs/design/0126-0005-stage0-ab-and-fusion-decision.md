# 0126-0005 — the Stage 0 A/B and the fusion go/no-go decision

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-31 |
| task | M0126-0005 — **decision task** |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage 0c + finding F15 — read it first; this doc does not restate it |
| depends on | `0126-0002` (re-baselined estimates), `0126-0003` (the changes being measured), `0126-0004` if triggered |

## 1. Scope

No code. Measure whether -0003(/-0004) closed the cascade-vs-MHJ per-row gap,
and write the **fusion go/no-go decision** that governs -0006/-0007. This is the
milestone's first decision point and the bundle calls a "skip" outcome the best
available one: no new operator, no new contract, no new bug class.

## 2. Protocol (runnable checklist)

1. Quiet host verified and recorded; server under
   `GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh`; never `pkill -f goopg`;
   reap orphaned psql clients (`timeout N psql` kills only the client).
2. **Derive the query set at the measurement HEAD** (F15): run `EXPLAIN` over
   the TPC-H set and take the queries that actually emit
   `Multi-Way Hash Join`. Do **not** reuse any historical list (the
   `operators_explain.go:1562-1572` set is an M0054-0002 baseline; the
   2026-07-31 snapshot set was `{Q2,Q3,Q7,Q9,Q10,Q11,Q18,Q21}` — re-derive).
3. A/B on the TPC-H SF1 bench (65433), matched server age / GOGC / GOMEMLIMIT:
   arm A = pre--0003 binary cascade, arm B = post--0003(+0004) — **both with
   `mhjPackingEnabled` forced off** (`SetMHJPackingEnabled`, `bushy.go:582-587`)
   so the cascade is what is measured; plus one MHJ-on reference arm.
4. Record to `analysis/cost-driven-second-try-200731/evidence/stage0-ab.txt`:
   per-query times, HEAD SHA, env, and the decision.
5. Never `-count=1` in any gate invocation.

## 3. Gates

SPOT per arm (fresh capped server); DS05 once at the measurement HEAD; the
evidence file committed.

## 4. Stop / decision conditions — the fork

- **Cascade within ~1.5× of the fused MHJ** on the derived packing set →
  **-0006 and -0007 are SKIPPED entirely** (each closed *not-triggered* with a
  ledger row); proceed to -0008.
- **A larger gap remains** → -0006/-0007 are IN, with the residual gap
  quantified in the written decision.

Either way the decision is written down in the evidence file; an unwritten
decision is an incomplete task.

## 5. Rollback

Nothing to roll back (no code). A surprising result is preserved, not re-run
until it agrees (bundle 10 §5 discipline).

## 6. What this doc deliberately does not decide

The fusion operator's design (bundle 03/04/05, task -0006) and anything about
join order (that is -0008's measurement).
