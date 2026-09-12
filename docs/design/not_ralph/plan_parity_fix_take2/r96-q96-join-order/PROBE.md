# R96 P0 outcome: ATTRIBUTED — 157.5 (0.7%) raw-min margin, build-side rows

No TEMP instrumentation was needed: the existing DP trace
(`GOOPG_PGSHAPED_DP_TRACE=1`) already logs every candidate with
producer, rows/width/cost, and `add_path` verdict. One opt-in EXPLAIN
(`/tmp/pp2/r96/q96-plan.txt`, byte-identical to `/tmp/pp2/r95/q96-optin.txt`;
no code changed, nothing to revert). Relids: {0}=store_sales
(719876/w428), {1}=hdem (720/w48), {2}=time_dim, {3}=store (1/w676).

## Candidate table (serial arms; rows/width/cost = filed values)

| relset | order (outer→inner) | method | rows | total | verdict |
|---|---|---|---|---|---|
| {0,1} | ss⨝hdem | hash | 68777 | 22936.58 | accepted |
| {0,3} | ss⨝store | hash | 57322 | 22661.30 | accepted |
| {0,1,3} | (ss⨝hdem)⨝store — PG's order | hash | 5477 | 23179.21 | accepted |
| {0,1,3} | (ss⨝store)⨝hdem — goopg's winner | hash | 5477 | 23021.71 | accepted |
| {0,1,3} | others (merge arms etc.) | — | 5477 | ≥23601 | accepted/dominated |

Margin: **157.50 (0.68%)**. Both finalists survive `add_path`
(which uses `comparePathCostsFuzzily`, 1% — def `path.go:751`,
applied in dominance at `:1014-1019`); the final election
`setCheapest` (`path.go:1154`) uses exact `comparePathCosts`
(`path.go:1096`) and takes the raw minimum. PG's `set_cheapest` is
likewise exact — so PG must genuinely price hdem-first cheaper in
ITS model (or tie-then-order); the fuzz-tiebreak hypothesis needs
PG's loser price, which EXPLAIN cannot show.

## Term decomposition (goopg `hashJoinCost`, `cost_funcs.go:640`)

- Build: `(cpuOperatorCost×clauses + cpuTupleCost)×innerRows +
  inner.Total` — NO fixed table-setup term. The level-3 increments
  are therefore: store-first +360.41 = probe 0.0025×57322 (143.30) +
  output 0.01×5477 (54.77) + build(720: 9.00+79.20) + bucket-walk
  remainder; hdem-first +242.63 = probe 0.0025×68777 (171.94) +
  output 54.77 + build(1: 0.01+1.15) + remainder.
- Level-2 gap (+275.28 for building on 720 hdem rows vs 1 store row):
  ~114.55 is output-row cpu_tuple (68777 vs 57322 output rows), ~160
  is build-side + bucket-walk. Rows are NOT the diverger (final rows
  agree: 5477 vs PG's 1769×3.1≈5484).

## PG anchors (`/tmp/pp2/r95/ds-pg-live.txt` §Q96, live `:65438`, divided rows)

- PG's taken increment (hash 1 store row): **79.11** (HJ2 16099.21 −
  HJ1 16020.10, read verbatim from the live capture — not recomputed)
  vs goopg's 242.63 for the same shape — consistent term-by-term in
  structure (probe + output + build + bucket), but scale confounds
  direct comparison: PG scan costs run HIGHER (store_sales
  15258.18×3.1 ≈ 47300 full-scale vs goopg 20132.76; hdem 143.00 vs
  79.20) while widths run 35× LOWER. A naive term ratio proves
  nothing; P1(a) must do the scale-adjusted audit.
- PG-side loser price (store-first in PG's model) is unobservable
  from EXPLAIN — P1(b) conditional on obtaining it.

## Ruling: ATTRIBUTED, hypotheses ranked

1. **Build-side cost transcription** — the 720-vs-1 build asymmetry
   decides a 0.7% election; audit `hashJoinCost` term-by-term against
   `initial/final_cost_hashjoin` with scale-adjusted PG numbers.
2. **Fuzz tiebreak at final election** (R72 pattern) — valid only if
   PG's two prices tie; needs the unobservable loser price.
3. Rows/sizing: RULED OUT (0.1% agreement). Widths: NOT directly
   implicated (build term is row-dominated here); R70 stays closed.

Q22 read-only; no estimate moved (log-only existing trace).

Provenance note: `internal/` shows one untracked file,
`internal/executor/zz_probe_r66_test.go`, which predates this round
(present in tree status before any R94–R96 work; flagged as prior-R66
throwaway in earlier reviews). It is foreign WIP, untouched here per
the never-touch-foreign-WIP rule; this round changed zero tracked
files under `internal/`.
