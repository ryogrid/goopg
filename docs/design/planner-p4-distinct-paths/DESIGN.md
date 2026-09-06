# C-16 (P4-07) — `create_distinct_paths`: the DISTINCT upper rel with hashed / sorted / unique-over-sorted paths

Status: **design only**. Nothing under `internal/` was modified while it was
written. Every goopg claim was read out of the tree at `280b99625` (C-15
closed); every PostgreSQL claim is cited to `./postgres` (PG 18.3, read-only
oracle). Upstream design: take3 `08-target-design.md` §7 (P4-07); dependency
take2 P1-25 (distinct estimation) landed. C-11 (registry) and C-15 (GROUP_AGG
producer, the structural template) landed — this cut is the third producer
on the same scaffolding.

Item, in two slices:

- **C-16a (planner only)**: the DISTINCT upper rel with hashed and sorted
  `PathDistinct` candidates priced against each other; the bare `&Distinct{}`
  wrapper becomes the winning path's node. No executor change: both shapes
  already run (`distinctOp` hash-dedups either way).
- **C-16b (planner-only after all)**: a `DistinctOn`-with-all-columns
  candidate over the producer-stacked Sort — unique-over-sorted with ~0
  executor LOC (`distinctOnOp` already streams; `*Distinct`/`*DistinctOn`
  already render `"Unique"`). Needs no executor gate, but gets the
  adjacent-dedup unit treatment anyway (sorted/unsorted/NULL/all-duplicate
  inputs run through the planner-emitted shape, checksummed).

Gate: take3 09 §5 P4 (PP + timing).

---

## 0. The three findings that shape everything below

**F1. There is no distinct rule to retire — only a bare wrapper.** SELECT
DISTINCT plans as `&Distinct{Child}` at two sites (`planner.go:2115`,
`:10450`), and `distinctOp` (`operators_distinct.go:30`) hash-dedups
unconditionally. Unlike C-15's three outcome-forcing rules, there is no
GUC-gated behavior to preserve and no mutation to delete: C-16a only ADDS
a choice where none existed. The blast radius is correspondingly smaller —
the risk is not "rules fight the model" but "the model picks a shape the
executor runs worse".

**F2. The executor already streams deduplication — and EXPLAIN already
calls it "Unique".** `distinctOnOp` (`operators_distinct.go:120-172`) is a
streaming adjacent-dedup over key columns with `datumKey` comparison (NULL
encodes as `"n"`, so NULLs dedup — the NULLS-NOT-DISTINCT semantics
DISTINCT needs). Crucially, BOTH `*Distinct` and `*DistinctOn` already
render as `"Unique"` in EXPLAIN (`operators_explain.go:2313-2314`,
`:2604-2610`), and PP keys on rendered text — so reusing `DistinctOn`
with keys = all output columns for unique-over-sorted needs ~0 executor
LOC AND prints exactly what PG prints. No new executor node, no new
executor gate: C-16b is planner-only (a third candidate + arm mapping),
which is why the two slices share one design doc and one gate run. The
reuse is audited per type-switch site at implementation (any pass
assuming `KeyCols ⊂ cols` must tolerate `KeyCols == all cols` — the
estimate arm already does: `estimateDistinctOnRows` over all cols IS
`estimateDistinctRows`).

**F3. P1-25 already sizes DISTINCT output.** `estimateDistinctRows`
(`cardinality.go:1584`) runs the `estimate_num_groups` analogue over every
output column, and `EstimateRows` routes `*Distinct` through it (`:76-83`).
The DISTINCT upper rel's Rows come from the same call — no new estimator,
and the B-06 CTE-stats work (which moves what these return) stays
orthogonal.

---

## 1. PostgreSQL oracle

`create_distinct_paths` (`planner.c:4816`) builds the `(DISTINCT, NULL)`
upper rel and delegates to `create_final_distinct_paths` (`:4819`) for
full paths (plus `create_partial_distinct_paths` for parallel — out of
scope; C-19 owns partial paths, and goopg's `splitAggregate`-era Gather
machinery is untouched by this cut):

- `numDistinctRows`: input rows when grouping/aggregation/HAVING precede
  (assume mostly unique), else `estimate_num_groups` over the distinct
  expressions.
- Sort-based arms, when sortable: Unique over each usefully-presorted input
  path, Unique over an explicit Sort of the cheapest input (plus the
  LIMIT-1 fast path when pathkeys go empty, and incremental-sort variants —
  both out of scope: no LIMIT-1 collapsing, C-14 blocked).
- HashAggregate arm: always considered (hashable), priced by `cost_agg`.
- Empty pathlist ⇒ `ereport(ERROR, "could not implement DISTINCT")`.
- DISTINCT ON uses the more rigorous of DISTINCT/ORDER BY pathkeys (goopg's
  `DistinctOn` node already exists with its own planning at
  `planner.go:2109` (`planSelectWithSettings`); this cut does not touch
  it — enforced by the `len(s.DistinctOn) == 0` producer gate).

Pricing: hashed-distinct = `cost_agg` HASHED with group cols = distinct
columns; Unique = input + sort + `cpu_operator` per input row (adjacent
comparison) + `cpu_tuple` per output row.

---

## 2. What goopg does today

`&Distinct{Child: out, schema}` unconditionally (both sites), priced by
`DeriveLegacyDisplayCost`'s pass-through arm (streams: startup = child's
startup — a lie for a blocking dedup, but the legacy function's documented
imprecision, not this cut's business). Output rows via `estimateDistinctRows`
(P1-25). ORDER BY above re-sorts (M0097-0046).

---

## 3. Cut C-16a — DISTINCT upper rel, hashed vs sorted

At both wrapper sites, build the `*Distinct` spec once, then (option (b)):

```
distinctRel := fetchUpperRel(reg, UpperDistinct, 0, tupleFraction)  // C-11
sizeDistinctRelFromNode(distinctRel, distinctNode)                   // §3.4 analogue:
                                                                     // Rows ← estimateDistinctRows,
                                                                     // Width/NCols/AvgVarBytes ← output
seed := newPrebuiltPath(distinctRel, childNode)                      // input rows/cost
addDistinctPaths(distinctRel, seed, distinctNode, cp, ps)
setCheapest(distinctRel)
best := getCheapestFractionalPath(distinctRel, tupleFraction)        // + empty→PlanError
node, _ = createPlanNode(best)                                       // PathDistinct arm → *Distinct
```

Candidates, at most two:

- **HASHED**: `PathDistinct` over the seed. Price = `costAgg`
  HASHED with numGroupCols = output width, numGroups = rel Rows, nAggs = 0
  (trans/final terms vanish; grouping comparisons + emit remain — exactly
  PG's hashed-distinct price). `enable_hashagg = off` ⇒ `DisabledNodes++`.
  Never offered when `len(s.DistinctOn) > 0` (see gate below — DISTINCT ON
  has its own node and planning; PG never hashes it,
  `planner.c:5195-5198`).
- **SORTED**: `PathDistinct` over `sortPathForBounded(seed, all-cols
  keys, cp, -1)`. Price = sort + `cpu_operator` per input row (adjacent
  comparison if it streamed — it does not yet, see C-16b) +
  `cpu_tuple` per output row. Same DISTINCT ON gate (a sorted `Distinct`
  over `DistinctOn` output is harmless but outside the stated blast
  radius — and under GUC-off it would be a real plan move).
- **UNIQUE (C-16b)**: `PathDistinct{Unique}` over the Sort input — same
  Sort as SORTED (shared construction), but the arm emits
  `DistinctOn{KeyCols: all output positions}` instead of `Distinct`,
  which `distinctOnOp` streams. Priced sort + `cpu_operator` per input
  row + `cpu_tuple` per output row (PG's Unique price). Offered only over
  a Sort the producer stacked (input order guaranteed by construction —
  never over an unordered seed). Inherits the Sort's `DisabledNodes`.

`Path` gains `Unique bool` — set only by the unique arm, read only by the
arm (which emits `DistinctOn` instead of `Distinct`). No strategy enum:
unlike aggregates the executor's DISTINCT form is single; the boolean only
switches the emitted node kind.

DISTINCT ON gate (both call sites): the producer runs only when
`len(s.DistinctOn) == 0`. Rationale, precisely: the `ast.go` contract SAYS
`s.Distinct` is also true when DistinctOn is set (`ast.go:894-898`), but
BOTH concrete parsers contradict it today — `select.go:152-154` leaves
`Distinct=false` ("deduplication is handled by DistinctOn"), and
`yacc_parser.go:12321-12325` mirrors that as a legacy quirk. So on today's
tree the bare `s.Distinct` check cannot fire on DISTINCT ON anyway (and the
second site declines DISTINCT ON at `:10410` before reaching `:10449`).
The gate is therefore defense-in-depth against the `ast.go` contract, not
a live-bug fix: it can never wrongly exclude plain DISTINCT, and it keeps
working if a parser starts honoring the comment. PG never hashes DISTINCT
ON (`planner.c:5195-5198`); belt-and-braces keeps that true here too.

Second-site coverage (`wrapMinMaxOrderByDistinct`, `planner.go:10450`):
the producer applies uniformly — the min/max rewrite's single-row output
makes the contest trivially one-sided (and deterministic), so no special
case; noted here so the uniformity is deliberate, not overlooked.

Empty pathlist ⇒ `PlanError` "could not implement DISTINCT" (unreachable:
hashed is always offered — the executor hash-dedups everything; defensive,
as C-15).

Sizing (`sizeDistinctRelFromNode`): Rows ← `estimateDistinctRows(schema,
child)` (F3, clamped ≥ 1); Width/NCols/AvgVarBytes ← output (the §4.3
duty; no spill arm exists for distinct, so these serve only future width
currency + the B-01c-adjacent width readers).

What may move: GUC-off DISTINCT (hash Disabled → sorted wins — PG parity:
PG builds Unique+Sort); GUC-on ties broken by startup (sorted streams in
price though not in fact — same honesty caveat as C-15's display story,
stated). Everything else keeps hashed by cost (Sort adds cost, same dedup
work). Values cannot move (same node, same executor).

Out of scope (C-16b or later): Unique node, presorted/index inputs,
DISTINCT ON (own node, own planning), partial paths (C-19), LIMIT-1
collapsing.

---

## 4. Cut C-16b — `DistinctOn`-reuse unique-over-sorted (planner-only)

No executor change: the candidate is `PathDistinct{Unique}` over the SAME
Sort the SORTED arm stacks; the arm emits `DistinctOn{KeyCols: all output
positions}` instead of `Distinct`, and `distinctOnOp` streams it
(adjacent `datumKey` compare; NULL encodes as `"n"` so NULLs dedup —
the NULLS-NOT-DISTINCT semantics, verified against
`operators_join_agg.go:4530-4533`, not against a test file that does not
exist). EXPLAIN prints `"Unique"` either way, so PP cannot tell the node
came from reuse.

Per-type-switch audit (any pass assuming `KeyCols ⊂ cols` must tolerate
`== all cols`): `estimateDistinctOnRows` (same result as
`estimateDistinctRows` — consistent by construction);
`pushSingleSideQualsIntoInnerJoinInputs` arms (position-based, unaffected);
MaybeAddGather `drivingScan` (add the `*DistinctOn`-emitted shape to the
re-verification list at implementation); EXPLAIN DistinctOn renderer
(prints keys — all-cols is a valid instance). The implementation
enumerates every `*DistinctOn` switch site with a disposition line; a site
that cannot tolerate all-cols FAILS the cut back to a real `*Unique` node
(this design bets it won't — the fallback is scoped, not open-ended).

`numDistinctRows` divergence (acknowledged, inherited from P1-25 not
introduced): PG uses *input rows* when GROUP BY/grouping sets/aggs/HAVING
precede ("assume mostly unique"); goopg always runs `estimate_num_groups`.
Same gap as P1-25 itself; out of scope.

ORDER BY merge: NO merge machinery exists (the DISTINCT sits below the
re-applied ORDER BY sort, M0097-0046) — C-16b adds a second sort where the
keys coincide. The structural win is future work, explicitly; the gate
claims no runtime for it. What C-16b buys now: the streaming form exists
in plans (unblocking the merge), and GUC-off DISTINCT stops paying hash.

Executor gate: none needed (no executor change), but the adjacent-dedup
unit treatment stands — sorted/unsorted/NULL/all-duplicate/single-row
inputs through the planner-emitted shape, checksummed on the SF0.5 gate
(a wrong bound returns wrong rows, which checksums catch).

---

## 5. What is provably unchanged vs what may move

Unchanged: emitted `*Distinct` spec (same Child position, same schema);
`MaybeAddGather` neutrality (node shape untouched — re-verify the three
functions, C-12 §5.3 drill); C-10c placement (no new evaluation site: the
Sort sits where ORDER BY sorts sit); values (C-16a: same executor work;
C-16b: adjacent-dedup == hash-dedup sets, checksummed).

May move (C-16a): GUC-off DISTINCT → Sort+Distinct (PG parity, intended);
exact-cost ties → sorted on startup (same tiebreak story as C-15's Q18 —
document per case, B-15 witness if it fires). May move (C-16b): sorted
inputs to Unique where the Sort was already going to run (ORDER BY merge
cases) — the intended win, values-checksummed.

Costs display: `*Distinct` carries no PlanCost (same as `*Aggregate` —
display stays legacy; selection uses the model). State once, do not
re-litigate per cut.

---

## 6. Gate (take3 09 §5 P4) and negative results in advance

Per slice (C-16a gates alone; C-16b re-runs all of them):

| step | instrument | pass condition |
|---|---|---|
| 1 | optimizer + executor suites | green |
| 2 | `RALPH_PRECOMMIT_SCOPE=units` | exit 0 |
| 3 | plan-gate structural | diff read line by line; moves only GUC-off DISTINCT + ties, all toward/neutral vs PG. NOTE: under default GUCs this step passes vacuously — the only behavior change needs `enable_hashagg=off` to show. The unit pins below are the real adjudicators: e2e `PlanWithSettings(hashAggSettings(false))` asserting the Sort+Distinct winner (mirrors C-15's `TestCreateGroupingPathsOffersHashedDisabled` trace test), plus a GUC-off plan-gate variant if the harness grows one. |
| 4 | plan-gate costs | re-pinned in-commit iff anything moves |
| 5 | `tpch-runner -digest` + `-diff` | VERDICT: PASS |
| 6 | TPC-DS SF0.5 sweep | PASS=95 MISMATCH=0 CKMISMATCH=0; TOTAL ±17% |
| 7 | timing | T on every moved query (1.2×); ten longest A/A |
| 8 | PP both suites | sort-strategy/aggregation-strategy diffs do not increase |

Negative results, stated in advance:

- *Sorted wins a GUC-on DISTINCT by cost.* The Sort price undercuts hash on
  a shape with no ordered input — investigate the input pricing (B-15
  witness class), do not force hash back.
- *C-16b checksum mismatch.* The Unique bound/keys are wrong (most likely
  NULL semantics vs `datumKey`, or key-column coverage). Revert; wrong-
  answer class.
- *A small-N DISTINCT regresses.* Same N-log-k crossover note as C-13a's
  design: with k close to N the heap/sort loses to the map. No executor
  bound exists — report per-query, do not invent a gate.

---

## 7. C-10c re-assert table (per-item duty, C-10c)

| PG equivalent | goopg site | assertion |
|---|---|---|
| `create_distinct_paths` sorted input Sort | producer's `sortPathForBounded` below `*Distinct` | `pushSingleSideQualsIntoInnerJoinInputs` `*Sort` arm still descends (same test shape as C-12/C-15, one node up again) |
| `Únique` input order guarantee | producer stacks the Sort itself, never trusts seed order | no `pathkeysContainedIn` shortcut for the unique arm (unlike ORDERED's input arm — C-16a HAS no input arm at all) |

---

## 8. Scope estimate

C-16a: **~150 LOC production + ~150 test LOC** (producer + arm + rel
sizing + wrapper tests). C-16b: **~0 executor + ~80 planner + ~150 test
LOC** (third candidate + arm mapping + adjacent-dedup suite — the
DistinctOn reuse, not a new node). Estimates.

---

*End of C-16 design. C-16a implements first after agent review (APPROVE),
committed (`-n`) and pushed before code; C-16b follows as its own reviewed
cut.*
