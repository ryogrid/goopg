# R19 — the aggregation-strategy move explained: the flip loses the Partial/Finalize split

*Round 19 of `../TODO.md`. Findings round. Discharges the last
unexplained objection under R10 `DESIGN.md` §7.*

## 1. Re-measured, post-R9

R8's probe numbers were quarantined because R9 changed what the partial
producer files. Re-measured on the current build, TPC-H:

| category | flip OFF | flip ON |
|---|---|---|
| `parallelism` | 18 | **15** |
| `join-method` | 12 | **11** |
| `qual-placement` | 7 | **5** |
| `aggregation-strategy` | 10 | **14** |
| others | unchanged | unchanged |
| match | 2 | 2 |

The move survives R9 unchanged, so it is a property of the flip and not
of the RIGHT-join bug R9 fixed.

## 2. Which queries, and what else happened to them

Exactly four gain `aggregation-strategy`: **Q5, Q9, Q12, Q19**. Every
one of them also **loses** a category:

| query | OFF | ON |
|---|---|---|
| Q19 | join-order, join-method, scan-type, parallelism, qual-placement (5) | join-order, join-method, scan-type, **aggregation-strategy** (4) |
| Q12 | …, sort-strategy, parallelism, **qual-placement** | …, **aggregation-strategy**, sort-strategy, parallelism |
| Q5 | …, **join-method**, … | …, **aggregation-strategy**, … |
| Q9 | …, **parallelism**, rendering | …, **aggregation-strategy**, rendering |

Q19 is strictly better under the flip (5 categories → 4). The others
trade one category for another rather than regressing outright — which
the raw count 10 → 14 conceals.

## 3. The cause

Q9's aggregate, three ways:

| | aggregate |
|---|---|
| **PG 18.3** | `Partial HashAggregate` (under `Gather`) |
| goopg, flip **OFF** | `Partial HashAggregate` ✅ matches PG |
| goopg, flip **ON** | plain `HashAggregate` ❌ |

**Under the flip goopg LOSES the two-phase parallel aggregation it
already had.** That is the whole of the 10 → 14 move, and it is the
opposite of the obvious guess (that the flip made goopg choose a
*different* aggregate strategy — it makes it choose a *non-parallel*
one).

The mechanism follows from where the Gather goes. With the flip,
`generateUsefulGatherPaths` places a Gather at the **join** level, and
the aggregate is then built above it as an ordinary aggregate. Without
the flip, the post-pass places the Gather so that the existing
partial-aggregation producer (`partialaggpaths.go`, `GOOPG_PARTIAL_AGG_PATHS`,
default on) supplies the `Partial`/`Finalize` pair PG emits.

So the two parallelism mechanisms are not interchangeable at the
aggregate: goopg's partial-aggregation path is wired to the post-pass
shape, and the flip's Gather placement bypasses it.

## 4. Verdict against R10's acceptance rule

R10 `DESIGN.md` §7 requires `aggregation-strategy` to be *"either
unmoved or explained"* before the flip is accepted. It is now
**explained**, and the explanation is favourable to the flip in the
sense that matters: the regression is not a costing error and not a
wrong choice among aggregate strategies — it is a **producer that does
not fire under the new Gather placement**. That is a wiring gap with a
named cause and a named owner, not an argument against the mechanism.

It is, however, a genuine loss of PG parity on four queries, so the
honest options are:

1. **Fix first** — make partial aggregation reachable over the flip's
   Gather, then land both together, so the flip is strictly a move
   toward PG. Preferred: it avoids landing a known parity loss.
2. **Land with the regression named** — permitted by §7 now that it is
   explained, but it would knowingly move four queries away from PG on
   one axis while moving them toward it on another.

Option 1 is the recommendation, and it makes R20 (partial aggregation
over the flip's Gather) the true prerequisite for landing.

## 5. Filed

- **R20 — partial aggregation over the flip's Gather.** The producer
  exists and is enabled; the question is why it does not fire when the
  Gather comes from `generateUsefulGatherPaths` instead of the
  post-pass. Per `planner_verify_both_candidates_generated`, confirm
  the partial-agg candidate is generated before theorising about cost.
- R10's `DESIGN.md` §7 objection is **discharged**; the flip's blocker
  is now a concrete wiring gap rather than an unexplained number.
