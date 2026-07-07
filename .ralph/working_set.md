Task: M-NIGHTLY triage re-confirmed no new work — the latest nightly run in
ci/logs/action-items.md (run 20260707-000712, 8 AI-items) is already fully
triaged/folded into fix_plan.md by prior loops (all 8 checked [x], including
the 17-loop pgbench keyLen-mismatch root-cause-and-fix). Actual loop work:
M0122-0001 backlog triage continuation, doc-only (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only — located each
entry via its unique "feature" line, inserted a `status` line after
`resume_point`/`task_id`, replaced `code_audit` with current evidence). No
other files touched except .ralph/progress.json (benign ralph-state-guard
auto-repair, timestamp bump only).

Key symbols: none new (pure backlog bookkeeping). Evidence cited this loop:
internal/auth/scram.go:279-289 (channel binding hard-rejected),
internal/config/defaults.go:184 (scram_iterations GUC registered but unused
by scram.go:45,75,150 scramDefaultIterations), internal/server/server.go:
405-409+949-954 + internal/activity/registry.go:556-624 (track_io_timing now
live-wired, RESOLVED), internal/executor/expr.go:3400-3461,7674-7683
(pg_relation_size family RESOLVED via M0122-0002), internal/autovacuum/
launcher.go:83-222 (real launcher exists but unwired in cmd/goopg/main.go —
PARTIAL/still open), internal/parser/function.go:898-909 (CREATE FUNCTION SET
parsed-then-discarded, proconfig always NULL).

Findings: triaged 10 of the 69 `no-match`+no-status backlog entries (indices
3,28,29,48,58,59,65,66,67,68 in that day's live-query numbering) via 2
parallel general-purpose agents (read-only) grouped by theme: (a) SASL/SCRAM
auth cluster (IDX3/28/29 — SASLprep, channel binding, scram_iterations GUC),
(b) GUC/stub cluster (IDX48/58/59/65/66/67/68 — track_io_timing,
min_parallel_*_scan_size, pg_relation_size family, planner GUC stubs,
jit/compute_query_id/plan_cache_mode, per-function proconfig, autovacuum).
Verdict: 2 flipped to `status: resolved` (IDX48 track_io_timing runtime SET,
IDX59 pg_relation_size/pg_table_size/pg_total_relation_size/pg_indexes_size
now compute real sizes via M0122-0002). 8 remain `status: open`, 1 of which
(IDX68 autovacuum) is nuanced PARTIAL text — real launcher infra exists but is
never instantiated in production main.go and ignores the scale-factor GUC.
Did NOT append new .ralph/deferral_ledger.md rows — pure triage/verification
of already-known-open items, not new implementation work (same precedent as
the prior 2 loops' notes).

Next step: continue M0122-0001 — 69 - 10 = 59 `no-match`+no-status entries
remain. Regenerate the live list fresh next loop (indices shift after this
edit):
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if 'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match']; print(len(nm)); [print(i,f['feature'][:90]) for i,f in enumerate(nm)]"`
Good next clusters (by likely hit-rate, same parallel-agent-per-theme method):
(a) TPC-H/planner perf items (~30 entries, e.g. Q5/Q9/Q14/Q20/Q21 optimization
attempts, NLI/semi-anti-join gaps, vectorized predicate batch wiring
FilterOp/SeqScanOp) — HIGH volume but perf work is often genuinely still open,
so expect a lower resolved-fraction than this loop's cluster; (b) remaining
storage/concurrency items (posting-growth B-tree integration, buffer-pool
pinCount race, connection-pool mutex partitioning, spill-path optimizations);
(c) the M0054-0007/M0091/M0092 "full verification"/"performance goal unmet"
meta-items — check current TPS/close-criteria state before re-triaging.

Gates run: `make ralph-state-guard` PASS (auto-repaired the same benign
transient running/completed mismatch seen the last 3 loops at this
checkpoint — confirmed still benign, not a regression). `.githooks/pre-commit`
pgbench smoke PASS (TPC-B 231 tps / simple-update 245 tps / select-only 14047
tps, 0 failed transactions across all 3 workloads) — ran automatically via the
commit hook even though no Go code was touched. `python3 -c
"json.load(...)"` confirmed valid JSON after the edit (181 entries preserved);
`grep -c '\\u'` confirmed zero unicode-escapes introduced. `git diff --stat`
confirmed only 20 targeted line insertions/replacements (10 status-line
inserts + 10 code_audit replacements) — no accidental reformat of the other
171 untouched entries. Committed as d19b2f7d.

In-flight: none.
