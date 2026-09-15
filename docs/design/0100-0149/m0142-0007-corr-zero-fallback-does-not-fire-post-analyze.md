# M0142-0007 — recon: does the `corr=0` index-pricing fallback (B10) still fire at HEAD?

Status: accepted (recon closed 2026-09-15, no code change)

## Task

`.ralph/fix_plan.md`'s M0142-0007 line, deferral ledger row
`m0137-0012-b10-corr-zero-fallback-max-io-cost`
(`.ralph/deferral_ledger.md:2163`): `indexCorrelationFor`
(`internal/optimizer/costindex.go:479-495`) returns `0` when an index's
leading column has no `STATISTIC_KIND_CORRELATION` slot, which prices every
such index scan at `max_IO_cost` — the fully-random Mackert-Lohman bound —
regardless of the column's true physical/logical ordering. The ledger row's
own resume point: **"re-ANALYZE the bench clusters to populate the
correlation slot and re-measure whether the `corr=0` fallback still fires in
practice corpus-wide"** — M0138-0004 (correlation-slot writer, landed
2026-09-13/14) may already have closed this as a side effect, but nobody had
re-measured. Recon only, per the task's own framing.

## Method — instrument before theorising

Added a temporary `GOOPG_M01420007_TRACE=1`-gated `fmt.Fprintf(os.Stderr, …)`
in `indexCorrelationFor` (`internal/optimizer/costindex.go`), logging every
call's index name and outcome on two paths: the early-return fallback
(`idx == nil || stats == nil`, `nilstats=true`, always returns `0`) and the
real-value path (`nilstats=false`, logs the measured `varCorrelation`).
Reverted after the recon — `git diff --stat` empty on `costindex.go` before
commit; `go build ./...` clean both with and without the instrumentation.

Built the instrumented binary to a private path
(`tmp/goopg-m0142-0007-bin`, per `goopg_bench_bin_shared_with_nightly_lane` —
`ci/batch/nightly-scheduler.sh` was live at loop start, PID 1606617), and ran
it as the TPC-DS SF0.25 goopg cluster (port 65437, `GOOPG_CG_UNIT`-capped via
`bench/tpcds/server.sh start sf025`). The TPC-H bench peer on :65433 was left
untouched throughout (`goopg_shared_bench_cluster_collisions` — it is a
different loop's live server and was not restarted, so TPC-H is **not**
covered by this recon; see Follow-up).

1. `ANALYZE;` against the SF0.25 `postgres` database (all 25 tables) to
   guarantee the correlation slot reflects M0138-0004's writer, not a stale
   pre-M0138 sample.
2. Restarted the server with the trace env var set (the trace fires inside
   the running server process, not the client, so it must be set at server
   start, not per-connection) and re-ran `ANALYZE;` in that session.
3. Ran `EXPLAIN` (no execution) over the full, git-tracked 100-query TPC-DS
   corpus (`bench/tpcds/runtime_goopg/tpcds-data/queries/query*.sql`) against
   the trace-enabled server, capturing the server log.

## Result

```
grep -c M01420007 goopg.sf025.log        -> 2905   (total indexCorrelationFor calls)
grep -c 'nilstats=true'                  -> 0      (fallback NEVER fires)
grep -c 'nilstats=false'                 -> 2905   (every call has real stats)
```

Distribution of the 2905 real (non-fallback) correlation values:

| value | count | share |
|---|---|---|
| `1` (exact) | 1771 | 61% |
| `8.4e-05` | 570 | 20% |
| `0.0140` | 264 | 9% |
| `0.0041` | 139 | 5% |
| `-0.0018` | 83 | 3% |
| `-0.0013` | 54 | 2% |
| `0.00074` | 24 | 1% |

Zero of 2905 calls returned the exact literal `0` either — the smallest-
magnitude bucket (`8.4e-05`) is a genuinely measured near-zero correlation
on some FK/secondary-index leading column, not the `idx==nil||stats==nil`
default.

**The `corr=0` "no correlation slot" fallback does not fire anywhere in the
TPC-DS SF0.25 query corpus at HEAD.** Across all 100 queries and every index
leading-column lookup they trigger, `ColumnStats.Correlation` is always
populated. The 61% exact-`1` cluster is dimension/fact tables loaded in
primary-key order (`date_dim`, `item`, `customer`, `store_sales`'s own
surrogate key, etc. — TPC-DS `dbgen` writes rows in ascending key order, so a
Pearson correlation of physical vs. logical order on the PK column is exactly
1.0, matching real PG's own `pg_stats.correlation` for the same load shape).
The remaining 39% are genuinely small non-zero correlations on non-PK leading
columns (foreign keys, secondary indexes) — these DO still price close to
`max_IO_cost` via the `csquared = correlation²` interpolation
(`costindex.go:282`), but that is PG's own formula computing PG's own
"mostly-uncorrelated" answer from a real measured value, not goopg's
"missing statistic" default kicking in.

## Verdict

**B10's `corr=0`-fallback half is CLOSED as measured at HEAD — no code
change warranted.** M0138-0004's correlation-slot writer plus a plain
`ANALYZE;` fully populate every leading-key column the TPC-DS SF0.25 corpus
touches; the ledger row's "operational gap" framing (bench heaps predating
the writer, never re-ANALYZEd) was the actual defect, and it is gone once
`ANALYZE` is re-run post-M0138-0004. This confirms `.ralph/deferral_ledger.md`
row `m0137-0012-b10-corr-zero-fallback-max-io-cost`'s own framing that the
persistence half was "already CLOSED per K27" — the fallback-firing half is
now closed too, by direct re-measurement rather than by inference.

**The ledger row's OTHER bundled half is untouched by this task and stays
open**: `estimateIndexGeometry` (`internal/optimizer/costindex.go:511`)
still synthesises `relpages`/`reltuples`/`tree_height` from the heap's row
count rather than measuring them, because ANALYZE never visits indexes (no
index-level `pg_class`-equivalent statistics exist in goopg at all). That is
a structurally different gap — no amount of re-ANALYZE-ing closes it, since
there is nothing for ANALYZE to visit — and is **not** re-scoped or answered
here.

## Floor measurements (mandatory for M0137–M0143 recon tasks)

No production code changed (instrumentation added and reverted in the same
loop): `git diff --stat internal/optimizer/costindex.go` empty before
commit, `go build ./...` clean. No plan-parity/`ea-ratchet` floor shift is
possible from a revert-to-identical diff, so the mandatory TPC-H
`match=8/22` / TPC-DS `match=2/99` / `ea-ratchet` 112-entry baseline pins are
unchanged by construction (same precedent as M0142-0003a/0003b/0004b/0010).
SF0.25 cluster (port 65437, private binary `tmp/goopg-m0142-0007-bin`)
stopped and the private binary removed before commit; TPC-H peer (:65433)
never touched.

## Follow-up

No new milestone task filed. Two explicit gaps remain on the books under
their existing owners, not reopened here:

- The synthesised-geometry half of B10 (`estimateIndexGeometry`) — still
  open, no task currently targets it specifically; a future loop scoping
  index-level catalog statistics (the ledger's own "resume point: index-level
  catalog statistics, not a better formula" comment on `estimateIndexGeometry`)
  should treat this doc as confirming the corr=0 half is no longer a
  contributing factor, narrowing that future task's scope.
- TPC-H's own correlation-fallback rate was not measured this loop (its bench
  peer on :65433 is another loop's live server and restarting it to inject
  the trace would violate `goopg_shared_bench_cluster_collisions`). Given
  TPC-H's `lineitem`/`orders` tables are also `dbgen`-loaded in key order,
  the TPC-DS finding (exact-`1` correlation dominating PK-ordered loads, real
  measured near-zero values elsewhere) is expected to generalise, but this is
  an expectation, not a measurement — a future loop with exclusive access to
  the TPC-H peer (or a private clone) can close this cheaply by reusing the
  same trace if it ever becomes load-bearing.
