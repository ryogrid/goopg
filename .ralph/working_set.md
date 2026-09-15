Task: M0142-0009 — recon: plain `Nested Loop`/`Gather` join nodes estimate
single-digit rows against four-to-five-digit actuals. **DONE and COMMITTED**
this loop (banner item 4, M0142 sub-group). Root-caused to a single-table
filter bug (not a join-estimation bug) and fixed.

Files: `internal/optimizer/selectivity.go` (`rangeOpSelectivityStats` guard
relaxed: `len(Histogram)<2 && len(MCV)==0`, was `len(Histogram)<2` alone),
`internal/optimizer/rangequery_test.go` (new
`TestRangeOpSelectivityUsesMCVWithoutHistogram`),
`docs/design/0100-0149/m0142-0009-mcv-only-range-selectivity.md` (new, full
method+floor-measurement writeup), `docs/design/README.md` (indexed),
`.ralph/fix_plan.md` (0009 `[x]`, 0010 follow-up filed),
`.ralph/deferral_ledger.md` (0009 row, names 0010's resume point),
`analysis/planner-refactor-take3/c20a-estimator-census-20260915/ea-baseline.txt`
(re-pinned 122->112), same dir's new `ea-capture-20260915-post0009fix.txt`
(+`.header`) / `ea-findings-20260915-post0009fix.json`.

Key symbols: `rangeOpSelectivityStats` (selectivity.go:333, the fix),
`computeColumnStats` (operators_analyze.go:1441,1489-1495, confirmed
PG-faithful — NOT touched), `histogramOpSelectivity`'s `k<1` fallback
(unchanged, now reachable with a real MCV mass instead of a histogram).

Findings: instrumented Q25 (the recon's smallest witness) down to a
single-table repro with NO join involved: `date_dim WHERE d_moy BETWEEN 4
AND 10 AND d_year = 1999` estimated `rows=1` vs live PG 18.3's `rows=212`
(actual 214) on identically-generated SF0.25 data — the join-level qerr the
task was filed against was only a downstream symptom. Root cause:
`rangeOpSelectivityStats` bailed out on `len(Histogram)<2` alone, discarding
a column's MCV list even when (as for `d_moy`, 12 distinct values) the MCV
list legitimately covers 100% of the mass and the missing histogram is
CORRECT ANALYZE behavior (mirrors PG's own `compute_scalar_stats`), not a
gap. PG's `scalarineqsel` sums `mcv_selec` and only defaults the (here empty)
non-MCV remainder; goopg defaulted the whole clause. One-line guard fix;
verified post-fix `date_dim` filter reads `rows=211`.
Floor measurements (mandatory for M0137-M0143, all done this loop, not
deferred): TPC-H `estimate-audit -plan-only` plan-parity match=8/22
**unchanged** pre/post-fix (A/B via `git stash` on a private HammerDB-data
clone); TPC-DS `pg-plan-parity-diff.py` vs the committed `bench/tpcds/plans-pg`
fixture match=2/99 (Q9, Q41) **unchanged**, byte-identical CATEGORIES lines
despite 83/99 plan shapes changing underneath (more accurate stats, still
not full-match); TPC-DS SF0.25 regression sweep `PASS=96 MISMATCH=0`; `make
ea-ratchet` **122->112 findings, 12 FIXED** (Q10/Q25/Q29/Q34x3/Q68x2/Q69/
Q73x2/Q79 — every named witness except the still-open C2/CTE-UNION-ALL
shape), **2 NEW smaller findings** one join-level up
(`date_dim+store+store_sales`, qerr~42 in Q34/Q73, previously masked by the
leaf's much larger error) — filed as **M0142-0010**, not chased this loop.
Baseline re-pinned; `make ea-ratchet` now PASSes clean.
All throwaway servers (private clones on ports 5533/5534, `tmp/m0142-0009/`)
stopped and removed before commit — none left running.

In-flight: none. No server/gate process left running.

Next step: per the banner, item 4 (M0137-M0143 group) is still open.
Recommended pick for the next loop: **M0142-0010** (the 2 new
join-level findings this loop's fix unmasked — instrument
`estimateJoin`/`estimateNLIndexJoin` on `date_dim+store+store_sales` the way
this loop instrumented `rangeOpSelectivityStats`) or **M0142-0004b** (the
still-open C2 CTE-UNION-ALL recon). Also open at the same priority:
M0142-0005/0006/0007/0008, or M0141 S2b/S3-S7.

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...` PASS
(incl. new test); `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=34);
`scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0; `make ea-ratchet` PASS (post-repin, clean); TPC-H/TPC-DS
plan-parity floor checks (see Findings) both held; `make ralph-state-guard`
— one self-repair (same recurring benign stale-clean-exit-marker pattern
several prior loops have noted), clean after repair; pre-commit hook's
mandatory pgbench smoke PASS (all 3 transaction types, 0 failed).
