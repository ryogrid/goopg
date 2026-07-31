Task: **M0125-0042** — OR-ed `IN (subquery)` operand keeps a stale column index
(silent wrong answer). **ROOT-CAUSED THIS LOOP; THE FIX IS NOT LANDED.**
Committed as diagnosis only — no engine file changed.

Files: `analysis/m0125-0042/` (README + 9 probes + 2 traces),
`docs/design/0125-0042-in-sublink-operand-stale-index.md`,
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

Key symbols: `planner.InExpr.Operand`, `reresolveJoinByName` / `predRebind`,
`applyJoinTreePosMap`, `buildBindingsPosMap`, `remapByPosMap`,
`resolveHostOperandIdx` (exists_to_any.go), `evalInExpr` (executor/expr.go).

Findings — do NOT re-derive these:
1. The operand carries the RIGHT `Name` and a STALE `Index`: bound **13**
   (`c_customer_sk` under `ca ++ c`), **9** at remap time (under `cd ++ c`),
   runtime needs **22** (under `ca ++ cd ++ c`). Index 9 is **`ca_zip`, a
   string**; `compareEq`'s string↔int coercion answers instead of raising.
2. Bisected by measurement: SEMI join exact (11996), value set exact (constant
   operand 11996), each arm exact (377/950), OR without EXISTS exact (11127).
   `A OR A` and `A OR ∅` are both 314 → one arm mis-evaluates.
3. Only **10** of goopg's 314 rows are in PG's 377 — the filed "over-match of
   35" is a cardinality coincidence, not the shape of the defect.
4. The item's filed first suspicion (`visitColumnRefs` descending into
   `*InExpr`) is **REFUTED** — that descent is correct.
5. Ruled out by measurement, do not re-test: hashed probe
   (`GOOPG_HASHED_SUBPLAN=off` identical), parallelism
   (`max_parallel_workers_per_gather=0` identical), determinism (stable), and a
   synthetic 6-table minimal case that reaches a **structurally identical plan
   and still answers correctly** (trigger is binding history, not plan shape).
6. Three maskings: `reresolveJoinByName` never fires (tree is
   `Filter → MultiHashJoin`, no `*Join`); a single `IN` unnests and rebinds by
   Name; and **EXPLAIN prints the right Name over the wrong index**.

Next step: implement the fix — generalise `resolveHostOperandIdx` to
hand-written `InExpr` operands (re-resolve by Name against the host node's
output schema after the last remap, under a `findUniqueColumnIndex`
unique-match guard; do not descend into the sublink's own `Plan`). Acceptance:
`probe35g.sql` → 1294 AND `analysis/m0125-0042/pAA.sql` → 377, plus a planner
test asserting the operand index equals the host schema position. Bar: units +
`tpch-spotcheck.sh` + TPC-H plan-diff + full 99-query SF0.5 gate.

Gates run: units PASS (`RALPH_PRECOMMIT_SCOPE=units`); pgbench smoke via the
commit hook. Planner gates N/A — no engine file changed.

In-flight: none. All instrumentation reverted (`git checkout` of bushy.go,
planner.go, expr.go); tree builds clean.

Clusters: goopg :65437 and :65436 DOWN, throwaway :5533 stopped and its data
dir removed; PG :65438 was already UP and is left UP. Nightly was NOT running.

Also this loop: **`M0125-0037` stage (ii) was measured out before selection** —
its acceptance `Q5 → 5|OK|100` is already green (Q8/Q14/Q54/Q71 too). Left
unchecked; see its item body. Timeout class is now **Q30 Q64 Q65 Q78 Q81**.
Banner order going forward: `-0042` (fix) → `-0041` → `-0034` join-order arm →
`-0038` last. Filed, unworked per the banner: nothing new
(AI-20260731-001201-001 was already on the M-NIGHTLY list).
