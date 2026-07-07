Task: M0122-0001 backlog triage continuation (no-match+no-status entries in
unimplemented_feat.json), doc-only bookkeeping (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only) is the only
file changed besides .ralph/progress.json (benign ralph-state-guard
auto-repair, timestamp bump only).

Key symbols: none new (pure backlog bookkeeping). Evidence cited this loop
(Q9/Q5/M0091-perf cluster, indices 10-19 of the 25-entry no-match remainder,
via 2 parallel general-purpose agents, plus 1 solo item I split off):
internal/planner/bushy.go attachUnusedCrossEdges (commit 2a9eade5, Q9
composite-NLI, already-known FULLY LANDED per .ralph/deferral_ledger.md:573
— flipped this entry too since it referenced a different task_id/day),
docs/design/fix-for-q5/ + M0077 4-slice commits 174cb902/71eeba32/da260d1c/
5d8bd431 (Q5 600s+->16-26s, closed 1998881e), commit 514bdf6e (M0093 pgbench
317->2,740 TPS, clears M0091's TPS>=1000 bar — docs/design/0093-0002-pgbench-
remeasurement-target.md:50 ties acceptance explicitly to M0091), commit
da7224d7 + e8874a08 (M0092-0005/M0122-0003 gated client-driven Pool/Manager/
AIO I/O hooks behind track_io_timing, resolving that m0091 entry). Confirmed
still-open with refreshed citations: plan-snapshot nondeterminism-vs-pool-
state investigation (never performed, docs/design/0076-0006-plan-snapshot-
harness.md:15-17 defers to M0077 which only shipped a workaround), build-flag
regression diagnosis (M0076-0003, PARTIAL/moot — M0098-0007 made GOAMD64=v3+
PGO default without isolating which knob caused +9.5%), FilterOp batch wiring
(operators.go filterOp.Next():229-258 still row-by-row evalExprSlot),
SeqScanOp batch wiring (operators_storage.go seqScanOp.Next():1316, same
gap — no predicate eval near SeqScanOp at all, it's all in filterOp),
spill-path closure-capture AND spill-path per-row-lookups (both = same
unresolved gap in internal/executor/spill.go WriteRow/ReadRowInto still
calling activity.LookupCurrentGoroutine() per row unconditionally, never
migrated to the fast-path-gated LookupTrackedGoroutine used elsewhere post-
M0122-0003 — these two backlog entries are literal duplicates of one gap,
flagged for a future de-dup pass but left as 2 rows per no-full-rewrite
discipline).

Findings: triaged 10 more of the 25 `no-match`+no-status backlog entries via
2 parallel general-purpose agents (read-only) + 1 solo follow-up (agent split
missed index 15/SeqScanOp, covered it directly via grep — no vectorized
predicate code anywhere near seqScanOp, confirmed-open). Result: 4 flipped
resolved (Q9 chained-NLI/slot-pipeline entry — NOT via a slot pipeline
[slot.go never built] but via bushy.go's attachUnusedCrossEdges instead; Q5
optimization fix; M0091 TPS>=1000 goal — via M0093, 2,740 TPS measured;
Client-driven Pool/Manager/AIO hooks m0091 entry — via M0092-0005/M0122-0003
track_io_timing gating). 6 confirmed still-open with refreshed code_audit
(plan-snapshot nondeterminism investigation, build-flag regression diagnosis
[PARTIAL/moot], FilterOp batch wiring, SeqScanOp batch wiring, spill-path
closure-capture, spill-path per-row — last two are duplicate framings of one
gap). Entry count 181 preserved; no-match count 25->21; status-count
156->160 (62 resolved / 98 open / 21 none). Did NOT append new
deferral_ledger.md rows — pure triage/verification, not new implementation
work; none of the 6 still-open items looked like they needed a fresh ledger
row (all already well-documented as deliberately out-of-scope/cold-path in
their originating design docs).

Next step: continue M0122-0001 — 21 no-match+no-status entries remain.
Regen command (indices shift after each edit):
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if 'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match']; print(len(nm)); [print(i,f['feature'][:100]) for i,f in enumerate(nm)]"`
Good next cluster: indices 16-20 of the 21-entry list (Pool.PinNew/Extend
dirty-tracking durability audit m0089-0002, plan cache M0092-0008 design-doc
rationale check, select-only perf structural fix M0092, needsVacuum
autovacuum-reloption bug — this one reads like a REAL live correctness
defect worth a deferral_ledger row if still open, inspect internal/executor
or internal/storage for `needsVacuum` and compare against pg's
autovacuum_vacuum_scale_factor/autovacuum_vacuum_threshold semantics — and
SeqScan/Project plannode.go opTreeSlab migration). Indices 0-9 are an older
Q15b/Q21/IN-list/concatRows cluster already fully audited (2026-07-08 dated,
confirmed still-open) — do not re-triage those without new evidence.

Gates run: `make ralph-state-guard` PASS (auto-repaired 1 benign issue:
progress.status completed-marker-from-prior-loop reconciled to in_progress,
same pattern as every prior loop). JSON validity + entry-count (181
preserved, status-count 156->160, no-match count 25->21, zero-unicode-escape)
confirmed via python3 before this working_set write. Pre-commit pgbench
smoke NOT YET RUN — will run automatically via `.githooks/pre-commit` when
the commit below executes.

In-flight: none (both background triage agents completed and were consumed
this loop; results applied to unimplemented_feat.json, about to commit).
