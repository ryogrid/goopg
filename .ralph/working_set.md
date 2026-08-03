Task: M0125-0002 commit 6 of 8 — the PAIR `conjunctIsLocalEligible` +
`localizeExprToLeaf` re-based onto `walkExprRefs` / `cloneExprRefs` —
**DONE, committed + pushed** (loop #38, 2026-08-03).

Files: internal/planner/local_filters.go (both bodies + `import "fmt"`),
internal/planner/local_filter_arms_test.go (NEW, 48 pins),
internal/planner/exprwalk_inventory_test.go (DEMOTE producer, DELETE
consumer; RC-1a 46 → 45), design doc §"Commit 6 of 8", fix_plan, 2 ledger
rows, analysis/m0125-0002-c6-plans-20260803/ (README, probe-source.md,
capture-{tpch,plans}.sh, before/after/probe arms),
plan_snapshots/m0125-0002-c6-{before,after,probe}.txt.

Key findings:
- D2 row 6 REFUTED: TPC-H 22/22 + SF0.5 96/96 byte-identical; probe
  0 C6ELIG / 0 C6LOC / 0 C6ABORT over 277+175 calls (complete live
  population, all 3 call sites), C6CALL/C6LOCC positive controls.
- Closed a latent WRONG-COLUMN read: `t.a IS NULL` on a binding with
  offset>0 was eligible-by-vacuous-true and then returned UN-REBASED.
- Asymmetric unknown handling is the design: producer declines, consumer
  panics (it cannot decline — the conjunct already left joinConjuncts).
- Grep trap: `C6LOCC` contains `C6LOC`; always grep `'C6LOC delta='`.
- Timed TPC-H run + SF0.5 sweep skipped a 4th time; ledger CONVERTS the
  per-commit obligation into ONE cumulative timed run owed at commit 8
  (reverts to per-commit if commit 7's plan diff is non-empty).

Next step: commit 7 of 8 — `visitColumnRefsByName` (bushy.go:~1653), the
LAST and largest. D3 predetermines the policy: plan slots **signal**, and
`extraInScans` must treat "an opaque child exists" as NOT matched,
inverting today's vacuous `true`. Expect a real plan diff there.

Gates run: units precommit PASS; full internal/planner green; census gate
green; 48 pins proved to FAIL against old bodies first; TPC-H plan A/B
22/22; SF0.5 EXPLAIN A/B 96/96; probe zero-delta; tpch-spotcheck
RESULT=PASS (Q12=2 / 23.1 s, Q13=35 / 11.3 s); pgbench smoke via hook;
ralph-state-guard OK (auto-repaired progress marker).

In-flight: none. (Worktree /tmp/c6probe-wt removed; probe source kept at
/tmp/zz_c6probe.go.keep and reproduced verbatim in probe-source.md.
Throwaway binaries tmp/goopg-c6-{before,after,probe} are safe to delete.)
