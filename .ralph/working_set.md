(idle — nothing in flight)

Last loop: M0127-P5.6-g-v — DONE, documented, committed, pushed. Facts the
next loop should NOT re-derive:

1. **Q18's residual is NOT a HAVING problem, and not a goopg defect.**
   Measured: PG 339 423 → 113 141, goopg 1 150 720 → 383 573 — both exactly
   ÷3 (DEFAULT_INEQ_SEL over an aggregate neither engine has stats for;
   upstream `cost_agg` scales `output_tuples` by
   `clauselist_selectivity(quals)`, goopg via the `*Filter` over the
   `*Aggregate`). The HAVING mechanism is ALREADY identical. The whole
   3.39× gap is the group estimate, and **goopg's ndistinct is the more
   accurate one** (truth 1 500 000; PG is 4.4× LOW). Closed with no
   estimator change. Do NOT "fix" Q18 toward PG — that degrades statistics.
2. **EXPLAIN's collapsed `Filter:` line printed PRE-qual rows** (fixed this
   loop). goopg splits a qual (`*Filter` wrapper) from the rows it filters;
   `walkPlanFiltered` collapsed the wrapper onto the child and printed
   `EstimateRows(child)`. The ESTIMATOR was always right — a parent reads
   `EstimateRows(*Filter)`, which is why a `Gather` over a filtered scan
   was correct while the scan under it was not. Only the rendered line lied.
3. **Every plan capture taken before commit reports filtered relations at
   UNFILTERED size.** `estimateaudit` parses that field (`nodeLineRe`) and
   the DS05 plans channel captures it. `date_dim WHERE d_year = 2000` went
   73 049 → 365 (PG 365). M0125-0026's "date_dim is costed at 73 049" was
   reading the renderer; C2's qual-PLACEMENT finding still stands. This is
   what successor P5.6-g-vi is for.
4. **Rendering changes cannot move plan shape — and that is provable
   cheaply.** Normalising `rows=` before diffing the two DS05 captures
   reduced 95 changed plans to a 6-line psql header diff. Reuse that
   technique for any render-only change.

Gates run: UNITS (green, exit 0); **tpch-spotcheck PASS** (Q12=2, Q13=35
canonical); `estimate-audit` **1 violation (Q18), unchanged from the p56gii
baseline** — all joinrel diffs sub-1 % ANALYZE sampling noise, none worse;
DS05 `plans` 95/99 changed but structurally identical (see 4); 3 new
regression tests each verified failing without the fix; pgbench smoke via
the commit hook.

Nightly triage 20260805-014309: unchanged — the same 2 items
(pgbench/nightly aborted txn, IsolationEvalPlanQual) stay filed under
M-NIGHTLY and unchecked per the banner. No new run since.

Next step: per the banner (M0124 → M0125 → M0127), the successor filed this
loop is **M0127-P5.6-g-vi** — re-read the closed findings whose reasoning
quotes a scan/aggregate row count from pre-fix plan text (M0125-0026 /
M0125-0035 C2 is the known one) and record which survive. Bookkeeping over a
corrupted instrument; no code change expected. The post-fix DS05 baseline
already exists at
`analysis/leftdeep-joins/2026-08-05-p56gv-ds05-plans-after.txt`.

In-flight: none.
