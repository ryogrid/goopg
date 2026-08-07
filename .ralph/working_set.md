M0128-P6.1 — column-path DISABLED; slot side-channel works.

Task: M0128-P6.1 — root cause FOUND and FIXED for self-join FOR UPDATE
  0-rows failure. Column path disabled; slot side-channel passes all gates.

Files:
  - internal/planner/planner.go: wireRowMarkCtidColumns call replaced with
    numCtid := 0 (+ comment documenting why column path is disabled)
  - internal/planner/locking_test.go: TestPlanCtidRowMarkWiring and
    TestPlanCtidRowMarkMultiTable updated for disabled column-path
    (CtidResno=-1, NumCtidCols=0)

Key symbols: wireRowMarkCtidColumns (now unused), planSelect line ~1628

Hypothesis/Findings: CONFIRMED. The ctid column added to SeqScan(a1)'s
  schema by wireRowMarkCtidColumns caused column misalignment in parent
  nodes (Hash Join). The Join's schema was built BEFORE ctid addition
  (3 left cols + 3 right cols = 6 output), but the SeqScan now produces
  4 values. The 4th value (ctid "(0,1)") leaks into a2's first output
  position, shifting all a2 columns by 1. The WHERE filter then evaluates
  a1.accountid = a2.accountid using the MISREAD a2 value (ctid string
  instead of accountid), which never matches → 0 rows. Evidenced by:
  cross-join WITHOUT WHERE showed "(0,1)" where a2.accountid should be.

Next step: DS05 sweep blocked by nightly CI (ci/batch running since 00:57).
  Wait for nightly to finish, then: FORCE=1 scripts/tpcds-sf05-regression.sh sweep
  If DS05 PASS → mark P6.1 [x], update root-0038 ledger, commit final.
  If DS05 FAIL → diagnose.

Gates run: UNITS PASS, SPOT PASS (Q12=2/Q13=35), ISOLATION PASS
  (eval-plan-qual + eval-plan-qual-trigger), DS05 BLOCKED (nightly CI)

In-flight: DS05 gate blocked by nightly CI (ci/batch PID 65718 since 00:57).
  Command: scripts/tpcds-sf05-regression.sh sweep
  Blocked by: FATAL: the nightly CI batch is running
