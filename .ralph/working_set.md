# Working set — loop #185 → next

ESCALATION (S2, wrong results) — OWNER PLACEMENT NEEDED (unchanged; filed, not selected):
- P0-E12 (2026-10-24): computed text group key over a borrowed scan aliases the page arena. Repro analysis/p0e12/.
- P0-E11 (2026-10-23): `0::numeric / 3` prints `0` (PG `0.00000000000000000000`). Repro analysis/p0e11/.
- P0-E9 / P0-E10 also await placement (fix_plan entries in the M-NIGHTLY section, ~line 749/770).
ESCALATION (R3) — 2026-10-27: M0147-0048 [!] — retire GOOPG_EXISTS_TO_ANY OFF now, or hold behind M0146-0012b?
  Evidence analysis/m0147/m0147-0048/. Owner decides.
Task: M0122-0020 landed `da7f0c117`, pushed — EXPLAIN ANALYZE actual time is per loop in every renderer (msPerLoop:
  text label, Hash line, Worker line, JSON), and instrumentedOp now times only its own Open/Next calls (EOF included)
  into loopNs, summing startup per loop (PG InstrEndLoop). S9 census at e8367f901 (routing-20261027l) committed with it.
Files: internal/executor/{instrument.go, operators_explain.go, explain_actual_time_per_loop_test.go (new)};
  docs/design/0100-0149/m0122-0020-actual-time-per-loop.md; ledger row (JSON Node Type = whole label gap).
Key symbols: instrumentedOp.Open/Next, nodeStats.loopNs, msPerLoop, rowsPerLoop, planToJSONWithStatsNamed.
Hypothesis/Findings:
- Item 10 works "the rest of M0122" in file order: next open [ ] is M0122-0019 (Planning Time omits the join search:
  Q64 prints 0.17 ms for ~1.6 s), then M0122-0013/0014/0015 (replication / logical / test-suite porting — large).
- FORMAT JSON "Node Type" carries the whole describePlan label ("Seq Scan on x o"); PG splits Relation Name/Alias/
  Index Name. Ledgered 2026-10-27 (M0122-0020 row), not filed as a task.
- Scratch PG oracle: tmp/pgscratch-m0053 (port 5545, socket /tmp; pg_ctl -o "-p 5545 -k /tmp"); nlij_* loaded.
- Regress compare recipe: GOOPG_REGRESS_DIFF_DIR=<abs dir> go test ./internal/testport/ -run '^TestPort_RegressSuite$';
  count diff lines per case of <case>_expected vs _actual; baseline now tmp/m0122-0020-regress (81 cases, 87076).
- Never put the loop-marker env assignment on a command line or in heredoc text — the bash guard denies it.
- Nightly fires ~01:00-01:20 and runs ~75 min; acceptance + sf025 refuse meanwhile (wait on run-nightly PID).
Next step: S9 — da7f0c117 touched internal/executor → FRESH census (label s9ad, routing-20261027m) + note in
  M0146-0014. Then item 10: M0122-0012 under the return-first rule (top eligible Candidate-pool entry, Target first; else G1–G5);
  M0122-0019 only during a one-task yield (find where the EXPLAIN path starts
  the planning timer vs optimizer.PlanWithSettings / preplanWithClause; PG times pg_plan_query whole).
Top-residual: n/a (TPC-H 22/22, TPC-DS SF0.25 99/99, SF1 99/99 at e8367f901)
Skip-rationale: M0146-0014(channel), M0146-0042(channel); M0146-0007/0012b/0067/0175,
  M0146-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239/0243(below-item-10);
  M0142-0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003/0010); P0-E9(owner-placement); P0-E10(owner-placement);
  P0-E11(owner-placement); P0-E12(owner-placement); M0119-0006 held [!]; M0146-0010a held [!];
  M0122-0012 is selectable again (return-first rule, OWNER DECISIONS 2026-10-27); M0147-0048(owner-R3 retire-vs-hold)
Gates run: S9 census fresh at e8367f901 (22/22·99/99·99/99); units PASS, spotcheck PASS, acceptance 24 MATCH, sf025
  PASS + plans same=99, TPC-H census on tree 22/22, RegressSuite counts = baseline (plpgsql flapper 1653->1644),
  Isolation PASS, commit hook pgbench PASS; fire-set out of scope (executor only).
M0122-0012: active — return-first rule (OWNER DECISIONS 2026-10-27); Candidate pool C1–C15 at the top of the entry,
  Below-bar list there too; no yield in progress.
In-flight: none
