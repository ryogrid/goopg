Task: M-NIGHTLY AI-007 EvalPlanQual — partial fix, updwctefail still not erroring

Files:
- internal/executor/operators_storage.go: `updateWithFrom` — two TM_SelfModified checks added:
  1. pre-HOT RLock probe (before HOT attempt, catches HOT-path bypass)
  2. non-EPQ else branch (after isConcurrentlyUpdated returns false)

Key symbols: updateWithFrom, tryApplyHOTUpdate, errTupleAlreadyModified, isConcurrentlyUpdated

Hypothesis/Findings:
- delwctefail NOW errors (confirmed by previous loop's 1458→1462 improvement).
  My changes did not regress this (verified: line count stable at 1462).
- updwctefail STILL does NOT error (1462 vs 1468 expected, 6 lines short).
  The TM_SelfModified checks added to updateWithFrom (pre-HOT probe + non-EPQ
  else branch) are NOT reached during the test. Root cause unknown — the
  checking row's xmax at the probed slot is not our XID, or the code path
  through scanMatching→pending→HOT/non-HOT does not encounter the pre-image
  tuple. The test permutation is multi-session (wx1 updwctefail c1 c2 read),
  so the EPQ wait+retry may alter the tuple state before my checks run.
- A third check was attempted inside tryApplyHOTUpdate (serena) but was
  reverted — it used position 0 for the error, and adding a `pos` parameter
  to the shared helper would be needed for a clean fix there.

Next step: Debug why the updateWithFrom TM_SelfModified checks aren't reached.
Add server-side logging to trace the tuple's xmax at the pre-HOT RLock probe
and at the non-EPQ else branch. The EPQ chain resolution (after wx1 commits)
may route the update to a different slot where xmax is Invalid.

Gates run:
- `go build ./internal/executor/...`: PASS
- `go test ./internal/executor/...`: PASS (6.077s)
- `scripts/tpch-spotcheck.sh`: PASS (Q12=2, Q13=35)
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`: PASS
  (0 failed, all 3 workloads)

In-flight: none
