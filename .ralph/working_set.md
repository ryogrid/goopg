Task: M0125-0002 commit 3 of 8 — `visitColumnRefs` re-based onto
`walkExprRefs` — **DONE, committed + pushed** (loop #34, 2026-08-03), plus a
separate gate repair `4fb87456` (units gate was RED at HEAD:
`TestMHJParallelNoDuplicates` missed by `e85e5347`'s MHJ-retire test opt-ins).

Files: internal/planner/bushy.go (new driver body; scopeIgnore; panic on
unknown), internal/planner/exprwalk_inventory_test.go (census pin DELETED —
first deletion in the series; header note), internal/planner/
visit_refs_arms_test.go (NEW: 11 newly-visited-kind pins, preserved arms,
scope declines, panic — all proved-fail-first), internal/executor/
parallel_mhj_test.go (opt-in, separate commit), design doc 0125-0002 §"Commit
3 of 8", analysis/m0125-0002-c3-plans-20260803/, plan_snapshots/
m0125-0002-c3-{before,after}.txt, fix_plan item + ledger row.

Key findings:
- All A/B instruments empty: TPC-H 22/22 byte-identical (== post-mhj-retire),
  SF0.5 EXPLAIN 96/96 byte-identical, and a side-by-side divergence probe
  (both walker bodies in one measurement binary) logged 0 deltas over all 118
  planned queries. The probe matters because EXPLAIN prints Name over Index
  (M0125-0042), and Index mutation is this commit's only behavioural surface.
- Label hygiene: `m0125-0005-relsize-default-stage2` is STALE (e85e5347 MHJ
  retire moved 19/22 plans). Current baseline label: `post-mhj-retire`.
  Same-cluster A/B remains the staleness-immune instrument.
- Timed TPC-H run skipped (ledger row; zero-hunk + probe). SF0.5 answer sweep
  not owed (zero hunks; old body was read-only — no commit-2-style metadata
  loss possible).

Next step: re-read the banner. Inside M0125-0002 the next slice is **commit 4
(`visitColumnRefsForTable`) — a first-order SHAPE mover; expect hunks, carry
the timed TPC-H run + SF0.5 sweep ⇒ needs a QUIET host** (nightly was live
all of loop #34). If the host is still busy next loop, other host-independent
M0125 items: -0041 is arguably closeable as bookkeeping (its acceptance Q30
PASS 1s/31 rows/ck=oracle was met by -0034's join-order arm per the banner,
loop #15, but the box was never checked — verify the sweep row then check
it), or -0040 (ROLLUP grouping-sets — big, code-heavy, acceptance needs
timed runs though).

Gates run: units precommit ×2 (first run caught the RED MHJ test at HEAD →
repaired; second run PASS exit 0); TPC-H plan A/B byte-identical; SF0.5
EXPLAIN A/B 96/96; probe 0/118; tpch-spotcheck RESULT=PASS (Q12=2 Q13=35,
35.2s); pgbench smoke ×2 via hook (both commits, 0 failed).

In-flight: none. (Do NOT `git stash` in this tree — loop #34's attempted
pathspec stash aborted on the untracked test file and the reflex `stash pop`
nearly applied a FOREIGN 2026-07-29 stash@{0}; use a HEAD worktree instead.)
