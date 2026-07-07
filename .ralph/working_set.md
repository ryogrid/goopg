Task: M0122-0001 backlog triage continuation (no-match+no-status entries in
unimplemented_feat.json), doc-only bookkeeping (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only) is the only
file changed besides .ralph/progress.json (benign ralph-state-guard
auto-repair, timestamp bump only).

Key symbols: none new (pure backlog bookkeeping). Evidence cited this loop:
internal/executor/operators_ddl.go:641-665 (execCreateSubscription/
execDropSubscription + tablesync_manager.go/applyworker.go/applylauncher.go
apply pipeline — subscriber setup RESOLVED), internal/parser/dml.go:267-279
parseConflictTargetColumnList + internal/executor/operators_upsert.go
encodeExprIndexKey/collectExprColumnNames (expression ON CONFLICT targets
RESOLVED via M0100-0006b), internal/analyzer/analyzer.go:234-281
analyzeSelectWithParent recursive per-branch analysis (star-expansion in
set-op branches was NOT a real gap — misdiagnosed originally), EXPLAIN+UUID
(no UUID-specific formatting ever needed — misdiagnosed), internal/executor/
plannode.go:16-22 (SeqScan plan-node migration done via commit 1953872c,
Project still an adapter — partial, stays open), internal/server/
dispatch.go:1502-1509 normalizeCompatSQL/normalizeSQLPreservingLiterals
(M0098-0005 plan cache does NOT help pgbench literal-substitution traffic —
0092-0008's skepticism re-confirmed still valid, stays open),
internal/storage/bufpool.go:1049-1124 PinNew dirty-bit (stays open but
audit doc still missing; noted a milestone-id collision with unrelated
landed M0089-0002).

Findings: triaged 10 more of the 39 `no-match`+no-status backlog entries via
2 parallel general-purpose agents (read-only), covering perf/plan-node
cluster (items 24,25,30,31,33: FilterOp/SeqScanOp vectorized batch wiring,
dirty-tracking audit, plan-cache pgbench skepticism, needsVacuum
autovacuum_enabled) and replication/DML/EXPLAIN cluster (items 34,35,36,37,
38: subscriber setup, expression ON CONFLICT, SeqScan/Project migration,
star-expansion in set-op branches, EXPLAIN+UUID). Result: 4 flipped resolved
(subscriber setup, expression ON CONFLICT targets, star-expansion — was
never a real gap, EXPLAIN+UUID — was never a real gap), 1 partial (SeqScan/
Project: SeqScan done, Project still open, code_audit refreshed but stays
open), 5 confirmed still-open with citations verified accurate or refreshed
with more precise detail (FilterOp/SeqScanOp batch wiring — vectorized infra
exists in expr_batch.go but wired nowhere; dirty-tracking audit — no bug
found but audit doc never written, noted M0089-0002 id collision; plan-cache
pgbench skepticism — M0098-0005's cache key preserves literals verbatim so
pgbench's literal-substitution traffic gets 0% hit rate, 0092-0008 still
valid; needsVacuum — internal/autovacuum/launcher.go:204-217 confirmed still
ignores AutovacuumEnabled reloption). Spot-checked 2 of the most surprising
reversals (subscriber setup, star-expansion) myself via grep/Read before
applying — both confirmed accurate. Did NOT append new deferral_ledger.md
rows — pure triage/verification, not new implementation work. NOT YET
COMMITTED — see Next step.

Next step: `git add unimplemented_feat.json .ralph/working_set.md
.ralph/progress.json && git commit` (message: chore(M0122-0001): triage 10
more no-match backlog entries; 4 flip resolved). Then continue M0122-0001 —
39 - 10 = 29 `no-match`+no-status entries remain. Regenerate the live list
fresh next loop (indices shift after this edit):
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if 'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match']; print(len(nm)); [print(i,f['feature'][:90]) for i,f in enumerate(nm)]"`
Good next cluster (by likely hit-rate): TPC-H/planner perf items (~30
entries, e.g. Q5/Q9/Q14/Q20/Q21 optimization attempts, NLI/semi-anti-join
gaps) — HIGH volume, but 2 consecutive loops now confirm perf/vectorization
items (filterOp/seqScanOp batch wiring) are genuinely still open at high
rate, not stale-citation false-opens like the DDL/parser/analyzer items were
— expect LOWER resolved-fraction there than in prior clusters; check
git log for "NLI"/"nested-loop"/"semi-anti" commits after each item's
deferred_date before assuming still-open, since M0070-M0077 slice landed a
lot of NLI/join work that may have superseded individual Q-specific items.

Gates run: `make ralph-state-guard` PASS (auto-repaired the same benign
transient running/completed mismatch seen for 6+ loops at this checkpoint —
confirmed still benign). JSON validity + entry-count (181 preserved,
status-count 142→146) + zero-unicode-escape + `git diff --stat` (11
insertions/7 deletions, 6 entries touched) all confirmed via python3/grep
before this working_set write. Pre-commit pgbench smoke NOT YET RUN — will
run automatically via `.githooks/pre-commit` when the commit below executes.

In-flight: none (both background triage agents completed and were consumed
this loop; results applied to unimplemented_feat.json, not yet committed to
git — see Next step above).
