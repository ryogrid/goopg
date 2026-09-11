# R81 Step-0: Q4 ordered-election measurement (post-R77 seed)

## Harvest (`GOOPG_PGSHAPED_DP_TRACE=1`, `:5556`, HEAD)

All 7 DPPATH lines for Q4 (only join/upper producers fire —
legacy NLI paths are display-derived, never filed):

| producer | rows | startup | total | pathkeys | verdict |
|---|---|---|---|---|---|
| semi-splice {1} | 5 | 0.38 | 8.27 | 0 | accepted |
| semi-splice {0} | 57066 | 0.00 | 15570.66 | 0 | accepted |
| nestloop.index {0,1} semi | 57066 | 0.38 | 490456.34 | 0 | accepted |
| upper.groupagg.hashed | 5 | 491312.33 | 491312.39 | 0 | accepted |
| upper.groupagg.sort | 5 | 495535.31 | 495963.37 | 1 | accepted |
| upper.ordered.sort | 5 | 491312.45 | 491312.46 | 1 | accepted |
| upper.ordered.input | 5 | 495535.31 | 495963.37 | 1 | **dominated** |

No `PathFinalizeAgg` anywhere (decline gate clear).

## P1–P3 adjudication

- **P1 CONFIRMED**: hashed + sorted both survive grouping
  (495963.37/491312.39 = 1.0095 < 1.01 fuzz).
- **P2 CONFIRMED**: ordered offers no-sort (sorted,
  translated pathkeys — translation works on live Q4) and
  Sort-over-hashed (inputtotal=491312.39).
- **P3 OUTCOME**: hashed+Sort wins. Exact verdict path:
  totals fuzzily equal (1.0095) → startup fuzzily equal
  (495535.31/491312.45 = 1.0086 < 1.01) → **M0129-S1
  tie-break** (never returns costsEqual here — costsEqual survives only for truly-identical costs, `path.go:788`; actual-total
  direction) elects hashed+Sort (491312.46 < 495963.37) →
  no-sort dominated. Pathkeys never consulted.

## Why PG differs (same chain, different inputs)

PG's ordered pair: O_sorted startup 69094 vs O_hashed+Sort
startup 69909 → 69909/69094 = **1.0118 > 1.01** → outside
fuzz → sorted wins ON STARTUP; tie-break never fires.
(69909 is the Gather startup from the sort-off plan, a proxy
for the unobserved Sort-over-hashed startup — conservative
direction, since a real Sort would only add overhead. PG
final totals are fuzz-equal too, 70122.64/69911.66 = 1.0030,
which is exactly why PG reaches the startup arm.)
goopg's pair sits inside fuzz on BOTH axes, so the tie-break
fires and picks actual-cheaper.

The startup gap is input scale, not mechanism:
goopg sort-startup 5079 on 57066 rows vs PG ~202 on 3439
rows (N log N). Seed totals likewise (490456 vs 68892).
Selectivity explains 57066→~13k on a serial basis
(0.2317×57066 ≈ 13.2k, cf. R72's 13490 vs 57066); the 3.4k
figure is the per-worker parallel presentation, not
selectivity alone. Both trace to semi selectivity 1.0
(goopg) vs 0.23 (PG)
— R78 BLOCKED, R79 verdict-(b) — and secondarily widths
448 vs 16 (widths ratio 28× exceeds the rows ratio 16.6×; no rows-vs-width split of the 5079 sort-startup shown here — R72's 20–28× anchors apply).

## Conclusion: comparator correct as designed; NO
## implementation in this round

Every stage behaved per its documented rule (survival,
translation, offer, fuzz, tie-break). Electing sorted here
would require overriding a correct verdict — forcing, which
the goal forbids. Q4-sort is blocked on INPUTS:

1. Semi selectivity (BLOCKED: R78 machinery-faithful,
   R79 nd2-exonerated) — the 57k→3.4k rows delta.
2. Semi output width 448 vs 16 — OPEN in name only: R70 STEP-A BLOCKED with numbers and CLOSED with no cut; unblocks on DatumBytes/pushdown (neither exists). Not scheduled by this round.

Follow-up (explicit, replacing the above): this Step-0 CLOSES with a deferral-ledger row (unblock conditions: (i) DatumBytes/pushdown landing, which re-opens the widths program; (ii) a non-nd2 semi-fraction source owned by the election/firing-rule program per R71/R72 — R80 owns estimators, not selectivity, so the owner is TBD and must be named at scheduling, not assumed). RE-MEASURE this election (same harvest) is the first gate of whichever round lands (i) — the verdict arm to beat is documented above. No round is scheduled by this close-out.
Hygiene note (separate scope if ever touched, and NOT load-bearing for the PG-vs-goopg divergence — PG decides at the startup arm before any tie-break; the churn-without-gain warning below is directional, not simulated): M0129-S1
never returning costsEqual diverges from PG's
`COSTS_EQUAL → pathkeys decide`; for Q4 it is NOT
load-bearing (PG decides on startup before any tie-break),
so changing it now would be churn without a Q4 gain —
explicitly not proposed here.

## State left behind

- `:5556` stopped after harvest; clone-tpch-r79 stats
  canonical (default targets, verified pre-harvest).
- PG `:65432` untouched (zero PG contact this round).
- No code changes; tree clean.
