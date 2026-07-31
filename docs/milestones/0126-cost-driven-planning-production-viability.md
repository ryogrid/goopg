# Milestone 0126 — Cost-driven planning made production-viable

**Status:** planned
**Filed:** 2026-07-31
**Reference plan:** `.ralph/fix_plan.md` (M0126 section)
**Design of record:** `analysis/cost-driven-second-try-200731/` — README (verdict),
**09** (stages + the UNITS/SMOKE/SPOT/PLAN/DS05/DIFF gate vocabulary), **10**
(kill switches + rollback), **07** (cost-model interaction). The per-task docs
under `docs/design/0126-*` are thin implementation specs; the bundle chapters are
the design. Do not re-derive what they settle.
**Prerequisites:** none outside the milestone. M0124/M0125 precede it by
priority, not by dependency.
**Branch:** `tpcds-fix2` (every implementation task runs in a git worktree off a
pinned clean HEAD, staged by explicit pathspec, and **re-runs its own named
guard test after any rebase or handoff** — bundle 10 §6; a Ralph loop edits
`internal/planner/` and `internal/executor/` continuously)

## Goal

End state, one of exactly two:

1. **Cost-driven join order is the default** — `GOOPG_COST_DRIVEN_JOINORDER`'s
   semantics are on without the env var — because the acceptance bar below was
   measured and met; or
2. **A documented no-go**: the bar was measured, missed, remediated once
   (M0126-0013), re-measured, and still missed — with the failing clause, the
   residual queries, their attributions, and a named successor recorded.

Both outcomes are successful completions of the milestone. **An unmeasured or
partially-measured outcome is the only failure mode.**

The milestone converts the analysis bundle `analysis/cost-driven-second-try-200731/`
into shipped behaviour. The bundle's stop conditions are binding, not advisory.

## The acceptance bar

This is the bar's **normative home**. Restatements elsewhere (the fix_plan
M0126 section, the milestones-README row, -0013's trigger) are convenience
copies and must be updated FROM here whenever this section changes.

> **M0126 acceptance bar.** Measured at final HEAD against the pinned `m0126-base`
> R0 baseline (captured once, by M0126-0001, before anything landed), on a
> verified quiet host, matched server age / GOGC / GOMEMLIMIT, symmetric
> timeouts on both arms:
>
> 1. TPC-H SF1: **all 22 queries complete** — zero hang, zero OOM, zero timeout.
> 2. Total wall time **within +20 %** of the FASTER of (a) R0's integer-planner
>    total and (b) a contemporaneous integer-planner arm measured at final HEAD
>    in the same session — a stale R0 alone could accept a flip that regresses
>    against the integer planner as it stands at flip time.
> 3. **No single query regresses more than 2×** against the faster of its R0
>    and final-HEAD-integer-arm times.
> 4. TPC-DS SF0.5 gate: **zero row-count deltas and zero checksum deltas**
>    against `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`.
>
> A green DS05 is recorded as "57/99 content-verified, 42/99 count-only", never
> as "99/99 verified" (bundle 08, anchor-corpus correction).

## The one operational fact everything is ordered around

Dropping `MultiHashJoin` is **not** a neutral refactor, and the repository
already knows it — `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md:189-196` (lightly condensed):

> dropping MultiHashJoin turns star/snowflake queries into binary cascades that
> materialise wide intermediates a single MHJ probe pass would stream — **Q5 and
> Q21 hang, Q9 times out, Q10 11.4×, Q18 4.3×, Q7 1.9×** … and the axis has a
> favourable direction too (Q2 18.8×, Q8 4.1×). The direction is not predictable
> from the code change, so it must be measured per commit.

And the cheapest gate cannot catch it: `scripts/tpch-spotcheck.sh` compares
Q12/Q13 **row counts** — every completing regression above passes it green.
Additionally: **Q5 contains no `MultiHashJoin` at all** (verified,
`analysis/cost-driven-second-try-200731/evidence/judge-verifications-20260731.txt`
V1/V7) — the worst regression in the evidence set is an ORDER failure, not a
fusion failure. Hence: streaming fixes ship and are measured **before** the MHJ
node is removed, and the order question gets its own measured re-validation.

## The conditionality forks, stated once

Three *decision* forks below, plus two further conditional tasks gated the same
way: **-0004** (by -0003's interim A/B — legacy-path traffic) and
**-0009/-0010** (by -0008's bar check leaving failures). A fork or task that is
*not entered* is a successful, recorded outcome — never a silent skip. Each
un-entered conditional task owes a `.ralph/deferral_ledger.md` row.

1. **The fusion fork (M0126-0005 → -0006/-0007).** If Stage 0's A/B shows the
   de-materialised cascade within ~1.5× of the fused MHJ on the packing queries,
   -0006 and -0007 are **skipped entirely** — the bundle calls this the *best
   available outcome* (no new operator, no new contract, no new bug class).
2. **The flip fork (M0126-0012).** The bar is measured; met → default flip;
   missed → documented no-go **and M0126-0013 is triggered**.
3. **The remediation fork (M0126-0013, filed by the USER 2026-07-31).** Only if
   -0012 records a no-go: make the cost model build-side-memory aware (PG's
   `initial/final_cost_hashjoin` + `ExecChooseHashTableSize` analogue that
   `hashJoinCost` has never had), then **re-run -0012's acceptance measurement**
   and re-judge the bar. Pass → flip; fail → final no-go.
   **If -0013 is never triggered** (the bar passed without it), it closes as
   *not-triggered* with a ledger row naming bundle **07 §7** as the outstanding
   argument — without memory realism the cost model never learns the cascade is
   expensive, an argument that survives a passing bar — and a successor owner.

## Why MHJ retirement (M0126-0011) precedes the flip (M0126-0012)

`internal/planner/bushy.go:18-21`: `GOOPG_COST_DRIVEN_JOINORDER=1` sets
`mhjPackingEnabled = false` **as a side effect**. If the order flip landed first,
one commit would change two variables — join order *and* the entire MHJ
plan-shape space — silently performing Stage 4's plan-shape change without its
four-step hand-reviewed snapshot procedure and without KS3 remaining an
independent revert. Landing -0011 first makes packing default-off on **both**
arms, so -0012's acceptance A/B is a genuine single-variable comparison of join
order alone. (Same confound-avoidance rule the bundle applies at Stage −1a and
Stage 2's F12 trap.) Corollary: after -0011, the env var's
`mhjPackingEnabled = false` assignment becomes redundant; -0012 notes it, does
not delete it — it is load-bearing if KS3 is ever reverted.

## Required Design Docs

| Task | Content | Provenance | Design doc |
|---|---|---|---|
| **M0126-0001** | Packer key-count guard (`len(keys)==len(scans)-1` in `collectMultiHashTables`) + `VirtualSlot.Materialize()` arena clone, two separate correctness commits; plus the **pinned R0 baseline** (timed TPC-H SF1 + `plan_snapshots/m0126-base.txt`) captured before either lands. | bundle 09 Stage −1, −1b; 08 R1/R3c | `0126-0001-packer-key-guard-and-slot-clone.md` |
| **M0126-0002** | `EstimateRows` gains its missing `*MultiHashJoin` arm (today every packed MHJ estimates 0 rows). PLAN diffs **expected** — every hunk hand-reviewed and classified; snapshot re-baselined. | bundle 09 Stage −1a; 08 R18 | `0126-0002-mhj-cardinality-arm-and-plan-rebaseline.md` |
| **M0126-0003** | Live-path de-materialisation: `*VirtualSlot` fast path in `Slot.fillFromTupleSlot` + extraction of a slot-taking `evalHashKeyDatumSlot` (kills the per-probe-row `lazyKeyRow` memcpy). PLAN must show **zero** diffs. | bundle 09 Stage 0a-live, 0b; 02 §4.1; F11 | `0126-0003-live-path-dematerialisation-and-slot-key-eval.md` |
| **M0126-0004** | Legacy `Build`-path slot chaining with per-pull source re-binding and a copy fallback (F7: children do not return a stable slot object). **Conditional** on -0003's measurement showing the legacy path still carries bench traffic. | bundle 09 Stage 0a-legacy; F7 | `0126-0004-legacy-build-path-slot-chaining.md` |
| **M0126-0005** | Stage 0 A/B (`mhjPackingEnabled` forced off; query set derived by EXPLAIN at the measurement HEAD, never inherited) and the written **fusion go/no-go decision**. | bundle 09 Stage 0c; F15 | `0126-0005-stage0-ab-and-fusion-decision.md` |
| **M0126-0006** | Fusion scaffolding: `buildEnv` plumbing, `tryFuseHashCascade`/`fusedHashJoinOp`, KS1/KS2 (default off), nine named tests incl. the DIFF harness. Switch off ⇒ production bit-identical. **Conditional** on -0005. | bundle 09 Stage 1; 03/04/05; 10 KS1/KS2 | `0126-0006-fusion-scaffolding-and-differential-harness.md` |
| **M0126-0007** | Fusion enabled in measurement only (F12: force `SetMHJPackingEnabled(false)`, never conflate with the order env var); DIFF/DS05/SPOT/low-`work_mem`/A-B/decline histogram. "Leave it off permanently" is a legitimate completion. **Conditional** on -0006. | bundle 09 Stage 2; F12; 08 R4/R10/R11 | `0126-0007-fusion-enablement-measurement.md` |
| **M0126-0008** | Symmetric-timeout re-validation of `GOOPG_COST_DRIVEN_JOINORDER` at post-Stage-0 HEAD (the 2026-07-24 A/B used 600 s vs 300 s and is invalid as a comparison); per-query table vs R0; decision document naming exactly which queries fail which bar clause. | bundle 09 Stage 3; 07 §5 | `0126-0008-cost-driven-order-symmetric-revalidation.md` |
| **M0126-0009** | One bounded attribution per still-failing query to a closed list of four mechanisms — (a) cardinality estimate, (b) join-order preference, (c) build-side memory not modelled (expected for Q5/Q9/Q21; routes to -0013's evidence file), (d) executor per-row cost surviving Stage 0. Diagnosis only, ≤2 probes per query. | `docs/design/cost-model/14-fk-aware-and-mcv-join-selectivity.md`, `15-mhj-in-cost-driven-star-shapes.md` | `0126-0009-order-failure-attribution.md` |
| **M0126-0010** | Bounded order-quality/cardinality fixes: ≤1 fix per query, ≤2 attempts per query, ≤4 landed commits total; **no** new penalty multiplier on cost totals, **no** shape preference (doc 15's prohibitions). | `docs/design/cost-model/15-mhj-in-cost-driven-star-shapes.md`; bundle 07 §6 | `0126-0010-bounded-order-quality-fixes.md` |
| **M0126-0011** | Retire `MultiHashJoin` as a plan node: `mhjPackingEnabled` default → `false`; node + operator **retained in-tree** one full nightly cycle; four-step snapshot procedure with hand review; `scripts/pg-plan-shape-diff.sh` in report mode; the `generateMultiHashJoinPath` question settled in writing. **Conditional** per Stage 4 preconditions. | bundle 09 Stage 4; 06 §4-§5; 10 KS3; 08 R17 | `0126-0011-mhj-plan-node-retirement.md` |
| **M0126-0012** | **Acceptance measurement + conditional default flip**: measure all four bar clauses at final HEAD vs R0; met → flip (`bushy.go:13-21`) + re-snapshot + update every "ships off by default" statement; missed → documented no-go, triggering -0013. | user directive 2026-07-31; supersedes bundle 07 §6's "no default change" (a statement about the bundle's own scope) | `0126-0012-cost-driven-order-default-flip.md` |
| **M0126-0013** | **Conditional remediation (USER, 2026-07-31)**: build-side memory-aware hash costing — hash-table byte estimate + `work_mem`-overrun penalty / spill cost in `hashJoinCost` (which today omits batching entirely), the analogue of PG `initial/final_cost_hashjoin` + `ExecChooseHashTableSize`; `goopg_hash_entry_width_multiplier` (default 6.0, the 48-byte-Datum realism) applied to the **memory/spill decision only, never the cost total**. Then **re-run -0012's measurement** and re-judge the bar. | user directive 2026-07-31; bundle 07 §2, §7; doc 15 (`GOOPG_MAT_MULT` lesson); PG `costsize.c:4134,4160`, `nodeHash.c:658,3622` | `0126-0013-build-side-memory-aware-hash-costing.md` |

## Order

```
0001 → 0002 → 0003 → [0004?] → 0005 → [0006 → 0007?] → 0008 → [0009 → 0010?]
     → 0011 → 0012 → [0013? → re-run 0012's measurement → final verdict]
```

- `[0004?]` — only if -0003's A/B shows the legacy `Build` path still carries
  measured bench traffic (every aggregate-topped TPC-H star query runs its joins
  there today — bundle 02 §9 — so expect it IN unless the slab migrated).
- `[0006 → 0007?]` — the fusion fork; skipped whole by -0005's ~1.5× decision.
- `[0009 → 0010?]` — only for queries -0008 leaves failing the bar.
- `[0013?]` — only if -0012 records a no-go. -0013 ends by re-running -0012's
  measurement protocol; that re-measurement is part of -0013, not a new task.

No task may be selected while any M0125 item is open (Current Priority banner).

## Definition of Done

1. **Confounds removed and baselined:** the `len(keys) == len(scans)-1` guard
   exists; `VirtualSlot.Materialize()` clones arena-backed Datums;
   `EstimateRows` has a `*MultiHashJoin` arm (no MHJ estimates 0 rows);
   `analysis/cost-driven-second-try-200731/evidence/r0-baseline.txt` and
   `plan_snapshots/m0126-base.txt` were committed **before** any of them landed.
2. **Stage 0 measured, not assumed:** `evidence/stage0-ab.txt` exists, its query
   set was derived by EXPLAIN at the measurement HEAD, and the ≥/< 1.5×
   decision is written down with its consequence for -0006/-0007 stated.
3. **If fusion was built, it is bit-safe:** KS1-off runs bit-identical to the
   pre-task run on every gate; `TestFusedCascadeMatchesUnfused` compares
   **ordered** output; DS05 with the switch on shows zero row **and** checksum
   deltas; the low-`work_mem` run is identical fused/unfused with non-zero
   temp-file usage both sides. If fusion was skipped by DoD 2's decision, the
   skip and its measurement are recorded — a satisfied item, not an omission.
4. **The order question re-measured honestly:** an SF1 A/B with identical
   timeouts on both arms at post-Stage-0 HEAD, per-query vs R0, recorded as a
   clause-by-clause bar verdict (`evidence/stage3-order-ab.txt`).
5. **Every still-failing query fixed or attributed:** a landed fix that moved
   *its own* query in a per-query A/B, or a written attribution to one of the
   four mechanisms plus a ledger row. No query left silently unexplained.
6. **`mhjPackingEnabled` defaults to `false`**, every flip diff hand-reviewed
   and enumerated, `plan_snapshots/post-mhj-retire.txt` captured,
   `scripts/pg-plan-shape-diff.sh` exists in report mode only,
   `multi_hash_join.go` + the node still in-tree and reachable via
   `SetMHJPackingEnabled`, and the `generateMultiHashJoinPath` decision written.
7. **The acceptance bar has a measured verdict** (all four clauses, quiet host,
   numbers recorded): **either** cost-driven order is default-on with every
   "ships off by default" statement updated, **or** a no-go document names the
   failing clause, residual queries, attributions, and — if -0013 also ran and
   failed — the final no-go with -0013's delta recorded.
8. **The remediation fork has a verdict:** either -0013 landed (byte estimate +
   overrun penalty in `hashJoinCost`; multiplier proven by unit test to move
   only the spill decision, never a non-spilling join's total; zero
   default-config plan diffs) and the re-measurement's clause-by-clause delta vs
   the first -0012 run is recorded — or -0013 closed *not-triggered* with a
   ledger row naming bundle 07 §7 and a successor owner. A skip with no ledger
   row is not done.
9. **Every rollback left an artefact** (bundle 10 §5): failing artefact under
   `evidence/`, a risk-register row naming the failed mitigation, a recorded
   retry/abandon decision.
10. **Every DS05 result reported as "57/99 content-verified, 42/99 count-only"**;
    any wrong-column/wrong-order-risk task ships ≥1 hand-written full-output
    comparison on a shape drawn from the 42.
11. Design docs indexed; milestone index row updated; this file's status is
    `accepted`.

## Evidence ledger

| artefact | owed by |
|---|---|
| `analysis/cost-driven-second-try-200731/evidence/r0-baseline.txt` | M0126-0001 |
| `plan_snapshots/m0126-base.txt` | M0126-0001 |
| `analysis/cost-driven-second-try-200731/evidence/stage0-ab.txt` | M0126-0005 |
| `analysis/cost-driven-second-try-200731/evidence/stage2-ab.txt` | M0126-0007 |
| `analysis/cost-driven-second-try-200731/evidence/stage3-order-ab.txt` | M0126-0008 |
| `analysis/cost-driven-second-try-200731/evidence/order-attribution-Q<N>.txt` | M0126-0009 |
| `plan_snapshots/m0126-mhj-card.txt` (re-baseline after the EstimateRows arm) | M0126-0002 |
| attribution summary table (query → class → routing) | M0126-0009 |
| `plan_snapshots/post-mhj-retire.txt` | M0126-0011 |
| `plan_snapshots/m0126-costdriven-default.txt` (flip path only) | M0126-0012 |
| `analysis/cost-driven-second-try-200731/evidence/acceptance-run-1.txt` | M0126-0012 |
| `analysis/cost-driven-second-try-200731/evidence/acceptance-run-2.txt` (delta col vs run 1) | M0126-0013 (if triggered) |

## Out of scope

- **The ~20-file commit that deletes the MHJ node and operator.** Bundle Stage
  4's "only after a clean nightly cycle" clause places it after -0011's nightly
  cycle, i.e. after this milestone closes. Reopen: one clean nightly at
  `mhjPackingEnabled=false`.
- **A blocking goopg-vs-PG structural plan gate.** `pg-plan-shape-diff.sh` ships
  report-mode only; goopg emits no `Hash` node and placeholder costs/widths
  (bundle 06 §6), so a blocking gate would block every commit. Reopen: those
  asymmetries settled.
- **Costing the fusion** (bundle 07 §4 invariant — the planner must cost the
  cascade as if fusion did not exist).
- **Open-ended order surgery.** Doc 15 characterises it as a separate large
  project; -0010's caps are the boundary.
- Any change to `./postgres/`.

**Scope note on bundle 07 §6:** its "no change to `GOOPG_COST_DRIVEN_JOINORDER`'s
default at any stage in this document set" is a statement about the *bundle's
own* scope. M0126-0012/-0013 supersede it **by user directive (2026-07-31)** —
record this so no reviewer reads the milestone as contradicting its own design
of record.

## PostgreSQL References

- `postgres/src/backend/optimizer/path/costsize.c` — `initial_cost_hashjoin`
  (:4134) / `final_cost_hashjoin` (:4160): the batch-count and spill-I/O terms
  M0126-0013 transliterates.
- `postgres/src/backend/executor/nodeHash.c` — `ExecChooseHashTableSize` (:658),
  `get_hash_memory_limit` (:3622) = `work_mem × hash_mem_multiplier × 1024`,
  the budget goopg's `hashJoinCost` has no analogue of.
- `postgres/src/backend/executor/nodeHashjoin.c` — build-side-only
  materialisation; the pipelined outer that makes a PG left-deep cascade stream.
- `postgres/src/backend/optimizer/path/joinrels.c` — join-order enumeration the
  cost-driven DP mirrors.
