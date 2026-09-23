# Working Set — ralph2 loop #2 end-state

Task: M0145-0008 (cutover) — prerequisite executor-capability sub-inventory
PRODUCED (docs/bookkeeping, no product code). 0008 stays `[ ]`.

Files:
- `docs/design/0100-0149/m0145-0008-executor-capability-inventory.md` (new).
- `docs/design/README.md` — new row `m0145-0008-inv`.
- `.ralph/fix_plan.md` — 0008 inventory bullet; new `[ ]` M0145-0027 (impl).
- `.ralph/deferral_ledger.md` — Q20 decorrelation row.

Findings: unexecutable set EMPTY (members 1-2 never generated = parity floors;
3 executable; 4 timing-only). Knob TPC-H acceptance 24/24 values identical
(`tmp/m0145-0008-inv-acceptance-knob.txt`); TPC-DS fireset 25/25 both scales
(`tmp/fireset-m0145-0018/`). NEW: Q20 29x on knob arm (scalar sublink inside
pulled IN body decorrelated; PG keeps SubPlan) → M0145-0027, blocks flip.
Captures: `tmp/hkp-out/inv0008d*.plans.txt` (default) vs
`tmp/hkp-out/hkpk1*.plans.txt` (knob), both PGSHAPED=1, parallel.
NOTE: the knob acceptance run overwrote `tmp/gate-stamps/tpch-acceptance-arm.json`
with a KNOB-arm PASS — re-run the default arm before any code commit (G8).

Next step: M0145-0027 — instrument Q20's scalar body planning on both arms
(which route plans it; body shape seen by `canUnnestSubquery`'s
`innerPlanIsIndexProbeCheap`), then arm-local fix + full gates.

Gates run: knob-arm acceptance (evidence only), TPC-H parity captures both
arms; pre-commit pgbench on commit.

In-flight: none.
