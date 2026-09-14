Task: M0137-0005 — re-baseline `make plan-gate`.
**COMPLETE and committed** (this loop, `plan-parity-with-pg-take2-ralph`).

Files: `plan_snapshots/m0137-0005-rebaseline-20260915.txt` (new baseline,
22 queries), `docs/design/0100-0149/m0137-0005-plan-gate-rebaseline.md`
(new), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0137-0005 checked off).

Key symbols: none (`make plan-snapshot-capture`/`plan-gate` +
`cmd/plan-snapshot`, `scripts/pg-plan-parity-diff.py`; no Go code touched).

Findings:
- `make plan-gate` picks the mtime-newest `plan_snapshots/*.txt` file
  (`Makefile:431-453`'s `ls -t … | head -1`); that was `warm-pin-20260905.txt`,
  ~9 days / ~125 rounds of intentional plan-shape work stale, so it DIFFERed
  on **20/22** queries (only `Q15a-VIEWBODY`, `Q19` MATCH) — reproducing the
  fix_plan line's own figure exactly.
- Per-query node-type census (added/removed EXPLAIN node counts from the
  full diff) attributes the drift to already-landed, already-gated
  mechanism classes: parallel/`Gather` adoption (Q1,Q3,Q5,Q9,Q12,Q16,Q18,Q22),
  index-scan/`Nested Loop` narrowing (Q7,Q8,Q9,Q17,Q21), new `Memoize` (Q8),
  `HashAggregate`<->`GroupAggregate` flips (Q3,Q13,Q16,Q18,Q7). Q1/Q10/Q11/
  Q14/Q20 DIFFER with identical added/removed node-type counts — shape
  moved sideways (operand/qual ordering, not node-type) — flagged for
  M0137-0010's qual-placement census, not root-caused here (out of scope).
- Correctness check before pinning: `scripts/tpch-spotcheck.sh` PASS
  (Q12 rows=2, Q13 rows=34) — the drift above is plan-shape movement, not a
  row-count regression riding along.
- Re-baselined: `make plan-snapshot-capture LABEL=m0137-0005-rebaseline-20260915`
  against goopg TPC-H `:65433` (started via `bench/tpch/setup_goopg.sh`,
  persisted `tpch` DB from the 2026-07-27 HammerDB rebuild, no reload
  needed). `make plan-gate` now reads **22/22 MATCH**. 18 pre-existing
  snapshot files left in place as inert frozen history (only the
  mtime-newest is ever consulted) — no cleanup was in scope.
- No production planner/executor/catalog code touched (pure instrument
  re-pin); no ledger row (nothing newly discovered about PG behaviour —
  this is `plan-gate`'s own baseline currency, not a PG-incompatibility).
- No TPC-DS re-baseline: `make plan-gate` is TPC-H-only
  (`PLAN_PORT ?= 65433`, `PLAN_DB ?= tpch`); TPC-DS already has its own
  current instrument (SF0.25 sweep, M0137-0004's subject).

Next step: read `.ralph/fix_plan.md`'s M0137 section and select the next
topmost unchecked task per the banner. In file order after M0137-0005 that
is **M0137-0006** (make the stats-epoch declaration a checked step). Before
starting, re-read `AGENT.md` §"Plan-parity harness" (fresh loop = fresh
session per the loop-start discipline).

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` —
ran in full; same single pre-existing FAIL (`internal/parser`
`TestLockingClauseParity`/`RangeVar.GroupedJoinUnaliased` package build
drift, already filed under M-NIGHTLY, zero Go files touched this loop) and
nothing else. `make ralph-state-guard` — repaired the same recurring
running/completed marker mismatch from the prior loop's clean exit as the
last several loops, then OK. `scripts/tpch-spotcheck.sh` — PASS (see above).
`make plan-gate` — run twice (pre-rebaseline: 20/22 DIFFER confirming the
task's premise; post-rebaseline: 22/22 MATCH confirming the fix).
Pre-commit hook's mandatory pgbench smoke fires automatically on `git
commit` (not run standalone this loop; the hook itself is the gate).

In-flight: none. TPC-H goopg bench server (`:65433`) was started by this
loop (`bench/tpch/setup_goopg.sh`, was down at loop start) and stopped again
(`bench/tpch/stop_goopg.sh`) before finishing — not left running. Nightly
scheduler (`ci/batch/nightly-scheduler.sh`, PID 849415) was observed idle
(sleeping, no active batch) throughout and untouched.
