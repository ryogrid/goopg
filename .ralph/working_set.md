Task: M0122-0001 backlog triage — COMPLETE this loop. All 181/181 entries in
unimplemented_feat.json now carry a final `status` field (64 resolved / 117
open, 0 untagged). Doc-only bookkeeping (exempt from design-doc rule).

Files: unimplemented_feat.json (16 surgical Edits — inserted `"status": "open"`
after `resume_point` for the final 16-entry no-match cluster; all 16 confirmed
still-open via already-present 2026-07-08-dated code_audit notes, none
flipped to resolved). .ralph/fix_plan.md (M0122-0001 checkbox -> [x], added
2026-07-08 completion summary). .ralph/progress.json (benign
ralph-state-guard auto-repair, timestamp bump only, same pattern every loop).

Key symbols: none new (pure backlog bookkeeping, no code changed).

Findings: the final 16-entry cluster was planner/perf architectural
follow-ups (TPC-H Q9/Q15b/Q21 NLI shapes, NOT-IN anti-semi-join, vectorized
FilterOp/SeqScanOp wiring, spill-path per-row activity-lookup cost,
plan-snapshot nondeterminism root-cause). Every one of the 16 already had a
2026-07-08-dated code_audit from an earlier pass in this same conversation
(before a context-compaction boundary) confirming genuinely-still-open status
-- this loop's job was mechanical: convert those completed audits into the
final `status` field. Did NOT add new deferral_ledger.md rows: the
vectorization pair (FilterOp/SeqScanOp) is already tracked at
.ralph/fix_plan.md:3118-3120 (M0122-0012), and the Q21/Q15b NLI-shape gaps
are documented inline in their own code comments / prior deferral_ledger
entries -- a new row would duplicate existing tracking.

Next step: M0122-0001 (the triage/re-verification pass itself) is DONE --
do not resume it. Future loops picking up individual `open` entries from
unimplemented_feat.json should treat each as its own M0122-00NN
implementation task, requiring its own reviewed design doc before code
(per the per-task rule in fix_plan.md around line 2113). To find them:
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; op=[f for f in d if f.get('status')=='open']; print(len(op))"`
(117 currently). Prioritize by task_id/milestone clustering as prior loops
did, or pick standalone quick wins first.

Gates run: `make ralph-state-guard` PASS (auto-repaired 1 benign issue,
identical pattern to every prior loop -- progress.status completed-marker
reconciled to in_progress). JSON validity + full status-field coverage
(181/181, 64 resolved/117 open, 0 remaining, zero \u-escapes / literal UTF-8
preserved) verified via python3 before this working_set write. Pre-commit
pgbench smoke will run automatically via `.githooks/pre-commit` on the
commit below.

In-flight: none (all work this loop was direct file Edits/reads, no
background agents or long-running processes started).
