(idle — nothing in flight)

Last loop: M0127-P5.6-g-ii — DONE, documented, committed, pushed. Facts the
next loop should NOT re-derive:

1. **The item as filed was the wrong half of itself.** PG has no
   "`*HashAggregate` arm": `examine_simple_variable` (selfuncs.c) hits
   `if (subquery->groupClause)`, sets `vardata->isunique` when the referenced
   output is the sole grouping column, and returns "cannot go further"
   **without a statistics tuple**. What crosses a grouping node is
   UNIQUENESS, never a distribution. Consumer =
   `get_variable_numdistinct`'s negative `stadistinct` ⇒ nd = the grouped
   relation's own rows. Do NOT add a stats-propagating arm to
   `resolveBaseColumn`; a test pins that MCVs must not leak up.
2. **`reduce_unique_semijoins` is numerically INERT at goopg's join order** —
   measured, not assumed: for a unique inner, `inner_rows` = nd2, so the INNER
   and SEMI formulas agree term for term. It buys join-ORDER freedom only.
   Ledger row; the blocker to solve first is that a goopg SEMI `*Join`'s
   `Output()` is left-only, so a node swap re-indexes everything above it.
3. **Q18 is 24 242× (was 42 837×) and the residual is a HAVING problem, not a
   join problem** — goopg's `l_orderkey` ndistinct (1 210 559) is *more*
   accurate than PG's (~339 000, truth 1 500 000), which is why its
   post-HAVING inner is 3.6× larger than PG's 113 141. Filed as P5.6-g-v.
4. **Reading the 20 s `plans` channel BEFORE the sweep paid off immediately**:
   it caught Q77 estimating 885 rows for a LEFT join whose outer is 8 885, i.e.
   `estimateJoin` never had an outer-join arm at all. Use it that way again.

Gates run: UNITS (green); TPC-DS SF0.5 **sweep** `PASS=94 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4` — identical to the `ce027cee` baseline
line for line (12 of 99 plans moved, zero rows; stream 2 116 s → 2 074 s);
three `plans` captures (before / after-arm / after-floor, the last `changed=0`
confirming the late `*DistinctOn` addition moved nothing); `estimate-audit`
on TPC-H (violations 2 → 1, no joinrel worse than ANALYZE's ±5 % noise);
pgbench smoke via the commit hook.

Nightly triage 20260805-014309: unchanged from last loop — the same 2 items
(pgbench/nightly aborted txn, IsolationEvalPlanQual) are already filed under
M-NIGHTLY and stay unchecked per the banner. No new run since.

Next step: per the banner (M0124 → M0125 → M0127), the successor filed this
loop is **M0127-P5.6-g-v** — Q18's residual. First action is cheap and
decisive: `EXPLAIN` the bare Q18 subquery (`select l_orderkey from lineitem
group by l_orderkey having sum(l_quantity) > 313`) on goopg 65433 and PG 65432
and read off which of the two numbers diverges — the group estimate or the
HAVING selectivity — before touching either. Bar: UNITS + DS05 `plans` + audit.

In-flight: none.
