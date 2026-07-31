# 0126-0007 — fusion enablement and measurement

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-31 |
| task | M0126-0007 — **CONDITIONAL on M0126-0006** |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage 2 (incl. trap F12), **08** R4/R10/R11 — read them first; this doc does not restate them |
| depends on | `0126-0006` |

## 1. Scope

No new code. Turn KS1 (`GOOPG_RUNTIME_JOIN_FUSION=1`) on **in the measurement
environment only** and run the six-item verification matrix. The honest outcome
"the incremental win over Stage 0 is small → leave the switch off permanently"
is a **legitimate completion** of this task, recorded as such — the plan-shape
benefit of the milestone does not depend on fusion being enabled.

## 2. The F12 ordering trap (read before running anything)

`tryFuseHashCascade`'s Q0 declines on any plan containing a
`*planner.MultiHashJoin`, and `mhjPackingEnabled` still defaults to `true`
until -0011 — so the queries that would benefit are exactly the ones that still
pack, and fusion declines on all of them. **Every measurement here must force
packing off via `SetMHJPackingEnabled(false)` (`bushy.go:582-587`) — never via
`GOOPG_COST_DRIVEN_JOINORDER=1`, which conflates join order into the A/B.**

## 3. Verification matrix (all with KS1 on)

1. **DIFF** across the whole executor + planner join corpus (ordered text).
2. **DS05** — zero row **and** checksum deltas ("57/99 content-verified,
   42/99 count-only" in the record).
3. **SPOT** — Q12=2 / Q13=35, fresh capped server.
4. **Low-`work_mem` run** (R4): force spill on ≥1 build side; identical results
   fused/unfused with **non-zero temp-file usage on both sides** (spill must
   survive fusion — contract C8).
5. **TPC-H SF1 A/B** on/off, matched server age →
   `analysis/cost-driven-second-try-200731/evidence/stage2-ab.txt`, plus the
   decline-reason histogram (R10).
6. **SMOKE** — no OLTP regression (R11: the predicate walk runs on every join
   build).

## 4. Gates

The matrix above **is** the gate set. Acceptance: zero correctness deltas
anywhere, and a measured win on the packing queries exceeding Stage 0's, or the
recorded "leave off permanently" verdict.

## 5. Stop / decision conditions

**Not-triggered close:** if -0006 was skipped, this task closes with a ledger
row citing the same -0005 decision — never silently.

KS1 flips off **immediately, without debate** on any DS05/SPOT/DIFF delta, any
new hang/OOM in a previously-completing query, or any pg-regress diff not
present before (bundle 10 §4). The failing artefact is preserved and a risk-
register row appended before any retry (10 §5).

## 6. Rollback

Flip KS1 — zero code change (bundle 10 §1 Stage 2 row).

## 7. What this doc deliberately does not decide

Whether fusion participates in parallel shared hash builds (declined by Q0 in
this cut; separate later design), and anything about join order.
