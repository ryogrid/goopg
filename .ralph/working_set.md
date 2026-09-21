(idle — nothing in flight)

# Loop #59 result — M-NIGHTLY AI-20260922-004850-001 FIXED and committed

`TestPort_IsolationEvalPlanQual` returned the PRE-update `tableAValue` where
PG returns `newTableAValue`. Root cause was NOT where loop #58 pointed.

**Root cause.** `resolveRowMarkCtidResnos` (internal/optimizer/planner.go)
located each locked-relation column in the root output by `(Name,
SourceTableIdx)`. A target-list ALIAS renames the output schema column, so
`SELECT ta.value AS ta_value` leaves no `value` entry and EVERY position
resolved to `-1`. At the merge site a `-1` means "not carried in the output",
so the correctly re-fetched post-update values were silently dropped.

**Fix.** `findLockedColByTarget` + `findTopProjectForOutput`: on a `-1`,
resolve through the Project TARGET, whose `ColumnRef.Name` keeps the original
source name under aliasing. `-1`-means-absent preserved.

**Correction carried forward:** loop #58 named the discriminator as "a SubPlan
attached to the locked scan". That was WRONG — re-running its minimal pair at
HEAD gave the correct answer. The spec step has both a subquery and aliases;
the hand-built probe kept the subquery and dropped the `AS`. The rule worth
keeping: when a hand-built repro disagrees with the failing test, the REPRO is
the suspect — instrument the real run instead of refining the imitation. (The
working instrument: snapshot the spawned server's `cluster.log`, which lives
under `t.TempDir()` and is deleted at teardown, while the test runs.)

## Gates (all green)
- units; `TestPort_IsolationEvalPlanQual` PASS; **entire** `TestPort_Isolation*`
  suite PASS (417s) — the right risk gate for a rowmark change
- tpch-spotcheck PASS (Q12=2, Q13=33 canonical), re-run against the STAGED tree
  so the stamp is valid
- tpcds-sf025 PASS — 99/99 plan shapes identical, confirming the change is
  inert outside rowmark plans
- Both new unit tests verified to FAIL with the fix disabled (not vacuous)

## Next loop
Re-read the banner and select fresh. M-NIGHTLY still has open items:
PgAmcheck003 x4 (-002..-005), PgoutputInterop x10 (-006..-015), and
`TestPort_RegressSuite` (-016, 4 subtests, confirmed still failing at HEAD and
NOT stale-sha fallout from the bpchar slices; note in fix_plan says to get the
diff via `scripts/pg-regress-runner.sh`, since that test does not persist one).

## Owner escalations STILL UNANSWERED (carried since loops 48 and ~57)
1. M0145-0018's cost-model no-go.
2. Banner ordering: M0145-0003 is strictly the first `[ ]` in item 3, yet
   loops 39-51 worked 0009 -> 0018.
