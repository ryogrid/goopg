Task: M0138-0004 — MCV, histogram and correlation from the shared sample.
**COMPLETE and committed/pushed** this loop (`44085c3`), branch
`plan-parity-with-pg-take2-ralph`.

Files: `internal/executor/operators_analyze.go` +
`operators_analyze_test.go` (production fix + 2 new regression tests),
`docs/design/0100-0149/m0138-0004-mcv-histogram-correlation-selection-rule.md`
(new), `docs/design/README.md` (+index row), `.ralph/deferral_ledger.md`
(flipped 2 rows to resolved, filed 1 new row), `.ralph/fix_plan.md`
(M0138-0004 checked off + DONE summary, plus a blast-radius correction on
the pre-existing parser AST-drift note).

Starting state (important for the next loop's trust calibration): found
UNCOMMITTED WIP already in the working tree at loop start (a prior loop had
applied 3 fixes from the M0138-0001 census — AvgWidth typlen fallback,
correlation tie-break via `sort.SliceStable`, MCV bucket tie-break by
ascending value — then got cut off before committing, leaving two orphaned
bench servers `:65433`/`:65437` running from its own measurement work).
Built on that WIP rather than discarding it; reaped the orphaned servers via
`goopg stop -D <dir>` (never `pkill`) so `tpch-spotcheck.sh`'s snapshot-clone
could get a quiescent copy.

What was added this loop (3 more divergences, found reading
`compute_scalar_stats`, analyze.c:2402-2919, line-by-line):
  1. MCV "complete list" shortcut: was `len(buckets) <= statsTarget` alone;
     PG's `track_cnt == ndistinct` requires EVERY distinct value to have
     repeated (track[] never holds a singleton). Fixed to `nmultiple ==
     len(buckets) && len(buckets) <= statsTarget && stats.StaDistinct() > 0`.
  2. `analyzeMCVList`'s candidate cap was `len(buckets)` (ndistinct); PG caps
     by `track_cnt` == `nmultiple`. Fixed to cap by `nmultiple`.
  3. Histogram boundary dedup: removed. PG's `compute_scalar_stats`
     (analyze.c:2806-2836) does NOT dedup adjacent equal boundaries; the
     selectivity consumer (`bucketFraction`, selectivity.go) already
     implements PG's own `binfrac = 0.5` equal-bounds fallback
     (selfuncs.c:1234-1237). The old dedup only threw away information.
  - Each fix verified empirically before committing: patched a throwaway
    copy of the OLD logic (via a small Python edit to a `/tmp` backup, never
    touching the working tree's real file) and re-ran a probe test to
    confirm the buggy behavior actually differed from the fix — do this
    before trusting a hand-derived "this should differ" claim; PG's
    analyze_mcv_list arithmetic is not obviously monotonic in candidate-list
    size, so a purely algebraic argument was not trusted alone.
  - Two new regression tests:
    `TestAnalyzeMCVExcludesSingletonsFromCompletenessAndCandidates` (60
    repeated + 40 singleton values, target 100 — MCV goes from "all 60" to
    "none", matching the near-uniform-rejection precedent) and
    `TestAnalyzeHistogramKeepsAdjacentDuplicateBoundaries` (a rank-3 value
    excluded from MCV candidacy but still spanning 2+ histogram boundary
    picks).
  - Deferred (ledger row `m0138-0004`): `compute_distinct_stats`
    (non-orderable kinds, e.g. bytea/interval) still uses a plain
    count-sort instead of PG's actual bounded track-list algorithm — no
    TPC-H/TPC-DS column exercises this path, so left unported without a
    corpus target to validate against.

Key symbols: `computeColumnStats` (`internal/executor/operators_analyze.go`,
the `completeAndFits`/`mcvCount`/histogram-`bounds` sections), PG oracle
`compute_scalar_stats` (`postgres/src/backend/commands/analyze.c:2402-2919`),
`analyze_mcv_list` (`:2980`), `ineq_histogram_selectivity`
(`postgres/src/backend/utils/adt/selfuncs.c:1049`).

Gates run: `go build ./...` clean. `go test ./internal/executor/...
./internal/optimizer/...` full packages green. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`: green for `internal/executor` and
`internal/optimizer`; `internal/parser` fails 60 test functions (pre-existing
`RangeVar.GroupedJoinUnaliased` AST-drift, filed 2026-09-15, unrelated —
confirmed the diff touches only executor files) plus the pre-existing
untracked `bak/` build failure. `scripts/tpch-spotcheck.sh`: `RESULT=PASS`
(Q12=2 rows / Q13=34 rows, canonical anchors) after reaping the two orphaned
servers. `make ralph-state-guard`: consistent.

In-flight: none.

Next step: Per the banner, re-check `.ralph/fix_plan.md`'s `## Current
Priority` banner fresh (M0139/M0140 may now be the topmost-unblocked item in
the M0138/M0139/M0140 independent-trio, or M0138-0005 "corpus re-measure at a
declared epoch" — the natural follow-up to this loop's mechanism fix, which
this loop deliberately did NOT do since the milestone's own task breakdown
assigns it to M0138-0005 explicitly). Before starting M0138-0005 or any
further M0138 corpus work, check for orphaned bench servers first (`ss -tlnp
| grep -E '65433|65437'`) — this loop found two left running by an
interrupted predecessor and had to reap them before the spotcheck gate could
run.
