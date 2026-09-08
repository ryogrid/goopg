# C-19h (P5-08) — retire `MaybeAddGather`: measured BLOCKED, with the blocker named

*take3 08 §8; gate take3 09 §5 P5. 2026-09-07.*

**Verdict: C-19h cannot land, in any form — not unconditionally, and not
conditionally on `GOOPG_GATHER_PATHS=all`.** The blocker is not the default
flip. It is that the path model does not yet produce the Gathers the post-pass
produces, and the largest missing piece is C-19g's own unfinished half.

This document exists so the next attempt starts from the measurement instead of
from the premise.

## 1. The premise the item was written on

Phase 5's argument is that once the search prices Gathers as paths, the
post-pass — a **size** rule running after planning — has nothing left to decide,
and deleting it is the natural end of the chain. C-19d/f/g each moved something
below the parallel boundary and each shipped default-OFF pending its own
measurement.

The premise is checkable, and it has two halves:

1. does the path model produce a Gather where the post-pass does?
2. does it produce one *at the default*?

Half 2 is obviously no. Half 1 turns out to be no as well, and that is the part
that was not known.

## 2. Evidence — the default arm

`generateUsefulGatherPaths` (gatherpaths.go) and `addPartialAggPaths`
(partialaggpaths.go) are the **only** producers of `PathGather` /
`PathGatherMerge`, and both return at their first line when their knob is
`off` — which is the default for both.

Measured on the SF=1 clone, one engine image, `GOOPG_PGSHAPED_DP=1`,
`GOOPG_PGSHAPED_COLLAPSE=1` (the engine defaults):

| arm | queries with a Gather | Gather nodes |
|---|---|---|
| default (`GOOPG_PARALLEL=on`, both path knobs off) | **12 / 22** | 12 |
| post-pass retired (`GOOPG_PARALLEL=off`, same knobs) | **0 / 22** | 0 |

`GOOPG_PARALLEL=off` is an exact stand-in for the retirement **in this arm
only**: with both path knobs off the search produces no Gather whether or not
the kill switch is set, so the switch removes precisely the post-pass.

Timing, same engine image, fresh capped server per arm, `GOGC=100`:

| | default | post-pass retired | |
|---|---|---|---|
| suite total | **232.35 s** | **467.03 s** | **+100.0 %** |
| Q18 | 43.17 | 154.22 | +257 % |
| Q21 | 17.65 | 61.12 | +246 % |
| Q19 | 2.59 | 25.26 | +875 % |
| Q15a | 3.31 | 17.97 | +443 % |
| Q10 | 6.32 | 19.00 | +201 % |

(Q1/Q2/Q4 read *faster* in the retired arm; those are the first queries of the
first sweep and carry its cold page cache. The queries above are all past that
point and all move the same way.)

A rule that is the only producer of a plan property cannot be retired by
deleting it. **C-19h is downstream of the default flip.**

## 3. Evidence — the parallel arm, which is the real blocker

The obvious salvage is a *conditional* retirement: make the post-pass stand
down wholesale whenever `GOOPG_GATHER_PATHS=all`, so the deletion becomes a
one-line follow-up when `all` becomes the default. That was implemented and
measured, and it **fails too**.

With a probe build whose post-pass stands down whenever the path model is
admitting Gathers, at the most aggressive Phase-5 settings
(`GOOPG_GATHER_PATHS=all`, `GOOPG_PARTIAL_AGG_PATHS=on`):

| arm | queries with a Gather |
|---|---|
| post-pass (today's default) | Q1 Q3 Q5 Q6 Q7 Q8 Q9 Q10 Q14 Q15a Q16 Q19 — **12** |
| path model only, `GP=all PA=on` | Q3 Q5 Q7 Q8 Q9 Q10 **Q21** — **7** |
| path model only, `GP=top PA=on` | identical 7 |

So retiring the post-pass on the parallel arm **loses parallelism on six
queries** (Q1, Q6, Q14, Q15a, Q16, Q19) and gains one (Q21 — C-19f's win, the
case only a path model reaches).

### 3.1 Why Q1 is lost, and what that names

Q1 is the interesting loss, because C-19g measured Q1 8.57 → 4.14 s **with
`GOOPG_PARTIAL_AGG_PATHS=on`** and that win is real. It is delivered through the
post-pass. C-19g replaced the *verdict* — `splitAggregateIsProfitable`'s five
invented constants became a priced path tournament — but not the
*construction*: `partialAggSplitPays` "returns only a boolean and constructs no
node", and `splitAggregate` (parallel.go) still builds the
`Finalize -> Gather -> Partial` shape. Its TODO_ALL row says so itself and is
marked `[~]`: "the upper-rel-resident half is unfinished".

Retiring the post-pass therefore retires C-19g's win along with it.

**C-19h's true prerequisite is C-19g's upper-rel-resident half**, not the
default flip — the flip is merely the second gate, after it.

## 4. What was NOT landed, and why

The conditional stand-down was written and then reverted rather than shipped.
Landing it would have degraded the `GOOPG_GATHER_PATHS=all` arm — six queries
serialised — and that arm is precisely the one on which the default flip is
going to be judged. A staging flag whose staging position is worse than both
endpoints is not staging.

The `subtreeHasGather` per-tree stand-down C-19d landed is therefore unchanged
and still correct: it is what stops the post-pass nesting a second Gather below
a costed one.

## 5. Double-Gather verification (the item's own requirement)

The interaction C-19d's stand-down exists to prevent — `findPartialSubtree`
descending through a terminating single-child node and nesting a second Gather
under the costed one, N workers each launching N — was re-verified rather than
assumed. Across every arm measured here, including
`GOOPG_GATHER_PATHS=all` + `GOOPG_PARTIAL_AGG_PATHS=on` with the post-pass
live, **no plan carries more than one Gather on any root-to-leaf path**: the
per-query Gather counts above are all exactly 1 (Q14 renders 2 in the
legacy-rewriter arm — two sibling subqueries, not nesting). The unit-level
statement of the same property, `subtreeHasGather` on the shape that produced
the nesting, is unchanged and green.

## 6. Recommended sequencing

1. finish C-19g's upper-rel-resident partial aggregation, so a `Finalize ->
   Gather -> Partial` shape exists as a **path** rather than as a post-pass
   construction (recovers Q1, and Q6/Q14/Q16/Q19 are the next cohort to
   diagnose the same way);
2. re-run §3's census; retire the post-pass conditionally on
   `GOOPG_GATHER_PATHS=all` only once that census reaches parity;
3. flip the default (needs the `plan_snapshots/` re-pin);
4. delete `MaybeAddGather`, `findPartialSubtree`, `sortPartialRootPays`,
   `terminatesPartial`, `rebuildWithGather`, `splitAggregate` and the two
   `dispatch.go` call sites.

Only step 4 is C-19h as written.

---

## 7. Re-run 2026-09-07 — blockers 1 and 3 discharged, a THIRD one found

C-19g's upper-rel-resident half landed (`cb4556791`) and §6's step 1 is done.
Steps 2–4 were then executed as a real probe, not a thought experiment: the
ADD half was deleted (`findPartialSubtree`, `partialTarget`,
`rebuildWithGather`, `terminatesPartial` with it), the enforcement half kept
and renamed `EnforceParallelPermission`, both `dispatch.go` call sites updated,
`go build ./...` clean. Full evidence, captures and probe patch:
`analysis/planner-refactor-take3/c19h-census-rerun-20260907/`.

**§3's census now reaches parity, and by a stronger test than the census.**
Same engine image, private SF=1 clone, engine defaults: 12/22 with the
post-pass and 12/22 without it, the SAME twelve — and `diff` of the two
22-query captures is **EMPTY**. Every TPC-H Gather now comes from
`partialaggupper.go`. Blocker 1 (§2) and blocker 3 (§3.1, "Q1's win is
delivered THROUGH the post-pass") are discharged.

**And that is exactly why the census was the wrong instrument.** All 22 TPC-H
queries carry an aggregate at the top; C-19g's producer is aggregate-only; so
a 22-query capture is blind to every plan whose top node is not an aggregate.
Probed directly, `select * from lineitem where l_extendedprice > 90000` goes
from `Gather (Workers Planned: 4) -> Parallel Seq Scan` to a bare `Seq Scan`
when the post-pass is deleted — and **vanilla PG 18.3 emits the Gather**, so
the deletion is a PG-parity regression rather than a cleanup.

**The remaining prerequisite is not the `GOOPG_GATHER_PATHS` default flip.**
Measured: at `GOOPG_GATHER_PATHS=all`, the most aggressive setting C-19d
offers, the retired build STILL emits the bare `Seq Scan`. The path model
declines a plain Gather on cost, for the reason C-19d's own DESIGN §5.1
states — `parallel_tuple_cost` 0.1/row against a 4-worker `cpu_tuple_cost`
saving of ≈0.0075/row, which `add_path` correctly dominates at any relation
size. So §6's sequencing is amended: the prerequisite is **C-19d's crossover
arithmetic for a plain base-rel Gather**, and flipping its knob does not
supply it.

Two further consequences of the deletion, both named in the TODO row as things
that must survive, both now measured rather than assumed:

- `debug_parallel_query` becomes a fully unconsumed GUC. Its only planner
  consumer is `computeParallelWorkers`, reached only from the deleted
  `findPartialSubtree`; `considerparallel.go` sizes with
  `computeParallelWorkerForRel`, which does not read it. PG implements the
  same GUC as a post-planning Gather wrap in `standard_planner`
  (`postgres/src/backend/optimizer/plan/planner.c:465-495`) — so a
  `debug_parallel_query`-gated post-pass is **PG-faithful in kind**, and is
  the shape any conditional retirement should take rather than a deletion.
- 68 call sites across 14 test files use `MaybeAddGather` as the
  forced-parallel constructor for the executor's parallel operators; it is
  their only construction path.

`StripGather` survived the probe intact — it is what `EnforceParallelPermission`
is made of — so requirement (a) of the row is not at issue.

The blocker is pinned as a unit test rather than left in prose:
`TestPostPassOwnsTheNonAggregateGather` (parallel_test.go).

### 7.1 Double-Gather verification, re-done

Per-query Gather counts are exactly 1 in all three arms — post-pass live over
a path-model Gather (the case `subtreeHasGather` exists to prevent), post-pass
deleted, and post-pass deleted at `GOOPG_GATHER_PATHS=all`. No plan carries
more than one Gather on any root-to-leaf path. `subtreeHasGather` keeps a live
second caller in `partialaggupper.go:92` and is untouched.
