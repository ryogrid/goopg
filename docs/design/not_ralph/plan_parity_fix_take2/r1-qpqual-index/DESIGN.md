# R1 — qpqual on the index path

*Round 1 of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Status: DESIGN + self-review (subagent delegation unavailable in this
environment — recorded honestly in §6, not elided). Implements
root-causes §7.1.*

## 0. Problem (K1, verified — not re-argued here)

Inside one `addPath` comparison the seq scan pays
`(cpuTupleCost + cpuOperatorCost × numQualOps)` per tuple scanned
(`costSeqscan`, `cost_funcs.go:192-196`) while the index scan pays bare
`cpuTupleCost` per tuple fetched (`costIndexScanCore`,
`costindex.go:243`) **and says so itself** (`costindex.go:227-242`,
names this follow-up). PG charges both sides
`(cpu_tuple_cost + qpqual) × tuples` (`costsize.c:822-830` vs `:326-329`).
The asymmetry favours index paths by the size of the qual. It explains
Q12 (merge-over-full-index-scan where PG runs filtered-seq + 31,354
probes) and is consistent with the E-21 Cut 1b evidence (full candidate
set, wrong side chosen).

## 1. Oracle transcription (read-only `postgres/`)

`cost_index` (`costsize.c:806-830`):

```c
cost_qual_eval(&qpqual_cost, qpquals, root);
startup_cost += qpqual_cost.startup;
cpu_per_tuple = cpu_tuple_cost + qpqual_cost.per_tuple;
cpu_run_cost += cpu_per_tuple * tuples_fetched;
```

where `qpquals` = restriction clauses NOT satisfied as index quals.
`cost_seqscan` (`:326-329`) does the same with the baserel's full
restriction list (`get_restriction_qual_cost`), **including startup**.
Two consequences:

1. The per-tuple term is what moves addPath comparisons; the startup
   term is ~0 for operator quals (per-node startup of Var/Const/OpExpr
   is zero — only functions/subplans accrue).
2. Full currency equality needs the startup term on BOTH sides, and
   goopg's `costSeqscan` has none either.

**Scope decision:** R1 adds the per-tuple term on the index side
(`cpuOperatorCost × numQualOps`, exactly the seq rival's currency) and
keeps startup = 0 on both sides. Rationale: the startup gap is
second-order (zero for every qual shape in either corpus — no function
or subplan appears as a qpqual there; verified by inspection during
implementation, asserted by unit test on the qual census), while adding
startup to `costSeqscan` would move EVERY plan in both corpora in one
commit — the opposite of one-variable-per-commit. Filed follow-up:
seq-side (and index-side) qpqual startup, re-open only with a corpus
qual that accrues it.

## 2. Per-site qual source (no new candidates — prices only)

`indexScanInputs` gains `numQualOps float64`: conjuncts of the rel's
local restriction list NOT satisfied as index quals. Source per site:

| site | index quals exist? | numQualOps source |
|---|---|---|
| `addOrderedIndexPaths` (`pathindexordered.go:211` + partial `:283` via `costIndexScanCore`) | no (selectivity=1.0 by construction — a clause used as an index qual makes the path parameterised) | ALL conjuncts: `len(splitConjuncts(s.relInfos[i].localFilter))`, the same expression `baseSeqScanCostInputs` counts (`joinsearch.go:480-487`) |
| `addOneIndexOnlyPath` (`pathindexonly.go:115`) | no (same full-scan construction) | same as ordered |
| `addBaseRelBitmapPaths` / parameterised bitmap (`pathbitmap.go:160`, `:577`, via `costBitmapIndexScan` + `costbitmap.go:37`) | YES (`indexClauses` — the generation gate requires them) | local conjuncts MINUS index-satisfied ones. The producer already holds both lists; count the difference. PG's `cost_bitmap_heap_scan` charges qpqual on heap tuples fetched — same rule |
| parameterised index (`pathparamindex.go:378`) | YES (probe `clauses`) | local conjuncts minus probe-satisfied ones. Index-qual *operator* cost (PG's `index_qual_cost` per-tuple on index tuples) stays OUT — see §5 |
| `planIndexScanFromWhere` (`planner.go:9837`, rule-based) | n/a | EXCLUDED: no cost competition exists there (shape match, no addPath). Dies with the legacy planner (P6). Documented, not touched |
| `costPartialIndexScan` | — | inherits via `costIndexScanCore`; no separate change |

`localFilter` reachability (K4 — call sites, not files): the ordered /
index-only / bitmap producers iterate `s.levelRels(1)` with an index
into `s.relInfos` (`pathindexordered.go:118-124` pattern), and
`baseRelInfo.localFilter` (`cardinality.go:419`) is what
`baseSeqScanCostInputs` already reads. When `hasLocalFilter` is false,
numQualOps = 0 — identical to the seq rival's count on the same rel,
which is what keeps the two in one currency even for filter-less
relations.

## 3. The change (one variable)

1. `indexScanInputs.numQualOps float64` + thread through the five
   call sites above (planner.go:9837 explicitly NOT threaded).
2. `costIndexScanCore`: `cpuRunCost := (cp.cpuTupleCost +
   cp.cpuOperatorCost*in.numQualOps) * tuplesFetched` — the identical
   expression `costSeqscan`/`costParallelSeqscan` evaluate, so the two
   rivals cannot disagree about anything but (pages, selectivity,
   correlation).
3. Bitmap heap side (`costbitmap.go` + `costBitmapIndexScan` path): same
   term on heap tuples fetched. If the heap-side function shares the
   core, no second edit; if separate, the same one-line term (verify at
   implementation — do not assume the sharing).

What does NOT change: selectivity, correlation, page math, descent
charges, SAOP terms, loopCount pro-rating, `btreeIndexAMCost`, the
`uniqueEqualityOnAllKeys` clamp, `costSeqscan` itself, any producer's
admission logic. No candidate added or removed anywhere.

## 4. Gates

- Unit: (a) per-tuple identity — index-side CPU term equals seq-side
  term for the same conjunct count (currency pin); (b) startup stays 0
  (locks the §1 scope decision); (c) filter-less rel counts 0 on both
  sides; (d) each site's numQualOps source (ordered=all conjuncts,
  bitmap/param=minus index-satisfied) with a two-qual fixture where one
  is index-satisfied; (e) partial inherits (no double charge).
- Suites: optimizer + executor green (`RALPH_PRECOMMIT_SCOPE=units`
  pre-commit bar).
- Values: TPC-H digest 24/24 MATCH vs R0 baseline arm;
  TPC-DS SF0.5 sweep PASS=95 all-zero.
- Parity (the round's subject): `pg-plan-parity-diff.py` match /
  shapediff / missing movement on TPC-H (expect Q12-class moves toward
  PG: fewer index-favouring upsets; MISSING-NODE count must not grow —
  it is partly tool blindness, so adjudicate each move, never the
  count alone) + `tpcds-plan-diff.py` same/changed on TPC-DS (expect
  moves; direction recorded, not pre-judged).
- Timing table reported per the goal rule (same plan ⇒ delta is not a
  regression); a timing move on an UNMOVED plan fails the round (it
  means something else happened).
- Re-pin nothing (estimates move by design; pins are re-cut only when a
  round is accepted — R1's report states the new counts).

## 5. Known remainders (filed, not bundled)

- **Index-qual operator cost** (PG's `index_qual_cost` per-tuple on
  index tuples) stays uncharged: parameterised/bitmap index paths keep
  a small pro-index bias. If Q12 (or any probe-heavy shape) does not
  move, this is suspect #1 — re-open with the B-15 `btcostestimate`
  evidence, not with a new instrument.
- **Seq-side qpqual startup** (§1.2): filed follow-up, needs a corpus
  qual that accrues startup to be measurable.
- **corr = 0** (R4) is orthogonal: it scales the I/O blend, not the CPU
  currency. R1 must move plans even at corr = 0 (the R0 bench state) —
  if it moves nothing, the cause is here (§5.1), not in R4.
- **Index-leaf repricing** (R2, `joinsearch.go:480`): a *different*
  hole (zero qual charge on post-restriction rows for index leaves),
  sequenced after R1 so the two charges are attributable separately.

## 6. Review record

Subagent delegation is unavailable in this environment (Task call
cancelled at R0 — recorded in TODO.md log). Review performed as a
second, adversarial pass over the tree + oracle by the author, with
falsification checks (each "confirmed" below was checked, not read):

- **Source pass** (`internal/`, at `9edf01adc`): the five call sites
  confirmed by grep (K4: `costIndexScan(` ×4 + `costPartialIndexScan`
  ×1 + `btreeIndexAMCost(` bitmap-only ×1); `planner.go:9837`
  confirmed rule-based with no addPath consumer (exclusion sound);
  `s.relInfos[i].localFilter` confirmed reachable at the ordered
  producer's loop; `splitConjuncts` confirmed the counter
  `baseSeqScanCostInputs` uses. The defect comment at
  `costindex.go:227-242` re-read verbatim — the design charges exactly
  what it says is missing, nothing more.
- **Oracle pass** (`postgres/`, read-only): `costsize.c:806-830`
  (index qpqual) and `:326-329` (seq qpqual + startup) re-read; the
  startup-on-both-sides fact is what forces the §1 scope decision
  (charging startup index-side-only would trade one asymmetry for
  another). `qpquals` = non-index quals confirmed at the `cost_qual_eval`
  call sites (indexquals go to `index_qual_cost`).
- **Correction applied by review:** first draft charged ALL local
  conjuncts at every site. The bitmap generation gate (`pathbitmap.go`
  "requires indexClauses") falsified that — bitmap/param paths HAVE
  index quals, so all-conjuncts would over-charge exactly the paths
  that already price an index side. The per-site table in §2 is the
  correction.
