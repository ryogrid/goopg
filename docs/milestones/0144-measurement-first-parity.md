# Milestone 0144 — Measurement-first parity: censuses, instrumented PG, route-order alignment

**Status:** in progress — 1 done (M0144-0001, canonical parallel TPC-H `match=1/22` measured on the post-reload epoch — reported for the §Goal floor re-pin), 10 open, 0 blocked/frozen in `.ralph/fix_plan.md` (2026-09-20); order per the fix_plan banner item 2
**Filed:** 2026-09-20 (owner directive)
**Source:** `../design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/03-forward-plan.md` — adopted wholesale, with the two owner overrides below
**Harness:** `AGENT.md` §"Plan-parity harness (M0137–M0144)"
**Prerequisites:** none.

## Why this milestone exists

The M0137–M0143 era repaired the harness and proved two things by measurement:

1. Election fidelity (`add_path`/`COSTS_EQUAL`) and cost-input timing were the
   largest plan-category movers.
2. Building PG-spec mechanisms one at a time across the corpus **does not
   convert categories into matches** — Parallel Append landed six verified
   slices and appears in `0/99` goopg plans; Incremental Sort reaches the
   executor 0 times; every absorption arm is byte-identical. The pattern is
   "admitted but never wins": the mechanism is correct but its *inputs* or its
   *route* diverge upstream.

The forward plan's response is to measure better before building more: rank
divergence *decision points* (not mechanism classes), price the residual, and
observe PG's own reasoning directly instead of ending every attribution at
"pricing".

## Owner decisions recorded in this filing

- **TPC-H parity is now measured in parallel mode** (2026-09-20). Both engines
  plan with `max_parallel_workers_per_gather` enabled; the headline score and
  floor are the parallel-mode numbers (see `AGENT.md` §Goal). Serial mode
  remains a diagnostic capture. `make plan-gate` was already parallel-mode by
  construction (`cmd/plan-snapshot` sets no serial GUC and `:65433` serves
  `max_parallel_workers_per_gather=4`), so this change aligns the headline
  score with what the gate has always compared.
- **Route-order alignment is prioritised** (2026-09-20). The owner's
  observation — fixes land but plans do not move because goopg's processing
  order diverges from PG's upstream of the fix — is already confirmed by the
  freshest data point: `M0142-0008-producer` landed `.SJInfo` production on
  the IN-unnesting paths and unit-verified it, yet corpus reachability stayed
  zero because every IN statement declines at `leaf-count`
  (`joinsearchseam.go:325`) before `semiAntiLinksHaveSJInfos` (:631) runs.
  M0144-0003 verifies this per item and files the alignment impl tasks.

## Scope

Phase A instruments, in banner order (`.ralph/fix_plan.md` M0144 section):

- **M0144-0001** — canonicalise parallel-mode TPC-H parity: flip
  `estimate-audit -serial` to default `false`, fresh parallel capture re-pins
  the floor.
- **M0144-0002** — first-divergence census: one mutually-exclusive
  `(parent, PG child, goopg child, depth)` record per divergent query over the
  committed captures; ranked table under `analysis/m0144/`.
- **M0144-0003** — route-order verification: per landed-but-inert item, cite
  PG's ordering and state the goopg route divergence; file one impl task per
  confirmed divergence.
- **M0144-0004/0005/0006** — instrumented PG 18.3 on a scratch checkout and a
  private `55xx` clone (R1 applies to the instrumented build's data sources):
  `OPTIMIZER_DEBUG` build; `debug_plan_candidates` trace-GUC patch;
  `-finstrument-functions` call-graph build.
- **M0144-0007** — cost-margin census: force PG's shape per divergence node,
  margin classes (<1% election / 1–20% input / >20% or unexpressible
  structural).
- **M0144-0008** — TPC-DS SF1 second capture (cadence start).
- **M0144-0009** — `plan-gate` reframing evaluation (fresh-clone-build diff vs
  live-binary pin; decides M0137-0022's scope).
- **M0144-0010** — ledger bulk-triage tooling (~2,100 open rows; the
  M0119-successor cadence).
- **M0144-0011** — first vertical-slice campaign, **gated on 0002+0007**: one
  representative query driven to MATCH or a named measured residue.

## Non-goals

- Vertical slices beyond the first are filed only after the census and margin
  tables exist — Phase B/C targeting belongs to the census output, not this
  milestone.
- No owner-decision reopening inside this milestone: frozen/deferred items
  keep their reopen conditions (`csq-R2`, R4, R7).
- The instrumented PG build is never a shared reference cluster and never
  touches `./postgres/` (read-only oracle).
- Design docs are written when each task is *selected* (D3), not up front.

## Definition of Done

- A ranked first-divergence table exists for TPC-H (parallel) and TPC-DS
  (SF0.25 + SF1), and every subsequently filed parity task can cite its
  cluster.
- The instrumented-PG pipeline answers "what did PG consider" end-to-end for
  at least one named query (candidate list + verdicts + winner), and the
  call-graph trace names PG's planner route for it.
- The route-diff table exists; each confirmed ordering divergence has an impl
  task; each non-divergence is recorded with its citation.
- The parallel-mode TPC-H headline is live: `estimate-audit` defaults to the
  parallel protocol and the floor is re-pinned from a fresh capture.
