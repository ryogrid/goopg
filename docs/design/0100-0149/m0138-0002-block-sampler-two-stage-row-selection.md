# M0138-0002 — port PG's block sampler and two-stage row selection

Status: accepted (landed 2026-09-15)

## Task

Per `.ralph/fix_plan.md`'s M0138 milestone and `AGENT.md` §"Plan-parity
harness": port `BlockSampler_Init`/`BlockSampler_Next`
(`postgres/src/backend/utils/misc/sampling.c:39,64`) plus
`reservoir_init_selection_state`/`reservoir_get_next_S`/`sampler_random_fract`
as `analyze.c:1228-1290` drives them, including PG's `pg_prng` sequence, so
`analyzeRelationWith`'s sampling algorithm matches PG's two-stage method
(`acquire_sample_rows`, `analyze.c:1199`) rather than the classic Algorithm R
over every block the M0138-0001 census found in its place.

Per the owner's Q2 decision (2026-09-14, cited in the M0138 milestone entry):
**port the algorithm and let its output be whatever it is** — no constant is
tuned toward a target number.

## What changed

### The sampler (`analyze_block_sampler.go`, new file)

A line-for-line port of three PG mechanisms, kept in one file because they
are one mechanism upstream (`sampling.c`):

1. **`pgPRNGState`** — PostgreSQL's `pg_prng_state`
   (`postgres/src/common/pg_prng.c`): a xoroshiro128** generator, seeded via
   `splitmix64`. Ported instead of substituting `math/rand` because Vitter's
   Algorithm Z (`reservoir_get_next_S`) is a floating-point recurrence whose
   behavior is defined in terms of this exact generator's `[0,1)` draws
   (`pg_prng_double`/`sampler_random_fract`) — the goal is PG's *algorithm*
   run with PG's own arithmetic, not a different sampler that merely looks
   similar.
2. **`blockSampler`** — PG's `BlockSamplerData`/Knuth Algorithm S
   (`sampling.c:39-116`): selects up to `sampleCap` block numbers uniformly
   at random out of the relation's blocks, in increasing physical order,
   degrading to "every block" when the relation has `<= sampleCap` blocks
   (`sampling.c:54`'s `Min(bs->n, bs->N)`).
3. **`reservoirState`** — PG's `ReservoirStateData`/Vitter Algorithm Z
   (`sampling.c:132-226`): given `t` rows already processed, computes a
   skip-count `S` — replacing Algorithm R's one-random-draw-per-row with a
   closed-form "how many rows until the next replacement", used only once
   the reservoir has filled.

### The caller (`operators_analyze.go`, `analyzeRelationWith`)

The block loop used to be `for blk := 0; blk < nBlocks; blk++` (every
block); it is now `for blkSampler.hasMore() { blk := blkSampler.next() }`.
Inside a sampled block, the per-row selection used to be Algorithm R
(`rng.Int63n(seen+1)`, one draw per row past the cap); it is now PG's
Algorithm X/Z via `reservoirState.getNextS` plus a separate victim-slot draw
on replacement — mirroring `acquire_sample_rows`'s `if (numrows < targrows)
… else { if (rowstoskip < 0) rowstoskip = reservoir_get_next_S(...); if
(rowstoskip <= 0) { replace } rowstoskip -= 1; }` structure exactly,
including the `samplerows`-before-increment argument PG passes to
`reservoir_get_next_S` (analyze.c:1291's comment).

Seeding: PG draws two independent `uint32` seeds from the process-wide
`pg_global_prng_state`, one for the block sampler and one for the row
reservoir (`analyze.c:1225,1232`). goopg has no such process-wide generator;
`analyzeRelationWith`'s caller-supplied `rng` (seeded via `analyzeSeedFor` —
`GOOPG_ANALYZE_SEED` mixed with the relation OID, or a wall-clock draw when
unset) is consulted exactly twice to seed the two ported sub-generators
instead. This preserves the existing determinism knob (a fixed
`GOOPG_ANALYZE_SEED` reproduces the identical sampled TID set against a
static relation, needed by the plan-capture harness — see
`goopg_plan_pin_three_drift_sources` memory) while the sampling arithmetic
downstream of those two seeds is PG's own.

### `RowCount` / `pg_class.reltuples`: now an extrapolated estimate

The M0138-0001 census flagged this as a scope question for this task
(ledger row `m0138-0001-reltuples-sample-extrapolation`): `acquire_sample_rows`
computes `*totalrows` from only the sampled blocks
(`floor((liverows/bs.m)*totalblocks+0.5)`, `analyze.c:1330-1339`), so it is
part of the same ported function, not a separate decision. Resolved: checked
that nothing outside ANALYZE consumes `analyzeRelationWith`'s `RowCount`
(`find_referencing_symbols` — only the ANALYZE call sites and their tests;
VACUUM has its own separate reltuples-publishing path,
`operators_vacuum.go:259-265`, unaffected), so the switch was unconditional.
`RowCount` now degrades to an exact count exactly when PG's would (every
block sampled), matching every existing unit test that uses small tables.

`AvgWidth` changed from `totalBytes/RowCount` (exact, both terms summed over
the same full scan) to `totalBytes/liverows` (both terms now summed over
only the *sampled* live rows) — the same value in the small-table/full-scan
case, and the correct unbiased-sample-average form once block sampling
actually skips blocks (RowCount is now an extrapolation, no longer the
right denominator for a sum that only ran over the sample).

## What was deliberately not done

- **Dead-row tracking.** PG's `heapam_scan_analyze_next_tuple` also counts
  `deadrows` (DEAD/RECENTLY_DEAD tuples and dead line pointers) and
  extrapolates `*totaldeadrows` the same way as `*totalrows`. goopg's loop
  only ever branches on `TupleVisible` (visible vs skip) and
  `catalog.TableStats` has no field to hold a dead-row estimate. Nothing in
  goopg currently consumes one (no autovacuum dead-tuple-fraction feature
  yet), so adding an unconsumed counter would be speculative gold-plating —
  deferred (ledger row `m0138-0002` in `.ralph/deferral_ledger.md`) until a
  consumer exists.
- **`stadistinct`/MCV/histogram/correlation selection rules** — unchanged by
  this task; they consume whatever the sampler hands them (M0138-0003 and
  M0138-0004's scope).
- Byte-identical sampling against a real PG server is explicitly not a goal
  and not achievable without controlling PG's own process-wide PRNG seed
  (which PG does not expose): "matches PG's `pg_prng` sequence" here means
  the same *algorithm*, run with the same arithmetic, reproducible within
  goopg for a fixed `GOOPG_ANALYZE_SEED` — not cross-process bit-identical
  output.

## Verification

- `go build ./...` clean.
- `go test ./internal/executor/...`: full package green (13.4 s), including
  every pre-existing ANALYZE test (`TestAnalyzeRelationPopulatesStats`,
  `TestAnalyzeRespectsStatsTarget`, `TestAnalyzeBuildsMCVForSkewedColumn`,
  `TestAnalyzePopulatesAvgWidth`, the extended-statistics suite, …) with no
  regression — all use small tables that hit the "every block sampled"
  degenerate path, so this is primarily a no-behavior-change confirmation
  for that regime.
- New `analyze_block_sampler_test.go`: `TestBlockSamplerSelectsExactCount…`,
  `TestBlockSamplerDegradesToFullScan…`, `TestBlockSamplerDeterministic…`,
  `TestReservoirGetNextSNonNegative` pin the ported primitives directly
  (both the Algorithm-X and Algorithm-Z branches of `getNextS` execute in
  the last one). `TestAnalyzeBlockSamplingSkipsBlocksOnLargeRelation` is the
  integration pin: a 60 000-row relation with `sampleCap=300` produces
  `Pages` far above the sample cap (confirming block skipping actually
  engaged, not the degenerate full-scan path) and a `RowCount` within 20 %
  of the true count (PG's extrapolation contract, not exactness).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: green except
  a **pre-existing, already-filed, unrelated** failure
  (`internal/parser`'s `TestLockingClauseParity`/sibling AST-drift cases,
  `.ralph/fix_plan.md`'s "Manually discovered … filed 2026-09-15" entry,
  caused by `dc91bd6b7`'s `RangeVar.GroupedJoinUnaliased` field predating
  this loop) — confirmed unrelated by `git stash`-ing this task's three
  files and re-running `go test ./internal/parser/...`, which failed
  identically without them.
- TPC-H/TPC-DS plan-parity re-measurement is explicitly **not** run in this
  task: M0138's own per-task discipline (`.ralph/fix_plan.md`'s M0138
  section) defers corpus-wide re-measurement to M0138-0005, since this
  sampler port alone shifts nearly every estimate corpus-wide and needs a
  declared epoch, not a per-task partial capture.
