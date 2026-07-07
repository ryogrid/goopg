Task: M-NIGHTLY triage re-confirmed no new work (same as prior 2 loops — all
AI-items have checked fix_plan tasks, gates green). Actual loop work: M0122-0001
backlog triage continuation, doc-only (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only — located each
entry's exact line range via its "feature" line as a unique anchor, inserted a
`status` line after `resume_point`, and for 19 entries rewrote `code_audit` to
cite current evidence). No other files touched.

Key symbols: none new this loop (pure backlog bookkeeping). See the JSON's
updated `code_audit` fields for exact file:line evidence per item — highlights:
`internal/wal/recovery.go` RecordKind* WAL logging, `internal/storage/bufpool.go`
MarkDirtyLogicalChange/MarkDirtyChangeRecord, `internal/server/twophase.go`
in-memory 2PC, `internal/testutil/pubsubcluster/cluster.go` dual-process logical
repl harness, `internal/wal/format.go` RmgrXLog-unknown-info PG-standby fix.

Findings: triaged the full 97-entry `resolution_check.ledger == "no-match"`
subset's first batch of 28 (the WAL/replication/standby cluster, chosen because
M0119-0006/M0102/M0103/M0105/M0106 landed a lot of relevant work recently — high
expected hit-rate). Used 5 parallel general-purpose agents (read-only, no edits)
grouped by subsystem, each asked for VERDICT (OPEN/RESOLVED/PARTIAL) + cited
evidence + a replacement code_audit string. Verdict: 7 flipped to `status:
resolved` (IDX in old no-match list: 0 WAL logical-change-record opt, 40
two-phase btree delete WAL, 10 pgoutput DELETE pre-image, 80 PageImage
classify-is-correct-by-design, 78 pg_stat_io walsummarizer rows, 79 dual-process
logical-repl harness via pubsubcluster, 85 PG-standby timeline/redo-LSN fix).
21 remain `status: open`, of which 5 got upgraded from blanket "confirmed-open"
to nuanced PARTIAL text (still open, since goopg's status vocabulary is binary
open/resolved — no "partial" value exists in the schema, matched existing
convention of 56 open / 28 resolved before this loop): WAL preallocation
(has-current-segment, lacks-next-segment-lookahead), WALInsertLock array
(scaffolding exists in internal/wal/padded_mutex.go but explicitly dead code),
pg_multixact/pg_twophase/pg_commit_ts (2PC now in-memory but none of the 3
persist to disk), parsePrimaryConninfo (user now parsed, sslmode/password still
not), and the ~25-item streaming-replication epic (~15 of 25 sub-items now
landed per docs/design/README.md 0005-*/0102-* entries, tracked remainder is
M0122-0013). Also 2 corrected-but-still-open citations (IDX 11 per-slot
catalog-xmin retention — old citation was wrong file, real evidence is
internal/mvcc/manager.go:586-602 + design doc 0008-0001 self-documents the gap;
IDX 82 DDL replication — corrected citation to docs/milestones/0008, noted this
matches real PG's own DDL-replication limitation so isn't even a goopg-specific
gap). Did NOT append new .ralph/deferral_ledger.md rows — this loop was pure
triage/verification of already-known-open backlog items, not new
implementation work that left something newly unimplemented, so the mandatory
deferral-ledger-append rule (which fires on landing code) doesn't apply here.

Next step: continue M0122-0001 — 97 - 28 = 69 `no-match` entries still lack a
`status` field (indices 1,3,4,5,6,7,8,9,12,23,24,25,26,27,28,31-39,41-77 minus
the ones done, 86-95 etc. in the *original* no-match-subset numbering used this
loop — re-derive the live index list fresh next loop since numbering shifts
once you regenerate the no-match filter). Good next clusters to batch (by likely
hit-rate/theme, same parallel-agent-per-subsystem method as this loop):
(a) TPC-H/planner perf items (~30 entries, e.g. Q5/Q9/Q14/Q20/Q21 optimization
attempts, NLI/semi-anti-join gaps, vectorized predicate batch wiring) — HIGH
volume, may have low hit-rate since perf work is often genuinely still open;
(b) SASL/auth (channel binding, SASLprep, scram_iterations — 3 entries, IDX
5/45/46 in the original list); (c) GUC stubs cluster (parallel/jit/
compute_query_id/plan_cache_mode/track_io_timing/autovacuum — ~7 entries);
(d) pg_relation_size/pg_indexes_size stub (IDX 77), pg_multixact WAL companion
to today's IDX 96 (still open, don't re-triage). Query to regenerate the live
no-match+no-status list: `python3 -c "import json; d=json.load(open(
'unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if
'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match'];
print(len(nm)); [print(i,f['feature'][:90]) for i,f in enumerate(nm)]"`

Gates run: `make ralph-state-guard` PASS (auto-repaired the same benign
transient running/completed mismatch seen the last 2 loops at this checkpoint —
confirmed still benign, not a regression). `.githooks/pre-commit` pgbench smoke
PASS (TPC-B 183 tps / simple-update 246 tps / select-only 14107 tps, 0 failed
transactions across all 3 workloads). No Go code touched (pure JSON edit) — go
build/test not required by the risk-based gate rules. `python3 -c
"json.load(...)"` confirmed valid JSON after the edit; `grep -c '\\u'` confirmed
zero unicode-escapes introduced (avoided last loop's json.dumps ensure_ascii
pitfall by using json.dumps(s, ensure_ascii=False) for all new code_audit
strings). `git diff --stat` confirmed only 28 targeted +1-line insertions plus
19 single-line code_audit replacements — no accidental reformat of the other
153 untouched entries. Committed as 9b36ccb8.

In-flight: none.
