# R70 SCOPE — hash footprint: compute-then-route (planner pruning cut vs minimize_datum dependency) (2026-09-11)

Follows R69 (NLI repriced 117k→246k-true; hash still 539k on PG's
partition — the flip belongs here) and R68 STEP-0. One round with a
compute gate up front: Step A decides, Step B executes one branch.
No code until Step A routes to the cut.

## 0. Baseline (all measured, none carried)

- Q9 L6 at HEAD (`STEP0.md`, re-verified at review): NLI-true
  ~246k (measured post-fix top 245962.63) vs PG-partition-hash 539k
  (528266.23 flipped / 539118.03 PG-orientation). PG's own join price
  ≈98k (`pg-q9.txt`: Hash Join 141225.55 minus outer Seq Scan 42814.00;
  serial, 64MB — the agg-top-minus form does not reproduce it).
  Rows TIED today (goopg 303093 vs PG 301659 inner builds — R53's
  97740 is stale serial-parallel drift, retired).
- Widths (K65/K67, standing): goopg builds full tuples (orders 9 cols
  → `EntryBytes = 48·ncols + 24 + avgVarBytes`, `hashsize`); Q9's
  flipped build ≈ 303093 × ~1100B ≈ 333MB → NBatch ~6 at 64MB; the
  PG-orientation build 1.5M × 456B ≈ 684MB → NBatch ~11. PG builds
  ~300k pruned rows ≈ 24MB → NBatch 1. K67: pruning to needed cols
  alone → ~72 B/row (still spills: 103–108MB at these row counts);
  PG-faithful 22 B/row needs `minimize_datum`'s DatumBytes work.
- `minimize_datum` state (read-only survey this round): MD-04+
  BLOCKED (take3 §8.2); datum shrink NOT landed. Only planner-side
  column pruning is actionable from here.
- Spill formula (current code, `cost_funcs.go:630-670`): build =
  `(cpuOp+cpuTuple)×innerRows + inner.Total`; `NBatch>1` adds
  `seqPageCost×innerPages` (startup) + `seqPageCost×(inner + 2×outer)`
  (run), with `spillPages(rows, ncols, avgVarBytes)` and
  `hashsize.Choose(rows, ncols, avgVarBytes, workMem)`. Footprint
  inputs are `RelOptInfo.NCols` + `AvgVarBytes` — R38's corrected
  target (its `Width` target was refuted in-review; K66).
- R38 (rejected, findings kept): executor sizes from the RUNTIME
  schema (narrowing planner width cannot under-size execution —
  hazard refuted); `considerparallel.go:611` reads `rel.Width` into
  `baseSeqScanCostInputs` with `joinsearch.go:479-488` keeping it as
  `fallbackWidth` for non-heap leaves only (plain heap scans price
  pages from physical size — the old `:567` citation and its "PG
  never does" mechanism are STALE, re-verify the fallback-only
  reachability before relying on it; adjacent, do not touch
  blindly); node-free keep-set is `neededKeepSet`
  (`narrowoutput.go:789-800`); placement between
  `relfromjoinlist.go:699` and `:707` with the base path re-costed;
  do NOT mutate `AvgVarBytes`/`ColVarBytes` (`entrywidth.go:53-56`
  fail-safe); PG order `set_rel_size` before `set_rel_pathlist`
  (`allpaths.c:322/:351`). Each re-verified (not carried) at
  implementation.

## 1. Step A — decision table (no behaviour change; one trace addition)

Recompute the CURRENT flipped-orientation terms exactly (DPPATH
startup/total/width/inputtotal + plan row counts; per-offer `ncols`/
`avgVarBytes` INSTRUMENTED trace-only, R53 precedent — never inferred:
DPPATH `width` is the R38-refuted `RelOptInfo.Width`, and R53-Slice-1
§3's residual inference was explicitly non-load-bearing; state
path-level (`pathNCols`) vs rel-level recomputation in the Step-A
notes, plus the AvgVarBytes fallback used when column stats are
absent (`entrywidth.go` fail-safe). Join each offer against its legs'
accepted DPPATH lines for both input totals (relset bits are the join
key; `inputtotal` is Children[0]-only). Then project the SAME join at
pruned widths — `NCols`/`AvgVarBytes` derived from Q9's ACTUAL
build-side keep-set (`neededKeepSet`), never K67's 72B Q12 anchor —
and at PG widths (sanity anchor):

| build | rows × bytes | NBatch@64MB | hash total | vs NLI-true 246k |
|---|---|---|---|---|
| current | 333MB | ~6 | 528266 (measured) | loses |
| pruned (Q9 keep-set derived) | COMPUTE | COMPUTE | COMPUTE | route iff §2 bar met |
| PG (22B) | 6.7MB | 1 | recompute (R53-Slice-1 shape STALE) | anchor only |

**Route rule (binding, pre-registered before computing):** land the
pruning cut iff pruned-hash beats NLI-true by MORE than the Step-A
measured residual/noise band AND the win survives NBatch≥2 under ±10%
row perturbation (a spill-line tie is not a win — K22); the Step-A
reviewer records the bar and the pass/fail against it in the Step-A
sign-off artifact (required enforcer — §4). Else close with a
dependency statement on `minimize_datum` (numbers attached, no code).
Landing a width cut that leaves Q9 unmoved churns shapes for nothing
(R38's own verdict) — forbidden here.

## 2. Step B — the pruning cut (conditional on Step-A routing)

Scope: narrow `RelOptInfo.NCols`/`AvgVarBytes` (and ONLY those —
never `Width`, never the executor-facing stats) for join-build
inputs to the needed-column set, at R38's placement, with base-path
re-costing; spill geometry re-derives from the narrowed inputs by
construction (no constant touched). Out: executor changes, datum
representation, `Width` semantics, the `considerparallel.go:567`
adjacent bug (filed separately if confirmed).

Predictions (falsifiable; K22-guarded — mechanism, not scoreboard):
- P0: Step-A table with exact current terms + pruned projection +
  route verdict (cut xor blocked-with-numbers; inventing either fails
  the round).
- P1 (cut branch): Q9's top becomes a hash join beating NLI-true by
  the Step-A margin (orientation adjudicated, not predicted — both
  orientations recompute and the DP picks min).
- P2: values 24/24 + sweep all-zero (width work must not change
  answers; executor sizes from runtime schema is WHY).
- P3: every moved plan adjudicated toward PG (sort footprints, Gather
  transfer costs, other hash joins all read width — K65's blast
  radius — expect movement; R10-§7 explained-not-outweighed; no
  unexplained away-move).
- P4: NO Q9-MATCH prediction (conjunction rule — join-order/shape
  residuals persist; judge by the hash-vs-NLI contest, never the
  match count).

## 3. Sibling audit

- `hashsize.Choose` shared with executor `buildGeometry` (P5.7-a):
  planner-side narrowing must not change what the EXECUTOR builds
  (it sizes from runtime schema) — else cost and execution disagree
  and the pin that pairs them must fail loudly. Name that pin in the
  implementation gates.
- `entrywidth.go` fail-safe, `considerparallel.go:567`, R63-#2
  (`soleBaseScan` conservatism), K58/K59 thresholds (`nliMaxOuterRows`,
  `memoizeMinOuterRows` — widths move rows across them; some shape
  changes will be heuristic-driven per K59, adjudicate as such).
- Trace channels untouched.

## 4. Gates (all FOREGROUND)

- Step A: decision table + route verdict, reviewed WITH this scope
  (the review below covers the route rule; Step-A numbers get their
  own review before any cut — two review gates, one round).
- Cut branch: `go test` suites (`optimizer`, `executor`,
  `testutil/estimateaudit`; new width pins pre/post; no `-count=1`
  — the trace-touch Malformed discipline makes estimateaudit
  load-bearing); `go vet`; spotcheck rule; values A/B; explain A/B +
  pp both refs (P3, with the NCols/AvgVarBytes reader set enumerated
  in the gates: memoize spill gates, sort-run cost, NLI ndistinct
  default, joinrel propagation, plus index-only/bitmap arms checked);
  DS sweep private lane; plan-gate triage; `REPORT.md`; review;
  commit (explicit pathspec, `-n`) + push.
- Blocked branch: dependency statement + close-out commit (docs-only).

## 5. Ledger (carried)

Slice (b) conditional status resolves here (one way or the other —
no third carry); minimize_datum dependency either way; R51 items 2–3;
R52 §4.2; R54 follow-ups; #6/R61-#4/(b) watches; R63-#1/#2; K58/K59.
