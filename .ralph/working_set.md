# Working set — inter-loop baton

Task: **M0145-0012 — MEASURED, and it is a NO-GO as filed.** Marked `[!]`.
No removal landed. This is an ESCALATION, not a completion.

## Banner

Item 3 now reads `… → M0145-0011 [x] → M0145-0012 [!]`. With item 3 exhausted,
**the next selectable work is item 4 (`M0141-S2a-fix2r`)** — re-read the banner
and confirm before selecting; items 0-2 must be checked first as always.

## What M0145-0012 measured

Apparatus landed: `GOOPG_CTE_ROWS_FALLBACK=off` (default ON, flag-provenance
table) + a `CTEROWSFALLBACK` DP-trace line + a unit gate.

```
census (TPC-DS SF0.25, DEFAULT arm, 99 queries): the arm engages on THREE
  Q31 ws 1->1846   Q39 inv 1->20   Q74 year_total 1->8325   (nowhere else)

A/B with the arm off: those same three move, each INTO the collapse class
  every equi-condition demoted from join condition to Join Filter on a NL
  Q39 3433->3564 ms | Q31 2133->2381 ms | Q74 1509->25005 ms  (16.6x)
  values BYTE-IDENTICAL in all three
```

- **Route 1 ("no collapse-class change") is FALSE** — it reproduces exactly
  that class, and per M0145-0011 the class is scale-dependent, so SF1 is worse.
- **Route 2 (`pushQualsThroughSingleRefCTEs`, refs==1) is INAPPLICABLE** — all
  three fires are MULTI-reference CTEs (ws x3, inv x2, year_total x4).
- **The real prerequisite is a third thing neither route names**:
  `examine_simple_variable`'s non-recursive-CTE arm
  (`postgres/src/backend/utils/adt/selfuncs.c:5736-5870`) finds the CTE's
  `subroot` via `cte_plan_ids` and RECURSES on the target-list `Var`, so PG
  estimates `year_total.dyear` from `date_dim.d_year`'s real statistics and
  never collapses. Port that and the arm can go.

**Carry this**: the regression is INVISIBLE to every value gate in the repo —
values byte-identical, only plan shape and clock move. That is why
`TestInitialRelRowsCTEFallbackGate` exists.

## Next step

Re-read the banner and select from item 4 onward (item 3 is exhausted:
0001-0007 `[x]`, 0008 blocked, 0009/0010/0011 done-or-escalated, 0012 `[!]`).
Item 4 is **M0141-S2a-fix2r** — re-apply the PG-faithful `hashAggEntrySize`
change discarded for parity reasons; degradations it causes are filed as their
own tasks, never reverted (owner Q4).

## Open owner escalations

M0145-0012 (this one), M0145-0010 (both premises measured wrong),
M0140-0007 (capability exists; label-only change declined), M0145-0008
(cutover blocked on a measured timing regression).

## Traps carried forward

- Value gates cannot see a cardinality regression; check plan shape + clock.
- Flags are read once at process START — an A/B needs two server runs.
- A knob-arm capture in the canonical results dir poisons the next default
  sweep's baseline — redirect `SF025_RESULTS_DIR`.
- Gate stamps hash the staged index — re-run a gate if code changed after it.

## Gates run

units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 default arm
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plans 99/99 identical,
verdict-changes=none; TPC-H acceptance arm 24 MATCH; pgbench smoke via hook.

## In-flight

none
