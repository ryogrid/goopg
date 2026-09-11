# R79 SCOPE: ndistinct sampler Step-0 (why 1.17M vs 347k)

Lineage: R78 PROBE (BLOCKED — model faithful at 0.7825; goopg nd2
1.17M vs PG nd2 347k). The FORMULA matches (`ndistinctEstimate`,
`operators_analyze.go:1061` = Duj1, same as upstream); the INPUTS
don't. This Step-0 finds whether the divergence is the sampler, the
sample handling, or the target — then rules replicate-vs-keep.

Anchors: `l_orderkey` — goopg `n_distinct=-0.1956` (~1.17M),
PG `n_distinct=347537` absolute, both no-MCV; true distinct ≈1.5M
(goopg closer to truth; PG's undercount is load-bearing for Q4's
0.23). Sample size matches already (`upstreamSampleMultiplier=300`,
`operators_analyze.go:589`).

## P0 — sampler mechanisms (code reading, no cluster)

- goopg: sampling method (reservoir over all blocks per the
  `computeColumnStats` comment?), f1/d/n counting, stats-target
  plumbing, MCV-list construction for high-cardinality columns
  (both sides MCV-less — by rule or by threshold?).
- PG cites: block sampler (`analyze.c`), Duj1 (`compute_scalar_stats`
  `compute_ndistinct`), MCV threshold rule.
- Deliverable: a mechanism table (method, sample rows for a 6M-row
  rel at target 100, f1 semantics) + the named most-likely diverger.
  No cluster, no ANALYZE.

## P1 — nd-vs-target curves (both engines, foreground)

On the private goopg clone + live PG `:65432` (superuser,
per-column `SET STATISTICS`, restore after): ANALYZE lineitem at
targets {10, 100, 1000}, record `l_orderkey` ndistinct each side.
(goopg ANALYZE scope gap noted in CLAUDE.md — per-table ANALYZE
inside db tpch errors; if the clone cannot ANALYZE, P1 BLOCKS with
that as the named piece and the verdict is argued from P0 + the
existing single-point pair.)

## Verdict (exactly one)

- (a) replicate-PG-sampling (parity-first, stats deliberately
  PG-quirky), with the blast-radius note (every ndistinct consumer
  moves) and a gate plan; or
- (b) keep superior stats and close Q4 elsewhere (widths need
  R70-unblock; rows need another input — name it or rule it out).

No estimator code changes in this slice either way (R80 owns them).

## Explicitly out

Widths (R70 CLOSED), election rules, fuzz, cost terms, Q13-inner,
parallelism, MCV construction changes, wire-0.78. Q22 read-only.
