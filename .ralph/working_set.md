Task: M0122-0001 backlog triage continuation (no-match+no-status entries in
unimplemented_feat.json), doc-only bookkeeping (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only) is the only
file changed besides .ralph/progress.json (benign ralph-state-guard
auto-repair, timestamp bump only).

Key symbols: none new (pure backlog bookkeeping). Evidence cited this loop
(Q21/Q5/Q9 cluster, indices 6-15 of the 31-entry no-match remainder, via 2
parallel general-purpose agents): internal/planner/unnest.go:2044-2095
(M0062-0005 non-equijoin residual lift, commit 0a417306 — Q21
range-correlation EXISTS resolved), internal/planner/bushy.go:1961-1968
(stale M0065-era comment, superseded by commit e8c37796 SourceTableIdx fix),
docs/handover/2026-05-10-tpch-status-phase5.md:33-38 (Q5 CPU pprof table —
M0073-0005 profiling ask WAS completed, unlike its DecodeRow sibling),
.ralph/deferral_ledger.md:573 (Q9 composite-NLI via attachUnusedCrossEdges,
commit 2a9eade5, "FULLY LANDED" 2026-07-07), internal/planner/unnest.go:1200
+1203 (isUnnestableNonCorrelatedIn — NOT IN anti-semi-join AND non-ColumnRef
LHS both still hard-rejected, unchanged since ebb267d6 — 2 confirmed
still-open), internal/planner/nl_index_join.go:133-141 (M0067-0003
schema-vs-runtime layout mismatch — M0068 never touched it, still open),
commit b0226c37 (NLI semi/anti partsupp_pk outer-row claim — marked
unverifiable/vague, no reproduction found, kept open pending concrete repro).

Findings: triaged 10 more of the 31 `no-match`+no-status backlog entries via
2 parallel general-purpose agents (read-only), covering the Q21/Q5/Q9
planner cluster (items 6-15: range-correlation EXISTS, Q21 Anti-NLI
outer-key-probe M0065-era corruption, Q5 optimization/correctness, Q21
EXISTS unnest zero-rows, Q9 composite-NLI TupleSlot deferral, anti-semi-join
for NOT IN, non-ColumnRef IN-subquery LHS, schema-vs-runtime layout
mismatch walker bug, Q5 CPU pprof profiling, NLI semi/anti partsupp_pk
outer-row emission). Result: 6 flipped resolved (higher than the 4/10 rate
of the prior loop — this cluster's Q21/Q5/Q9 items were mostly closed by the
M0071-0009/M0077/2026-07-07-Q9-refix work, confirming the calibration note
from last loop's carry). 4 confirmed still-open with refreshed code_audit
citations (anti-semi-join NOT IN — zero grep hits for AntiJoin/anti-semi
conversion; non-ColumnRef IN-subquery LHS — same function, unchanged;
schema-vs-runtime layout mismatch — M0068 never actually fixed it, only
worked around elsewhere for a simpler case; NLI partsupp_pk outer-row claim
— marked unverifiable/vague since the described bug was never reproduced
distinct from the Q21 hash-join bug that WAS fixed). Did NOT append new
deferral_ledger.md rows — pure triage/verification, not new implementation
work.

Next step: continue M0122-0001 — 31 - 6 = 25 `no-match`+no-status entries
remain. Regen command (indices shift after each edit):
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if 'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match']; print(len(nm)); [print(i,f['feature'][:100]) for i,f in enumerate(nm)]"`
Good next cluster: indices 0-9 of the regenerated 25-entry list (items 16-25
of the old 31-entry numbering — vectorized predicate batch wiring
FilterOp/SeqScanOp, M0091 TPS>=1000 goal, spill-path optimizations, plan
cache M0092-0008, needsVacuum autovacuum-reloption bug, SeqScan/Project
plannode.go migration). These look like perf/M0091-M0092 cluster items —
check git log for "M0091" "M0092" "plan cache" "vectorized" "SeqScanOp"
commits after each item's deferred_date; also check if `needsVacuum` bug
(item on autovacuum reloptions) is a live correctness bug worth a
deferral_ledger row if still open (it reads like a real unfixed defect, not
just a stale-doc entry — inspect internal/executor or internal/storage for
needsVacuum and verify against pg autovacuum reloption semantics).

Gates run: `make ralph-state-guard` PASS (auto-repaired 1 benign issue:
progress.status completed-marker-from-prior-loop reconciled to in_progress,
same pattern as most prior loops). JSON validity + entry-count (181
preserved, status-count 150->156, no-match count 31->25, zero-unicode-escape)
confirmed via python3 before this working_set write. Pre-commit pgbench
smoke NOT YET RUN — will run automatically via `.githooks/pre-commit` when
the commit below executes.

In-flight: none (both background triage agents completed and were consumed
this loop; results applied to unimplemented_feat.json, about to commit).
