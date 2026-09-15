Task: M0142-0004a — recon: is the unique-pkey `IndexScan` `est=1`
finding a planner bug or a loops-vs-total capture artifact? **DONE and
FIXED this loop** (banner item 4's M0142-0004 group, second pick after
last loop's M0142-0004 itself).

Files: `internal/executor/operators_explain.go` (added `rowsPerLoop`/
`round2` helpers, wired the text/JSON/per-worker EXPLAIN ANALYZE
renderers through them instead of the raw cumulative `rowsOut`),
`internal/executor/explain_analyze_test.go` (2 new unit tests),
`.ralph/fix_plan.md` (M0142-0004a `[x]`, filed M0142-0004c follow-up),
`.ralph/deferral_ledger.md` (2 new rows: the fix's own follow-up pointer,
+ a side-discovery about SubPlan child nodes rendering no `(actual ...)`
annotation at all, unverified/unfixed), `docs/design/0100-0149/m0142-0004a-explain-analyze-loops-average.md`
(new), `docs/design/README.md` (+1 row).

Key symbols: `rowsPerLoop(rowsOut, loops int64) float64` and
`round2(v float64) float64` (`operators_explain.go`, near `nsToMs`);
call sites at the text renderer's `(actual ... rows=%.2f loops=%d)`
line, `planToJSONWithStatsNamed`'s `obj["Actual Rows"]`, and the
`Worker %d: ...` per-worker line. Root cause lived in `instrument.go`'s
`nodeStats.rowsOut` (accumulates across every Open/Next cycle, never
reset on re-Open) vs PG's `explain.c:1835,1901`
(`rows = instrument->ntuples / nloops`).

Findings: confirmed via the q34 witness M0142-0004a itself named
(`store_pkey`, raw capture line `actual rows=9969.00 loops=10082`):
9969/10082 ≈ 0.99, matching goopg's own `est=1` almost exactly — the
finding was never a cardinality-estimator defect, it was goopg's own
EXPLAIN ANALYZE misreporting `actual` as a cumulative total instead of
PG's per-loop average. This is a genuine PG-compatibility bug beyond the
estimator census: any real `EXPLAIN (ANALYZE)` over a correlated
subplan or NL/NLI inner side previously printed `rows=` inflated by up
to `loops`x vs real PG for the identical plan. Scoring script
(`scripts/estimate-parity/parity.py`) was ruled OUT — it already
implements PG's per-loop convention correctly.
Side discovery (unverified, not chased — see ledger): a correlated
scalar SubPlan's inner nodes render NO `(actual ...)` annotation at all
under ANALYZE, even though the SubPlan's own `calls=N rebuilds=...`
summary proves it re-executed. Two attempted end-to-end regression
tests (SubPlan-based, GUC-forced-NestedLoop-based) both failed to
exercise the fix for unrelated reasons (see design doc); settled for 2
direct unit tests on the pure helpers instead.

In-flight: none. No server/gate process left running.

Next step: per the banner, item 4 (M0137-M0143 group) is still open.
Recommended pick for the next loop: **M0142-0004c** (just filed —
re-run `make ea-ratchet`, confirm the 19 C1 findings collapse below
qerr>=10 post-fix, check whether other findings share the unnoticed
`loops>1` shape). Also open at the same priority: **M0142-0004b**
(the C2 CTE-UNION-ALL recon, independent of this loop's fix),
**M0142-0005/0006/0007/0008**, or M0141 S2b/S3-S7.

Gates run: `go build ./...` clean; full `internal/executor` unit suite
green (`go test ./internal/executor/...`, ~13s); `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=34 — mandatory per Hard-won Rule #1, executor package
touched); `make ralph-state-guard` — one self-repair (same recurring
benign stale-clean-exit-marker pattern several prior loops have noted),
clean after repair. Did NOT re-run `make ea-ratchet` this loop (that
~10min capture is M0142-0004c's own job, filed rather than run inline,
matching this milestone's established two-artefact precedent).
