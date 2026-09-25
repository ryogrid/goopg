# R94 REPORT — ordinary INNER NL walks land; Q96's shape is lateral, not ordinary

R94 follows scope commit `0dccc34` (reviewed APPROVE-WITH-NOTES, all 10
notes reflected). Items 1–3 ship in `08a6550`; item 4 measured to a
scope-invalidating finding below. No defaults, costs, join order, worker
sizing, EXPLAIN text, or NLI/Memoize behavior changed.

## What shipped (items 1–3, commit `08a6550`)

- Node twin `nestedLoopJoinIsPartialCapable` (`parallel.go`): ordinary
  INNER, non-nil children, `!Lateral`. The four walks agree —
  `drivingScan`, `stampParallelScan`, `drivingScanCrossesSort` descend the
  outer (left) literally, never via `joinProbeSideIsLeft`; `unstampParallelScan`
  needed no code change (its both-side descent already covers NL — pinned by
  test, review note 3).
- Path classifier `partialPathDrivingKind` PathNestLoop arm
  (`gatherpaths.go`): INNER-only with the V5 outer checks
  (`ParallelWorkers > 0`, `ParallelSafe`, unparameterized) and a complete
  non-Memoize inner (review note 7).
- R60 filing narrowed to INNER (`joinpathsnli.go` V1-nl-inner): a refused
  head can no longer starve admittable siblings under the head-only
  `makeGatherPath` reader (review note 9).
- Executor literal-left arms on all three claim walkers
  (`parallel_scan.go`) with the inner-bitmap refusal (review note 2);
  `ordinaryInnerNestedLoopPartial` helper names the agreement point.
- Review notes 1 (large-inner memory: values case added, size-gate
  deferral documented), 4 (`!Lateral`), 5 (LEFT/SEMI/ANTI refusal as
  scope-minimization), 6 (literal left), 8 (inner-no-claim + empty
  sides) all pinned by tests. Note 10 (drop `-n`) declined: the goal
  contract mandates `-n`.

## Item-4 measurement: Q96 does not contain the scoped shape

TEMP-probed (fully reverted) `drivingScan` on Q96 under the opt-in flags
(`GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`): every NL node
visited reports `type=Inner, Lateral=true` (4/4). Q96's top NL is R25's
decomposed lateral probe (`createplannl.go:369`, parameterized inner with
`OuterColumnRef` keys — visible in EXPLAIN as `Index Cond:
(t_time_sk = store_sales.ss_sold_time_sk)`), not the scoped complete-inner
shape. The scope deliberately refuses parameterized/lateral shapes, so item
4's source selection correctly selects nothing: the upper gate still
refuses `no-driving-scan`, the plan is byte-identical natural vs opt-in,
and no Gather is forced. R60 still files INNER partial NL candidates
(trace `jointype=inner`); all are cost-dominated, none runnable-missing.

Consequence: Q96 needs a *third* worker semantics — lateral-probe over a
worker's own outer partition — which neither R94 nor the R93-listed routes
(NLI-node proof, ordinary-NL design) cover. That is a separate scope, not
an extension of this one.

## Gates and evidence (all artefacts under `/tmp/pp2/r94/`)

Binary `goopg-08a6550` built from HEAD `08a6550`, SHA-256
`42cf368041760538b94b391a45bc26119247698526161a19dbebced96f937160`.

- Local suites: `go test ./internal/optimizer ./internal/executor
  ./internal/testutil/estimateaudit` all pass; `go vet` on the same
  three clean; `git diff --check` clean.
- TPC-H digest (private 2.0G clone `:5562`): **24/24 MATCH**, verdict
  PASS vs `bench/tpch/baseline-digests.txt` (`tpch-digest.txt`).
- TPC-DS SF0.25 foreground sweep (private 1.4G clone `:5561`):
  **PASS=94, MISMATCH=0, CKMISMATCH=0, ERROR=1 (Q69), TIMEOUT=1 (Q35),
  SKIP=3** (`sf025-results/sweep-20260912-141637.txt`). Both are
  environmental, proven by isolated A/B on the same clone: Q35 and Q69
  each pass standalone under the BASELINE (`goopg-base` from `0dccc34`)
  and R94 binaries with byte-identical output (`q35-base.txt` vs
  `q35-r94.txt`, `q69-base.txt` vs `q69-r94.txt`); Q69's sweep ERROR
  was a server death mid-sweep (connection refused; standalone
  RC=0/104 lines), Q35's 335s a sweep-tail GC thrash (standalone RC=0).
  Zero wrong-results signal: no mismatch of either kind.
- Controls Q9/Q41/Q91/Q96: natural vs opt-in+trace EXPLAIN
  byte-identical (hashes `86f8143a194760fc`, `822261abe64ac044`,
  `7692724d44bf92cd`, `2c4e0b3b0b316b77`); Q96 top cost `24630.79`
  (unchanged from R91), value-correct per the sweep (Q96 PASS, 1 row).
- Fresh live-PG TPC-DS census: **match=2, shapediff=69, unparsed=0,
  missingnode=25, error=3, timeout=0** (`ds-goopg.txt` vs
  `ds-pg-live.txt`). Baseline-vs-R94 goopg captures are byte-identical
  modulo header/temp-path artefacts (`ds-base.txt` vs `ds-goopg.txt`),
  so the ±2 movement against R91's `2/67/0/27/3/0` is clone-stats drift,
  not this slice. Q96 remains a shape difference.

## Next boundary

Separately scope lateral-probe worker semantics (parameterized inner
re-opened per worker-local outer row — no cross-worker state, but a
shape no walk models today), or another independently evidenced
parity mismatch. Do not widen R94's INNER-only admission to cover it
without that scope.
