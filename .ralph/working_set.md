Task: M-NIGHTLY AI-007 EvalPlanQual — partial fix (delwctefail ERROR now fires; updwctefail still missing)

Files:
- internal/mvcc/visibility.go: `TupleVisible` — fixed xmax==currentXID check:
  cmax >= curcid → pre-image visible (was: unconditionally invisible).
  Matches PG's HeapTupleSatisfiesMVCC.
- internal/mvcc/subxact_visibility.go: `TupleVisibleSubxact` — same fix for
  the subxact-aware variant.
- internal/executor/operators_storage.go: `deleteWithUsing` — added
  TM_SelfModified check inside the EPQ chain-following path (truncated
  comment site at ~L6509). After epqFollowHOT/epqFollowChain resolves the
  new slot, re-pins and checks cmax against curcid. Different CID →
  errTupleAlreadyModified("deleted"); same CID → epqSkipDel.

Key symbols: TupleVisible, TupleVisibleSubxact, deleteWithUsing,
  errTupleAlreadyModified, GetCmax

Hypothesis/Findings:
- Root cause (visibility): TupleVisible unconditionally returned false for
  xmax==currentXID, hiding the pre-image that PG shows (cmax >= curcid →
  visible). The DELETE's scanMatching never collected the checking row
  because every version was invisible.
- Root cause (missing TM_SelfModified): the EPQ chain-following code had a
  truncated comment "// TM_SelfModified guard: the chain can lead into a
  version a" — the check was never implemented. After following the EPQ
  chain to our own transaction's version, the code went straight to
  predicate re-evaluation without checking whether a different sub-command
  (function call from CTE RETURNING) had stamped a different cmax.
- updateWithFrom (updwctefail) needs the same fix but the insertion point
  is different (no epqRetry loop; the check goes after the re-pin before
  stamping). Attempted twice but caused regression in line count — needs
  more careful placement.
- The parser defaults CREATE FUNCTION to Volatile="v", so
  inferSQLFunctionVolatility is never called for typical user functions.
  The command counter DOES advance for function calls — confirmed via
  server log showing cmax values differing from curcid.

Next step: Add TM_SelfModified check to updateWithFrom EPQ path (after
  isConcurrentlyUpdated else branch + re-pin, before oldTupleBytes).
  Then verify both delwctefail and updwctefail permutations raise errors.

Gates run:
- `go build ./internal/executor/... ./internal/mvcc/...`: PASS
- `go test ./internal/executor/...`: PASS (6.018s)
- EvalPlanQual repro: improved 1458→1462 lines (delwctefail now errors)
  but still FAIL (1462 vs 1468 expected; updwctefail still missing)
- `scripts/tpch-spotcheck.sh`: PASS (Q12=2, Q13=35)
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`: PASS
  (0 failed, all 3 workloads)
- `make ralph-state-guard`: REPAIRED (consistent after repair)

In-flight: none
