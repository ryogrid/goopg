# R47 — Price NLI/semi probe executions; teach the ordered rel sorted aggregates (K101)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Aggregation-strategy + sort-strategy axes. Chosen by nearest-miss
ranking: TPC-H Q4 differs on `[aggregation-strategy, sort-strategy,
qual-placement]` with NO join-order divergence — the nearest TPC-H
miss — and all three divergences sit in the upper rel.*

## 0. Baseline (R46 `de85a10`, canonical data, pinned env)

- TPC-H: `match=1 shapediff=19`,
  `join-order=20 join-method=11 scan-type=13 parameterisation=6
  aggregation-strategy=16 sort-strategy=10 parallelism=15
  qual-placement=4 rendering=7`.
- TPC-DS: `match=1 (Q9) shapediff=69`,
  `join-order=95 join-method=66 scan-type=74 parameterisation=35
  aggregation-strategy=79 sort-strategy=83 parallelism=89
  qual-placement=12 rendering=33`.

Q4 (goopg vs PG fixture):

```
goopg: Sort -> HashAggregate -> NestedLoop Semi (JoinQual + stray true)
PG:    GroupAggregate -> Sort -> NestedLoop Semi (probe-side Filter)
```

## 1. Root cause (K101, two halves)

**Half A — NLI/semi inner executions unpriced.** Q4's grouping
tournament (`createGroupingPaths`, `groupingpaths.go:46`,
`setCheapest` at :90) prices hashed vs sorted off the child's
display cost as seed (`legacyDisplayCostOf`, `:73-76`). The child
is a `NestedLoop Semi Join` over 57066 outer probes, priced at
**1141 total = 0.02/probe**. PG prices the same subtree at
**192514 (3.3/probe)**: `cost_nestloop` charges the inner index
execution per outer row. goopg's `DeriveLegacyDisplayCost`
(`plancost.go:116`) `default:` arm sums each child ONCE
(`legacyDisplayChildren` returns both join sides; no
per-outer-row factor) — and the probe child's own display cost
(0.05, uncosted legacy index) contributes nothing either.

Measured consequence (scratch unit probe of `costAgg` with both
input scales): with goopg's seed (~1k) hashed wins 4.6x; the sort
(4651 on 57k rows) can never compete. The tournament machinery
is CORRECT — with PG-scale inputs the gap is 0.5%, inside the
1.01 fuzz where `setCheapest`'s pathkeys tie-break
(`path.go:1141-1147`) picks the sorted path. The inputs are
wrong, not the comparator. (K26 §9.2's "costing remains" is this.)

Quantitative bar (honest, load-bearing): with per-probe pricing
at PG levels (~3.3), seed ≈ 190k; hashed adds ~285 (trans+hash on
57k rows), sorted adds ~sort+trans+cmp (~5k at width 448).
Ratio ≈ 195/190 ≈ 1.025 — OUTSIDE the fuzz without help. The
semi output width (448 vs PG 16) inflates the sort further.
Step 0 (below) measures the real numbers; if the bar is missed,
the round re-scopes to mechanism-only (probe pricing lands,
Q4-match deferred) rather than forcing shapes.

**Half B — the ordered rel cannot see sorted-aggregate order.**
Even if sorted wins grouping, the top Sort stays: with
`enable_hashagg=off` the plan is
`Sort -> GroupAggregate -> Sort -> Semi` (verified live) —
PG has NO top sort (GroupAgg output order satisfies ORDER BY).
`inputNodePathkeys` (`upperorderedinput.go:150`) handles
Sort/Filter/Limit but has NO `*Aggregate` arm, so the ORDERED
rel (`createOrderedPaths`, `upperordered.go:64`) never sees the
group-key order and always stacks a Sort. PG's groupagg paths
carry pathkeys; goopg's sorted PathAgg HAS Pathkeys
(`groupingpaths.go:399`) but the reader drops them at the
aggregate boundary.

## 2. Change

**(a) Price inner executions in the legacy join display cost**
(PG `cost_nestloop` shape: `outer_total + inner_total ×
outer_rows`, single execution model; Memoize-dedup and
rescan-discount refinements are named follow-ups, not this
round). This changes every legacy NLI/semi/anti display cost
(estimates column only — N1-verdict-neutral) AND every tournament
seed built on one (planning — the point). Precedent for display
costs feeding plans: the grouping seed bridge itself.

**(b) `case *Aggregate` in `inputNodePathkeys`**: a sorted-strategy
aggregate preserves its group-key order → emit group-key
pathkeys (in the node's own output coordinates, per the file's
two rules); all other strategies → nil (today's behavior).
Tiny, bounded, with a unit pin.

Out of scope for this round (named): qual-placement (probe
Filter vs JoinQual — NLI probe construction) and the stray
`Filter: (true)` (unnest leftover) → R48; join-method costing
(Q84/Q96); K100; width narrowing (448 vs 16 — observed; the bar
math may recruit it later, not now).

## 3. Success tests (all must hold)

1. Unit pins (before corpus A/B): hand-computed NLI/semi display
   cost with probe executions (outer × inner, PG-shape numbers);
   Aggregate-arm pathkeys pin (sorted → group keys; hashed →
   nil).
2. Q4 `aggregation-strategy` + `sort-strategy` fall (stretch:
   full MATCH modulo qual-placement — the bar in §1 decides;
   mechanism-only landing is an honest outcome, recorded not
   forced).
3. Corpus: `aggregation-strategy`/`sort-strategy`/`join-order`
   move favorably with ZERO EXTRA flips (per-query census);
   shape-delta reported alongside (R35/K50).
4. Standard gates: units, suites, TPC-H spotcheck, SF0.5 sweep
   `MISMATCH=0`-class, byte-guard A/B both corpora.
5. `match` count is NOT a criterion (conjunction rule).

## 4. Non-goals / follow-ups (named, not owned)

- R48: semi JoinQual placement + `Filter: (true)` drop.
- Q84/Q96 join-method costing (`chooseInnerJoinAlgo` unit-row
  model vs PG's full join costing).
- K100 (executor reg*[] comparison); TPC-H fixture parallelism
  (owner decision); K26 implied-equalities re-wire (candidate
  half, still open); width narrowing.
- NLI rescan-discount/Memoize-aware probe pricing refinements.

## 5. Evidence archive

- `/tmp/pp2/oc-tpch-r46c.txt` (Q4 before-shape),
  `bench/tpch/plans-pg/Q4.txt` (reference)
- `enable_hashagg=off` probe (sorted candidate builds; top Sort
  stays): §1 Half B verification
- Servers `:5553`/`:5554` (r46 clean binary); foreign `:5545`
  untouched.
