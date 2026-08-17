# Working set — M0134-0001 S16 LANDED

**Task:** M0134-0001 (`aggregates.sql`), slice **S16 — an order-sensitive
aggregate refuses its SPLIT, not the whole plan**. Selected per the Current
Priority banner (M-NIGHTLY drained: `ci/logs/action-items.md` still run
`20260817-011734`, all 6 `[x]`; nothing new to file).

**Fix:** deleted the `case *Aggregate:` arm from `subtreeHasUnsafeNode`
(`internal/optimizer/parallel.go` ~184-194). It suppressed every `Gather` in the
statement whenever an undecorated `array_agg`/`string_agg`/`json*_agg`/`xmlagg`
appeared anywhere — redundant (the split is refused independently by
`aggregateSplitIsSafe`→`AggregateIsDecomposable`, which the veto was never wired
to) and too strong (PG's `max_parallel_hazard_walker`, `clauses.c:827-970`, has
NO `Aggref` order case). `AggregateIsOrderSensitive` kept as documented
zero-caller code for the deferred S10b split gating.

**The measurement worth carrying — the predicted win did NOT materialise:**
`aggregates` **1001→999 lines, 29→29 hunks**. The structural claim still holds,
verified by the hunk's own context rather than the line count: `Aggregate`,
`Gather` and `Workers Planned: 2` all moved into unchanged context. What
survives is ONE line — `Parallel Seq Scan` vs `Seq Scan` — a **renderer** gap,
not a planner one. PG prefixes `"Parallel "` whenever `plan->parallel_aware` is
set (`explain.c:1630-1631`, beside the sibling `"Async "`); goopg's EXPLAIN
walkers have no such prefix. **This surface was unobservable before S16** — with
the veto in place no `Gather` was ever planted, so there was no
scan-below-a-Gather to mislabel. Third bucket lesson of the milestone and the
first where the label held: S11 and S15 were misattributions of *cause*; this
one was right about the cause and wrong only about how much diff sat behind it.

**An existing test encoded the old veto:** `TestPartialAggregateRefusals` went
red. Its positional-equality assertion held only because `isSplit=false` used to
imply "no Gather anywhere". Relaxed to sorted-multiset for `string_agg`/
`array_agg` only (two orderings observed across consecutive runs); `isSplit`
false, exact row count, and `count(DISTINCT v)`'s exact compare all kept.

**Files:** `internal/optimizer/{parallel.go,parallel_agg.go,parallel_test.go}`,
`internal/executor/parallel_agg_split_test.go`,
`docs/design/0134-0001-p7-parallel-aggregate-veto.md` + README row.

**Gates run:** `go build ./...` PASS; UNITS suite PASS (~9 min, warm cache);
regress fresh — `aggregates` 999/29, sentinels byte-identical (`functional_deps`
56, `groupingsets` 2373); `scripts/tpch-spotcheck.sh` PASS Q12=2/Q13=35;
pgbench smoke PASS via hook.

**Deferral ledger:** row 1440 flipped `resolved`; 2 new rows 2026-08-17 — the
`"Parallel "` label prefix, and the untested `array_agg(v ORDER BY v)` decorated
form (no coverage of the sort-below-the-aggregate path under parallelism).

**Next step:** continue **M0134-0001** with the **`"Parallel "` label prefix**
slice — cheapest remaining, EXPLAIN-text blast radius only, closes the last line
of the `array_dims` hunk and the same residue at `v_pagg_test`. Needs the
per-node parallel-aware flag readable in `internal/executor/operators_explain.go`
**and its ANALYZE twin** (`walkPlanFiltered`/`walkPlanAnalyzeFiltered` — the S11
twin-divergence trap). Other buckets: deparser/C11c 8, S6 min/max-InitPlan 5,
isolated bugs 4, qualification 3, Parallel Append (S10b) 2.

**Delegation:** `tmp/ralph-handoffs/m0134-0001-s16-agg-veto/` — `brief.md`
(researcher), `impl-brief.md` + `decision-brief.md` (implementer, 2 rounds,
round 2 = coordinator's multiset decision), `gate-brief.md` (tester, 1 round).

**In-flight:** none.

**Note:** untracked `internal/nodes/int2_cast_test.go` and
`internal/testport/datconnlimit_durability_test.go` are foreign WIP — untouched.
