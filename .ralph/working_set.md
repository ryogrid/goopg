(idle — nothing in flight)

## Loop #12 (2026-09-01) result — M0134 declared EXHAUSTED, priority falls to M0119 (commit `4f14b6fba`)

**Nightly triage:** `ci/logs/action-items.md` still shows run `20260901-010436`
(7 items, mtime 02:11) — same run loops #10/#11 already confirmed filed (all 7
subjects have M-NIGHTLY tasks in fix_plan.md, re-verified this loop). No new
items.

**Task:** resolved the standing question loops #9-#11 had each partly
investigated but never closed: is there any regress-sql case with a
`failed`/`not-tried` CSV status that lacks an M0134 task ID (i.e. genuinely new
work)? Answer, verified mechanically: **no.**

Method: extracted the canonical 189-row `task ↔ case` table from
`docs/milestones/0134-regress-sql-failed-not-tried-digestion.md`'s own "Task
list" section, diffed it (`comm -23`) against every regress-sql row in
`docs/test-port/postgres-oracle-target-inventory.csv` currently `failed`/
`not-tried` (167 rows) — empty result, every one already has an ID. Also
confirmed zero active `fix_plan.md` task bodies still carry the original
unattempted-boilerplate filing text. (Tasks 0091-0140 appearing absent from
the active `fix_plan.md` turned out to be a red herring — legitimate archival
commit `1d74052c5` moved them to `completed_milestones/completed_fix_plan_012.md`
months ago; each ID's real landing commit was confirmed via `git log`, e.g.
`eac970d26` M0134-0092, `608a2bb81` M0134-0140.)

**Conclusion:** all 189 filed M0134 regress-sql cases have been sized at least
once and are each CLOSED (green or stale-pass) or PARKED on a named
REFACTOR-tier prerequisite with its own re-arm trigger (parallel-query
execution, SQL/JSON constructors, outer-join nullability, physical GiST/GIN/
SP-GiST/BRIN index-scan integration, geometry operator lexing, the `money`
type, `pg_shdepend`/object-address enumeration, etc.). **M0134 has no
selectable task.** Documented in
`docs/milestones/0134-regress-sql-failed-not-tried-digestion.md` ("Exhaustion
note (2026-09-01)", status header) and in `fix_plan.md`'s Current Priority
banner + M0119 section (M0119-0005 was already fully landed — only M0119-0006
pg_amcheck server tier remains open).

**Concurrency note (important for next loop):** mid-session a concurrent Ralph
process committed `57eb66cdd` ("condense remaining tasks/subtasks to
final-status-only"), shrinking `fix_plan.md` from ~3972 to ~679 lines
(compressing nightly-item and M0134-task-list bodies to terse bullets). It
landed between my Reads and Edits of `fix_plan.md`; my banner/M0119-section
edits (old_string matches were all in sections the condenser didn't touch)
applied cleanly on top and are already part of that commit — verified via
`git diff` (empty) before this loop's own commit. No corruption, but a
reminder that a second Ralph loop is/was active on this tree concurrently
(matches `[[concurrent_ralph_loops_corrupt_tree]]` /
`[[ralph_fixplan_driver_churn_defeats_edit]]`) — re-read `fix_plan.md` fresh
before editing it, don't trust a cached view.

**NEXT LOOP:** Re-check the `## Current Priority` banner first. It now says
M0134 is exhausted and active selection is **M0119** (sole task: M0119-0006 —
pg_amcheck server tier). Per the M0119 per-task rule, write a design doc under
`docs/design/M0119-0006-NNNN-*.md` (draft → accepted, agent-reviewed) before
implementing. Re-verify M0119-0006 hasn't already landed (check git log /
`.ralph/deferral_ledger.md` first — M0119 tasks require "verify it already
landed" per the milestone's own rule). After M0119 clears, M0122's remaining
items are next. If any M0134 PARKED case's named prerequisite has since
landed as its own milestone, that re-arms M0134 selection for that one case
only (re-run `scripts/pg-regress-runner.sh --verbose <case>` and re-size).

**Gates run:** `go build ./...` clean; `make check-testport-inventory` PASS;
`make ralph-state-guard` PASS (one auto-repair, same benign pattern as loops
#4-#12); pre-commit pgbench smoke PASS (507/650/11726 TPS, 0 failed) — fired
automatically via the git hook on the milestone-doc commit. Did not run the
full `ralph-precommit-test.sh` units suite or `tpch-spotcheck.sh` this loop
(no engine/planner/executor code touched — pure docs/fix_plan bookkeeping).

**In-flight:** none.
