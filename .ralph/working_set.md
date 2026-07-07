Task: M0122-0001 backlog triage continuation (no-match+no-status entries in
unimplemented_feat.json), doc-only bookkeeping (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only) is the only
file changed besides .ralph/progress.json (benign ralph-state-guard
auto-repair, timestamp bump only).

Key symbols: none new (pure backlog bookkeeping). Evidence cited this loop
(TPC-H/planner perf cluster, indices 0-9 of the 35-entry no-match remainder):
docs/handover/2026-05-11-tpch-status-phase9.md:30-58 (clean 22/22 TPC-H SF=1
sweep incl. Q9/Q14/Q20 — closed 3 "empirical HammerDB validation" items + Q14
regression item), .ralph/deferral_ledger.md:572-573 (2026-07-07 Q9/Q20
re-fixes with regression tests — closed Q20 wall-clock item),
internal/planner/nl_index_join_test.go:254-278 TestNLIRuleSkipsIsolatedScopeOuter
(Q15b isolated-scope-Project NLI gate still deliberately off — stays open),
internal/executor/operators_join_agg.go:2899 concatRows (never wired to
row_pool.go's sync.Pool reuse; old "Borrow contract" was implemented THEN
REMOVED in M0071-0015 Stage E, superseded by slot Materialize() — stays open,
citation was previously mismatched to btree.go, now fixed),
internal/planner/in_list_test.go:14-45 (no Append/Union plan node anywhere in
internal/planner — stays open, confirmed via grep), internal/planner/
q21_live_test.go:163,176 + nl_index_join.go:302-316 (Q21 Anti-NLI conjunct-
lift was abandoned as a design decision, not completed — stays open/partial;
correctness bug itself IS resolved via unnest.go:2078-2090 SourceTableIdx
fix), commit 977ff220 (Q21 build-phase-cancel literal claim resolved via
ctx.Err() check in joinOp.openLazyHashJoin; bundled derived-table NLI
rewrite half stays open, same gap as Q15b item).

Findings: triaged 10 more of the 35 `no-match`+no-status backlog entries via
2 parallel general-purpose agents (read-only), covering the TPC-H/planner
perf cluster (items 0-9: empirical DecodeRow<=15% metric, HammerDB SF=1
power test completion, Q15b equi-key extractor, concatRows buffer reuse,
IN-list Append/Union plan node, Q20 wall-clock validation, M0054-0007 22
close-criteria verification, Q21 Anti-NLI conjunct-lift, Q21 build-phase
cancel/derived-table NLI, Q14 perf regression). Result: 4 flipped resolved
(HammerDB SF=1 power test — completed clean 2026-05-11 then Q9/Q20
re-regressed+re-fixed 2026-07-07; Q20 wall-clock validation — same evidence;
M0054-0007 22-criteria verification — resolved by supersession into
M0063/M0075-M0077/M0122 series, M0054-0008 itself never landed as its own
milestone; Q14 perf regression — 19.49s in the clean sweep, well under both
29s baseline and 600s timeout). 1 marked PARTIAL with refined code_audit
(item 0: DecodeRow<=15% empirical pprof metric specifically was never
captured even though the feature it measures landed). 5 confirmed
still-open/partial with code_audit refreshed to current file:line citations
(Q15b isolated-scope-Project NLI gate; concatRows buffer reuse — fixed a
previously mismatched citation pointing at btree.go; IN-list Append/Union
plan node — zero grep hits, confirmed absent; Q21 Anti-NLI conjunct-lift —
correctness bug is fixed but the specific NLI-promotion approach was
abandoned as a design decision; Q21 build-phase-cancel — the cancel-lag part
IS resolved but the bundled derived-table-NLI-rewrite half stays open, same
gap as Q15b). Did NOT append new deferral_ledger.md rows — pure
triage/verification, not new implementation work.

Next step: continue M0122-0001 — 35 - 4 = 31 `no-match`+no-status entries
remain (all in the TPC-H/planner perf cluster; see prior loop's regen
command, indices shift after each edit):
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if 'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match']; print(len(nm)); [print(i,f['feature'][:100]) for i,f in enumerate(nm)]"`
Good next cluster: indices 10-19 or so of the regenerated list — remaining
items include range-correlation EXISTS (Q21 non-equijoin), Q5 optimization/
correctness (multiple entries — check if all still point at the same root
cause or if some were superseded by the M0075-M0077 Q5 unlock,
[[m0077_q5_unlocked_4_slice]] in memory), Q9 chained-NLI/composite-NLI
TupleSlot pipeline follow-ups, schema-vs-runtime layout mismatch walker bug,
anti-semi-join for NOT IN, non-trivial IN-subquery LHS expressions. Continue
checking git log for NLI/nested-loop/semi-anti/Q5/Q9 commits after each
item's deferred_date — the M0070-M0077 slice landed a lot of join/NLI work
that superseded several already (4/10 this loop, higher than the 2/10 rate
originally guessed, so keep expectations calibrated upward not downward for
this cluster).

Gates run: `make ralph-state-guard` PASS (clean, no auto-repair needed this
time — first clean run in 6+ loops at this checkpoint). JSON validity +
entry-count (181 preserved, status-count 146→150, no-match count 35→31,
zero-unicode-escape) confirmed via python3 before this working_set write.
Pre-commit pgbench smoke NOT YET RUN — will run automatically via
`.githooks/pre-commit` when the commit below executes.

In-flight: none (both background triage agents completed and were consumed
this loop; results applied to unimplemented_feat.json, about to commit).
