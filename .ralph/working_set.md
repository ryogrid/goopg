Task: M0122-0001 backlog triage continuation (no-match+no-status entries in
unimplemented_feat.json), doc-only bookkeeping (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only — located each
entry via its unique "feature" line, inserted a `status` line after
`resume_point`/`task_id`, replaced `code_audit` with current evidence). No
other files touched except .ralph/progress.json (benign ralph-state-guard
auto-repair, timestamp bump only).

Key symbols: none new (pure backlog bookkeeping). Evidence cited this loop:
internal/executor/expr.go:1359-1428 (OpAdd/OpSub dispatch, no
timestamp-timestamp interval path), internal/parser/select.go:3009-3016,
3089-3092 (interval unit switch: day/month/year only, no sub-day units),
internal/executor/datum.go:21-64 (DatumKind enum has no KindDate),
internal/analyzer/analyzer.go:1570-1571 (RANGE/GROUPS window frame 0A000
rejection, ROWS-only still current), internal/access/btree/btree.go:578-614
(splitMu still guards all split-path inserts, no true crab-walk),
internal/access/btree/posting.go:89-109 (appendTIDToPosting/
promoteSingleToPosting still zero callers), internal/storage/bufpool.go:
55-57,1049-1124,1172-1188 (M0056-0001 PinNew race RESOLVED, superseded by
fully lock-free per-slot atomic state — also closes the mislabeled
"connection pool mutex partitioning" entry, M0070-0006, which was actually
about this same bufpool.go poolMu), internal/wal/writer.go:392 +
stripe_writer_core.go:5-40 (appendMu still single RWMutex, splitting staged
but not wired).

Findings: triaged 10 of the 59 `no-match`+no-status backlog entries via 2
parallel general-purpose agents (read-only), grouped by theme: (a) SQL
semantics cluster — timestamp-timestamp interval subtraction, sub-day
interval units, KindDate carrier, window frame ROWS/RANGE/GROUPS, window
frame clauses + named windows (5 items, ALL confirmed still open — the
window-frame entries had very fresh 2026-07-05 code_audit from the
M0122-0004 window-function work, re-verified unchanged: ROWS landed,
RANGE/GROUPS still 0A000-rejected); (b) storage/concurrency cluster —
Lehman/Yao crab-walk, posting-growth steady-state wiring, storage pool
pinCount race, connection-pool mutex partitioning, WAL appendMu splitting
(5 items, 2 flipped to resolved: pinCount race via M0056-0001 lock-free
redesign, and "connection pool mutex partitioning" which turned out to be a
stale mislabeling of the SAME bufpool.go poolMu already fixed by the
128-partition→lock-free evolution — not a real connection pool, no such
struct exists in the repo). 3 remain open: Lehman/Yao crab-walk (splitMu
still serializes all writers), posting-growth helpers (still dead code,
zero callers), appendMu splitting (single RWMutex still guards writer.go,
staged primitives unused). Did NOT append new .ralph/deferral_ledger.md
rows — pure triage/verification of already-known-open items, not new
implementation work (same precedent as the prior 3 loops' notes).

Next step: continue M0122-0001 — 59 - 10 = 49 `no-match`+no-status entries
remain. Regenerate the live list fresh next loop (indices shift after this
edit):
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if 'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match']; print(len(nm)); [print(i,f['feature'][:90]) for i,f in enumerate(nm)]"`
Good next clusters (by likely hit-rate, same parallel-agent-per-theme
method): (a) TPC-H/planner perf items (~30 entries, e.g. Q5/Q9/Q14/Q20/Q21
optimization attempts, NLI/semi-anti-join gaps, vectorized predicate batch
wiring FilterOp/SeqScanOp) — HIGH volume but perf work is often genuinely
still open, expect lower resolved-fraction; (b) the M0054-0007/M0091/M0092
"full verification"/"performance goal unmet" meta-items — check current
TPS/close-criteria state before re-triaging; (c) remaining misc storage
items not yet covered (e.g. dirty-tracking audit, spill-path hooks,
Borrow-semantics rewrite, plan cache deferral) — smaller cluster, moderate
hit-rate expected since M0091/M0092 design docs explain some as
intentionally-deferred-by-design (would resolve as "confirmed still
deferred by design, not a bug" rather than a code fix).

Gates run: `make ralph-state-guard` PASS (auto-repaired the same benign
transient running/completed mismatch seen the last 4 loops at this
checkpoint — confirmed still benign, not a regression). `.githooks/pre-commit`
pgbench smoke PASS (TPC-B 235 tps / simple-update 250 tps / select-only
14409 tps, 0 failed transactions across all 3 workloads) — ran automatically
via the commit hook even though no Go code was touched. `python3 -c
"json.load(...)"` confirmed valid JSON after the edit (181 entries
preserved, status-count 122→132); `grep -c '\\u'` confirmed zero
unicode-escapes introduced. `git diff --stat` confirmed only 20 targeted
line insertions + 10 deletions (10 status-line inserts + 10 code_audit
replacements) — no accidental reformat of the other 171 untouched entries.
Committed as bc249951.

In-flight: none.
