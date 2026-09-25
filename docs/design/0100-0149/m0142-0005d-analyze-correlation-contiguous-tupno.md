# M0142-0005d — ANALYZE correlation: contiguous non-null positions (PG `tupno`)

Kind: impl. Parent: M0142-0005c.

Status: implemented + verified on SF0.25 clone (DONE 2026-09-19).

## The bug

`computeColumnStats` (`internal/executor/operators_analyze.go`) collected
correlation pairs as `(value, pos)` where `pos` was the raw index into the
reservoir `sample`. When the column has nulls the null `continue` leaves gaps
in `pos`, so the physical axis was **sparse** — e.g. `[1,3,5,7]` for a sample
of eight rows with every other row null.

The Pearson closed form goopg ports —

    corr = (n·Σxy − Σx·Σy) / (n·Σx² − Σx·Σx)

with `Σx = n(n−1)/2`, `Σx² = n(n−1)(2n−1)/6` — is only valid when **both**
axes are permutations of `0..n−1`. Sparse positions silently break those sum
identities, and the result can leave `[-1,1]`. Live evidence on the TPC-DS
SF0.25 clone (pre-fix ANALYZE output): **28 columns** stored
`correlation > 1.0`, including `item.i_rec_end_date = 3.6665926` and the
M0142-0005c census anomaly `catalog_sales.cs_catalog_page_sk = 1.0019597`.

## PG's semantics

`compute_scalar_stats` assigns `values[values_cnt].tupno = values_cnt`
(`postgres/src/backend/commands/analyze.c:2495`) **after** the null
`continue` — so `tupno` counts `0,1,…,nonnull−1` contiguously over the
non-null rows only. After the sort it accumulates
`corr_xysum += i · values[i].tupno`, i.e. sorted position × contiguous
non-null position. `compare_scalars` breaks equal-value ties by `tupno`
ascending, which goopg already reproduces via `sort.SliceStable` on pairs
appended in scan order (M0138-0004) — contiguous numbering preserves that:
non-null order is monotone in scan order.

## The fix

- `corrPairs` now records `pos = nonNull − 1` (the contiguous non-null
  index, PG's `tupno`) instead of the raw sample offset.
- The result is additionally clamped to `[-1,1]`. PG stores the raw quotient
  unclamped because the closed form is exact for permutations; the goopg
  clamp is numerical-hygiene only — a materially out-of-range result would
  mean the permutation assumption was violated, which is the bug this task
  removes, not a state worth propagating into `pg_statistic` / the
  `correlation²` blend in `costIndexScanCore`.

## Verification

- New unit test `TestAnalyzeCorrelationNullGappedPositions`
  (`internal/executor/operators_analyze_test.go`): ascending/descending
  null-interleaved samples return exactly `1`/`−1` (the old sparse numbering
  yields `5.0` on the same shapes), and a null-gapped mixed sample matches a
  hand-derived contiguous-position value (`−0.1`; old code gives `1.8`).
- Live census on a private SF0.25 clone (`:5533`, data dir copied from
  `bench/tpcds/runtime_goopg/data-sf025`): stored `|correlation| > 1`
  columns went **28 → 0** after re-running `ANALYZE` with the fixed binary.
  `cs_catalog_page_sk`: `1.0019597 → 0.97990215` (honestly high, now valid);
  `i_rec_end_date`: `3.6665926 → 0.33348146`.
- Gates: `go test ./internal/executor/` PASS; `RALPH_PRECOMMIT_SCOPE=units`
  PASS; `tpch-spotcheck` Q12=2/Q13=34 PASS; SF0.25 sweep PASS=96
  MISMATCH=0, plan shapes 99/99 identical (stored stats on the gate cluster
  are unchanged until its next ANALYZE, so no shape movement expected or
  observed).

## Consequence

The fix makes nullable-column correlations *honest*, which can only move
`index_pages_fetched` inputs toward truth; it does not by itself close the
M0142-0005c corpus-layout gap (TPC-H goopg heaps are genuinely clustered,
`corr=1.0` both before and after). That decision remains M0142-0005f.
