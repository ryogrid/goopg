Task: M0138-0001 — ANALYZE divergence census. **COMPLETE and committed/pushed**
this loop (`3b6643da9`), branch `plan-parity-with-pg-take2-ralph`. Recon task
per the plan-parity harness (M0138-0001 is one of the three named
no-production-change recon tasks) — filing/measurement only, no `.go` files
touched.

Files: `docs/design/0100-0149/m0138-0001-analyze-divergence-census.md` (new),
`docs/design/README.md` (+index row), `.ralph/deferral_ledger.md` (+3 rows,
task-id `m0138-0001`), `.ralph/fix_plan.md` (M0138-0001 checked off, DONE
summary).

What landed: started the two down goopg bench clusters (`bench/tpch/setup_goopg.sh`
no `--reset` — idempotent restart of existing data; `bench/tpcds/server.sh
start sf025`), left all four `:6543x` clusters (goopg/PG x TPC-H/TPC-DS)
running per the shared-lane convention (never restart a reference). Captured
`pg_stats` for 61 TPC-H columns + 121 TPC-DS columns per engine (raw at
`/tmp/m0138-census/*.txt`, scratch not committed). Reconfirmed the milestone
doc's named block-representation gap (goopg full-block-scan Algorithm R vs
PG's `BlockSampler_Init`-bounded-blocks Vitter Algorithm Z) by source read
(`operators_analyze.go:791-923`, `analyze.c:1199-1373`, `sampling.c:39-115`)
plus live data (TPC-DS `customer`, small enough that PG also scans every
block, shows near-identical n_distinct; `lineitem`/`store_sales`, far bigger
than `targrows`, do not).

Three **new** divergences surfaced, none named in the milestone doc, each
filed as a ledger row with a resume point:
  - `RowCount`/`reltuples`: goopg exact (full scan) vs PG's
    `floor((liverows/bs.m)*totalblocks+0.5)` sample extrapolation
    (`analyze.c:1330-1339`) — scope question for **M0138-0002**.
  - Correlation tie-break: PG's `compare_scalars`/`tupnoLink` breaks
    value-sort ties deterministically by physical scan position
    (`analyze.c:2438-2550,2931-2950`); goopg's `sort.Slice`
    (`operators_analyze.go:1216-1247`) has no tie-breaker, unspecified tie
    order. Live evidence: TPC-DS `store_sales`/`customer` — goopg
    correlation bands to `[0.09,0.16]` on 36/118 columns vs PG's 7/119,
    concentrated on highest-duplicate-density FK columns. **Hypothesis, not
    confirmed** — needs a controlled synthetic-table test. Owner:
    **M0138-0004**.
  - `pg_stats.avg_width`: PG falls back to fixed catalog `typlen` for every
    non-varlena type (`analyze.c:2026,2206,2381,2586,2902,2914`); goopg's
    `datumVariablePayloadWidth` (`operators_analyze.go:1115-1130`) has no
    `default:` fallback, so int2/int4/int8/date/float/numeric-fast-path
    columns report `avg_width=0`. Live: 32/61 TPC-H + 70/121 TPC-DS columns
    affected, reproduces even on a fully-scanned small table (independent of
    block sampling). Clean, low-risk, well-understood fix — just out of this
    task's no-code-change scope. Owner: **M0138-0004**.

Key symbols: `analyzeRelationWith` (`operators_analyze.go:791`),
`computeColumnStats` (`:1136`), `datumVariablePayloadWidth` (`:1115`) —
goopg side. `acquire_sample_rows` / `compute_scalar_stats` / `compare_scalars`
(`postgres/src/backend/commands/analyze.c`), `BlockSampler_*`
(`postgres/src/backend/utils/misc/sampling.c`) — PG oracle side.

Gates run: recon task, no build/test gate applicable beyond the pre-commit
hook's mandatory pgbench smoke, which ran and passed (commit succeeded, hook
not bypassed). `make ralph-state-guard`: same running/completed marker
mismatch as recent loops (previous loop's clean-exit marker), auto-repaired,
then OK — still worth someone fixing at the marker-writer source if it keeps
recurring every loop.

In-flight: none. The four `:6543x` bench clusters (goopg TPC-H `:65433`,
PG TPC-H `:65432`, goopg TPC-DS SF0.25 `:65437`, PG TPC-DS `:65438`) are
**left running** intentionally (shared lane, not throwaway) — verify with
`pg_isready` before assuming down, do not restart them.

Next step: M0138-0001 done. Per the banner's selection order, M0138's next
unblocked task is **M0138-0002** ("port PG's block sampler and two-stage row
selection") — it is the natural next pick since it inherits this census's
evidence directly and the RowCount/reltuples scope question needs answering
before M0138-0004 can build on a settled sample source. Re-check the banner
in `.ralph/fix_plan.md` fresh next loop before committing to M0138-0002 vs an
M0139/M0140 task that might have become topmost-unblocked in the meantime —
selection is "topmost milestone (M0138) with an unblocked task" per the
banner text, and M0138-0002 is that milestone's next unchecked item.
