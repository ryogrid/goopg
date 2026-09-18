Task: M0141-S2b-15 — per-call-site Gather row stamping (PG override_rows port). Marked [!] ESCALATED in fix_plan — code complete + staged, commit hard-blocked on the TPC-H HOLD.

Files: staged uncommitted — internal/optimizer/gatherpaths.go (overrideRows flag through makeGatherPath/makeGatherMergePath/generateUsefulGatherPaths), joinsearchlevel.go + geqo.go (call sites pass false), generateUpperRelGatherPaths (true), gatherpaths_test.go + gatherpaths_crossover_test.go + joinpathsparallel_test.go. Committed docs bcf99f4bc: design doc 0100-0149/m0141-s2b-15-gather-rows-stamp.md, README row, fix_plan [!] + new M0141-S2b-16, ledger row.

Key symbols: makeGatherPath/makeGatherMergePath(rel, sub, cp, overrideRows); rows := rel.Rows unless overrideRows → computeGatherRows(sub,cp); stamped rows feed Path.Rows AND gatherCost (PG cost_gather's * path->path.rows, costsize.c:452-463).

Findings: units PASS; tpcds-sf025 sweep PASS=96 MISMATCH=0 + stamp (40 plans moved, real fuzz-band churn); TPC-DS parity match=2 Q9/Q41 floor holds (agg 44→43, sort 69→67, qual 23→22); TPC-H patched-vs-HEAD plans byte-identical on private preloss-clone copy (match=7 both, floor ≥7 holds, nothing lost); manual Q12=2/Q13=34 canonical; ea-ratchet FAIL 10 NEW/13 FIXED — all NEW pg_est=None relset-key churn (needs triage + repin on resume, same class as S2b-14).

Next step: owner lifts bench/tpch/runtime_goopg/data.HOLD (unclean-shutdown hold, stale pid 1808130; recovery scripts/tpch-ref-recover.sh --i-am-owner) OR adds a gate-exceptions row for M0141-S2b-15 + tpch-spotcheck/tpch-acceptance-arm. Then: re-run those two gates on the staged tree → triage the 10 ea NEW findings → repin if churn → commit internal/.

Gates run: units PASS; tpch-spotcheck SKIP-BLOCKED (stamp); tpch-acceptance-arm not run (same blocker); tpcds-sf025 PASS + stamp; ea-ratchet FAIL (10 NEW, expected churn); pgbench smoke PASS (pre-commit hook).

In-flight: none — private servers (5534/5535) stopped, worktree tmp/head-wt removed, clone dirs tmp/tpch-s2b15-data + tmp/c20a/data-sf025 left on disk (tmp/, git-ignored).
