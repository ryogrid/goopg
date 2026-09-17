Task: M-NIGHTLY `race/internal/executor` (banner: P0-E6 still `[!]`
owner-run; items 1-7 re-scanned, nothing selectable — M0143-0007b needs an
owner decision, M0141-S2b-9/-8 explicitly blocked on banner item 4's
"diagnosis only", M0142-0005a/M0139-0007c are implementation not recon,
M0142-0003i needs P0-E6 — same fall-through as last 2 loops — to
M-NIGHTLY). Re-confirmed NOT stale (real race, reproduces every run) and
root-caused to an already-twice-ledgered defect; NOT fixed this loop (fix
needs a signature change too big for one loop — scoped it instead).
Committed.

Files: `.ralph/fix_plan.md` (race/internal/executor entry: re-confirm note
+ new nested sub-task `M-NIGHTLY-instrumentscope-race-fix` with a concrete
mechanical fix plan), `docs/design/executor-ex0-03b-rows/DESIGN.md`
(new "5. Erratum" section correcting B1's now-falsified "never racy"
claim). No production code touched.

Key symbols: `instrumentScope`/`instrumentScopeMu`
(`internal/executor/instrument.go:313`), `maybeInstrument` (`:444`, reads
the global with NO lock), `buildUnderFreshScope`/`buildUnderNilScope`
(`:350/:370`, write it WITH the lock), `gatherOp.buildChildForSlot`
(`operators_gather.go:122`), `acquireSubPlanOp` (`subplan.go:302`, the
lazy cross-goroutine reader via `Build(plan)`).

Findings: race is real, reproduces in ~60s (not a 45m timeout hang), full
log `tmp/race-internal-executor-20260918.log` (untracked, regenerate via
`go test -race -timeout 45m ./internal/executor/`). It is the SAME defect
as `.ralph/deferral_ledger.md`'s `take3-instrumentscope-datarace`
(2026-09-06) and `e18-instrumentscope-global-races-coop-producers`
(2026-09-07) — do NOT file a third ledger row, the ledger already has the
diagnosis; what was missing was a concrete, sized fix_plan task, now
filed. It is NOT ANALYZE-only: `buildChildForSlot` round-trips the global
on every parallel worker spawn regardless of ANALYZE, so any ordinary
parallel query with a concurrent lazily-built SubPlan can hit it. The
"just widen the mutex to guard reads too" shortcut was considered and
explicitly rejected again (per the ledger's own prior warning) — it would
silence `-race` but not fix the real hazard (a lazy producer subtree
adopting an unrelated sibling worker's live scope). Traced a concrete,
narrower-than-"thread through Context" mechanical plan that needs ZERO
changes to `Build`/`BuildWorker`'s ~200 external call sites (see the new
fix_plan sub-task for the 3-step plan: `buildNode` gets an explicit
`scope` parameter threaded through its ~33 internal call sites;
`Build`/`BuildWorker` become nil-scope-only thin wrappers; a new
unexported `buildScoped` serves the ~4 call sites that actually need
ambient scope — `operators_explain.go`'s top-level build, the two
`gatherOp`/`gatherMergeOp` `buildChild` closures, `operators_cte_dml.go`'s
`buildUnderScope`). One open design question flagged but NOT resolved:
whether a lazily-built SubPlan/EXISTS tree in a *serial* ANALYZE query
should inherit the ambient scope (real PG does instrument SubPlan
children) — needs a probe test before implementing, see the fix_plan
task's "Open question" paragraph.

Next step: next loop re-reads the banner fresh (P0-E6 still owner-run
expected). If still nothing selectable in items 1-7, either (a) implement
`M-NIGHTLY-instrumentscope-race-fix` (start with the "Open question" probe
test — serial EXPLAIN ANALYZE + correlated EXISTS, observe today's actual
per-node output — before touching `buildNode`'s signature), or (b)
continue top-to-bottom through the M-NIGHTLY list past this item:
`testport/TestPort_IsolationIntraGrantInplace`,
`testport/TestPort_IsolationStats`,
`testport/TestPort_LockRowsSortOverJoinTakesRowLock`,
`testport/TestPort_PgDumpConnectionSetup`, `units/internal/parser`
(likely same root cause as `parser/TestLockingClauseParity` — re-run both
together first), `race/internal/parser`,
`testport/TestPort_IsolationEvalPlanQual`, `testport/TestPort_IsolationSuite`
(subtests specs/detach-partition-concurrently-1/tuplelock-upgrade-no-deadlock),
`testport/TestPort_IsolationFkContention`, `testport/TestPort_IsolationFkDeadlock`,
`testport/TestPort_UpdateLockedTuple`. Re-run each repro at HEAD first per
the loop rule (some may be stale).

Gates run: `go build ./...` clean (confirmed before AND after — no code
changed). `go test -race -timeout 45m ./internal/executor/` FAIL (2 real
races, expected/documented, not a new regression — this loop did not fix
it). `make ralph-state-guard`: same benign status/progress mismatch as
prior loops (previous loop's clean-exit completed-marker), self-repaired,
consistent after repair. No TPC-H/sf025 gate needed — doc/tracking-only
change, no `internal/`/`cmd/` file touched. Pre-commit hook's pgbench
smoke will run automatically on commit (not run standalone, since it's
mandatory on every commit anyway).

In-flight: none.
