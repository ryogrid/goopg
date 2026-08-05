(idle — nothing in flight)

Last loop: **M0127-P5.6-d** — LANDED, gates green, committed + pushed.
Facts the next loop must NOT re-derive:

1. `costJoinCandidate` (`internal/planner/bushy.go`) no longer adds anything to
   `hashJoinCost`. M0126-0013's `overshoot² × cpu_tuple_cost × innerRows` above
   a fixed `largeBuildThreshold = 2_000_000` is DELETED. Do not re-add it: a
   comment at the deletion site says why, and
   `TestCostJoinCandidateHasNoRowCountPenalty` fails if anything is layered back
   on top of the cost function.
2. **The measurement that justifies it.** The penalty keyed on a ROW COUNT;
   spilling is decided in BYTES. Against the 512 MB default budget: a 4 M-row
   1-column build FITS (NBatch=1) and the penalty charged it 40 000 anyway; a
   1 M-row 40-column build spills to 4 batches and the penalty charged it
   nothing. P5.7-a's `hashsize.Choose` term fires on exactly the builds the
   executor batches.
3. Fit boundary for writing future tests (probed, `DefaultMemLimitBytes`
   = 512 MB): at ncols=1, 4 M rows fits and 5 M spills — the bucket array
   (48 B/slot, power-of-two) is what tips it, not the 72 B entries. A
   zero-column entry is 24 B but still allocates that array, so ncols=0 is NOT
   a "no-spill baseline".
4. Test helper added: `dpEntryOfWidth(rows, ncols, cost)` in
   `cost_funcs_test.go` — a `dpEntry` whose plan is `&SeqScan{schema:
   make(Schema, ncols)}`, because `entryNCols` reads only `len(plan.Output())`.
5. **DS05 not run and that is not a skip** (same reasoning as P5.7-a):
   `costJoinCandidate` is reachable only under `costDrivenJoinOrder`, OFF by
   default, so the default planner arm is byte-identical. SPOT was run anyway
   and confirms it empirically.
6. No deferral row: upstream has no such penalty, so nothing PG does is left
   unimplemented by removing it.

Gates run: UNITS green (`/tmp/units-p56d.log`, exit 0, zero FAIL lines, planner
re-ran at 0.65 s); SPOT `scripts/tpch-spotcheck.sh` RESULT=PASS
(`/tmp/spot-p56d.log`, Q12 rows=2 in 16.0 s, Q13 rows=35 in 12.0 s, both
canonical, peak 10 833 MB); commit-hook pgbench smoke. No orphaned servers.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY (fix_plan lines 1097/1203/1215) and left unchecked per the banner.
No new nightly run since.

Next step: per the banner (M0124 → M0125 → M0127), the head of open M0127 work
is **M0127-P5.7-b** — plumb PG's `tuple_fraction` into `grouping_planner`'s
choice between `CheapestTotal` and `CheapestStartup`, so the Startup/Total split
P5.7-a made meaningful actually SELECTS a plan under LIMIT. 04 §4.1 end.

In-flight: none.
