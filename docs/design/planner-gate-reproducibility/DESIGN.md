# Gate reproducibility — pinning the ANALYZE sample for plan captures

Status: accepted. Owner item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
A-05 (non-skippable plan pin) and ground rule 3 (plan-shape pin on every
commit). Filed 2026-09-05 while gating C-02c.

## 1. The problem, measured

The plan-shape pin compares a capture of the current build against a
baseline capture. Two captures of the **same binary**, taken back to back on
fresh servers over the same data, disagreed:

| A/A comparison (TPC-H, 22 queries, same binary, no code change) | differing lines |
|---|---|
| full capture (estimates included) | 455 |
| shape only (`cost=`/`rows=`/`width=` stripped) | 27 |

The shape differences were whole join-method flips, not cosmetics — Q3
alternated between `Nested Loop` and `Merge Join`, and Q9 alternated between
a hash spine and a merge spine. A/B noise between two *different* binaries
measured 420/14 lines, i.e. **smaller than the A/A noise**. Under those
conditions the pin reports changes no commit caused, and a real regression
is indistinguishable from a re-run.

This also invalidated a timing conclusion during the same session: C-02c
appeared to double TPC-H Q9 (12.7 s → 26.4 s), which re-measurement against
pinned statistics showed to be 11.71 s vs 11.54 s — a statistics artifact,
not the change.

## 2. Root cause

Two independent sources, both statistical rather than logical:

1. **The capture harness re-ANALYZEs.** goopg's statistics are
   per-connection (`cmd/estimate-audit` `-warm-stats`, default **true**,
   issues `ANALYZE <table>` for every TPC-H table on the audit session
   before capturing). ANALYZE is a *sampled* reservoir scan, seeded from the
   wall clock, so each capture session plans against a different sample.
2. **The autovacuum launcher re-ANALYZEs mid-run.** `cmd/goopg/main.go`
   starts the launcher when the `autovacuum` GUC is on; it calls
   `executor.AnalyzeRelationSampled` every `autovacuum_naptime` (60 s) for
   tables past `MinAnalyzeAge`. A TPC-H power run lasts minutes, so
   statistics moved *between the arms of an A/B*, and sometimes between two
   queries of one arm.

Neither is a bug. PG samples too (`acquire_sample_rows`,
`postgres/src/backend/commands/analyze.c`) and PG's autovacuum also
re-analyzes. The bug is in the measurement protocol, which assumed the
planner's inputs were constant across a restart.

## 3. Decision

**D-1. Expose the existing seed hook to the harness.**
`executor.Context.AnalyzeRandSeed` already made the reservoir sampler
reproducible, but only tests set it. A process-wide fallback,
`GOOPG_ANALYZE_SEED`, is read once at package init and used when no
Context-level seed is set.

- Unset (production, and every run that does not opt in): unchanged —
  a fresh wall-clock seed per ANALYZE, matching upstream.
- Set: every ANALYZE in the process draws the same sample, so a capture is
  reproducible.
- Unparsable, or an explicit `0`: treated as unset. Parsing to zero would
  pin every sample to a single draw without anyone asking for it. The
  fail-open is deliberate but silent — a mistyped seed is indistinguishable
  from unset in the resulting artefact, so the seed belongs in the recorded
  regime line of a verdict file, not only in a shell history.

The pinned seed is mixed with the relation OID (`seed ^ oid`). One seed
shared by every relation would make each table's reservoir replay the
identical random stream, correlating which sample positions survive across
all tables; the gate would then pin a statistics set less representative
than an unpinned draw. Mixing keeps a run reproducible (OIDs are stable for
a cluster) while leaving the per-table samples independent.

Applied at all three sampler constructions (`analyzeRelationCtx`, the
test-only `analyzeRelation` wrapper, and `AnalyzeRelationSampled`, which is
autovacuum's entry point) so the sibling paths cannot drift — the
encode/decode-class failure this repository has hit repeatedly.

**D-2. Pin the benchmark cluster's statistics.**
`autovacuum = off` for the TPC-H bench cluster, written by the **tracked
generator** (`bench/tpch/setup_goopg.sh`'s conf heredoc) as well as the live
data dir. The data dir is untracked, so a setting placed only there is
silently reverted by the next `setup_goopg.sh --reset` — the ephemeral-driver
failure mode `scripts/tpch-acceptance-arm.sh`'s own header warns about.

A benchmark cluster is a measurement instrument; a background job that
rewrites the planner's inputs every 60 s makes every timing and plan
comparison on it conditional on when it ran. This is
configuration of one cluster, not a code default: the GUC default stays
upstream's `on`.

## 4. Result

With `GOOPG_ANALYZE_SEED` pinned and autovacuum off, two back-to-back
captures of the same binary are **byte-identical** (0 differing lines,
was 455). The plan pin now measures the commit.

## 5. Consequences to carry forward

- **A third drift source, found while re-pinning: goopg's ANALYZE updates
  the PERSISTED statistics.** So what a capture sees depends on whether
  anyone ANALYZEd the data directory earlier — and every plan-parity capture
  does, because `estimate-audit -warm-stats` is on by default. A baseline
  taken on an untouched server and a diff taken after a capture disagreed on
  98 lines, reporting Q1/Q3 DIFFER for BOTH arms of an A/B. Fixed by giving
  `cmd/plan-snapshot` the same `-warm-stats` step (default on, one
  connection because goopg statistics are per-connection), so capture and
  diff normalise to the same state whatever the server did before. With the
  seed pinned this converges: `make plan-gate` now passes 22/22 in
  `MODE=costs` — cost-EXACT pinning — across a restart and a binary change.
- **Existing baselines were captured under the old regime.** The plan-gate
  snapshots and the A-04 timing roll-up
  (`analysis/planner-refactor-take3/a04-baseline-20260905/`) predate the pin,
  so the first strict `make plan-gate` after this lands can report shape
  differences no commit caused. Re-capture once under the pinned regime;
  after that the pin measures the commit.
- **Nothing sets visibility-map bits any more on that cluster.** The
  index-only cost path reads `allvisfrac` from `catalog.RelAllVisible`
  (`internal/optimizer/pathindexonly.go`), which is VM-derived and only
  written by VACUUM. With autovacuum off, a freshly reloaded cluster prices
  index-only paths pessimistically forever. Run one manual `VACUUM` after
  each HammerDB load; it is deterministic, unlike the sampled ANALYZE, so it
  does not reintroduce the noise.
- **The seed only takes effect where the SERVER reads it.** ANALYZE executes
  server-side and the value is read once at server start, so exporting it
  beside a `psql` call does nothing. It is set in `bench/tpch/env_goopg.sh`
  (sourced by `setup_goopg.sh` and `scripts/tpch-spotcheck.sh`, and inherited
  through `systemd-run --user --scope`) and in `scripts/tpch-acceptance-arm.sh`,
  which starts its own server.

## 6. What this does not do

- It does not make plans reproducible across *data* changes (a reload
  re-pins the anchors, as `bench/tpch/spotcheck_expected.env` already
  states).
- It does not change which plan goopg picks for any unpinned run, and it
  does not touch selectivity, costing, or the sampler's algorithm.
- It does not remove the need to hold server age constant in a timing A/B
  (CLAUDE.md "sweep-tail collapse").

## 7. Gate

`go test -run TestAnalyzeSeed ./internal/executor/` — three pins: the unset
default stays wall-clock (a pinned production default would silently make
every ANALYZE draw one sample), the env seed wins on every call, and a
non-numeric value behaves as unset. The subprocess pattern is required
because the variable is read once at init, which is deliberate: re-reading
the environment per ANALYZE would put a `getenv` on the ANALYZE path and
allow the seed to change mid-run.

Protocol change for every later TODO_ALL item: plan captures and A/B arms
run with `GOOPG_ANALYZE_SEED` set to a fixed value, recorded in the item's
close line alongside the other regime fields.
