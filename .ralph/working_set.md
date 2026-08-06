(idle — nothing in flight)

Last loop: **M0125-0040 ("C6") CLOSED — grouping sets no longer re-scan the
source once per set. Candidate (a) landed, default ON.**

1. **Mechanism reused, not built.** goopg already materializes a non-recursive
   CTE on its first reference and REPLAYS it from `ctx.CTERowCache`
   (`cteScanOp.Open`). So the fix is an AST rewrite, not an executor node:
   `shareGroupingSetsSource` hoists `FROM`+`WHERE` into a synthetic
   `__gs_src_<parse-pos>` materialized CTE projecting exactly the referenced
   columns (never `*`), and every generated branch reads it.
2. **Two correctness holes were found by writing the guards, not by testing.**
   `WHERE` moves into the body unrewritten, so a reference resolving to no FROM
   table is an outer correlation a CTE body cannot carry (first draft checked
   only the target list → would have shipped 42703 on every correlated
   grouping-sets subquery); and the hoist renames columns to `__gs_cN`, so a
   target with no `AS` needs its name pinned back or the statement's own
   `ORDER BY` stops resolving (PG `FigureColname`).
3. **Fail-closed everywhere.** `rewriteGSExpr` is an exhaustive type switch
   with NO pass-through default. Declines: explicit JOIN, derived tables,
   LATERAL, FROM-clause CTEs, any sublink, FOR UPDATE, WINDOW, single set.
4. **Measured** (SF0.5 subset probe, `sweep-20260806-140755.txt`):
   `PASS=6 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`; Q67 82→14 s (5.9×),
   Q18 37→8 s (4.6×), Q27 31→10 s (3.1×), Q22 21→7 s (3.0×). The gate's
   99-query plan channel: `same=95 changed=4` — exactly the grouping-sets
   queries. Q77/Q80 are the fail-closed path in production (their sets read
   CTEs): declined, unchanged.
5. **Honest gap:** the source EXECUTES once but EXPLAIN still RENDERS the body
   N times (`preplanWithClause` clones per consumer — Q67 prints 8 CTE Scans),
   so the item's literal "ONE scan" wording holds in execution, not rendering.
   Filed as `M0125-0049`; the faithful `AGG_MIXED` aggregate is `M0125-0048`.

Files: `internal/planner/groupingsets_share.go` (new),
`internal/planner/planner.go` (one call site), `internal/planner/flaglabels.go`
+ `scripts/planner-flags.env` (new flag stamped),
`internal/planner/groupingsets_share_test.go`,
`internal/executor/grouping_sets_share_test.go`,
`docs/design/0125-0040-grouping-sets-source-sharing.md` + README index,
`.ralph/fix_plan.md`, `.ralph/deferral_ledger.md` (3 rows).

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
SF0.5 subset probe PASS=6/0 mismatches; existing grouping-sets compat pins
green through the new path; pgbench smoke via the pre-commit hook. `make
plan-diff` (TPC-H) NOT run — TPC-H has no grouping-sets query, so no TPC-H plan
can reach this code, and the SF0.5 99-query plan channel is the stronger
statement.

NEXT LOOP (banner: M0124 closed → M0125 → M0127 → M-NIGHTLY → M0123).
**Read `ci/logs/action-items.md` FIRST.** If it is a NEW run and `status: pass`,
the M0127 S7 gate is met and **M0127-P6.1 (delete fusion) is selectable**. If it
is still run `20260806-011323`, no nightly has run: pick the next open M0125
item. Note -0031/-0032/-0033/-0041 are all marked `[→ M0127: absorbed]` and must
NOT be selected standalone, so the live M0125 choices are the two just filed —
**`M0125-0049`** (EXPLAIN CTE rendering; small, and it also fixes how every
multiply-referenced user CTE reads) then **`M0125-0048`** (the faithful
grouping-sets aggregate; select for fidelity, not speed — -0040 already took
the runtime).

In-flight: none.
