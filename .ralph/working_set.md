(idle — nothing in flight)

Last loop: **M0127-P5.6-f-vii** — LANDED, all three named gates green,
committed + pushed. It also **closed M0127-P5.6-f-viii** (Q47).
Facts the next loop must NOT re-derive:

1. `estimateAggregate` no longer answers `child/2`. `estimateNumGroups`
   (`internal/planner/cardinality.go`) is the port of `estimate_num_groups`
   (selfuncs.c:3449) — unique vars per grouping expr, per-relation ndistinct
   product clamped to `rel->tuples` (÷10 above one var, floored at the largest
   single nd), the Yao/Dell'Era restriction term (new `relFilteredRows` walk
   recovers `rel->rows` from the plan tree), product across rels, closing
   clamp to `input_rows`. Also ports `get_variable_numdistinct`'s no-stats
   tail and `clamp_row_est` (`clampRowEstF`).
2. **Q47 is FIXED.** The item was filed as explicitly NOT load-bearing for it;
   it was. `v1` body 3 626 → 7 252 (PG 7 643), `CTE Scan on v1` outer 6 → 12,
   `Nested Loop rows=1958` → `Hash Join rows=7252`, 12 s vs a 300 s timeout,
   100 rows matching the oracle. No rescan-cost term was needed — -f-viii's
   alternative hypothesis ("the rescan is unpriced") was never reached and
   stays unmeasured.
3. **The DS05 named TIMEOUT set is EMPTY** for the first time since §5.15.
4. Guard to remember: a new hand-written Expr type switch fails
   `TestExprSwitchInventoryIsPinned`. Build on `walkExprRefs`/`exprChildSlots`
   (exprwalk.go) — which is also what let `groupVarsOfExpr` distinguish
   "variable-free constant" (walk exhaustive, no refs → ignore) from "opaque
   expression" (walk aborted → DEFAULT_NUM_DISTINCT).
5. Ledgered, NOT implemented (4 upstream refinements + 1 sibling gap): EC
   dedup (step 3), `estimate_multivariate_ndistinct`, the boolean
   short-circuit (`exprType` unreachable in this package), the volatile arm,
   and `estimateSetOp` / `*Distinct` / `*DistinctOn` still running no group
   estimate. Each needs a planner facility that does not exist yet.

Gates run: UNITS green (`/tmp/units-p56fvii.log`, 0 failures, executor
re-ran at 6.2 s); estimate audit `2026-08-05-p56fvii.txt` — exit 1 is the
UNCHANGED standing state (Q18's only violation, improved 23 433× → 23 015×),
no new violation, all joinrels <1 % except Q20 which IMPROVED 30.2× → 24.9×;
DS05 sweep `sweep-20260805-112902.txt` exit 0, PASS=95 MISMATCH=0
CKMISMATCH=0 ERROR=0 **TIMEOUT=0**, delta named `PASS +Q47 / TIMEOUT −Q47`,
59/99 plan shapes changed; commit-hook pgbench smoke.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY and left unchecked per the banner. No new nightly run since.

Next step: per the banner (M0124 → M0125 → M0127), pick the next open M0127
P5.6 item from `.ralph/fix_plan.md` — the -f chain is now closed through
-f-viii, so the head of the remaining work is the P5.6-g / P5.7 items.

In-flight: none.
