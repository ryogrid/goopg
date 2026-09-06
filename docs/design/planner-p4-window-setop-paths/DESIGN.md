# C-18 (P4-09) — `create_window_paths` + set-operation paths: the WINDOW and SETOP upper rels

Status: **design only**. Nothing under `internal/` was modified while it was
written. Every goopg claim was read out of the tree at `418fa6cb6` (C-16
closed); every PostgreSQL claim is cited to `./postgres` (PG 18.3, read-only
oracle). Upstream design: take3 `08-target-design.md` §7 (P4-09). C-11
(registry) and C-12/C-15/C-16 (producers, the structural template) landed —
this cut is the last two producers on the same scaffolding, and the one
that completes C-17's "every upper rel".

Item, in two halves sharing one file and one gate run:

- **WINDOW**: the WINDOW upper rel with a single `PathWindow` candidate
  per window group — input + priced window work — selected trivially.
- **SETOP**: the SETOP upper rel with a single `PathSetOp` candidate per
  set-operation node — inputs + priced set work — selected trivially.

Gate: take3 09 §5 P4 (PP + timing).

---

## 0. The honest finding, stated first

**F1. Neither half has a choice to offer — and that is the design, not a
gap.** The window executor sorts by PARTITION BY/ORDER BY internally
(`operators_window.go:14`, permutation over precomputed keys); the set-op
executor runs its single form per node. Above the seam the inputs are
finished Nodes with no pathkeys, so no presorted/index/order-aware variant
can be built (C-14 Incremental Sort is BLOCKED with no executor
counterpart; re-reading that sentence after C-14 lands is the resume
point). A single-candidate rel still does three real jobs: (i) the work is
priced for the first time (window/set terms below — selection-neutral
today, load-bearing the day a second candidate exists); (ii) every upper
rel now exists, which is exactly C-17's remaining scope (verify + close
after this lands); (iii) `set_cheapest`/`getCheapestFractionalPath` run on
uniform rails (no special cases for "the rel with no producer").

**F2. The prices are selection-neutral AND display-invisible — both stated
so the gate cannot be misread.** `*WindowAgg` and `*SetOp` carry no
PlanCost (no `planCostSetter`: EXPLAIN recomputes legacy display), exactly
as `*Aggregate`/`*Distinct` before them. The gate therefore asserts shape
identity (same nodes) + selection neutrality (single candidate = same
winner), never cost movement. A reviewer asking "where do the new prices
show up?" has found the design working as specified.

---

## 1. PostgreSQL oracle (structure only — no arm is ported)

- `create_window_paths` (`planner.c:4533`): `(WINDOW, NULL)` rel; one
  `WindowAggPath` per surviving input path (cheapest + presorted ones),
  each stacking Sort/Incremental-Sort as needed; `set_cheapest`.
- Set-operations: `Append` (UNION ALL), `Unique` (UNION), `HashSetOp` /
  `SetOp` (INTERSECT/EXCEPT) paths over append children, each priced
  (child costs + per-row terms), `set_cheapest`. goopg collapses all of
  these onto its single `*SetOp` executor form (see §3).
- `create_one_window_path` prices input + sort + window eval; set-op
  paths price children + comparison/hash terms. The goopg ports (§3) keep
  the term SHAPE (input + per-row work + per-output emit) with flat
  `cpu_operator` rates, as C-15's F3 did for aggregates.

---

## 2. What goopg does today

- Window: `buildWindowStage` (`planner.go:6699`) chains one `*WindowAgg`
  per spec group (partition+order resolved, `stampWindowInputTarget`
  keys-only). No Sort inserted (executor sorts internally). Priced by
  `DeriveLegacyDisplayCost`'s pass-through arm (streams — wrong for a
  blocking sort-then-emit node, but legacy imprecision, not this cut).
- Set-ops: `*SetOp{Left, Right, Op, All}` at three sites
  (`planner.go:1047,3480,3531`) + `wrapSetOpSortLimit` (`:681`) stacking
  Sort/Limit above. Priced pass-through likewise.

---

## 3. The cut

One new file `windowsetoppaths.go`, two producers, two path kinds, two
arms — all single-candidate:

```
winRel := fetchUpperRel(reg, UpperWindow, 0, tupleFraction)   // C-11
sizeWindowRelFromNode(winRel, windowNode)                      // Rows ← child rows (window preserves
                                                               // cardinality); Width/NCols/AvgVarBytes ← output
seed := newPrebuiltPath(winRel, childNode)                     // input rows/cost (C-12 door)
addWindowPaths(winRel, seed, windowNode, cp)                   // ONE PathWindow
setCheapest(winRel)
best := getCheapestFractionalPath(winRel, tupleFraction)       // + empty→PlanError
node, _ = createPlanNode(best)                                 // PathWindow arm → *WindowAgg (same spec)
```

- **WINDOW candidates (one per spec group, at the build site inside
  `buildWindowStage`'s groups loop — the group IS the unit PG prices):**
  `PathWindow` over the seed. Price (`costWindow`): input total +
  internal-sort term `costSortRun(cp, rows, nkeycols, 0, -1)` over the
  partition+order key count (the executor REALLY sorts — same honesty as
  C-12's Sort pricing, just housed inside the window price) +
  `cpu_operator` per row per window func + `cpu_tuple` per output row.
  `Path` gains NO strategy field (one form — the C-16b lesson: no payload
  without a second consumer); the arm copies the spec over the built
  input (C-15's copy, minus Strategy which does not exist).
- **SETOP candidates (one per `*SetOp` node, at all three sites):**
  `PathSetOp` over TWO seeds (left+right prebuilt paths — the only
  two-input upper candidate in the tree; `Children: [lseed, rseed]`).
  Price (`costSetOp`): leftTotal + rightTotal + `cpu_operator` per input
  row both sides (+ hash-compare terms for non-ALL — flat rate, named as
  interim per F3 precedent) + `cpu_tuple` per output row (output rows =
  left+right for ALL, else the smaller side — PG's UNION-size heuristic
  simplified and NAMED; estimates here are wild anyway, B-area).
  The arm emits the same `*SetOp` over the two built inputs.
- **B-01c stamps**: `stampWindowInputTarget` stays at its site, running on
  the emitted node (same ordering rule as aggregates).
- **Empty pathlist** ⇒ `PlanError` ("could not implement WINDOW" /
  "could not implement set operation") — unreachable (single candidate
  always offered), defensive as C-15/C-16.

Out of scope: presorted/incremental window inputs (C-14 BLOCKED — resume
point named twice so it cannot be missed); multiple window-path shapes;
partitionwise anything; DISTINCT ON interplay (none — different nodes);
partial paths (C-19).

---

## 4. What is provably unchanged vs what may move

Unchanged: emitted node specs (same fields, same children positions);
`MaybeAddGather` neutrality (same C-12 §5.3 drill — re-verify the three
functions at implementation); C-10c placement (no new evaluation site —
the producers introduce no Sort/Filter/Join, only the rel around existing
nodes); values (same executor work, bit for bit).

Moves: NOTHING observable — single candidate, invisible prices, same
nodes. The gate asserts exactly that (shape identity is the PASS
condition, not a disappointment). Any diff at all is a defect.

---

## 5. Gate (take3 09 §5 P4) and negative results in advance

| step | instrument | pass condition |
|---|---|---|
| 1 | optimizer + executor suites | green |
| 2 | `RALPH_PRECOMMIT_SCOPE=units` | exit 0 |
| 3 | plan-gate structural vs pre-cut capture | 22/22 MATCH (any DIFFER is a defect — no moves exist by construction) |
| 4 | plan-gate costs | 22/22 MATCH (invisible prices — same) |
| 5 | `tpch-runner -digest` + `-diff` vs pre-cut binary | VERDICT: PASS |
| 6 | TPC-DS SF0.5 sweep | PASS=95 MISMATCH=0 CKMISMATCH=0; TOTAL ±17% |
| 7 | timing | ten longest A/A (no moved queries exist; A/A only) |
| 8 | PP both suites | identical before/after (any delta is a defect) |

Negative results, stated in advance:

- *Any plan-gate DIFFER.* By construction there is no mechanism for one:
  go straight to the producer call sites (a skipped vs wrapped shape).
- *Timing moves with no shape change.* Not C-18 (same executor work).
  Re-run the baseline (late-session drift ~1.7%).

---

## 6. C-10c re-assert table (per-item duty, C-10c)

| PG equivalent | goopg site | assertion |
|---|---|---|
| `create_window_paths` input (no new quals) | producer wraps, never inserts | no Sort/Filter/Join introduced ⇒ C-10c pass input identical — assert the emitted subtree contains exactly the pre-producer nodes (pointer walk, not behavior) |
| set-op children | two-seed structure | same: no node introduced between the seeds and the SetOp |

---

## 7. Scope estimate

**~150 LOC production + ~120 test LOC.** Two producers + two arms (each
~40 lines with the doc comments this repo writes), two cost functions
(~30), call-site wiring (3 SetOp sites + window loop), pointer-walk
tests. Estimate.

---

*End of C-18 design. Implementation starts after agent review (APPROVE),
committed (`-n`) and pushed before code. C-17 (tuple_fraction end-to-end)
closes immediately after: with WINDOW + SETOP rels existing, "every upper
rel" is enumerable and the verification is a census, not a build.*
