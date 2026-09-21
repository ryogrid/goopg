(idle — nothing in flight)

# Loop #78 result — M0145-0009 CLOSED by its completion census

Banner (now COMMITTED as `e1ab727d0`): OWNER GO — M0145-0009 → **M0145-0019**
→ M0145-0020, then 0004 → 0005 → 0007 → 0008. This loop took the first,
M0145-0009, whose only remaining step was "re-run the census afterwards".
Recon: no `internal/`/`cmd/` file touched (C1).

## RESOLVED: loop #77's uncommitted fix_plan WIP
The owner's commit `e1ab727d0` INCLUDED the M0122-0008 write-up and both
residual tasks from the working tree. Nothing was lost; fix_plan is clean and
committable again.

## The census answers the question — the `*CTEScan` bucket is NOT a residual
TPC-DS SF0.25 private lane, knob arm, `GOOPG_NLI_CENSUS=1`, 99 queries, BOTH
arms captured this loop (same recipe, same binary sha):
| class | CTE_LEAF off (default) | on |
|---|---|---|
| `(pulled)` | 27 | **42** |
| `any-body-leaf-(*optimizer.CTEScan)` | 15 | **0** |
| `SubqueryExpr@scalar` / `ExistsExpr@or` / `InExpr@or` | 15 / 2 / 1 | unchanged |
27+15=42 — ONE-FOR-ONE conversion, nothing partial, no other class moves.
M0145-0013 already addresses it in code; only that arm's promote-or-delete
(C5) decision remains.

## The arm is NOT free — ledgered against M0145-0013
302 plan-diff lines between the two goopg captures; `qual-placement` 28 → 29,
`match` still 1. Inside ±3 so not a regression, but not "no plan change"
either. 0013's decision must re-measure with a repeat capture, not read the
+1 off this run.

## MEASUREMENT RULE discovered (ledgered) — do not trust census totals
M0145-0015's census, nominally the same recipe one day earlier, reported
almost exactly DOUBLE every figure across all five classes, proportions
unchanged (54/30/29/4/2 vs 27/15/15/2/1). Five independent halvings are far
less likely than one capture-recipe difference. NOT resolved. Consequence:
**absolute census totals compare only within an identical capture recipe; the
class PROPORTIONS carry.** This loop's conclusion is a within-capture A/B, so
it holds either way.

## Gates
Recon — no production diff, so no value gates (C1). state guard OK; pgbench
smoke via the commit hook. Lane cleaned up by the capture script's own trap.

## Next loop
**M0145-0019** — nested-loop costing for a derived inner (owner GO, 0018's
option (c)). Then M0145-0020 (`examine_simple_variable` CTE arm, 0012's
prerequisite).

## Owner escalations — the M0145 ones are ANSWERED; two remain
M0145-0001 lineage and M0145-0018/0012 were answered by the 2026-09-22 GO.
Still open: partition_aggregate's inventory row; the template1 namespace
collision (Option A vs B) with two dependent tasks.
