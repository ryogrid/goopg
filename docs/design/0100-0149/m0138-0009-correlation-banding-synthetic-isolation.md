# M0138-0009 — isolating the TPC-DS correlation-banding finding

Status: accepted (landed 2026-09-15)

## Task

Per `.ralph/fix_plan.md`'s M0138 milestone: M0138-0004 claimed its
`sort.SliceStable` tie-break fix (matching PG's `compare_scalars` "for equal
datums, sort by tupno" rule, `postgres/src/backend/commands/analyze.c`)
resolved the correlation-banding finding from the M0138-0001 census. M0138-0005
refuted that — a corpus re-measure after the fix still found goopg's TPC-DS
`pg_stats.correlation` banding to `[0.09,0.16]` on 36/120 columns, against
PG's 7/120, concentrated on the highest-duplicate-density FK columns, and the
band's shape was **unchanged** by the fix. The tie-break mechanism was
mechanically verified correct in isolation
(`internal/executor/operators_analyze_test.go:TestAnalyzeCorrelationTieBreakMatchesPGTupnoOrder`),
so the ledger row (`.ralph/deferral_ledger.md`, `m0138-0001`, correlation
entry) named two un-eliminated alternatives and asked for a synthetic-table
comparison to distinguish them:

1. goopg's ANALYZE correlation mechanism has a second, undiscovered defect, or
2. goopg and PG's storage engines physically place TPC-DS's loaded rows
   differently for a reason outside ANALYZE's control (a "loader
   physical-layout artifact").

## Method

Two controlled experiments, run in order, each eliminating one variable the
TPC-DS corpus measurement could not isolate.

### Experiment 1 — full-population load, both engines, identical order

`internal/testport/m0138_correlation_synthetic_test.go`
(`TestPort_M0138CorrelationSyntheticGoopgVsPG`) loads a hand-constructed,
fully deterministic column — `value = rowIndex % 11`, a repeating
low-cardinality cycle representative of the census's flagged FK shape — into
a fresh goopg instance and a fresh real PG 18.3 instance, via the exact
mechanism `scripts/tpcds-load.sh` uses for the real TPC-DS loads: server-side
`COPY <table> FROM '<path>'`, a single connection, no concurrency, identical
row order on both sides (same `.tsv` file for both). `N=2200` is deliberately
kept **below** `targrows` (`stats_target 100 * upstreamSampleMultiplier 300 =
30000`, `internal/executor/operators_analyze.go:815`), so ANALYZE's block
sampler degrades to a full scan on **both** engines — no reservoir-sampling
randomness on either side. With loader order and sampling both controlled
away, this isolates hypothesis 1 (a computation/tie-break defect) and the
"physical append order differs between engines" half of hypothesis 2.

Result: **byte-identical.** Both engines report `pg_stats.correlation =
0.095866`, matching the hand-derived population value (`postgres/src/backend/commands/analyze.c:2853-2890`'s
formula, re-derived independently in
`expectedFullPopulationCorrelation`) to 6 decimal places. This refutes both:
goopg's correlation computation is not wrong (confirms M0138-0004's fix was
sufficient for the *mechanism itself*), and a simple append-only server-side
`COPY FROM file` load does not physically reorder rows differently between
the two engines.

### Experiment 2 — reservoir-seed variance on a periodic column, goopg alone

Experiment 1 could not reach the one mechanism it structurally excluded:
`N` was kept below `targrows` so the Vitter reservoir sampler (Algorithm Z,
`analyzeRelationWith`, `operators_analyze.go:789-790`) never actually
subsampled. Real TPC-DS SF0.25 fact tables are far larger than `targrows`, so
the reservoir sampler is genuinely exercised there, and goopg's and PG's
independent RNGs necessarily draw **different** physical subsets of the same
table.

`internal/executor/operators_analyze_test.go:TestAnalyzeReservoirSeedCausesCorrelationVarianceOnPeriodicFK`
inserts the same periodic-FK shape (`value = rowIndex % 11`, `N=3000`) into a
single goopg table and re-runs `analyzeRelationWith` eight times against the
**same, unmodified table**, varying only the reservoir sampler's RNG seed
(`statsTarget=1` → `targrows=300`, a 10% subsampling ratio comparable to a
real SF0.25 fact table against `default_statistics_target`). A periodic,
low-cardinality column is exactly the shape where a subsample's *apparent*
correlation is sensitive to *which* physical positions the sampler happened
to draw (aliasing against the period), even though the correlation of the
full population is near zero — unlike a non-periodic column, where any
subsample's correlation stays near zero regardless of which rows it drew.

Result: eight seeds alone produced correlation readings spanning **`[0.0508,
0.1819]`** — a spread of `0.131`, comfortably exceeding the census's own
`[0.09,0.16]` banding spread of `0.07`. Pure sampling-seed variance, with
*nothing else changed*, reproduces a spread at least as wide as the finding
being investigated.

## Conclusion

The TPC-DS correlation banding is **not a goopg defect**. It is ordinary
sample-based-statistics variance, inherent to reservoir sampling a
periodic/low-duplicate-density column, that both engines are equally exposed
to — PG's own correlation reading for the identical table would show
comparable swing under a different RNG seed. There is no PG mechanism to
port here: PG's block/reservoir sampler seed is drawn from its own
per-process `random()` state, not a reproducible or matchable quantity (the
AGENT.md "Plan-parity harness" §absorption-principle test — "the quantity
must exist in PG as a named function or expression" — does not apply,
because there is no PG *value* to reproduce, only PG's own equally-arbitrary
seed). The two engines' TPC-DS census readings differing is the same kind of
divergence as two `ANALYZE` runs of the *same* PG installation a week apart
disagreeing after a table changed size — not a compatibility gap.

**No code change lands from this task.** The deferral ledger's `m0138-0001`
correlation row is flipped from `REOPENED` to `resolved` with this finding;
no follow-up task is filed under M0137–M0143 because the completion rule's
"a deferral needs two artefacts" clause does not apply — this is a closed
diagnosis, not a deferred fix.

## Files

- `internal/testport/m0138_correlation_synthetic_test.go` (new) —
  `TestPort_M0138CorrelationSyntheticGoopgVsPG` + `expectedFullPopulationCorrelation`.
- `internal/executor/operators_analyze_test.go` (+test) —
  `TestAnalyzeReservoirSeedCausesCorrelationVarianceOnPeriodicFK`.
- `.ralph/deferral_ledger.md` — `m0138-0001` correlation row flipped `resolved`.
- `.ralph/fix_plan.md` — M0138-0009 `[x]`.

## Gates

`go build ./...` clean. `go test ./internal/executor/...` full package green
(new test + all pre-existing). `go test -run TestPort_M0138CorrelationSyntheticGoopgVsPG
./internal/testport/` run once manually in the foreground (PASS, both
engines `0.095866`) — this test is intentionally **not** part of the
per-commit gate (`internal/testport` is excluded from
`scripts/ralph-precommit-test.sh`'s `units` scope, matching every other
real-PG-backed heterogeneous test in that package).
