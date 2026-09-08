# R10 — adopt PG's mechanism for producing parallel plans

*Round 10 of `../TODO.md`. Unblocked by R9 (K17).*

## 1. The decision this round takes

goopg has two ways of producing a parallel plan:

- **The post-pass**: build a serial plan, then *stamp* a `Gather` over
  it (`stampParallelScan`, `createplangather.go:112`, a copy-on-write
  walk over an already-built tree). This is what runs today.
- **Partial paths**: build parallel candidates during the search and let
  `add_path` choose them — **PG's mechanism**. Fully implemented, and
  **switched off**: `GOOPG_GATHER_PATHS` defaults to `off`, so
  `generateUsefulGatherPaths` — the only consumer of a joinrel's
  `PartialPathlist` — reads nothing and every partial path is discarded
  (K16, R8 §1).

The knob's own comment says it was left off pending a **timing**
decision: D-05 measured three correct hash-join cost fixes each losing
10–22% of TPC-H by moving the plan off the shape the post-pass can
gather.

> *"Flipping the default is a measured decision (TPC-H A/B, timing per
> moved plan) and this slice does not take it."*

**This workstream's rule voids that objection**: a plan that matches PG
is not a regression however slow. The decision the knob was parked on is
one this goal has already made, and taking it is this round.

## 2. Why this is the goal's own instruction, not a cost tweak

The goal says plans must match *"as a result of the same cost
computation and the same planning logic"* — and forbids forcing shapes.

The post-pass is **not PG's logic**. PG has no such pass: it decides
parallelism by generating partial paths and letting the cost comparison
choose. Leaving goopg on a mechanism PG does not have, while trying to
match PG's output, is trying to reach the same answer by a different
method — the opposite of what the goal asks.

So R10 is not "a change that might improve the numbers". It is
switching goopg onto PG's algorithm. **That is the justification, and
it holds even if the category counts move sideways** — which R8's probe
suggests they partly will.

## 3. What R8's probe measured (before R9's filter)

| | off | all |
|---|---|---|
| `Parallel Hash Join`, TPC-H | 0 | 19 (PG: 9) |
| `Parallel Hash Join`, TPC-DS | 0 | 132 (PG: 139) |
| TPC-H `parallelism` | 18 | **15** |
| TPC-H `join-method` | 12 | 11 |
| TPC-H `qual-placement` | 7 | 5 |
| TPC-H `aggregation-strategy` | 10 | **14** |
| TPC-H match | 2 | 2 |

Those numbers were taken with RIGHT joins still being filed (the K17
bug). **They must be re-measured**, not carried forward — R9 changed
what the producer files, so R8's counts describe a build that no longer
exists. This is the "profile-derived bound is only valid against the
build it was profiled on" lesson, applied before making the mistake.

## 4. The change

One line: `gatherPathsMode`'s default becomes `all`. The knob stays, so
the old behaviour remains reachable for bisecting.

Not changed: any cost function, any producer's admission logic beyond
R9's filter, the post-pass itself (it still runs where no partial path
wins — that is `MaybeAddGather`'s business and C-19h's open question,
not this round's).

## 5. Gates — the heaviest of any round so far

This flips a default that changes plan *selection* on both corpora, so:

- **Values, both corpora, all-zero.** Non-negotiable and the real risk:
  partial paths route execution through the shared-hash prebuild and
  `attachParallelScan`, which the default-off configuration has never
  exercised on these corpora.
- **Parity, both corpora**, per-query, against the live references.
- **`aggregation-strategy` must be explained** before acceptance. R8 saw
  it go 10 → 14 on TPC-H. If the flip makes goopg pick *worse*
  aggregate strategies, that interaction needs a named cause — plausibly
  K12 (a partial aggregate changes what ordering is available below),
  and plausibly something else. An unexplained regression in a category
  is not accepted merely because another category improved.
- Suites: optimizer + executor.

## 6. Prediction

- `Parallel Hash Join` appears on both corpora, at counts **below** R8's
  19/132 (RIGHT joins no longer filed).
- TPC-H `parallelism` falls from 18; it does **not** reach 0, because
  K14's page-density effect drives worker-count differences that no
  planner change touches.
- **Match count: I expect 2 and 0, i.e. no movement.** These queries
  diverge in several categories at once; removing one rarely closes a
  query outright. Landing this round on the strength of §2 rather than
  on a match-count improvement is the honest framing, and saying so in
  advance stops a flat result being written up as a disappointment or a
  moved one as vindication.
- Values: unchanged. Any movement is a bug this round introduced.

## 7. Acceptance rule, stated before the measurement

Accept if: values all-zero on both corpora, no query regresses from
MATCH, and `aggregation-strategy` is either unmoved or explained.

Reject (and report) if: values move, a MATCH is lost, or the
aggregate-strategy move is unexplained. **A parity-neutral result is an
ACCEPT** — the mechanism argument in §2 is the round's basis, and the
counts are evidence about how much else is broken, not about whether
PG's algorithm is the right one to run.

## 8. Review record

Subagent delegation unavailable (`Task` not exposed; recorded since R0).
Self-review:

- **The strongest objection was sought, not avoided**: this round makes
  goopg slower on TPC-H by D-05's measurement, and could make some
  plans differ from PG *more* (the `aggregation-strategy` move). §2 is
  the answer to the first — the goal rule is explicit — and §7 refuses
  to wave past the second.
- **R8's numbers are explicitly quarantined** (§3) rather than reused,
  because R9 changed the producer between the probe and this round.
- **The post-pass is deliberately left running.** Removing it is a
  separate question (C-19h) with its own evidence, and bundling it here
  would make an unexplained plan move impossible to attribute.
