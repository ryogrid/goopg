Task: M0137-0010 — qual-placement census + duplicate-sensitive values check.
**COMPLETE and committed this loop**, branch `plan-parity-with-pg-take2-ralph`.
Implementation task (not recon) per the plan-parity harness.

Files: `scripts/qual-placement-census.py` (new), `scripts/qual-placement-census-test.py`
(new, 8 tests), `internal/executor/distinct_limit_paramref_test.go` (new),
`internal/optimizer/tuplefraction.go` (`limitBoundMovable` extended),
`docs/design/0100-0149/m0137-0010-qual-placement-census-and-duplicate-check.md`
(new), `docs/design/README.md` (+index row), `.ralph/fix_plan.md` (M0137-0010
checked off with DONE note).

Two deliverables, both named by the fix_plan line:
1. `qual-placement-census.py`: counts `Filter:`/`Index Cond:` lines per query
   across two `=== KEY`-section capture files, diffs between arms. Unlike
   `pg-plan-parity-diff.py` (report-only, PG-oracle required), this is a real
   gate (exit 1 on any mismatch/arm-only query, exit 2 unreadable input) that
   works on a same-engine before/after pair — the shape every M0139 slice
   produces. Live-validated against a real on-disk flag-flip A/B
   (`analysis/planner-refactor-take3/c06-flip-remeasure-20260907/`), mismatch=0.
2. Closed C1 (`METHODOLOGY3/02-open-problems.md`): `limitBoundMovable`
   (tuplefraction.go:101-112) only accepted `*IntegerConst` for the R83
   "defer LIMIT past DISTINCT" rewrite; `LIMIT $1` (`*ParamRef`) fell through
   to the pre-R83 truncate-then-distinct order. Added
   `TestDistinctLimitAppliesAboveDistinct_ParamRef`, confirmed it failed at
   HEAD exactly as predicted, then extended the guard to also accept
   `*ParamRef` (two type assertions, deliberately not a `switch` — avoids
   registering a new site in `TestExprSwitchInventoryIsPinned`'s Expr-switch
   census, which the first attempt tripped and had to be reverted).

Key symbols: `limitBoundMovable` (internal/optimizer/tuplefraction.go),
`planSelect`'s LIMIT stage (internal/optimizer/planner.go:2067-2080, 2424-2427),
`census()`/`count_conds()` (scripts/qual-placement-census.py).

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...
./internal/executor/...` green (incl. `TestExprSwitchInventoryIsPinned`).
`python3 scripts/qual-placement-census.py --self-test` 5/5,
`qual-placement-census-test.py` 8/8. `scripts/tpch-spotcheck.sh` PASS
(Q12=2 rows, Q13=34 rows — no regression from the planner change).
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: same
~59 `internal/parser` failures as a stashed-clean-HEAD control run (identical
failure set, confirmed via `git stash`) — pre-existing, already tracked as
nightly items AI-20260914-235643-001/003 and the "Manually discovered
parser/TestLockingClauseParity" fix_plan entry (both filed 2026-09-15,
`GroupedJoinUnaliased` field added by `dc91bd6b7` breaks the parity-golden
comparison broadly, wider than that entry's original scope — worth noting
for whoever picks that item up next). Not this loop's regression; not fixed
here (out of scope for M0137-0010). `make ralph-state-guard`: same
running/completed marker mismatch every recent loop has hit (previous loop's
clean-exit marker), auto-repaired, then OK.

In-flight: none. No throwaway server left running — `tpch-spotcheck.sh`'s
private clone/port (5580) and its own scope were torn down by the script
itself on exit (fresh clone + fresh scope every invocation, per M0137-0007).

Next step: per `.ralph/fix_plan.md`'s M0137 section, the one remaining open
M0137 task is **M0137-0013** (close/delete the ten-plus-round ledger carries,
split out of M0137-0009 — explicitly campaign-sized, its own multi-loop
budget). Once M0137-0013 lands, M0137 is fully closed and M0138/M0139/M0140
become the frontier per the banner's selection order (M0139's slices are now
unblocked: M0139-S1/-S2/-S3 were all "gated on M0137-0010"). Re-read AGENT.md
§"Plan-parity harness" fresh next loop before picking, per loop-start
discipline — confirm against the banner before committing to M0137-0013 vs.
starting an M0138/M0139/M0140 task if the banner has moved.
