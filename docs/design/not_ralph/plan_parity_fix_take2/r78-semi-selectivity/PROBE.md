# R78 P0 outcome: BLOCKED — model faithful, nd2 stats diverge

Binary: `/tmp/pp2/r78/goopg-r78probe` (HEAD `c64c74c` + TEMP
env-gated probe, reverted after). Cluster: private clone
`/tmp/pp2/clone-tpch-r65` `:5533`. Query `/tmp/pp2/r71/q4.sql`
EXPLAIN ×4 total, all byte-identical to `/tmp/pp2/r77/q4-plan1.txt`.

```
R78PROBE o_orderkey=l_orderkey frac=0.7825 approx=false outerRows=57066 wouldBe=44654   (stable)
```

## What the probe proved

- The EXISTING `eqJoinSelectivitySemi` machinery runs on node-side
  inputs (probe Keys + `columnStatsByName` + relation tuples) and
  returns a measured (approx=false) nd-heuristic fraction — no new
  model needed.
- Two probe-side corrections were required to get here (both
  documented, both in the reverted temp): `tuples` must be set
  (else the `v.tuples<=0` gate takes the 0.5 default), and
  `innerRows` must be the inner RELATION rows (per-probe rows=5
  clamp nd2 to 5 → frac 0.0000).
- PG's 0.2317 re-derived from PG's own stats (live `:65432`
  `pg_stats`): `l_orderkey n_distinct=347537` absolute, no MCVs →
  `347537/1500000 = 0.2317` via the same nd heuristic. The model is
  faithful across engines.

## Why BLOCKED, not PASS (SCOPE bar was ≈0.23)

goopg's own nd2 for `l_orderkey` is fractional `-0.1956` → `1.17M`
vs PG's absolute `347k` (3.4×). Same formula, different inputs →
0.78 vs 0.23. The gap is the ANALYZE ndistinct estimator, not the
selectivity model and not the wiring. Per SCOPE: BLOCKED with the
named piece → re-scope as a stats program (ndistinct estimator
parity for high-cardinality FK columns).

## Re-scope input (not a ruling)

Wiring 0.78 now (semi rows 57066→44654, toward PG's 13490 but far)
is a direction improvement that does NOT close Q4 and spends the
rows-movement budget without the election flipping (R72's 4.26× gap
needs ~0.23-scale rows AND widths). A "wire-0.78" slice needs its
own SCOPE + adjudication; the recommended next is the nd2
estimator program, possibly paired with widths (R70 unblock —
both multiply the same election).

Temp fully reverted; optimizer builds clean; tree clean.
