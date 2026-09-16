Task: M0142-0008a-3i-plumbing-c11 item (b), continued. Found + LANDED a
real bug (SourceTableIdx collision across sibling EXISTS clauses) but it
did NOT fix Q69's actual crash. New root cause pinned via live
instrumentation; follow-on filed as **c12** (fix_plan.md, not yet started).

Files this loop:
- internal/optimizer/unnest.go: new `maxSourceTableIdxDeep` helper (walks
  *Join.Left/*Join.Right/*Filter.Child rather than trusting Output()) +
  `unnestExistsExpr`'s `srcTableOffset` computation now uses it instead of
  scanning `outerChild.Output()` alone. LANDED, this is the loop's only
  production diff.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §46.6 —
  method, both findings (STI collision fixed; real cause pinned, not
  fixed), verification evidence, concrete c12 next step.
- .ralph/fix_plan.md: (1) nightly triage — filed/merged all 17 items from
  run 20260917-004357 (2 closed as stale/racing-checkout artifacts, 3 new
  testport subjects opened, rest AI-id-appended to existing tasks); (2)
  c11 entry updated with this loop's finding; (3) new task
  **M0142-0008a-3i-plumbing-c12** filed with the concrete next
  instrumentation step.
- .ralph/deferral_ledger.md: new row for this loop.

Key symbols: `unnestExistsExpr` (unnest.go ~4476) and its new
`maxSourceTableIdxDeep` sibling (added just above it) — landed.
`createNestLoopIndexJoinPlan`/`outerParamKey` (createplannl.go:178-209) —
confirmed NOT buggy, faithfully converts whatever ColumnRef it's handed.
The REAL c12 target is upstream of these: whatever DP-search code sets
`RequiredOuter` on a candidate index Path (near `GOOPG_NLI_COSTGATE`) —
not yet located, only proven to exist by elimination.

Findings: (1) unnestExistsExpr's srcTableOffset was computed from
outerChild.Output(), which a Semi/Anti Join deliberately does not grow
after splicing (RHS columns never publish through Semi/Anti's Output()) —
so a 2nd/3rd sibling EXISTS in one statement reused the SAME offset as the
1st, colliding SourceTableIdx (Q69: store_sales/web_sales/catalog_sales
all got STI=5, all 3 date_dim occurrences got STI=6). Fixed, verified live
production-safe via full SF0.25 sweep (private GOOPG_BIN, joinInfoList
untouched): PASS=96/MISMATCH=0/ERROR=0/SKIP=3; only 3 queries (Q16,Q69,
Q94) show a plan-text diff and in all 3 it's purely an EXPLAIN
alias-disambiguation correction (e.g. bare "date_dim" used for 2 distinct
correlated scans -> date_dim_1/date_dim_2). (2) This did NOT fix Q69's
actual runtime crash: re-run with the STI fix + the temporary
`joinInfoList: ctx.joinInfoList` one-liner (§46.3, needed to reach the
search at all) produced a BYTE-IDENTICAL EXPLAIN and the IDENTICAL error
to before the fix — refuting the "stray unrebased OuterColumnRef" theory
the c11-filing loop proposed. (3) Root cause of the ACTUAL crash, pinned
via live instrumentation (temporary walkPlanExprsDeep prints around
runJoinSearchBelowPinned, reverted before commit): the DP search builds
AND WINS an NLI candidate that probes customer_demographics keyed on
`cd_demo_sk = c.c_current_cdemo_sk`, using the store_sales+date_dim
EXISTS synthetic leaf as the candidate's OUTER/driving side — but
`customer` is not in that leaf's relids at all, so no coordinate
numbering could ever make c.c_current_cdemo_sk resolve there. This is a
missing/broken "clause's required relids subset-of candidate outer
relset" check in the index-path candidate generator, not a
coordinate-rebase bug.

Next step: c12 (fix_plan.md, filed this loop). Instrument the DP search's
index-path candidate generator (upstream of createPlan, wherever
RequiredOuter gets set on a candidate index Path — search near the
GOOPG_NLI_COSTGATE machinery) to print, for every NLI candidate built
while searching Q69, the candidate's outer relset alongside the index
qual clause's required relids. The first candidate where the clause's
relids are NOT a subset of the outer relset is the bug. Same private-
binary SF0.25 method as c9-c11 (§46.1), with the temporary joinInfoList
one-liner re-applied locally to reach the search — revert it before
commit either way, per the c9-c11 established discipline.

Gates run this loop: `go build ./...` clean. `go test
./internal/optimizer/...` PASS. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS except the same pre-existing
`internal/parser` GroupedJoinUnaliased AST-drift failure every recent loop
has hit (unrelated, unchanged, tracked under M-NIGHTLY; optimizer/executor
both explicitly confirmed `ok` inside this same run). `scripts/tpch-
spotcheck.sh` SKIPPED (documented, pre-existing: TPC-H bench data still
needs a reload per CLAUDE.md's M0142-0003k note — not this loop's
regression). `scripts/tpcds-sf025-regression.sh sweep` run with a private
GOOPG_BIN, joinInfoList untouched (production state): PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3 — the gate for this loop's actual
landed diff. `make ralph-state-guard`: run after this write-up, see status
block.

In-flight: none. Private diagnostic binaries/data (`tmp/goopg-c11b-bin`,
`tmp/goopg-c11b-sweep-bin`, `tmp/c11b-sf025-data`, `tmp/c11b-server.log`,
`tmp/c11b-q69-runtime.sql`) all stopped/removed before this write-up
(`tmp/` is gitignored regardless). All temporary instrumentation (the
planner.go OuterColumnRef walk, the unnest.go srcTableOffset print, the
temporary joinInfoList one-liner) reverted before commit — `git diff
--stat` shows only unnest.go as the production diff plus the doc/ledger/
fix_plan/working_set bookkeeping files.
