# R21 (index-leaf) — give prebuilt index leaves their qual charge

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md` (the R21
row). Status: DESIGN + self-review (subagent delegation unavailable —
recorded honestly in §5, not elided). **OUTCOME: DECLINED, see
REPORT.md** — implemented, unit-pinned, A/B-measured, reverted: census
proved zero index leaves in 1282 pricings across both corpora, so the
charge has no witness. Follow-up filed there (absorbed-leaf
index_qual_cost needs its own design).*

## 0. Problem (K6, re-verified at HEAD, not re-argued)

The base-rel seed (`newPrebuiltPath`, `joinsearch.go:434`) is priced by
`costSeqscan` via `baseSeqScanCostInputs` (`joinsearch.go:480`). When
the pre-search planner already chose an index, that leaf gets
`numQualOps = 0` on fallback pages/rows. Proof (instrumented, TODO K6):
TPC-H Q12's inner `Index Scan ... (cost=0.00..60475.14)` printed
IDENTICALLY before and after R1 while the Merge Join above it rose by
exactly the qpqual charge (75,015.65 = 5 conjuncts × 0.0025 × 6,001,255)
— goopg displays one cost for that node and costs the join with
another. `cost=0.00` startup is impossible from `costIndexScanCore`
(it always charges a descent), confirming the leaf never passes
through `cost_index`: **R1's charge never reached the winning scans**.

## 1. Scope: the count, not the model

`baseSeqScanCostInputs` returns fallback pages/rows + 0 quals for any
non-`*SeqScan` leaf. The fix changes ONLY the third return for index
leaves:

| leaf class | pages/rows | numQualOps |
|---|---|---|
| `*SeqScan` | baseRows/baseRelPages (unchanged) | local conjuncts (unchanged) |
| `*IndexScan`, `*IndexOnlyScan` | legacy fallback (UNCHANGED — an index leaf is the rule-based choice standing in for the relation and must not be repriced as a full sequential scan; the comment's rationale stands) | **local conjuncts (NEW)** |
| `*BitmapHeapScan` (rule-built leaves exist, `planner.go:9850`) | legacy fallback (unchanged) | **local conjuncts (NEW)** — PG's bitmap rule charges the full restriction on heap fetch (lossy-recheck), same as R1's heap side |
| everything else | legacy fallback (unchanged) | 0 (unchanged) |

Why the count-only change is principled, not just small: the fallback
rows ARE the post-restriction survivors (`initialRelRows`), and PG
charges qpqual × tuples_fetched where tuples_fetched ≈ survivors — so
quals × fallback-rows is PG's rule at this leaf, while quals × baseRows
would over-charge it. The partial twin (`considerparallel.go:567`)
reads the same resolver, so serial/partial agreement is preserved by
construction.

What does NOT change: page/row math, `costSeqscan` itself, any
producer, admission logic, the displayed-cost pipeline. No candidate
added or removed. The index-qual *operator* cost (DESIGN §5 suspect
#1) stays out — this round is the precondition for testing it, not the
test.

## 2. Oracle

`cost_index`: `cpu_per_tuple = cpu_tuple_cost + qpqual.per_tuple` on
`tuples_fetched` (`costsize.c:822-830`). The leaf executes as
index-scan + heap filter, so every fetched tuple pays the local quals —
the term is real work, not parity theater (the built plan keeps
Filter{IndexScan} shapes; nothing consumes the filter).

## 3. Gates

- Unit: (a) index leaf with N-conjunct filter prices quals × fallback
  rows (exact arithmetic pin); (b) filter-less index leaf prices
  exactly as before (zero preserved); (c) SeqScan arm untouched
  (existing tests); (d) partial twin inherits (same resolver — assert
  via `addPartialSeqScanPath` inputs or the shared-resolver comment
  + existing crossover tests green); (e) BitmapHeapScan leaf counted.
- Suites: optimizer + executor green (pre-commit bar).
- Values: TPC-H digest 24/24 MATCH vs pre-round arm; TPC-DS SF0.5 sweep
  PASS=95 all-zero.
- Parity (subject): shape verdicts both corpora, judged by CATEGORY
  movement per the roadmap (not the match count, which cannot move on
  a conjunction). Expect: Q12's inner Index Scan cost now carries
  qpqual (the `0.00..60475.14` line moves); direction recorded. The
  DISPLAYED leaf cost changing is intended — it was the uncharged
  price, not a stable pin.
- Timing table reported, not adjudicated (goal rule); a timing move on
  an unmoved plan fails the round.

## 4. Risks

- Seed-vs-candidate double counting: the prebuilt seed and generated
  index paths now BOTH carry qpqual — correct (both execute filter
  per fetched tuple), not double (they are alternative prices for
  alternative executions compared in addPath).
- If Q12 (or any probe-heavy shape) does not move after this AND R1,
  suspect #1 (index-qual operator cost) is next — re-open with B-15
  evidence, not a new instrument.

## 5. Review record

Subagent delegation unavailable (Task cancelled at R0; TODO.md log).
Review as adversarial second pass by the author:

- **Source pass** (at HEAD): `baseSeqScanCostInputs` re-read with both
  call sites (`joinsearch.go:438`, `considerparallel.go:567`) —
  single-resolver agreement confirmed, so the change propagates to the
  partial twin with no second edit. `leafBaseScan` confirmed to peel
  Filters (the count reads the same chain `extractFilterConjuncts`
  reads — consistent with R1's `localQualOpCount`, though this round
  uses `ri.localFilter` via `splitConjuncts` exactly as the SeqScan
  arm does, so seq/index currency is textual identity, not
  construction agreement). `*BitmapHeapScan` leaf existence confirmed
  (`planner.go:9850` rule-built) — included rather than left as a
  second hole. `initialRelRows` confirmed fallback rows =
  post-restriction survivors for base-table leaves (the §1
  principled-ness argument depends on this; verified, not assumed).
- **Oracle pass**: `costsize.c:822-830` re-read; qpqual-on-fetched
  confirmed; bitmap full-restriction rule re-confirmed (already cited
  in R1 design §2 correction).
- **Correction applied by review:** first draft repriced index leaves
  through `costIndexScan` (geometry + selectivity). Rejected: it
  changes pages/rows/selectivity in the same commit as the qual charge
  (unattributable), contradicts the `baseSeqScanCostInputs` comment's
  standing rationale, and the TODO row scopes "their qual charge
  instead of numQualOps = 0" — the count. The full repricing is NOT
  filed as follow-up: PG prices such a leaf as cost_index, but goopg's
  seed path is costSeqscan-shaped by design (04 §1 currency comment),
  and changing that is a different round with its own gate.
