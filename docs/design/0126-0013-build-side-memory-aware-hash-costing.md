# 0126-0013 — build-side memory-aware hash costing (conditional remediation) + bar re-check

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-31 |
| task | M0126-0013 — **CONDITIONAL: triggered only by an M0126-0012 no-go** |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` — the bar is defined there, not here |
| design of record | `analysis/cost-driven-second-try-200731/` **07** §2 (the recommendation + the 6× justification) and §7 (why it is required companion work) — read them first; this doc adds only the formula, the call sites, and the trigger machinery |
| depends on | `0126-0012` (the trigger), `0126-0009` (class-(c) attribution files are the evidence base) |
| filed by | USER directive 2026-07-31 |

## 1. Trigger — verbatim, binding

> **TRIGGER:** M0126-0012 recorded a **no-go** — at least one of: a
> HANG/OOM/timeout on any of the 22, total wall time above +20 % of R0, a single
> query worse than 2× R0, or a DS05 delta — on the cost-driven arm.
>
> If -0012's bar was met and this task never fires, close it as
> ***not-triggered*** with a `.ralph/deferral_ledger.md` row naming bundle
> **07 §7** as the outstanding argument (without memory realism the cost model
> never learns the cascade is expensive — an argument that survives a passing
> bar) and a successor owner. A silent skip is a bookkeeping defect.

## 2. The defect being remediated

The three HANGs (Q5/Q9/Q21) are not a duration problem: **the planner has no
`work_mem`/`hash_mem` analogue at all**, so it picks enormous build sides with
no penalty. `hashJoinCost` (`internal/planner/cost_funcs.go:100-112`) says so in
its own comment — "Batching/spill I/O is omitted (design ch. 06 §5)" — and
`costParams` (`:35-55`) carries no memory budget. At goopg's **48-byte** `Datum`
(`internal/executor/datum.go:119`) a build side is ~6× PG's `MinimalTuple`
bytes, so even a PG-faithful port would under-predict *when goopg thrashes* by
that factor (bundle 07 §2).

## 3. The change — PG transliteration map

Transliterate PG's hash-join memory model; cite these sites in code comments:

| goopg site | change | PG analogue |
|---|---|---|
| `internal/planner/cost_funcs.go:35-55` `costParams` | add `workMemBytes`, `hashMemMultiplier`, `hashEntryWidthMultiplier` | `work_mem`, `hash_mem_multiplier` |
| `internal/planner/cost_funcs.go:100-112` `hashJoinCost` | estimate `innerBytes = innerRows × tupleWidth × hashEntryWidthMultiplier`; derive `numBatches`; add spill-I/O + startup terms when `numBatches > 1` | `postgres/src/backend/optimizer/path/costsize.c:4134` `initial_cost_hashjoin`, `:4160` `final_cost_hashjoin` |
| new helper `chooseHashTableSize` (same file) | bucket/batch sizing from bytes vs budget | `postgres/src/backend/executor/nodeHash.c:658` `ExecChooseHashTableSize` |
| budget | `workMemBytes × hashMemMultiplier` | `postgres/src/backend/executor/nodeHash.c:3622` `get_hash_memory_limit` |
| width source | reuse `tupleWidth`/`typeWidth` (`internal/planner/relsize.go:334`, `:236`) — no new width machinery | PG uses `plan_width` (already packed) |
| callers | the cost-driven hash-join path generation in `internal/planner/pathgen.go`, incl. whatever -0011 decided about `generateMultiHashJoinPath:100-105` | — |

**The one non-negotiable placement rule (doc 15's `GOOPG_MAT_MULT` lesson):**
`hashEntryWidthMultiplier` (default **6.0**) applies **only** to the
memory/spill decision and any memory-pressure penalty — **never as a multiplier
on cost totals**. A global penalty was measured to distort all 22 queries'
costs and still lose the race it was tuned for.

## 4. Test matrix (proves the placement rule mechanically)

| test | asserts |
|---|---|
| `TestHashCostMultiplierMovesOnlySpill` | changing `hashEntryWidthMultiplier` changes the batch count / spill term, and leaves a **non-spilling** join's total cost bit-identical. If this test cannot be written, the term is in the wrong place. |
| `TestHashCostBudgetIsHashMemLimit` | the budget equals `workMemBytes × hashMemMultiplier` (the `get_hash_memory_limit` identity) |
| `TestHashCostDefaultConfigInert` | with cost-driven off, no plan in the snapshot corpus changes (`hashJoinCost` is unreachable from the integer planner) |

## 5. Commit split, gates

Max **two commits** (model + tests; calibration follow-up if needed), never in
the same commit as any executor change (bundle 07 §3: unbisectable).

Gates per commit: UNITS, SMOKE, SPOT, **PLAN — zero diffs in the DEFAULT
config** (a default-arm diff means the term leaked into the integer planner —
revert), cost-driven-arm plan diffs hand-reviewed and enumerated, DS05.

## 6. The re-measurement (part of this task, not a new task)

After landing: **re-run M0126-0012's §1 measurement protocol unchanged** (same
host, same quietness verification, same symmetric timeouts, per-query vs R0) →
`analysis/cost-driven-second-try-200731/evidence/acceptance-run-2.txt`, with a
**delta column against acceptance-run-1** so the memory term's effect is
attributable rather than inferred. Then re-judge the bar clause-by-clause:

- **Pass** → execute -0012's flip path (its §2 edit list applies verbatim).
- **Fail** → the milestone's **final no-go**: record the failing clause, the
  per-query deltas, and the successor owner. That is a successful completion of
  the milestone (its Goal §2).

## 7. Stop conditions

1. **One knob.** No sweep of the multiplier to make a query win; if 6.0 is
   wrong, the width model is wrong, and that is where the fix goes.
2. If after two commits the three HANGs are unchanged on the cost-driven arm,
   **stop** — do not iterate. Record the negative result with its measurements
   (the doc-15 precedent: a recorded implemented-and-reverted design is the most
   useful artefact class in the cost-model set) and write the final no-go.

## 8. Rollback

Revert the model commit(s); default config was provably inert throughout
(§4 test 3), so the revert is cost-driven-arm-only. Preserve both acceptance
runs regardless of outcome (bundle 10 §5).

## 9. What this doc deliberately does not decide

Statistics persistence (stats are per-connection; `estimate_rel_size` fallback
governs cold starts — cost-model roadmap deferral), and any order-preference
change (prohibited here; -0010's caps were the boundary and they are spent by
the time this task fires).
