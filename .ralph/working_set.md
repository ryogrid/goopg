# Working set — inter-loop baton

Task: **M0145-0017 — CLOSED `[x]` as measured-no-gap.** The census line is the
whole production change; step 2 would have been built against zero witnesses.

## Banner — the loop-48 escalation is STILL UNANSWERED

Item 3: `… 0016 [x] → 0017 [x] → 0018`. **Next selectable: M0145-0018**
(`outer-over-derived` firewall relaxation — owner GO already given).

The ordering question filed under M0145-0016 has had no owner response: by the
strict selection rule **M0145-0003 is the first `[ ]` in item 3** (as are 0004,
0005, 0007, 0008), yet loops 39-50 worked 0009 → 0017. The loop continues on
the standing assumption that 0003-0008 are umbrella items whose children are
the selectable work. **Still the banner's call, not the loop's.**

## What M0145-0017 established

PG's reach is bounded from the ORACLE, not assumed: `pull_up_sublinks`
(`prepjointree.c:468`) begins and ends at `root->parse->jointree`, one caller
(`plan/planner.c:737`). A jointree carries quals in exactly two places — the
`FromExpr`'s (WHERE) and each `JoinExpr`'s (ON). Target list, HAVING, GROUP BY,
ORDER BY and nested scalar contexts are **never candidates upstream either**.
So the goopg/PG reach difference is EXACTLY the ON clauses.

```
new instrument: ONSUBLINK jointype=<t> site=<kind>@<position>  (planJoinPredicate)
TPC-DS SF0.25, 99 queries, BOTH arms:   ONSUBLINK 0
routes: knob 46 jointree-pullup + 567 pinned-spine | default 398 pinned-spine
textual scan of the query corpus agrees: no ON clause holds a subquery
```

The join TYPE is recorded because PG's legality boundary differs by it — INNER
both sides, LEFT the RHS only, RIGHT the LHS only, **FULL never** (pulling from
a null-preserved side is a wrong-answer class).

Ledgered as still-unbuilt: goopg's ON-clause walk itself. Resume condition is
mechanical — if `ONSUBLINK` ever fires with a pullable jointype at `@top`/`@not`,
extend `pullUpSublinksIntoJointree` under the same side/type rules.

## Next step

**M0145-0018** — relax the `outer-over-derived` firewall. Owner GO is on
record. Its preconditions are to be CHECKED, not assumed:
1. **M0145-0013 landed** — it did (loop 43).
2. **Fresh E1 re-verification on the current tree**: re-run the SF0.25 seam
   census for the CURRENT `outer-over-derived` set (the population drifts — it
   was 12, then 3, and the last census showed **6**), then re-capture plans +
   timings + values for Q77/Q78 and the current fire set on the knob arm with
   `GOOPG_DERIVED_FIREWALL=off`. Read the full task text before starting.

## Traps carried forward

- **Re-read the banner every loop** (and see the unanswered escalation).
- A seam change is NOT pipeline-scoped — `tryPGShapedJoinSearch` is shared;
  check the default-arm gate before claiming knob-arm-only (caught in loop 49).
- When `translateToLayout` refuses, print the columns that WERE available at
  that node before theorising about which mechanism owns the failure.
- A new hand-written Expr type switch fails `TestExprSwitchInventoryIsPinned`;
  pin it AND add a ledger row.

## Gates run

units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 default arm
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plans 99/99 identical,
runtime-moves=0; TPC-H acceptance arm 24 MATCH; the commit hook's smoke runs
on commit.

## In-flight

none
