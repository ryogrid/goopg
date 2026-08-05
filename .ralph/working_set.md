(idle — nothing in flight)

Last loop: **M0127-P5.9 acceptance bar RUN 2 executed** (HEAD `c00db762`).
Second documented NO-GO, flag stays OFF, P5.9 stays OPEN. Do NOT re-derive:

1. **Clause 1 went four failures → two.** `22 MATCH, 1 ROWS-DIFF, 1 VALUE-DIFF`.
   Q7/Q8/Q9 and Q17 now MATCH on full digests — -c/-e/-f all held. Clause 5
   PASS again (0 `MultiHashJoin`, 0 fusion, both arms). Clause 2 FAIL 1.36×
   (OFF 372.50 s / ON 506.45 s / allowance 447.00 s); clause 3 FAIL Q7 2.14×,
   Q9 3.23×, Q10 3.78×, Q18 2.42× — **but Q9's named ≤170.9 s bar PASSES at
   53.56 s.** Clauses 4/6 again not reached (they score plan quality; Q2 is
   still wrong under the flag).
2. **THE HEADLINE: goopg's DEFAULT planner returns a wrong answer for TPC-H
   Q5, ~24× inflated.** PG 18.3 `5.59e7`, goopg default `1.34e9`, goopg
   flag-ON `5.73e7`. `{c_nationkey,s_nationkey,n_nationkey}` is one EC over
   three relations; the default plan emits ONE clause from it and never
   nation-constrains `supplier`. Writing all three equalities out redundantly
   does NOT bring it back. Filed **M0119-0011** (independent of M0127). This
   forced clause 1's second amendment: adjudicate every non-MATCH cell
   against PostgreSQL before attributing it (09 §3.4).
3. **The only blocker on run 3 is M0127-P5.9-g (Q2 = 0 rows vs 455).** It is
   P5.9-f's Q17 shape with a 4-relation inner under a 5-relation outer;
   start at `unnestSubquery`'s `outerWidth` splice. It was NEVER the P5.9-c
   rotation — unchanged across runs 1 and 2. Timing work is P5.9-h and is
   deliberately deferred behind it (bisect Q10 first: the only ratio that did
   not move between runs).
4. Harness: the arm script's nightly interlock `pgrep -f
   ci/batch/run-nightly.sh` SELF-MATCHED and refused on a quiet host. Fixed
   in place (`[c]i/...`); the scripts-wide sweep for the same shape is a
   ledger row.
5. Reproduce the whole bar: `NO_BUILD=1 PGSHAPED=0|1
   scripts/tpch-acceptance-arm.sh <name> <out>` on ONE binary, then
   `tmp/tpch-acceptance-runner -diff <off> <on>`.

Files: `scripts/tpch-acceptance-arm.sh` (1-line guard fix).
Docs: 09 §3.4 (new), bundle README status, IMPLEMENTATION-TODO P5.9 + new
P5.9-g/-h, fix_plan P5.9 + P5.9-g/-h + M0119-0011, 4 ledger rows,
`analysis/leftdeep-joins/2026-08-05-p59run2-*` (arms, diff, EXPLAINs, write-up).

Gates run: **both acceptance arms + `tpch-runner -diff` (the loop's own gate)**;
EXPLAIN sweep both arms (clause 5 PASS); PG-oracle Q5 comparison on :65432
(cluster started and stopped again); pgbench smoke via the commit hook;
`make ralph-state-guard` (self-repaired). No Go code changed, so UNITS/SPOT/DS05
were not applicable.

Nightly triage 20260805-014309: unchanged run, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9-g** — Q2's decorrelated-aggregate splice returns 0 rows.

In-flight: none.
