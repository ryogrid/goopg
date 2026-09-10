# R54 Step-2 REPORT — Q5 filed-not-won split pricing (2026-09-10)

Date: 2026-09-10. Instrument binary: `/tmp/pp2/bin/goopg-r54step2`
(tree @ `257ecd448` + uncommitted Step-2 cut; §5). Baseline binary:
`/tmp/pp2/bin/goopg-r54step2base` (clean-HEAD worktree `/tmp/pp2/wt-base`,
since removed). Same capped clone and GUCs as Step-0/1
(`/tmp/pp2/clone-tpch` :5534, `work_mem=64MB`, `mpwg=4`). Top-mode only
(`GOOPG_PGSHAPED_DP_TRACE=1`, `GOOPG_GATHER_PATHS=top`), Q5+Q9 ×2 runs
each, one fresh server per phase. Evidence (tmp-only, NOT committed):
`/tmp/pp2/r54s2/` (`q{5,9}-s2{base,t,i}.N.plan`, per-phase server logs
`start-*.log`). Driver: `run-step2.sh` (tmp-only) + one manual
inertness phase (same driver functions, gate-off).

## 1. Plan identity (instrument inert; mode effects reproduce Step-1)

| query | inert (instr gate-off vs base, ×2) | mode (base vs top) | run-stable |
|---|---|---|---|
| Q5 | 4/4 IDENTICAL | 246521.33 → 98673.84 (serial HashAgg over Gather+2 PHJ) | ×2 identical |
| Q9 | 4/4 IDENTICAL | 618239.80 → 451804.20 (Finalize→Gather→Partial+2 PHJ) | ×2 identical |

Every new DPPATH line ×2 identical (all counts below are per 2 traced
runs). The top plans are byte-identical to Step-1's `q{5,9}-s1t` plans —
the election under study did not move between rounds.

## 2. Upper costing census (new `width`/`inputtotal` fields live)

Q5 legs (rows=1, width=136 on all upper legs; join partials width=3154):

| producer | total | inputtotal (= input leg) | verdict |
|---|---|---|---|
| `upper.groupagg.gathered` (Agg→Gather→join, pathkeys=1) | 98673.84 | 98673.83 (nsGather) | accepted = WINNER |
| `upper.groupagg.gathered` (pathkeys=0) | 98673.83 | 98673.81 | accepted |
| `upper.groupagg.split` (Finalize→Gather→Partial) | 98674.16 | 98674.12 (Gather-over-partial) | accepted, loses by **0.32** |
| `upper.partialgroupagg.partial` | 97673.72 | 97673.71 (pseed) | accepted |
| `upper.groupagg.hashed` (serial over serial join) | 216735.03 | 216735.02 (seed) | accepted, out by 118k |

Q9 legs (rows=5000, width=144; join partials width per arm):

| producer | total | inputtotal | verdict |
|---|---|---|---|
| `upper.groupagg.split` | 338876.10 | 338663.60 | accepted = WINNER |
| `upper.groupagg.gathered` (pathkeys=0) | 340931.27 | 340565.74 | **dominated**, loses by 2055.17 |
| `upper.partialgroupagg.partial` | 335663.60 | 335525.34 (pseed) | accepted |
| `upper.groupagg.hashed` (serial) | 482908.23 | 482542.70 | accepted, out by 144k |

Loser totals are ON the lines (`verdict=dominated` filed, per the §2
contract — no losing candidate went unlogged). Serial arms are out by
6 figures in both queries: the seed cost is undivided while pseed
divides run by 4 — the C-19g tournament working as designed
(`partialaggupper.go:348-356`, the Q3/Q10 guard). The live contest in
both queries is split vs gathered-no-split, same pseed underneath.

## 3. Decomposition (three terms, all from the lines)

Both Gather legs share the same sub-cost (pseed); they differ only in
the tuple term. Calibration from Q9's Gather-over-partial
(338663.60−335663.60 = 3000.00 over crossedRows = 5000×4 = 20000):
`parallelSetupCost = 1000`, `parallelTupleCost = 0.1` — textbook PG
(`defaultCostParams`, `cost_funcs.go:134-135`; GUC boots
`internal/utils/misc/defaults.go:866/874`, wired via
`plannersettings.go` / `dispatch.go`; Q9's 3000.00/20000 derivation
confirms both independently).

**Q5: split loses by 0.32** (measured against the pathkeys=1 gathered
leg, 98673.84 — the pathkeys=0 leg at 98673.83 would read 0.33) =
+0.28 (Gather tuple: 1000.40−1000.12, i.e. 0.1×4 group-states vs
0.1×~1.2 input rows) + 0.03 (Finalize over 4 states vs serial agg
over ~1 row) + 0.01 (Partial pre-aggregation price). The input estimate
is ~1 row: nsGather added 1000.12 = 1000 + 0.1×1.2, and both agg legs
add ~0.01–0.04 (one trans call, not thousands).

**Q9: split wins by 2055.17** = −2040.40 (Gather tuple: 0.1×20000
states vs 0.1×40404 input rows) − 153.03 (Finalize over 20000 states
vs serial agg over 40404 rows) + 138.26 (Partial pre-aggregation
price). Same three terms, opposite sign — the election turns entirely
on input-rows vs crossed-rows.

## 4. Priced answer (what Step-2 owes the fix round)

The upper seed rows come from `EstimateRows(child)` → `seed.Rows`
(`groupingpaths.go:68-76`, the legacy display estimate of the finished
child tree), while the join search prices the SAME relation at
1834 (partial) / 7335 (serial) rows for Q5 — a ~3-order estimator gap
on one relation. The `<1 → 1` clamp (`partialaggupper.go:283-285`) is
excluded by direction: it is a floor and cannot produce 1.2, so 1.2
passes through unclamped and the seed estimate itself is ~1.2. Q5's ~1-row seed makes the Partial reduce nothing
(1 row → 1 group), the Gather tuple term favor the whole-input
crossing (0.1 vs 0.4), and Finalize cost more than the serial agg;
every term favors gathered by a combined 0.32. At the search's own
join estimate the Gather tuple term alone flips by 0.1×(1834−4) ≈ 183
and the split wins by hundreds — exactly Q9's shape (seed 40404 rows,
margin 2055). PG's Q5 parallelises the upper for the same reason: its
input estimate is large.

**Fix proposal (NOT this round):** size the upper seed (`seed.Rows`,
hence `inputRows`/`perWorkerRows`/`crossedRows`) from the search
joinrel's rows instead of the legacy child estimate — any wider
estimator reconciliation is a separate scope decision for the fix
round — with (i) PG-faithfulness: direction is toward PG (PG
parallelises Q5's upper; Q9's already-matching shape must keep
winning), and (ii) no-regress list: Q9 split keeps winning (margin
2055 absorbs estimator moves; re-measure), Q1/Q84 identical under top,
TPC-H bench shapes unchanged outside Q5. Moving any constant or the
seed source is the fix round. Widths are consistent within channel
(upper 136/144, join 3154) and name no separate term.

## 5. Instrument (trace-only, production-inert)

`pathtrace.go` `formatPathLine`: appended `width=<rel.Width>`
`inputtotal=<Children[0].Cost.Total, -1 if inputless>` at END (same
append-at-end rule as jointype/partition labels; `enumtrace.go`
ignores DPPATH entirely so no parser change). Every upper arm already
carries `Children[0]` (serial arms `Children: []*Path{input/seed}`
at `groupingpaths.go:340/358`; split chain at
`partialaggupper.go:475/505`; no-split hashed at `:402`), so
`inputtotal` is the priced input on all four shapes — with one
precision caveat: the *sorted* no-split arm's `Children` is the
Sort-above-Gather (`groupingpaths.go:409`), so its `inputtotal` is
what `costAgg` was called with (one level up), not the join-below
total proper. `-1.00` renders only for inputless paths (scan
leaves, fixtures — no upper line shows `-1` in the corpus), never a
zero that would parse as free. Pins:
`TestPathTraceRendersWidthAndInputTotal` (agg + scan cases);
`TestPathTraceRendersOuterInnerPartition` trailing pin re-pinned to
the new tail. Gates: optimizer + estimateaudit suites green (no
`-count=1`), `go vet` clean, inertness 4/4 (§1).

## 6. Step-2 exit (owed by §4 of STEP2)

Priced fix proposal delivered (§4) with magnitudes (0.32 loss = three
named terms; 2055 win = same three terms flipped), PG-faithfulness
argument, and no-regress list. Pricing numbers WERE the deliverable;
no constant moved, no seed source changed — that is the fix round.
