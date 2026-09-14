Task: M0137-0013 — close or delete the ten-plus-round ledger carries.
**COMPLETE and committed this loop** (`33762b1`, pushed), branch
`plan-parity-with-pg-take2-ralph`. **M0137 is now FULLY CLOSED** — every task
in the milestone (0001-0013) is `[x]` in `.ralph/fix_plan.md`.
Filing/determination task (not recon, not implementation) per the task line
and per the plan-parity harness's carve-outs.

Files: `docs/design/0100-0149/m0137-0013-ledger-carry-determinations.md`
(new — full per-item determinations table), `docs/design/README.md` (+index
row), `.ralph/deferral_ledger.md` (+1 row:
`m0137-0013-nli-semi-anti-match-fraction-gap`), `.ralph/fix_plan.md`
(M0137-0013 checked off with DONE summary).

What landed: traced P7's ("ledger carry" finding,
`METHODOLOGY3/03-process-retrospective.md:344-349`) ten named items through
`TODO.md`'s round log (R51-R130) and cross-checked against
`METHODOLOGY3/02-open-problems.md`. Result:
- 3 items **discharged** by later rounds, closed with citation: R52 §4.2
  (parallel admission) by R54's Redesign LANDED 2026-09-11; 2 of R54's 3
  follow-ups (Q7 sort gap, Q19 range/IN-OR defaults) by R55/R56 LANDED
  2026-09-11.
- 1 item **deleted as superseded**: R51's `GOOPG_PGSHAPED_DP_TRACE`
  re-verification ask, superseded by R53 Step-0's broader DPTRACE
  instrumentation.
- 6 items already have **one permanent, non-duplicative home** in
  `02-open-problems.md` (N45 covers R55 §3 tie-break/F3 procost/Q8
  gap/AGG_MIXED/R61-#4; N50 covers R51's nullable-side assertion; B4/O9
  covers "#6") — closed the carry, no new filing needed, still open
  engineering work but no longer untracked.
- 1 item was a **real, previously-unfiled gap**: "the NLI staleness comment"
  traced to `estimateNLIndexJoin` (`internal/optimizer/cardinality.go:235-239`)
  never calling `semiJoinMatchFraction` for `JoinTypeSemi`/`JoinTypeAnti`,
  unlike its sibling `estimateJoin` (`cardinality.go:608-625`) which does —
  confirmed live at HEAD by direct code read. New ledger row filed.

Key symbols: `estimateNLIndexJoin` / `estimateJoin` / `semiJoinMatchFraction`
(internal/optimizer/cardinality.go) — the newly-filed gap's site.

Gates run: filing/determination only, no `.go` files touched — no build/test
gate applicable beyond the pre-commit hook's mandatory pgbench smoke, which
ran and passed (`git commit` succeeded, hook not bypassed).
`make ralph-state-guard`: same running/completed marker mismatch every
recent loop has hit (previous loop's clean-exit marker), auto-repaired, then
OK — this is now a recurring pattern worth someone fixing at the source
(the marker-writer, not the guard) if it keeps recurring every loop.

In-flight: none. No server/gate process left running.

Next step: **M0137 is fully closed.** Per the banner's selection order
(`.ralph/fix_plan.md` "## Current Priority"), the frontier moves to
**M0138 (PG-faithful ANALYZE statistics)**, **M0139 (executor-side
narrowing)**, or **M0140 (TPC-DS parallelism)** — independent of one
another, select the topmost with an unblocked task. M0139's slices
(S1/S2/S3) were gated on M0137-0010, which landed 2026-09-14, so they are
now unblocked. Re-read `AGENT.md` §"Plan-parity harness — applies ONLY to
M0137-M0143" fresh next loop before picking (loop-start discipline), then
read the M0138/M0139/M0140 task lines in `.ralph/fix_plan.md` to pick the
topmost unblocked one — do not assume M0139 without checking M0138's own
task list for anything unblocked and higher/equal priority per the banner's
own "take the topmost with an unblocked task" rule.
