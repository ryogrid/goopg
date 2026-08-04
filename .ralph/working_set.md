(idle — nothing in flight)

Last loop: M0127-P5.6-g-i (the carried TPC-DS SF0.5 gate) — DONE, documented,
committed. Four facts the next loop should NOT re-derive:

1. **The gate is clean and the carry is discharged.** `PASS=94 MISMATCH=0
   CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4`, identical to the `ce027cee`
   baseline line for line (all 57 checksums, the single Q47 TIMEOUT).
2. **Three commits owed a gate, not two** — `4b820ab8` (P5.6-f-ii) landed after
   the last DS05 baseline and was never swept either.
3. **Attribution (whole-corpus EXPLAIN, four arms, noise floor zero):
   P5.6-f-ii moved 74 of 99 plans, P5.6-g moved 1 (Q83), P5.6-g-iv moved 4**
   (Q13, Q41, Q48, Q85). The nullable-key premise that raised this item is
   MEASURED FALSE. Do not re-run the sweep to "check P5.6-g on TPC-DS".
4. **The capture harness is committed** —
   `analysis/leftdeep-joins/2026-08-05-p56gi-capture.sh` runs an EXPLAIN-only
   pass over all 99 (every statement prefixed, so Q14/23/24/39's second
   statement never executes). Reuse it instead of rewriting it; it is the
   starting point for the successor P5.6-g-i-b.

Gates run: DS05 sweep PASS (evidence committed); plan A/B ×4 arms + a
same-binary noise-floor capture; pgbench smoke via the commit hook.
No Go code changed this loop, so no UNITS/spotcheck run and no ledger row.

Next step: per the banner, **M0127-P5.6-g-ii** (the `*HashAggregate` arm for
`resolveBaseColumn` + PG's dedup of a `GROUP BY`-unique subquery into an INNER
join — `reduce_unique_semijoins`, analyzejoins.c). Both halves together; the
arm alone measures worse. Its bar (UNITS + DS05 + audit) is now runnable: the
nightly clears around 05:20 and the DS05 baseline to diff against is
`analysis/leftdeep-joins/2026-08-05-p56gi-ds05-sweep.txt`.
Caution for that item: in goopg a SEMI `*Join`'s `Output()` is left-only, so a
literal SEMI→INNER node swap changes the output width — read the plan-shape
implications before copying PG's rewrite verbatim.

In-flight: none.
