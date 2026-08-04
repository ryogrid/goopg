(idle — nothing in flight)

Last loop: M0127-P5.6-g-vi — DONE (docs-only), committed, pushed. Facts the
next loop should NOT re-derive:

1. **Reading any plan capture taken before `20e17fa5`: a `rows=` is
   trustworthy iff its node line has NO `Filter:` detail beneath it.** Proven
   over the DS05 corpus, not argued: the two line-aligned captures bracketing
   the fix differ on **836 of 3 283** `rows=` node lines (25.5 %, 96/99
   queries) — **836 of the 966 `Filter:`-carrying lines, 0 of the 2 317 bare
   ones**. Overstatement median 9×, p90 18 000×, max 1 920 800×.
2. **The rule covers JOIN nodes too.** Q1's `Hash Join (INNER) … Filter:
   (date_dim.d_year = 2000)` went `rows=716 → 3`. P5.6-g-v's "join nodes carry
   no collapsed `*Filter`" is a TPC-H-only fact.
3. **Every audited closed finding SURVIVES** — M0125-0026 C2 (both forms),
   M0125-0038 (C5), M0125-0040 (C6), M0125-0031, and the §5.3–§5.12 audit
   joinrel conclusions. Nothing needs re-deriving. Do not re-open them.
4. **C2's row-count claim was NOT an artifact** (P5.6-g-v's wording was too
   broad and is now narrowed in place at 09 §5.13). C2 measured 66 of 68 quals
   on join nodes ⇒ the `date_dim` scans carry no filter ⇒ 73 049 is the number
   the estimator really used. Only C2's two named exceptions (Q14/Q54 scalar
   SubPlans) are corrupted, and they are cited for placement, not rows.
5. **C5 corroborated the renderer bug days early**: its `365.25` is
   `73 049 × DEFAULT_EQ_SEL`, divided out of the join estimate — nowhere in
   the plan text. The estimator held the post-qual number all along.

Method worth reusing: when a render-only change lands, diff the bracketing
captures POSITIONALLY (they stay line-aligned) and classify changed lines by a
structural predicate. It converts "which conclusions are corrupt?" from a
judgement call into a two-population count.

Gates run: docs-only loop, no Go source touched ⇒ no unit/spotcheck/DS05 run
(none applies). `make ralph-state-guard` green; commit-hook pgbench smoke
green.

Nightly triage 20260805-014309: unchanged — both items (AI-…-001
IsolationEvalPlanQual, AI-…-002 pgbench/nightly) were already filed under
M-NIGHTLY and stay unchecked per the banner. No new nightly run since.

Next step: per the banner (M0124 → M0125 → M0127), the P5.6-g series is now
closed. Remaining open P5.6 tail, in doc order: **M0127-P5.6-d** (delete the
quadratic build penalty, bushy.go:632) and **M0127-P5.6-f-iii** (the DS05
SF0.5 gate's single TIMEOUT hop). Re-read the banner before selecting.

In-flight: none.
