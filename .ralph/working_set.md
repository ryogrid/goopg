# Working set — M0134-0001 S22 was a SCOPING loop: two NO-GOs, residual re-characterised

**Task:** M0134-0001 (`aggregates.sql`), slice **S22**. Selected per the Current
Priority banner (M-NIGHTLY drained: `ci/logs/action-items.md` still run
`20260817-011734`, all 6 filed and `[x]`; nothing new to file).

**No code changed this loop — by design.** Both candidates the S21 map named as
next-best were scoped and both came back NO-GO with evidence. Recording that is
the deliverable; manufacturing a GO would have been the failure.

**Baseline reconfirmed at HEAD `45cb67c0`: `aggregates` 930 lines / 25 hunks** —
S21's prediction hit exactly.

**NO-GO 1 — `Group Key:` qualification (hunks #8/#9).** Not an EXPLAIN-formatter
gap: `operators_explain.go:778-816` already reuses the shared `formatExprQual`/
`qualify` helper. Real cause is **three** independent mechanisms —
`planner.go:3013,3059` gives every child scan of an inherited/partitioned table
the SAME `SourceTableIdx` (so `explain_names.go:96-98,189-205` counts a
multi-child `Append` as one relation; PG uses `rtable_size > 1`,
`explain.c:774-782`); a partitioned table has **no parent scan node at all** in
goopg's plan (hunk #9's `p_t1` names nothing); and PG's `inherit.c` positional
Seq-Scan aliasing is absent (hunk #8). Entangles the already-failing
`partition_aggregate.out`, which uses the OPPOSITE alias rule.

**NO-GO 2 — NOTICE trans-function ordering (hunk #14). S21's hypothesis is
REFUTED.** goopg's `1,3,1,3` is **not a bug** — it is PG's own baseline
semantics, correctly implemented. PG's `1,1,3,3` is a side effect of the
presorted-DISTINCT-aggregate optimization (`aggpresorted`,
`nodeAgg.c:4260-4271` / `planner.c:3199-3227`, GUC `enable_presorted_aggregate`).
**Trap for the next loop:** goopg's `applyPresortedAggregateRule` (S8 slice 2a)
ports only the pathkey-*ordering* half of that same PG function — it does NOT
set `aggpresorted`. Closing 1 hunk would cost a new planner feature plus a third
arm in `operators_join_agg.go:applyAgg`/`finishAgg` (hottest aggregate path,
every TPC-H/DS query), and built-in DISTINCT aggs already advance inline while
user-defined ones buffer — naive unification regresses built-ins.

**Residual re-characterisation (the real finding):** after S21, **no small
isolated slices remain** in `aggregates`. All 25 hunks are ruled out (deparser
8, varno 1, VERBOSE/parallel 4, SubPlan-absence 3) or large/confounded (min/max
inheritance bundle, Incremental Sort — operator absent, join-method selection,
#8/#9, #14).

**Next step:** do NOT slice M0134-0001 further. Either (a) scope hunk #7 knowing
it is two orthogonal planner gaps in one hunk needing de-confounding first, or
(b) **re-scope/park M0134-0001 and advance to M0134-0002** — the remaining
`aggregates` work is 3-4 genuine feature milestones, not slices.

**Files:** `.ralph/fix_plan.md` (S22 note + residual re-characterisation),
`.ralph/deferral_ledger.md` (2 rows 2026-08-17). No `internal/` changes.

**Gates run:** `scripts/pg-regress-runner.sh --verbose aggregates` (baseline
930/25, unchanged). No build/unit gates needed — zero code change.

**Delegation:** `tmp/ralph-handoffs/m0134-0001-s22-groupkey-qual/` (researcher
`a2921fd55e90154b3`, 2 rounds, both NO-GO; report.md has full citations).

**In-flight:** none.
