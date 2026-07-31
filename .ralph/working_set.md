Task: **M0125-0041** (correlated scalar-aggregate sublink over a WITH item,
Q30/Q81) — root cause FIXED and committed; **the item stays UNCHECKED** because
its acceptance is a completing Q30 and Q30 still TIMEOUTs. Nothing in flight.

**Next loop: read the `## Current Priority` banner FIRST.** The banner's
standing order is now `-0043`(done) → `-0042`(done) → `-0041`(worked, open) →
**`M0125-0034`'s join-order arm** → `M0125-0038` (last). Expected selection is
**`M0125-0034`**, which now also owns Q30/Q81's acceptance.

Findings — do NOT re-derive:
- The scalar pull-up **never declined** for Q30. Every gate accepts it
  (`canUnnestSubquery`=true, avg NULL-on-empty, `*1.2` strict, AND-reachable,
  1 param / 0 residuals, inner not probe-cheap). It died in
  `clonePlanReplacingOuter` on the `default:` arm — no `*CTEScan` case — and a
  clone failure is a *bail*, so the gap looked like policy. Fixed: `*CTEScan`
  (body shared verbatim) + `*MaterializedCTEScan`, guarded by new
  `planSubtreeHasOuterRefDeep`; matching arms in sibling `planCloneSupported`.
- **Q30 TIMEOUTs at 300 s AND at 1200 s** after the fix → shape defect, not a
  budget crossing (same class as Q21/`M0125-0032`). Residual = C1: the plan
  keeps `Nested Loop (CROSS)` of ~2×10⁴ CTE rows × 5×10⁴ `customer_address`
  rows = 10⁹ pairs, each an index probe into `customer`.
- **First probe for `-0034` on this query:** `ca_state = 'AR'` is a single-table
  local filter (~50× shrink) that STILL does not reach the `customer_address`
  scan — it sits in the top `Filter` beside the formerly sublink-bearing
  conjunct. Find out whether the sublink's presence kept it there.
- **`make plan-diff LABEL=tpcds-round2-head` is STALE: 22/22 diverged, with or
  without this change** (stash A/B proves the live plans are identical in both
  arms). Snapshot is S-cold (bare `Seq Scan`, no `Gather`); live plans carry
  `(stats)` + parallel workers since the warm-stats programme. Re-capture the
  label before relying on it; use a stash A/B meanwhile.
- goopg's parser rejects `WITH` inside a sublink (PG accepts it), so the
  outer-ref guard is unreachable from SQL and has no test — ledger row.
- TPC-DS SF0.5 timeout class unchanged: **Q30 Q64 Q65 Q72 Q78 Q81** (6).
- Nightly triage: `ci/logs/action-items.md` still has only
  AI-20260731-001201-001 (testport/TestE2E_FailoverGoopgToPG), ALREADY filed
  under M-NIGHTLY. Nothing new to file; stays unselected per the 2026-07-28(b)
  amendment (not a build break, not an M0124/M0125 gate).

Gates run this loop (all PASS): `go build ./...`; `go vet ./internal/planner/`;
`go test ./internal/planner/... ./internal/executor/`;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`;
`scripts/tpch-spotcheck.sh` (RESULT=PASS, Q12=2 Q13=35); TPC-H plan A/B
(0/22, byte-identical, `plan_snapshots/m0125-0041-{before,after}.txt`);
full 99-query TPC-DS SF0.5 sweep (PASS=89 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=6 SKIP=4, **all 99 cells identical in status/rows/ck** to
`sweep-20260731-121447.txt`); pre-commit pgbench smoke (hook);
`make ralph-state-guard` (repaired 1 stale marker, then OK).

In-flight: none. All bench servers stopped and verified down (65433/65436/65437).
PG oracle :65438 was already UP from a prior loop and is left UP, untouched.
