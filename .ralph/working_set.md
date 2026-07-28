(idle — nothing in flight)

Last loop (#48): M-NIGHTLY item AI-20260725-011243-004
(`TestPort_IsolationEvalPlanQual`) FIXED and committed.

Root cause was NOT an EPQ tuple-version bug (the loop-47 triage guess).
`lockRowsOp` buffers its whole result (`drainAndStamp` → `drained=true`, rows
served from `pending[pos++]`) and its `Open` is the operator's ExecReScan entry
point, but `Close` cleared only `pending`. The SECOND `Open` —
`classifySubPlan`'s `rescanCloseOpen` for the `EXISTS (… FOR UPDATE)` sublink,
performed during the EvalPlanQual recheck — answered `Next` with EOF without
re-scanning, so `EXISTS` went FALSE with zero `noisy_oper()` NOTICEs and
`updateOp` dropped the row (silently lost update: `checking|400` vs PG `-800`).
Fix = reset `pending`/`pos`/`drained` at the top of `lockRowsOp.Open`
(3 lines + comment). `drained` is unique to `lockRowsOp` — no sibling path.

Design doc `docs/design/root-0030-lockrows-rescan-state.md` (+ README index).
Ledger row appended: `lockRowsOp` still locks EAGERLY (whole child drained and
stamped at first `Next`) where `ExecLockRows` locks one row per parent pull.

Remaining open M-NIGHTLY task (preempts M0124):
- The 9 genuine sub-timeout regress divergences: `errors`, `index_including`,
  `portals_p2`, `select`, `select_distinct` still diverge in the FULL suite at
  HEAD but pass in isolation ⇒ suite-ordering state leakage (a case mutating
  shared `test_setup` fixtures), not normalization rules. Re-run with an
  explicit `-timeout 60m` (the default 10m hit inside `tidscan`) plus
  `GOOPG_REGRESS_DIFF_DIR` to capture the real diffs, and do NOT run it while a
  nightly batch is live (co-load).
Then M0124 → M0125 per the 2026-07-28 directive.

Gates run: `TestPort_IsolationEvalPlanQual` PASS (27.6 s);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`go test ./internal/executor/` PASS; 21 row-lock isolation specs PASS (80.8 s);
14 FK/MERGE isolation specs PASS (36.7 s); `scripts/tpch-spotcheck.sh` PASS
(Q12 rows=2, Q13 rows=35). TPC-DS SF0.5 gate deliberately skipped — no TPC-DS
query builds a `lockRowsOp` (rationale recorded in the design doc).
In-flight: none.
