# R58 SCOPE — worker-count sizing audit: faithful arithmetic, shape-driven 4-vs-2, no sizing gap (2026-09-11)

Follows R57 LANDED (`r57-join-rows-nlead/SCOPE.md`): the N-lead
closed as a units error (5874 goopg TOTAL vs 2520 PG PER-LOOP;
normalized 5874 vs 6048, 3% apart), and the residual ~193
re-owned to worker-count selection (goopg 4 vs PG 2) + AGG_MIXED.
This round audits the first of those two. Read-only probes
(captured plans + DPPATH traces + oracle source; no code, no
servers).

Terms: DPPATH is the trace-line prefix the parallel-admission
instrument (R54 Step-0) files one path candidate per line
with; AGG_MIXED is the ledgered Hash-vs-GroupAgg strategy
preference at ~6K rows / 2 groups (the residual's second
owner, R56 REPORT §6); "§3 tie-break calibration" in §2 below
points at R55 SCOPE §3 (not this doc's §3).

## 1. Probe E verdicts

### (i) Worker-count arithmetic is faithful: NO-MECHANISM for a sizing gap

Every worker-count rule checked matches the oracle:

- Per-relation sizing: goopg's `computeParallelWorkerForRel` /
  `computeParallelWorker` / `parallelWorkerLadder`
  (`considerparallel.go:629-700`) is a faithful port of the
  oracle's `compute_parallel_worker`
  (`postgres/src/backend/optimizer/path/allpaths.c:4274`, log3
  ladder). Verified in goopg's trace, not assumed: the Q7
  block veto lines file `base rel={customer} workers=2` and
  `{lineitem}`/`{orders} workers=4`
  (`pp-start-r56.log:934-936`). The PG side of the split rests
  on code reading (`compute_parallel_worker`), not in-plan
  evidence — PG's Q7 plan only ever sizes customer as a
  parallel scan (Workers Planned: 2 via the NL chain);
  lineitem/orders appear solely as index probes with no worker
  count. Same ladder, asymmetric evidence; stated as such.
- Partial-join inheritance: the oracle's `create_nestloop_path`
  takes `outer_path->parallel_workers` ("a foolish way to
  estimate parallel_workers, but for now",
  `util/pathnode.c:2733`; the identical comment+assignment
  appears for mergejoin at `:2799` and hashjoin at `:2865-2866`)
  — goopg's outer-takes-all (`ParallelWorkers:
  o.ParallelWorkers` at `joinpathsparallel.go:243` for the hash
  twin and `:457` for the merge twin, with the `workers=`
  verdict traces at `:246`/`:460`) is the same rule on both
  the oracle and goopg sides: the partial join's workers ARE
  the outer's. (For the oracle's non-`parallel_hash` hashjoin
  variant the inner must still be complete — workers remain
  outer-derived.)
- Gather inheritance: the oracle's `num_workers =
  subpath->parallel_workers` (`util/pathnode.c:2127` for
  GatherMerge, `:2198` for Gather) — goopg's inheritance is
  implicit via `Children[0]`, per the comment at
  `gatherpaths.go:210-213` ("`num_workers` is not stored: it
  IS `subpath->parallel_workers`"), read out by `gatherCost` /
  `computeGatherRows`. `generate_gather_paths` wraps the
  cheapest partial, the same cheapest-first semantics on both
  engines (`allpaths.c:3099`, `:3083` comment header).
- Divisor: d=4.0 at 4 workers, d=2.4 at 2 (leader fraction
  `1.0−0.3·workers` when positive) — R57 §1.i, both sides.

There is no worker-count bug to fix. The 4-vs-2 needs a
different owner.

### (ii) The 4-vs-2 is shape-driven, not sizing-driven

The two Q7 winners (`/tmp/pp2/r56/pp-r56.txt` Q7,
`pp-pg-live.txt` Q7, same GUCs incl. `max_parallel_workers_per_gather`
(`mpwg`)=4):

- PG: parameterized index-NL chain — `Parallel Seq Scan
  customer` (2 workers; 62500/loop × 2.4 = 150000 ✓) →
  Index Scan orders (15/outer) → Index Scan lineitem (5/outer,
  shipdate as qpqual Filter) → 60539-row/loop outer; top Hash
  Join vs the 800-row supplier⋈n1; worker sorts; Gather
  Merge(2). Total ~78k. The 2 Workers Planned is INHERITED
  from the customer partial through the NL chain (rule §1.i).
- goopg: all-hash chain — Parallel Seq Scan lineitem (1.8M,
  4 workers) ⋈ full Seq Scan orders (1.5M) ⋈ customer ⋈
  nations; top hash vs supplier; Gather(4). Total ~149k. The
  4 Workers Planned is INHERITED from the lineitem partial
  (same rule).

Both engines apply the same inheritance rules; the counts
differ because the SHAPES differ. Worker-count selection is a
consequence here, not a mechanism — auditing it further would
be auditing the shape choice under a wrong name.

### (iii) goopg HAS the NL shapes — they lose on price, not absence

Q7-block producer census (`pp-start-r56.log:3071-4480`): 150
`join.nestloop`, 110 `nestloop.index`, 13
`index.parameterised` (the parameterized index-scan producer
exists and fires). Q7 relid map: {0}=supplier, {1}=lineitem,
{2}=orders, {3}=customer, {4}=n1, {5}=n2. The chain in-trace:

- `{3,4,5}` nation-driven customer probe: ACCEPTED @1372.02.
- `{2,3,4,5}` customer-side-driven orders probe
  (`outer={3,4,5} inner={2}`): ACCEPTED @120221.91
  (≈11990 outers × ~10/probe — PG's shape at this level,
  present and surviving).
- `{1,2}` lineitem extension (`outer={2} inner={1}`):
  DOMINATED @13308902 (≈1.5M outers × ~8.9/probe). The chain
  dies HERE, at the lineitem probe — and the hash rival
  (lineitem⋈orders-side partial @~354k) wins by default.

Per-probe, goopg vs PG (both "one execution" conventions —
goopg's `loopCount` arm pro-rates exactly so the join above
can multiply, `costindex.go:235-258`; PG's `cost_index`
likewise):

| probe | goopg | PG | run-cost ratio |
|---|---|---|---|
| lineitem by orders (`reqouter={2}`) | rows=2, total=9.20 (run 8.82) | rows=5, 0.43..1.20 (run 0.77) | **11.5×** |
| orders by customer (`reqouter={3}`) | rows=16, total=10.13 (run 9.75) | rows=15, 0.43..1.54 (run 1.11) | **8.8×** |

(Ratios are run-cost to run-cost: (9.20−0.38)/(1.20−0.43)
and (10.13−0.38)/(1.54−0.43). Totals-ratio would read
7.7×/6.6× — the R57 units discipline applies inside this
table too.)

Row estimates agree within 2.5× (16 vs 15; 2 vs 5 — the
shipdate-filter placement differs, implementation-round
detail); the gap is COST, ~9-11× per probe run. The
single-model
discipline holds: `index.parameterised` prices through
`costIndexScan` with `loopCount: s.loopCountFor(req)` (PG's
`get_loop_count`, `pathparamindex.go:378,392`), tied to the
same calibration knob as every other index cost — no forked
model to reconcile.

One surface absence, stated not buried: goopg has NO
partial-nestloop producer (zero refs in code) — PG's
2-worker partial-NL shape cannot be built, serial NL only.
But the serial NL chain EXISTS and loses at the lineitem
extension on price (13.3M dominated, 99.7% inner-probe-driven
per its `inputtotal=43435.00`), so partial-NL is NOT the
blocker on Q7 — and the arithmetic has headroom to spare:
even crediting a hypothetical partial-NL the full d=4
divisor on everything, (13308902−43435)/4+43435/4 ≈ 3.3M,
still 6×+ above the `{1,2}` partial-hash rival (516023.49)
and 9× above the `{1,2,3,4,5}` partial (354480.74). The
~9/probe inner price is undivided by parallelism under
PG-style costing, so the conclusion follows regardless of
the producer's existence. Partial-NL stays a ledgered
surface item (needed for the parallel NL shape if the probe
price ever justifies it), not this round's owner.

### (iv) Prime suspect WITH a genuine tension: the 2.0 probe calibration

`indexProbeCostMultiplier = 2.0`
(`cost_funcs.go:873-901`, calibrated C-20d 2026-09-05):
DELIBERATELY departs from PG's constants because goopg's
executor materializes the whole TID list eagerly per probe
(ch. 06 §5) — a measured execution reality, not a guess.
It buys wall-clock: Q7 15.72s → 5.86s, Q5 21.60s → 4.07s,
Q9 13.17s → 7.06s (mult=1 vs 2, SF=1 serial, values 24/24
MATCH per arm). At mult=1 the DP picks the PG-shaped NL
plans — and they run 2-3× SLOWER on goopg's executor.

The multiplier scales ONLY the heap-I/O bounds
(`costindex.go:250-258`); the btree descent (`idxStartup`,
`idxTotal−idxStartup`) and the CPU terms (`:290-299`,
divided by the parallel divisor but never multiplied) are
unscaled — so it owns at most ~2× of the I/O-bound portion,
strictly less than 2× of the total. Most of the 11.5×/8.8×
run-cost gaps is therefore OPEN (estimate, not measurement —
R59 must not treat any remainder as a measured residual).
Candidates, all read off `costIndexScanCore`'s loop-count
arm, none verified:

- correlation = 0 on every goopg index today
  (`indexCorrelationFor` returns 0 without stats) → the
  `max_IO_cost` (fully random) bound always prices; PG's
  lineitem probe may blend toward `min_IO_cost` on
  `l_orderkey` correlation;
- index geometry is derived (width/fillfactor guess,
  `estimateIndexGeometry`), not measured — wrong `indexPages`
  shifts both Mackert-Lohman inputs;
- `loopCount` pro-rating inputs (`totalTablePages`,
  `effectiveCacheSize` share) unverified against PG's;
- R1 qpqual (`numQualOps`: shipdate + unbound join clauses
  per fetched tuple) — same-currency by design, magnitude
  unchecked;
- probe rows 2 vs 5 (shipdate selectivity inside the probe
  vs PG's qpqual placement).

Decomposing 9.20 vs 1.20 term-by-term is the R59
implementation round's first test — NOT this scope.

## 2. The authorised cut: NONE — close as faithful, re-own residual

No planner change follows from probe E; there is no sizing
gap to fix. The 4-vs-2 re-owns to index-probe pricing (R59
candidate) — with the C-20d tension stated up front: closing
the plan-parity gap means pricing probes at PG's constants
while goopg's executor still pays the eager-materialization
cost, i.e. knowingly electing plans measured 2-3× slower
(Q7 5.86s → 15.72s at mult=1). The comment on the knob names
the real fix ("after the NL-probe execution work"): make
the executor cheaper, THEN lower the knob — a cross-layer
round with its own measurement, not a constant tweak here.
Quota check for R59's first test: the knob stays 2.0 until
that round measures otherwise; nothing here authorises
touching it.

Out of scope (hard, ledgered): R59 index-probe pricing audit
(needs its own scope round first); partial-NL producer
(surface expansion; NOT the Q7 blocker per §1.iii);
AGG_MIXED strategy preference (the residual's second owner);
§3 tie-break calibration; F3 procost; Q8's cost-margin gap.

## 3. Falsifiability

- Sizing: any in-trace per-rel worker assignment outside the
  oracle ladder bands, or any partial join / Gather whose
  workers ≠ its outer / subpath workers, FAILS §1.i (sizing
  gap real, re-open with the line number).
- Inheritance: a goopg partial join priced with a recomputed
  (non-outer) worker count FAILS the outer-takes-all claim.
- R59's entry bar: the 9.20-vs-1.20 decomposition must
  attribute the run-cost gap across the full term set
  (2.0-multiplied heap-I/O bounds + unscaled
  descent/CPU/startup + correlation-0 bound choice +
  geometry + loopCount pro-rating + qpqual + rows). The
  terms interact multiplicatively inside Mackert-Lohman
  (geometry shifts both bound inputs; correlation blends
  the bounds), so an attributed-interaction remainder is
  allowed — but a remainder with NO named term FAILS the
  re-ownership and re-opens "estimator gap".

## 4. Must-holds

- No code, no plans, no estimates change in this round —
  TPC-H digests, DS sweep, and parity-diff are vacuously
  unmoved (nothing to re-run; same dispensation as R57).
- TODO.md closes the worker-count item as faithful (shape
  consequence, numbers above) and names the R59 index-probe
  pricing audit as the next scope round WITH the C-20d
  tension quoted, so R59 cannot misread the knob as a free
  tweak.

## 5. Status

R58 = this audit (probe E, §1) + review in the next task.
Nothing here authorises a constant, a knob change, a
partial-NL producer, or §3 calibration.
