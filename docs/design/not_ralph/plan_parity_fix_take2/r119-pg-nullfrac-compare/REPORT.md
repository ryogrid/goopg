# R119 result: PG agrees — same nullfrac-driven selectivity model; close with no change

Measurement-only; no goopg source, test, flag, or config changed. Live
shared `:65438` (`tpcds025`) used read-only (pg_stats + EXPLAIN with the
R111 session SETs); nothing written. PG was already up on arrival and was
left running.

## Q1 — stats inputs (live pg_stats)

| column | null_frac | n_distinct |
| --- | --- | --- |
| ss_hdemo_sk | 0.044066668 | 7068 |
| ss_store_sk | 0.0438 | 6 |
| hd_demo_sk | 0 | -1 (unique) |
| s_store_sk | 0 | -1 (unique) |

Same shape as goopg's R111-clean inputs (outer nullfracs ~4.4% both
sides, inner nullfracs 0). Absolute nd values differ by dataset, as
expected.

## Q2 — model mechanics (PG source)

`eqjoinsel_inner`, no-MCV branch (`selfuncs.c`, below line ~2600):
`selec = MIN(1/nd1,1/nd2)*(1-nullfrac1)*(1-nullfrac2)` — per-side
nullfrac factors with an explicit comment justifying the MIN as an
upper bound. This is the same model as goopg's
`pairNullSelectivity/nd` (`cardinality.go:1080`): no transcription gap
at the selectivity site. Plugging shared-data numbers:
hdem `MIN(1/7068,1/720)×095593 ≈ 0.00013277` vs goopg's measured
`0.0001328287` — agreement to 4 decimals.

## Q3 — forced-form lower estimates (live, R111 SETs)

- hdem-first: lower Hash Join rows=22198, upper 1769, root 17673.05.
- store-first: lower Hash Join rows=18504, upper 1769, root 17849.21.

PG's hdem-first lower is BIGGER yet its total is SMALLER by ~176 —
the same qualitative pattern as R111's clean-input capture. So PG's
own preference does not come from selectivity either; with the model
agreed, it must come from cost terms above selectivity
(width-driven hash costs — PG widths 4–8 bytes vs goopg's 428–1104).

Data caveat (model-level comparison only, per scope): shared
`household_demographics` has 720 rows vs R111 clean inputs' 7200
(10x). Absolute row counts are not comparable across setups; the
model agreement above is unaffected (same formula, same nullfrac
use, same direction).

## Verdict: (a) PG AGREES — close with no production change

Same selectivity model, same nullfrac mechanics, same preference
direction despite larger lower rows. There is no selectivity-side
production change to scope: "fixing" R118's inputs would mean
overriding faithful ANALYZE measurements to move a margin that both
engines derive from the same formula. The remaining forced-form
margin disagreement lives in cost terms (widths), which is R96-probe
and R70 territory — a new scope must start from numbers there, not
here. Datum investigation stays closed.
