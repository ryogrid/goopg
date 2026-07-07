Task: M0122-0001 backlog triage continuation (no-match+no-status entries in
unimplemented_feat.json), doc-only bookkeeping (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only — located each
entry via its unique "feature" line, inserted a `status` line after
`resume_point`/`task_id`, replaced `code_audit` with current evidence). No
other files touched except .ralph/progress.json (benign ralph-state-guard
auto-repair, timestamp bump only — same pattern seen every loop this
checkpoint, confirmed still benign).

Key symbols: none new (pure backlog bookkeeping). Evidence cited this loop:
cmd/goopg/main.go:1181 + internal/server/server.go:634-637 (SIGHUP reload
still v0 no-op, control-socket plumbing wired but callback does nothing),
docs/design/0122-0003-explain-format-xml-yaml.md (BUFFERS/SETTINGS/XML/YAML
EXPLAIN rendering landed 2026-07-04, superseding stale 0018-0004 citations),
internal/executor/operators_lockrows.go (lockRowsOp.stampLock — real
row-locking code; corrects a prior triage pass's mislabeled citation into
operators_window.go), internal/analyzer/analyzer.go:395-427 (GROUP BY/HAVING
+ FOR UPDATE guard exists; bare-aggregate-in-target-list detection does not;
corrects a prior mislabeled internal/planner/locking_test.go citation to the
real internal/analyzer/locking_test.go), internal/server/plancache.go
(cross-session 16-shard plan cache, M0098-0005, resolves statement-level
caching ask), internal/executor/slot.go:1-17 (TupleSlot pipeline superseded
the literal BorrowSemantics design, resolves the "full rewrite" ask under a
different architecture).

Findings: triaged 10 of the 49 `no-match`+no-status backlog entries via 2
parallel general-purpose agents (read-only), grouped by theme: (a)
EXPLAIN/config cluster (5 items: SIGHUP reload, BUFFERS/SETTINGS rendering,
FORMAT XML/YAML, per-CTE pg_stat_* tracking, M0018-0004 SETTINGS dup) — 3
flipped resolved (all landed via M0122-0003 on 2026-07-04, a doc the prior
triage passes hadn't cross-checked against — the OLD code_audit citations
pointed at docs/design/0018-0004 which still literally says "deferred" even
though the follow-up landed elsewhere), 2 remain open (SIGHUP reload is a
genuine no-op; per-CTE pg_stat_* is likely open-by-design since PG itself has
no such catalog); (b) locking/perf/borrow cluster (5 items: per-row lock
timestamp, aggregate-in-FOR-UPDATE detection, writeHeapRow pinning,
statement-level caching, Borrow-semantics rewrite) — 2 flipped resolved
(statement caching via M0098-0005 plan cache; Borrow-semantics via the
TupleSlot pipeline superseding the original design), 3 remain open (lock
timestamp and aggregate detection both had MISLABELED prior citations
pointing at the wrong file — corrected this loop; writeHeapRow pinning is
genuinely still absent, only page-selection-for-pin-contention exists).
Lesson for future triage: when a code_audit citation's file path looks odd
for the claimed content (e.g. "operators_window.go" for lock-timestamp
logic), independently verify the file rather than trusting the old citation —
2 of 10 entries this loop had citations pointing at entirely wrong files.
Did NOT append new .ralph/deferral_ledger.md rows — pure triage/verification
of already-known-open items, not new implementation work (same precedent as
prior loops' notes). Committed as 4febf3d0.

Next step: continue M0122-0001 — 49 - 10 = 39 `no-match`+no-status entries
remain. Regenerate the live list fresh next loop (indices shift after this
edit):
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if 'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match']; print(len(nm)); [print(i,f['feature'][:90]) for i,f in enumerate(nm)]"`
Good next clusters (by likely hit-rate, same parallel-agent-per-theme
method): (a) TPC-H/planner perf items (~30 entries, e.g. Q5/Q9/Q14/Q20/Q21
optimization attempts, NLI/semi-anti-join gaps, vectorized predicate batch
wiring FilterOp/SeqScanOp) — HIGH volume but perf work is often genuinely
still open, expect lower resolved-fraction; check the M0122-* design docs
first since two consecutive loops now found stale-citation false-opens
concentrated around later M0122 work superseding older deferrals; (b) the
M0054-0007/M0091/M0092 "full verification"/"performance goal unmet"
meta-items — check current TPS/close-criteria state before re-triaging; (c)
remaining misc storage items (e.g. dirty-tracking audit, spill-path hooks,
plan cache deferral now likely stale given M0098-0005 landed, Full
SeqScan/Project plan node migration, subscriber setup for logical
replication) — moderate hit-rate expected.

Gates run: `make ralph-state-guard` PASS (auto-repaired the same benign
transient running/completed mismatch seen the last 5 loops at this
checkpoint — confirmed still benign, not a regression). `.githooks/pre-commit`
pgbench smoke PASS (TPC-B 186 tps / simple-update 250 tps / select-only
14279 tps, 0 failed transactions across all 3 workloads) — ran automatically
via the commit hook even though no Go code was touched. `python3 -c
"json.load(...)"` confirmed valid JSON after the edit (181 entries
preserved, status-count 132→142); `grep -c '\\u'` confirmed zero
unicode-escapes introduced. `git diff --stat` confirmed only 20 targeted
line insertions + 10 deletions (10 status-line inserts + 10 code_audit
replacements) — no accidental reformat of the other 171 untouched entries.
Committed as 4febf3d0.

In-flight: none.
