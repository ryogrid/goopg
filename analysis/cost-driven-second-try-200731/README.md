# Cost-driven, second try — demoting MultiHashJoin from a plan node to a runtime strategy

| field | value |
| --- | --- |
| status | **DESIGN ONLY — no code is changed by this document set, and nothing in it has been measured on a live server.** Every runtime number is re-derived from committed evidence files. |
| date | 2026-07-31 |
| directory | `analysis/cost-driven-second-try-200731/` |
| supersedes-in-spirit | `docs/design/cost-model/15-mhj-in-cost-driven-star-shapes.md` — the *first* try (make the DP emit MHJ), implemented, measured and reverted |
| depends on | `docs/design/0038-0001-multi-way-hash-join.md`, `docs/design/cost-model/07`, `/12`, `/14`, `/15`, `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md` |
| oracle | PostgreSQL 18.3, read-only, `./postgres/` |
| how it was produced | two **independent** design passes over the same brief, neither seeing the other, each with its own adversarial agent review; then a synthesis pass that re-verified every point of disagreement against source. Provenance and the full panel analysis: [13](13-panel-synthesis.md) |
| verdict | **ADOPT THE GOAL, REJECT THE PROPOSED FIRST MECHANISM** — see below |

## The proposal being evaluated ("the third option")

> PostgreSQL's binary hash join materialises only the inner (build) side and streams the outer
> side fully pipelined. A PG left-deep cascade with the fact table as the outermost outer
> therefore already streams fact rows through N hash tables without materialising
> intermediates — logically the same thing goopg's `MultiHashJoin` (MHJ) does by hand.
>
> So: **the planner should only ever emit PG-identical left-deep binary join trees (MHJ
> disappears as a plan node, EXPLAIN becomes PG-isomorphic); the EXECUTOR fuses adjacent INNER
> hash joins at runtime** when they satisfy the tree / single-column-key / composite-free
> conditions. MHJ is demoted from a *plan node* to a *runtime execution strategy*.
>
> Claimed benefits: (1) PG plan-shape parity becomes real, so `make plan-gate` and
> `scripts/pg-oracle-diff.sh` become meaningful; (2) it escapes the dead end of cost-model
> doc 15, because the fusion no longer has to win on cost; (3) the qualification predicate and
> the 651-line executor already exist, so this is closer to a relocation than a new
> implementation.

## Verdict

**The premise is correct about PostgreSQL and wrong about goopg, and that gap is the whole
finding.**

Correct about PG, and verified in goopg's own code: `joinOp` already drains only the build side
(`drainRowsBounded(o.right, budget)`, `internal/executor/operators_join_agg.go:645`) and streams
the probe side (`openProbeSide`, `:510-522`). A left-deep cascade with the fact table at the
bottom-left genuinely is a pipelined N-way probe, and MHJ genuinely is a hand-fusion of it.

Wrong about goopg, because **goopg's cascade re-materialises its probe input at every level —
twice, and at a different address on each of goopg's two execution paths**:

- legacy `Build` path: `slotRow(probeSlot)` (`operators_join_agg.go:1214`) →
  `VirtualSlot.Row()` (`slot.go:159-164`) = `acquireRow` + a per-column 48-byte copy, then a
  *second* full-width copy into `lazyKeyRow` so the hash key can be evaluated (`:1216-1241`);
- live slab path (`BuildFastIterator`, `internal/server/dispatch.go:2839`): the first call is
  free, and the copy has moved into `joinOpKernelNext` → `Slot.fillFromTupleSlot`
  (`opnode.go:869-876`, `:133-152`), which also copies twice.

All of it at goopg's **48-byte** `Datum` (`datum.go:119`) against PG's 8. MHJ avoids this not by
fusing join *logic* but by composing **one** `VirtualSlot` over N persistent per-table slots
(`multi_hash_join.go:265-291`) and never materialising an intermediate at all.

**Therefore the first move is not to build a fusion operator. It is to de-materialise the join
seam on both execution paths** — which delivers MHJ's structural advantage inside a
PG-identical plan shape, with no new operator, no new semantic contract, and no new class of
silent-wrong-answer bug. That is **Stage 0**, and it is blocking: its measurement decides
whether the rest of the proposal is worth building at all.

### The three claimed benefits, adjudicated

| claim | verdict |
| --- | --- |
| A left-deep cascade with fact-outermost already streams like MHJ | **TRUE in PG, NOT YET TRUE in goopg** — two copy sites on two paths ([02 §2](02-premise-audit.md)) |
| MHJ is a genuine PG-compat defect worth removing from the plan space | **TRUE** |
| (1) Fusion makes `plan-gate` / `pg-oracle-diff` meaningful | **FALSE.** `make plan-gate` (`Makefile:376-390`) diffs goopg against **goopg's own** newest snapshot; `scripts/pg-oracle-diff.sh` diffs **result text**, not plans. Removing MHJ does not repair an existing gate — it makes a **new** goopg-vs-PG structural gate *possible*, which is unwritten work. And it is not sufficient on its own: goopg emits **zero** bare `Hash` nodes where PG always emits one (verified — [evidence/judge-verifications-20260731.txt](evidence/judge-verifications-20260731.txt) V2) |
| (2) Fusion escapes doc 15's dead end | **TRUE OF THE STATED OBJECTION, FALSE OF THE BINDING CONSTRAINT.** The two panels split on this and the split is instructive — see [02 §3](02-premise-audit.md) and [13](13-panel-synthesis.md). Fusion does genuinely escape the *cost-competition* argument. But doc 15's own conclusion is *"the blocker underneath is the join ORDER, not the MHJ cost"*, and fusion does not touch order: on a bushy tree the fusion predicate simply declines and the 804 s stands. Net benefit on the queries doc 15 targeted: **≈ zero** |
| (3) "Closer to a relocation than a new implementation" | **FALSE.** `multi_hash_join.go` cannot be lifted: its build path has no `WorkMem` and no spill, its spanning-tree walk silently drops cycle-closing edges, and its residual-filter walker is incomplete. What is reusable is the *idea* and the predicate's *shape*, not the code ([02 §6](02-premise-audit.md)) |

### The best argument for the change is one the proposal never makes

Not speed. **Deleting `MultiHashJoin` as a plan node deletes an index-skew bug generator.**
The flatten-to-an-OID-ordered-table-list-and-remap-every-`ColumnRef` round trip
(`bushy.go:1790-1830` + `buildMHJPosMap` `:2349` + `remapColumnRefsAfterRewrite` `:1854`) exists
only because MHJ is a plan node, and it has produced this project's worst bugs — including the
stale-permutation defect recorded at `internal/planner/plan.go:869-886` and the tip-of-branch
commit `23a077ae`. There are **28 non-test `case *MultiHashJoin:` arms across 15 files**, each an
independent chance for a new pass to forget the node. Speed must merely not regress.

## The single most important operational fact

Dropping `MultiHashJoin` is **not** a neutral refactor, and the repository already knows it —
`docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md:188-198`:

> dropping MultiHashJoin turns star/snowflake queries into binary cascades that materialise wide
> intermediates a single MHJ probe pass would stream — **Q5 and Q21 hang, Q9 times out, Q10
> 11.4×, Q18 4.3×, Q7 1.9×** … The same section shows the axis has a favourable direction too
> (Q2 18.8×, Q8 4.1×), so "any MHJ change is bad" is the wrong lesson. The right one: **the
> direction is not predictable from the code change, so it must be measured per commit.**

and the reason the cheapest gate cannot catch it: `scripts/tpch-spotcheck.sh` compares Q12/Q13
**row counts** — every completing regression above would have passed it green.

Every stage ordering decision in [09](09-staged-implementation-plan.md) exists to make that
regression impossible to land: **the streaming fix ships and is measured before the MHJ node is
removed, never the other way round.**

## Document set

| # | file | what it settles |
| --- | --- | --- |
| 00 | `README.md` (this file) | index, verdict, how to read |
| 01 | [01-motivation-and-measured-evidence.md](01-motivation-and-measured-evidence.md) | every number re-derived from the raw evidence; the claims in circulation that do not survive it; what is and is not established |
| 02 | [02-premise-audit.md](02-premise-audit.md) | right / wrong / missed; the two copy sites on two execution paths; why goopg's DP emits **bushy**, not left-deep, trees |
| 03 | [03-semantic-contract.md](03-semantic-contract.md) | C1–C15: what fusion must preserve **bit-for-bit** — row order, column order, NULL semantics, residual order *and timing*, LEFT/SEMI/ANTI exclusion, cancellation, spill, FOR UPDATE, parallelism, EXPLAIN, re-Open |
| 04 | [04-fusion-site-and-data-structures.md](04-fusion-site-and-data-structures.md) | where the decision is made, on what data structure, why the plan tree is never rewritten, and the `buildEnv` plumbing that is not free |
| 05 | [05-qualification-predicate.md](05-qualification-predicate.md) | Q0–Q9, fail-closed, clause by clause |
| 06 | [06-explain-and-plan-shape.md](06-explain-and-plan-shape.md) | EXPLAIN invariance; how `EXPLAIN ANALYZE` reports a fused pipeline; the goopg-vs-PG shape gate this unlocks and what it still cannot do |
| 07 | [07-cost-model-interaction.md](07-cost-model-interaction.md) | costing the cascade honestly in goopg's 48-byte-`Datum` units; the "never cost the fusion" invariant and its corollary |
| 08 | [08-risk-register.md](08-risk-register.md) | R1–R18, dominated by silent row-count regression |
| 09 | [09-staged-implementation-plan.md](09-staged-implementation-plan.md) | Stage −1 … Stage 4, each with its exact gate commands and stop condition |
| 10 | [10-rollback-and-kill-switches.md](10-rollback-and-kill-switches.md) | three independent kill switches and the revert unit per stage |
| 11 | [11-adversarial-review.md](11-adversarial-review.md) | both panels' adversarial reviews, the fold-back log, and what each review actually changed — **read this before implementing anything** |
| 12 | [12-claim-verification-table.md](12-claim-verification-table.md) | every factual claim with `file:line` and how it was checked |
| 13 | [13-panel-synthesis.md](13-panel-synthesis.md) | how this bundle was produced: consensus, contradictions, partial coverage, unique insights, blind spots |
| — | [evidence/](evidence/) | the synthesis pass's own verification transcript, plus the second panel's independent chapters retained as source material |

## The five things most likely to be got wrong

Restated here because a reader who skips [11](11-adversarial-review.md) will otherwise implement
the first drafts' mistakes. Each was confirmed against source.

1. **A width check does not protect the column mapping.** `Join.Output()` returns a *cached*
   schema (`internal/planner/plan.go:889-897`) that can go stale as a **same-width permutation**
   (`:869-886`). Only the element-wise column-identity assertion
   ([04 §4](04-fusion-site-and-data-structures.md), [05 Q6 clause 3](05-qualification-predicate.md))
   is a real mitigation.
2. **`drainRowsBounded` deep-copies build rows, and that copy is REQUIRED** — arena-backed
   Datums must be promoted before accumulation or a hash entry aliases a reused scan buffer
   (`spill.go:384-402`, the M0097-0058 class). Do not "optimise" it away.
   [03 C13](03-semantic-contract.md).
3. **`VirtualSlot.Materialize()` does *not* clone arena-backed Datums** (`slot.go:167-169`),
   unlike its siblings. Any slot-chaining work must fix this **first** or it hands consumers
   dangling arena references. [08 R3c](08-risk-register.md).
4. **`Build`/`buildRec` have no root, no session and no `*Context`** (`executor.go:21`, `:424`).
   The kill switch therefore cannot be a session GUC, and Q0's root walk needs new plumbing.
   [04 §1.1](04-fusion-site-and-data-structures.md), [10 KS1](10-rollback-and-kill-switches.md).
5. **`EstimateRows` has no `*MultiHashJoin` arm** (`internal/planner/cardinality.go:38+`,
   verified), so every MHJ estimates **0 rows** and every ancestor's `BuildLeft`/algorithm
   decision above a packed chain is made on that zero — **today, in the default configuration**.
   It is both a live defect and a confound that would silently corrupt the Stage 0 A/B.
   [08 R18](08-risk-register.md).

## How a later autonomous loop should use this

Read [09](09-staged-implementation-plan.md) first. **Do not start at Stage 1.** Stage 0 is
executor-only, plan-shape-neutral, independently valuable, and its measurement is the input that
decides whether Stages 1–2 are worth building. Every stage names the exact gate commands from
this repository (`scripts/tpch-spotcheck.sh`,
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`, `make plan-gate`,
`scripts/tpcds-sf05-regression.sh sweep`) and a stage is not done until its gates are green — a
**SKIP is not a pass**.

## What this set deliberately does not do

- It does not re-open the join-**order** question. Doc 15's conclusion on order stands; this set
  records order as an **external blocker** ([02 §3](02-premise-audit.md), Stage 3).
- It does not change `GOOPG_COST_DRIVEN_JOINORDER`'s default at any stage. The committed A/B says
  cost-driven order is a net loss on TPC-H SF1 today, and this work does not by itself change that.
- It does not propose costing the fusion ([07 §4](07-cost-model-interaction.md)).
- It does not delete `internal/executor/multi_hash_join.go` or the `MultiHashJoin` plan node
  before Stage 4, and Stage 4 is explicitly conditional on measurement.
- It does not touch `./postgres/`.
